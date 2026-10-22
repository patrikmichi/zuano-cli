package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fwEndpointGateway serves /api/fw/github/mcp the way the gateway's firewall
// endpoint does: a session at initialize, and for a tools/call whatever answer
// the test chooses.
type fwEndpointGateway struct {
	mu    sync.Mutex
	srv   *httptest.Server
	seen  []fwEndpointRequest
	call  func(w http.ResponseWriter, n int)
	toolN int
	pollN int
}

type fwEndpointRequest struct {
	Method   string
	ID       json.RawMessage
	Result   json.RawMessage
	Session  string
	Version  string
	Accept   string
	Progress any
}

const fwSessionID = "fws_3q1Lx9mZk2VhE0aQ8dPbWn7Y"

func newFwEndpointGateway(t *testing.T, call func(w http.ResponseWriter, n int)) *fwEndpointGateway {
	t.Helper()
	g := &fwEndpointGateway{call: call}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case "/api/fw/github/mcp":
			raw, _ := io.ReadAll(r.Body)
			var rpc struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Params struct {
					Meta struct {
						ProgressToken any `json:"progressToken"`
					} `json:"_meta"`
				} `json:"params"`
			}
			_ = json.Unmarshal(raw, &rpc)
			g.seen = append(g.seen, fwEndpointRequest{rpc.Method, rpc.ID, rpc.Result, r.Header.Get("Mcp-Session-Id"), r.Header.Get("MCP-Protocol-Version"), r.Header.Get("Accept"), rpc.Params.Meta.ProgressToken})
			switch rpc.Method {
			case "initialize":
				w.Header().Set("Mcp-Session-Id", fwSessionID)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}}}}`))
			case "tools/call":
				g.toolN++
				g.call(w, g.toolN)
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		case "/api/fw/oauth/pending":
			g.pollN++
			if g.pollN < 2 {
				_, _ = w.Write([]byte(`{"connections":[{"slug":"github","title":"GitHub","connectUrl":"https://x/api/fw/oauth/start?upstream=github"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"connections":[]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.srv.Close)
	withAPIKeyGateway(t, g.srv)
	t.Setenv("ZUANO_AGENT", "")
	approvalPollInterval = time.Millisecond
	t.Cleanup(func() { approvalPollInterval = 2 * time.Second })
	return g
}

func (g *fwEndpointGateway) requests(method string) []fwEndpointRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []fwEndpointRequest
	for _, r := range g.seen {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

func TestFirewallEndpoint_InitializeIssuesASessionTheCallEchoes(t *testing.T) {
	g := newFwEndpointGateway(t, func(w http.ResponseWriter, _ int) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`))
	})
	out, err := runWithInput(t, CallCmd(), "", "fw/github", "list_issues")
	if err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("err %v\n%s", err, out)
	}
	init := g.requests("initialize")
	call := g.requests("tools/call")
	if len(init) != 1 || len(call) != 1 {
		t.Fatalf("initialize %d, tools/call %d", len(init), len(call))
	}
	if init[0].Session != "" || call[0].Session != fwSessionID {
		t.Errorf("sessions: initialize %q, call %q", init[0].Session, call[0].Session)
	}
	// Without the header the gateway reads a request as the 2025-03-26 revision, which has no URL elicitation.
	for _, r := range g.seen {
		if r.Version != "2025-11-25" || !strings.Contains(r.Accept, "text/event-stream") || !strings.Contains(r.Accept, "application/json") {
			t.Errorf("%s sent version %q, accept %q", r.Method, r.Version, r.Accept)
		}
	}
	if call[0].Progress != mcpProgressToken {
		t.Errorf("progress token = %v", call[0].Progress)
	}
}

func TestFirewallEndpoint_ACallAnsweredAsAStreamRelaysAndFinishes(t *testing.T) {
	stream := contractFixture(t, "mcp-firewall-stream.txt")
	g := newFwEndpointGateway(t, func(w http.ResponseWriter, _ int) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
	})
	out, err := runWithInput(t, CallCmd(), "", "fw/github", "list_issues")
	if err != nil || !strings.Contains(out, "listed 3 issues") {
		t.Fatalf("err %v\n%s", err, out)
	}
	answers := g.requests("")
	var relayed *fwEndpointRequest
	for i := range answers {
		if len(answers[i].Result) > 0 {
			relayed = &answers[i]
		}
	}
	if relayed == nil {
		t.Fatalf("the relayed request was not answered: %+v", g.seen)
	}
	if string(relayed.ID) != `"fwr_a1B2c3D4e5F6"` || string(relayed.Result) != `{"action":"decline"}` || relayed.Session != fwSessionID {
		t.Errorf("answer = id %s, result %s, session %q", relayed.ID, relayed.Result, relayed.Session)
	}
}

func TestReadMCPResponse_PrintsProgressAndAnswersEachKindOfRequest(t *testing.T) {
	var answers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &m)
		if m.Method == "" {
			answers = append(answers, string(raw))
		}
		if m.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", fwSessionID)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	withAPIKeyGateway(t, srv)
	c, err := approvalsClient("")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`data: {"jsonrpc":"2.0","id":"fwr_1","method":"ping"}`, "",
		`data: {"jsonrpc":"2.0","id":"fwr_2","method":"roots/list"}`, "",
		`data: {"jsonrpc":"2.0","id":"fwr_3","method":"sampling/createMessage","params":{}}`, "",
		`data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":"zuano-call","progress":2,"total":4,"message":"step\u001b[31m two"}}`, "",
		`data: {"jsonrpc":"2.0","id":1,"result":{}}`, "", "",
	}, "\r\n")
	resp := &http.Response{Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(body))}
	var progress bytes.Buffer
	final, err := readMCPResponse(t.Context(), c, "/api/fw/github/mcp", resp, 1<<20, &progress)
	if err != nil || string(final) != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Fatalf("final %s, %v", final, err)
	}
	if got := strings.TrimSpace(progress.String()); got != "progress 2/4: step two" && !strings.HasPrefix(got, "progress 2/4: step") {
		t.Errorf("progress = %q", got)
	}
	if strings.ContainsRune(progress.String(), 0x1b) {
		t.Errorf("a control character reached the terminal: %q", progress.String())
	}
	want := []string{
		`{"id":"fwr_1","jsonrpc":"2.0","result":{}}`,
		`{"id":"fwr_2","jsonrpc":"2.0","result":{"roots":[]}}`,
		`{"error":{"code":-32601,"message":"method not supported"},"id":"fwr_3","jsonrpc":"2.0"}`,
	}
	if strings.Join(answers, "\n") != strings.Join(want, "\n") {
		t.Errorf("answers = %v", answers)
	}
}

