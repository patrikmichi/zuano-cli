package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/client"
)

const stepUpBody = `{"error":"step_up_required","message":"Confirm this change with your passkey.","step_up":{"action":"policy_change","digest":"d1"}}`

// stepUpGateway refuses /act with a step-up challenge until the person has
// confirmed (the test flips confirmed), as the real gateway does when it finds
// a confirmation made for the same change.
type stepUpGateway struct {
	status    int
	confirmed atomic.Bool
	actCalls  atomic.Int32
	retryBody atomic.Value
}

func newStepUpGateway(t *testing.T, g *stepUpGateway) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/act" {
			t.Errorf("unexpected request %s", r.URL.Path)
			return
		}
		g.actCalls.Add(1)
		if g.confirmed.Load() {
			b, _ := io.ReadAll(r.Body)
			g.retryBody.Store(string(b))
			_, _ = io.WriteString(w, "acted")
			return
		}
		w.WriteHeader(g.status)
		_, _ = io.WriteString(w, stepUpBody)
	}))
	t.Cleanup(srv.Close)
	withAPIKeyGateway(t, srv)
	return srv
}

// personConfirms is the person's answer: pressing Enter after confirming in
// the console, which is when done runs.
func personConfirms(done func()) io.Reader {
	return readerFunc(func(p []byte) (int, error) {
		if done != nil {
			done()
		}
		return copy(p, "\n"), nil
	})
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// stepUpEnv replaces the prompt streams; in is what the person types.
func stepUpEnv(t *testing.T, interactive bool, in io.Reader) (*bytes.Buffer, *[]string) {
	t.Helper()
	var out bytes.Buffer
	oldOut, oldIn, oldInteractive := stepUpOut, stepUpIn, stepUpInteractive
	stepUpOut, stepUpIn, stepUpInteractive = &out, in, func() bool { return interactive }
	t.Cleanup(func() { stepUpOut, stepUpIn, stepUpInteractive = oldOut, oldIn, oldInteractive })
	return &out, stubBrowserWith(t, nil)
}

func resolveForStepUp(t *testing.T, srv *httptest.Server) *client.Client {
	t.Helper()
	c, err := client.Resolve(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStepUp_NamesTheActionPointsToTheConsoleAndRetriesTheSameRequestOnce(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		g := &stepUpGateway{status: status}
		srv := newStepUpGateway(t, g)
		out, opened := stepUpEnv(t, true, personConfirms(func() { g.confirmed.Store(true) }))

		resp, err := resolveForStepUp(t, srv).Post("/act", "application/json", strings.NewReader(`{"n":1}`))
		if err != nil {
			t.Fatalf("%d: %v", status, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if string(body) != "acted" || g.retryBody.Load() != `{"n":1}` {
			t.Errorf("%d: body %q retry body %v", status, body, g.retryBody.Load())
		}
		if g.actCalls.Load() != 2 {
			t.Errorf("%d: the request was sent %d times, want the refusal and one retry", status, g.actCalls.Load())
		}
		if len(*opened) != 1 || (*opened)[0] != srv.URL+"/" {
			t.Errorf("%d: opened %v", status, *opened)
		}
		if !strings.Contains(out.String(), `"policy_change"`) || !strings.Contains(out.String(), "passkey") {
			t.Errorf("%d: output %q lacks the action", status, out)
		}
	}
}

func TestStepUp_AskedAgainAfterTheRetryIsExit14(t *testing.T) {
	g := &stepUpGateway{status: http.StatusUnauthorized}
	srv := newStepUpGateway(t, g)
	stepUpEnv(t, true, personConfirms(nil))

	_, err := resolveForStepUp(t, srv).Get("/act")
	if got := ExitCode(err); got != 14 || g.actCalls.Load() != 2 {
		t.Errorf("exit %d (%v) after %d calls: want 14 after exactly one retry", got, err, g.actCalls.Load())
	}
}

func TestStepUp_PrintsTheLinkWhenTheBrowserFails(t *testing.T) {
	g := &stepUpGateway{status: http.StatusUnauthorized}
	srv := newStepUpGateway(t, g)
	out, _ := stepUpEnv(t, true, personConfirms(func() { g.confirmed.Store(true) }))
	stubBrowserWith(t, io.ErrClosedPipe)

	resp, err := resolveForStepUp(t, srv).Get("/act")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !strings.Contains(out.String(), srv.URL+"/") {
		t.Errorf("output lacks the console link:\n%s", out)
	}
}

func TestStepUp_WithoutAPersonStopsWithExit14AndNamesTheAction(t *testing.T) {
	g := &stepUpGateway{status: http.StatusUnauthorized}
	srv := newStepUpGateway(t, g)
	out, opened := stepUpEnv(t, false, personConfirms(nil))

	_, err := resolveForStepUp(t, srv).Get("/act")
	if got := ExitCode(err); got != 14 {
		t.Fatalf("exit = %d (%v), want 14", got, err)
	}
	if !strings.Contains(err.Error(), "policy_change") || !strings.Contains(err.Error(), "console") {
		t.Errorf("error %q lacks the action or the console", err)
	}
	if len(*opened) != 0 || g.actCalls.Load() != 1 || out.Len() != 0 {
		t.Errorf("opened %v, act calls %d, output %q: nothing may be opened, asked or retried", *opened, g.actCalls.Load(), out)
	}
}

func TestStepUp_NoAnswerEndsWithExit14(t *testing.T) {
	g := &stepUpGateway{status: http.StatusUnauthorized}
	srv := newStepUpGateway(t, g)
	stepUpEnv(t, true, strings.NewReader(""))

	_, err := resolveForStepUp(t, srv).Get("/act")
	if got := ExitCode(err); got != 14 || g.actCalls.Load() != 1 {
		t.Errorf("exit %d (%v) after %d calls", got, err, g.actCalls.Load())
	}
}

func TestStepUp_ZuanoCallExitsWith14WhenNobodyIsAtTheKeyboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, stepUpBody)
	}))
	t.Cleanup(srv.Close)
	withAPIKeyGateway(t, srv)
	stepUpEnv(t, false, strings.NewReader(""))

	_, err := runCommand(t, CallCmd(), "stripe", "create_refund")
	if got := ExitCode(err); got != 14 {
		t.Fatalf("exit = %d (%v), want 14", got, err)
	}
	if err == nil || !strings.Contains(err.Error(), "policy_change") {
		t.Errorf("err = %v, want the action", err)
	}
}
