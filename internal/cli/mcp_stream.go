package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// mcpProgressToken is the token a tools/call carries, so the gateway forwards
// the progress of the call instead of dropping it.
const mcpProgressToken = "zuano-call"

// mcpMessage is the part of a JSON-RPC message the reader needs to tell a
// notification, a request the server makes of the client and the answer apart.
type mcpMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (m mcpMessage) hasID() bool { return len(m.ID) > 0 && string(m.ID) != "null" }

var errStreamTooLarge = errors.New("the response stream exceeds the size cap")

// cappedReader fails once more than limit bytes were read.
type cappedReader struct {
	r     io.Reader
	limit int64
	n     int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		return n, errStreamTooLarge
	}
	return n, err
}

// readMCPResponse returns the JSON-RPC answer of a call: the body as it is, or
// the last message of an event stream after the server's requests on it were
// answered. A stream is how a gateway reports progress and relays a request of
// the upstream server.
func readMCPResponse(ctx context.Context, c *client.Client, path string, resp *http.Response, limit int64, progress io.Writer) ([]byte, error) {
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "text/event-stream" {
		return readLimitedResponse(resp.Body, limit)
	}
	r := bufio.NewReader(&cappedReader{r: resp.Body, limit: limit})
	var data []string
	handle := func() ([]byte, bool, error) {
		raw := strings.Join(data, "\n")
		data = nil
		return handleMCPEvent(ctx, c, path, []byte(raw), progress)
	}
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(rest, " "))
		}
		if (line == "" || err != nil) && len(data) > 0 {
			final, done, herr := handle()
			if herr != nil {
				return nil, herr
			}
			if done {
				return final, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("the response stream ended without an answer")
			}
			return nil, fmt.Errorf("read response: %w", err)
		}
	}
}

func handleMCPEvent(ctx context.Context, c *client.Client, path string, raw []byte, progress io.Writer) ([]byte, bool, error) {
	var m mcpMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false, fmt.Errorf("the response stream carried a message that is not JSON-RPC: %w", err)
	}
	switch {
	case m.Method == "" && m.hasID():
		return raw, true, nil
	case m.Method != "" && m.hasID():
		return nil, false, answerServerRequest(ctx, c, path, m)
	case m.Method == "notifications/progress" && progress != nil:
		printProgress(progress, m.Params)
	}
	return nil, false, nil
}

func printProgress(w io.Writer, params json.RawMessage) {
	var p struct {
		Progress float64 `json:"progress"`
		Total    float64 `json:"total"`
		Message  string  `json:"message"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	line := fmt.Sprintf("progress %g", p.Progress)
	if p.Total > 0 {
		line += fmt.Sprintf("/%g", p.Total)
	}
	if p.Message != "" {
		line += ": " + textsafe.Clean(p.Message)
	}
	fmt.Fprintln(w, line)
}

// answerServerRequest answers a request the gateway relays from the upstream
// server, on the same endpoint and session. The CLI offers no form, no model
// and no roots, so a form or link elicitation is declined and sampling is
// refused; a ping is answered.
func answerServerRequest(ctx context.Context, c *client.Client, path string, m mcpMessage) error {
	reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
	switch m.Method {
	case "ping":
		reply["result"] = map[string]any{}
	case "roots/list":
		reply["result"] = map[string]any{"roots": []any{}}
	case "elicitation/create":
		reply["result"] = map[string]any{"action": "decline"}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "method not supported"}
	}
	body, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	resp, err := c.MCPPostContext(ctx, path, body)
	if err != nil {
		return fmt.Errorf("answer %s: %w", m.Method, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, client.MaxJSONBytes))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("answer %s: HTTP %d", m.Method, resp.StatusCode)
	}
	return nil
}