func TestReadMCPResponse_RefusesAStreamThatEndsWithoutAnswerOrRunsTooLong(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	defer srv.Close()
	withAPIKeyGateway(t, srv)
	c, err := approvalsClient("")
	if err != nil {
		t.Fatal(err)
	}
	stream := func(body string) *http.Response {
		return &http.Response{Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
	}
	if _, err := readMCPResponse(t.Context(), c, "/p", stream("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n"), 1<<10, nil); err == nil || !strings.Contains(err.Error(), "without an answer") {
		t.Errorf("a stream with no answer: %v", err)
	}
	if _, err := readMCPResponse(t.Context(), c, "/p", stream("data: "+strings.Repeat("x", 4096)+"\n\n"), 1<<10, nil); err == nil {
		t.Error("an oversized stream was read")
	}
	if _, err := readMCPResponse(t.Context(), c, "/p", stream("data: not json\n\n"), 1<<10, nil); err == nil {
		t.Error("a message that is not JSON-RPC was accepted")
	}
	plain := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{}}`))}
	if got, err := readMCPResponse(t.Context(), c, "/p", plain, 1<<10, nil); err != nil || string(got) != `{"jsonrpc":"2.0","id":1,"result":{}}` {
		t.Errorf("a plain body: %s, %v", got, err)
	}
}

func connectRequired(t *testing.T, gatewayURL string) func(w http.ResponseWriter, n int) {
	fixture := strings.ReplaceAll(contractFixture(t, "mcp-connect-elicitation.json"), "https://gateway.example", gatewayURL)
	return func(w http.ResponseWriter, n int) {
		if n == 1 {
			_, _ = io.WriteString(w, fixture)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"listed 3 issues"}]}}`)
	}
}

