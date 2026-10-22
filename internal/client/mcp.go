package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/patrikmichi/zuano-cli/internal/brand"
)

// MCPSessionHeader carries the session id a gateway assigns at initialize.
const MCPSessionHeader = "Mcp-Session-Id"

const mcpProtocolVersion = "2025-11-25"

// mcpProtocolHeader names the revision a request speaks. Without it a gateway
// takes the request for the 2025-03-26 revision, which has no URL elicitation.
const mcpProtocolHeader = "MCP-Protocol-Version"

// mcpAccept lists both answers Streamable HTTP lets a server choose from: a
// call is answered as a stream only when it reports progress or asks the
// client something.
const mcpAccept = "application/json, text/event-stream"

// mcpInitializeBody is the initialize request. Declaring URL elicitation lets
// the gateway answer a call that needs approval with a link to open instead of
// a held-call result.
func mcpInitializeBody() []byte {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
			"clientInfo":      map[string]any{"name": brand.Name, "version": clientVersion},
		},
	})
	return body
}

var mcpInitializedBody = []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

// MCPPostContext posts one JSON-RPC request to an MCP endpoint. The first
// request to an endpoint is preceded by an initialize that declares URL
// elicitation; the Mcp-Session-Id the gateway returns is sent from then on. A
// gateway that does not issue sessions is used without one, and one that
// forgets a session (HTTP 404) gets a fresh initialize and one retry.
func (c *Client) MCPPostContext(ctx context.Context, path string, body []byte) (*http.Response, error) {
	return c.MCPPostWith(ctx, path, body, nil)
}

// MCPPostWith is MCPPostContext with extra headers on the request itself, not
// on the initialize that may precede it.
func (c *Client) MCPPostWith(ctx context.Context, path string, body []byte, extra http.Header) (*http.Response, error) {
	sid := c.mcpSession(ctx, path)
	resp, err := c.mcpPost(ctx, path, sid, body, extra)
	if err != nil || sid == "" || resp.StatusCode != http.StatusNotFound {
		return resp, err
	}
	_ = resp.Body.Close()
	c.mcpMu.Lock()
	delete(c.mcpSessions, path)
	c.mcpMu.Unlock()
	return c.mcpPost(ctx, path, c.mcpSession(ctx, path), body, extra)
}

func (c *Client) mcpPost(ctx context.Context, path, sid string, body []byte, extra http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.GatewayURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range extra {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", mcpAccept)
	req.Header.Set(mcpProtocolHeader, mcpProtocolVersion)
	if sid != "" {
		req.Header.Set(MCPSessionHeader, sid)
	}
	return c.Do(req)
}

// mcpSession returns the session id for path, initializing once per endpoint.
// An initialize that fails is not an error here: the call that follows
// reports whatever is wrong, such as a refusal, in its own terms.
func (c *Client) mcpSession(ctx context.Context, path string) string {
	c.mcpMu.Lock()
	defer c.mcpMu.Unlock()
	if sid, done := c.mcpSessions[path]; done {
		return sid
	}
	if c.mcpSessions == nil {
		c.mcpSessions = map[string]string{}
	}
	sid := ""
	resp, err := c.mcpPost(ctx, path, "", mcpInitializeBody(), nil)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxJSONBytes))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			sid = resp.Header.Get(MCPSessionHeader)
			if note, err := c.mcpPost(ctx, path, sid, mcpInitializedBody, nil); err == nil {
				_ = note.Body.Close()
			}
		}
	}
	c.mcpSessions[path] = sid
	return sid
}
