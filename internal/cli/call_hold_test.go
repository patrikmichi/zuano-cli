package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	mcpDone       = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"refund done"}]}}`
	mcpHeldAfter  = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"pending_approval","approval_id":"apr_1","service":"stripe","tool":"create_refund","risk":"destructive","expires_at":"2026-10-22T12:15:00.000Z","held_seconds":45}}}`
	mcpHeldNoHeld = `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}],"structuredContent":{"status":"pending_approval","approval_id":"apr_1","service":"stripe","tool":"create_refund","risk":"destructive","expires_at":"2026-10-22T12:15:00.000Z"}}}`
)

// holdGateway answers tools/call with callReply and records the Prefer header
// of each call and the timeout of each await.
type holdGateway struct {
	mu       sync.Mutex
	prefer   []string
	toolN    int
	awaitS   []int
	awaitOut string
}

func newHoldGateway(t *testing.T, callReply string) *holdGateway {
	t.Helper()
	g := &holdGateway{awaitOut: `{"status":"approved","approval_id":"apr_1","executed":true,"result":{"content":[{"type":"text","text":"refund done"}]}}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		raw, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/stripe/mcp":
			var rpc struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(raw, &rpc)
			switch rpc.Method {
			case "tools/call":
				g.toolN++
				g.prefer = append(g.prefer, r.Header.Get("Prefer"))
				_, _ = io.WriteString(w, callReply)
			case "initialize":
				if r.Header.Get("Prefer") != "" {
					t.Errorf("initialize carries Prefer: %q", r.Header.Get("Prefer"))
				}
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
			default:
				w.WriteHeader(http.StatusAccepted)
			}
		case "/api/approvals/apr_1/await":
			var body struct {
				Timeout int `json:"timeout_s"`
			}
			_ = json.Unmarshal(raw, &body)
			g.awaitS = append(g.awaitS, body.Timeout)
			_, _ = io.WriteString(w, g.awaitOut)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withAPIKeyGateway(t, srv)
	t.Setenv("ZUANO_AGENT", "")
	awaitRetryDelay = time.Millisecond
	t.Cleanup(func() { awaitRetryDelay = time.Second })
	return g
}

func TestCallHold_WaitAsksTheGatewayToHoldTheCallAndAResultInTheWindowIsOneRequest(t *testing.T) {
	g := newHoldGateway(t, mcpDone)
	out, err := runCommand(t, CallCmd(), "stripe", "create_refund", "--wait=2m")
	if err != nil || !strings.Contains(out, "refund done") {
		t.Fatalf("%q, %v", out, err)
	}
	if g.toolN != 1 || len(g.awaitS) != 0 || g.prefer[0] != "wait=120" {
		t.Errorf("tools/call %d times, awaits %v, Prefer %q: want one call asking for 120 seconds and no poll", g.toolN, g.awaitS, g.prefer)
	}
}

func TestCallHold_ThePreferHeaderIsCappedAndAbsentWithoutWait(t *testing.T) {
	g := newHoldGateway(t, mcpDone)
	for _, args := range [][]string{{"--wait=10m"}, {"--wait=0"}, {}} {
		if _, err := runCommand(t, CallCmd(), append([]string{"stripe", "create_refund"}, args...)...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if got := strings.Join(g.prefer, "|"); got != "wait=240||" {
		t.Errorf("Prefer headers = %q, want the cap, then none for --wait=0 and none without --wait", got)
	}
}

func TestCallHold_AHeldResultReportsHowLongTheGatewayWaitedAndStaysPending(t *testing.T) {
	g := newHoldGateway(t, mcpHeldAfter)
	out, err := runCommand(t, CallCmd(), "stripe", "create_refund")
	if exitCodeOf(err) != exitPending || !strings.Contains(out, "waited:   45s for a decision before answering") {
		t.Fatalf("exit %d, %q", exitCodeOf(err), out)
	}
	if g.prefer[0] != "" {
		t.Errorf("a call without --wait sent Prefer: %q", g.prefer[0])
	}
	out, err = runCommand(t, CallCmd(), "stripe", "create_refund", "--json")
	if exitCodeOf(err) != exitPending || !strings.Contains(out, `"held_seconds":45`) {
		t.Errorf("--json keeps held_seconds: exit %d, %q", exitCodeOf(err), out)
	}
}

func TestCallHold_APendingAnswerAfterTheWindowContinuesWithTheRestOfTheWait(t *testing.T) {
	g := newHoldGateway(t, mcpHeldAfter)
	out, err := runCommand(t, CallCmd(), "stripe", "create_refund", "--wait=60")
	if err != nil || !strings.Contains(out, "refund done") {
		t.Fatalf("%q, %v", out, err)
	}
	if g.toolN != 1 || len(g.awaitS) != 1 || g.awaitS[0] > 15 || g.awaitS[0] < 13 {
		t.Errorf("tools/call %d times, awaits %v: want one call and one await of the 15 seconds left", g.toolN, g.awaitS)
	}
}

func TestCallHold_ThePendingResultKeepsTheExitCodesWithoutHeldSeconds(t *testing.T) {
	newHoldGateway(t, mcpHeldNoHeld)
	if out, err := runCommand(t, CallCmd(), "stripe", "create_refund"); exitCodeOf(err) != exitPending || strings.Contains(out, "waited:") {
		t.Errorf("exit %d, %q", exitCodeOf(err), out)
	}
	g := newHoldGateway(t, mcpHeldNoHeld)
	g.awaitOut = `{"status":"denied","approval_id":"apr_1","approver":"boss@example.com"}`
	if _, err := runCommand(t, CallCmd(), "stripe", "create_refund", "--wait=30"); exitCodeOf(err) != exitDenied {
		t.Errorf("denied while waiting: exit %d", exitCodeOf(err))
	}
}