func TestFirewallEndpoint_ConnectLinkWithoutAPersonIsPrintedAndExits72(t *testing.T) {
	var g *fwEndpointGateway
	g = newFwEndpointGateway(t, func(w http.ResponseWriter, n int) { connectRequired(t, g.srv.URL)(w, n) })
	t.Setenv("ZUANO_AGENT", "1")
	opened := stubBrowserWith(t, nil)
	out, err := runWithInput(t, CallCmd(), "", "fw/github", "list_issues")
	if got := exitCodeOf(err); got != exitAccountNotConnected {
		t.Fatalf("exit %d (%v)\n%s", got, err, out)
	}
	for _, want := range []string{"Account not connected", "/api/fw/oauth/start?upstream=github", "host: " + strings.TrimPrefix(g.srv.URL, "http://"), "Connect it at the link"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(*opened) != 0 || g.pollN != 0 || g.toolN != 1 {
		t.Errorf("opened %v, polls %d, calls %d", *opened, g.pollN, g.toolN)
	}
}

func TestFirewallEndpoint_ConnectLinkWithWaitPollsThenRunsTheCallOnce(t *testing.T) {
	var g *fwEndpointGateway
	g = newFwEndpointGateway(t, func(w http.ResponseWriter, n int) { connectRequired(t, g.srv.URL)(w, n) })
	stubBrowserWith(t, nil)
	out, err := runWithInput(t, CallCmd(), "", "fw/github", "list_issues", "--wait=30")
	if err != nil || !strings.Contains(out, "Connected. Running the call again.") || !strings.Contains(out, "listed 3 issues") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if g.toolN != 2 || g.pollN != 2 {
		t.Errorf("calls %d, polls %d", g.toolN, g.pollN)
	}
}

func TestFirewallEndpoint_ConnectLinkOnAnotherHostIsNotOpened(t *testing.T) {
	g := newFwEndpointGateway(t, func(w http.ResponseWriter, n int) {
		if n > 1 {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"listed 3 issues"}]}}`)
			return
		}
		_, _ = io.WriteString(w, strings.ReplaceAll(contractFixture(t, "mcp-connect-elicitation.json"), "https://gateway.example", "https://phish.example"))
	})
	opened := stubBrowserWith(t, nil)
	out, err := runWithInput(t, CallCmd(), "y\n", "fw/github", "list_issues")
	if err != nil {
		t.Fatalf("err %v\n%s", err, out)
	}
	if len(*opened) != 0 || !strings.Contains(out, "host: phish.example") || !strings.Contains(out, "not on your gateway") {
		t.Errorf("opened %v, polls %d\n%s", *opened, g.pollN, out)
	}
}

func TestElicitationFromError_TheGatewaysApprovalIdWinsOverTheLink(t *testing.T) {
	e := &mcpRPCError{Code: -32042, Data: json.RawMessage(contractFixtureData(t, "mcp-url-elicitation.json"))}
	got := elicitationFromError(e)
	if got == nil || got.ApprovalID != "apr_1" || got.Upstream != "" {
		t.Fatalf("got %+v", got)
	}
	connect := elicitationFromError(&mcpRPCError{Code: -32042, Data: json.RawMessage(contractFixtureData(t, "mcp-connect-elicitation.json"))})
	if connect == nil || connect.Upstream != "github" || connect.ApprovalID != "" || connect.ID != "fwe_Zm9v" {
		t.Fatalf("connect = %+v", connect)
	}
}

func contractFixtureData(t *testing.T, name string) string {
	t.Helper()
	var env struct {
		Error struct {
			Data json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(contractFixture(t, name)), &env); err != nil {
		t.Fatal(err)
	}
	return string(env.Error.Data)
}
