package cli

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type routeMap = map[string]func(*packServer, http.ResponseWriter, *http.Request)

func workflowGateway(t *testing.T, routes routeMap) *packServer {
	t.Helper()
	ps := packGateway(t, routes)
	old := workflowPollDelay
	workflowPollDelay = time.Millisecond
	t.Cleanup(func() { workflowPollDelay = old })
	return ps
}

const (
	installationJSON = `{"id":"wfi_k3j2h1","name":"Invoice check","status":"enabled","paused_reason":null,"resource_id":"res_invoice","version":"1.2.0","custom_manifest_id":null,"mcp_tool_name":"invoice_check","consecutive_failures":0,"failure_threshold":5,"last_run_at":"2026-10-20T10:00:00Z"}`
	pausedJSON       = `{"id":"wfi_p4u5s6","name":"Weekly","status":"paused","paused_reason":"installer_removed","resource_id":"res_weekly","version":"1.0.0","consecutive_failures":2,"failure_threshold":5}`
)

func TestWorkflowList(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/installations": jsonReply(200, `{"installations":[`+installationJSON+`,`+pausedJSON+`]}`),
		"GET /api/v1/workflows/templates": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"templates":[{"resource_id":"res_invoice","slug":"invoice","name":"Invoice check","summary":"Checks invoices","category":"finance","version":"1.2.0","max_risk":"write"},{"resource_id":"res_plain","name":"Plain","version":"1.0.0"}],"next_cursor":null}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wfi_k3j2h1", "res_invoice@1.2.0", "enabled", "paused: installer_removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}
	out, err = runCommand(t, WorkflowCmd(), "list", "--gallery", "--category", "finance", "--service", "slack", "--ready", "--query", "inv")
	if err != nil || q != "category=finance&q=inv&ready=true&service=slack" {
		t.Fatalf("gallery: %q %q %v", q, out, err)
	}
	for _, want := range []string{"res_invoice", "Checks invoices", "write"} {
		if !strings.Contains(out, want) {
			t.Errorf("gallery lacks %q:\n%s", want, out)
		}
	}
	for _, args := range [][]string{{"list", "--json"}, {"list", "--gallery", "--json"}} {
		if out, err := runCommand(t, WorkflowCmd(), args...); err != nil || !strings.Contains(out, "{") {
			t.Errorf("%v: %q %v", args, out, err)
		}
	}
}

func TestWorkflowList_Empty(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/installations": jsonReply(200, `{"installations":[]}`),
		"GET /api/v1/workflows/templates":     jsonReply(200, `{"templates":[],"next_cursor":null}`),
	})
	if out, err := runCommand(t, WorkflowCmd(), "list"); err != nil || !strings.Contains(out, "No workflows installed") {
		t.Errorf("%q %v", out, err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "list", "--gallery"); err != nil || !strings.Contains(out, "No templates.") {
		t.Errorf("%q %v", out, err)
	}
}

func TestWorkflowShow(t *testing.T) {
	var version string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/installations/wfi_k3j2h1": jsonReply(200, installationJSON),
		"GET /api/v1/workflows/installations/wfi_k3j2h1/preapprovals": jsonReply(200, `{"preapprovals":[
			{"id":"pre_1","step_path":"post","service":"slack","tool":"post_message","per_day_limit":50,"approved_by":"u1","expires_at":"2026-11-01"}]}`),
		"GET /api/v1/workflows/templates/res_invoice": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			version = r.URL.Query().Get("version")
			_, _ = io.WriteString(w, `{"resource_id":"res_invoice","name":"Invoice check","version":"1.2.0","summary":"s",
			 "required_connectors":[{"service":"slack","purpose":"post","optional":false},{"service":"mail","purpose":"x","optional":true}],
			 "analysis":{"permissions":[{"service":"slack","tool":"post_message","risk":"outward"}],"preapprovableSteps":["post"]}}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "show", "wfi_k3j2h1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"wfi_k3j2h1 Invoice check (enabled)", "source res_invoice@1.2.0", "tool name: invoice_check", "pre-approval pre_1 step post (slack.post_message): up to 50 a day"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	out, err = runCommand(t, WorkflowCmd(), "show", "res_invoice@1.2.0")
	if err != nil || version != "1.2.0" {
		t.Fatalf("%q %q %v", out, version, err)
	}
	for _, want := range []string{"Invoice check 1.2.0", "Connections: slack, mail (optional)", "outward", "slack.post_message", "Steps you can pre-approve: post", "zuano workflow install res_invoice"} {
		if !strings.Contains(out, want) {
			t.Errorf("template lacks %q:\n%s", want, out)
		}
	}
	if out, err := runCommand(t, WorkflowCmd(), "show", "res_invoice", "--json"); err != nil || !strings.Contains(out, "required_connectors") {
		t.Errorf("template json: %q %v", out, err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "show", "bad slug"); err == nil {
		t.Error("a bad reference was accepted")
	}
}

func TestWorkflowInstall(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/installations": jsonReply(201, `{"id":"wfi_n3w1","name":"Mine","status":"draft"}`),
	})
	out, err := runCommand(t, WorkflowCmd(), "install", "res_invoice@1.2.0", "--name", "Mine", "--param", "channel=C1", "--run-as", "caller")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b := ps.body("POST /api/v1/workflows/installations", 0)
	if b["resourceId"] != "res_invoice" || b["version"] != "1.2.0" || b["name"] != "Mine" || b["runAs"] != "caller" || b["params"].(map[string]any)["channel"] != "C1" {
		t.Errorf("body = %v", b)
	}
	for _, want := range []string{"Created draft wfi_n3w1 (Mine)", ps.URL + "/workflows/wfi_n3w1", "zuano workflow run wfi_n3w1 --dry-run"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "install", "acme/invoice-check", "--channel", "beta", "--run-as", "usr_1"); err != nil {
		t.Fatal(err)
	}
	b = ps.body("POST /api/v1/workflows/installations", 1)
	if b["listing"] != "acme/invoice-check" || b["channel"] != "beta" || b["name"] != "invoice-check" || b["runAs"] != "fixed" || b["runAsUserId"] != "usr_1" {
		t.Errorf("listing body = %v", b)
	}
	if _, err := runCommand(t, WorkflowCmd(), "install", "bad slug"); err == nil {
		t.Error("a bad template was accepted")
	}
}

func TestWorkflowInstall_NoInstallationIsAnError(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/installations": jsonReply(201, `{}`)})
	if _, err := runCommand(t, WorkflowCmd(), "install", "res_invoice"); err == nil {
		t.Error("an answer without an installation was accepted")
	}
}

func runRoutes(started, status, steps string) routeMap {
	return routeMap{
		"POST /api/v1/workflows/wfi_k3j2h1/runs": func(ps *packServer, w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Idempotency-Key") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			jsonReply(map[bool]int{true: 202, false: 200}[strings.Contains(started, "run_id")], started)(ps, w, r)
		},
		"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, `{"run":{"id":"wfr_a1b2c3","status":"`+status+`","mode":"live","error_code":"step_failed","error":"boom","output":{"ok":true}},"steps":[`+steps+`],"next_after":null}`),
	}
}

const acceptedRun = `{"run_id":"wfr_a1b2c3","status":"queued","links":{}}`

func TestWorkflowRun_ModesAndInputs(t *testing.T) {
	ps := workflowGateway(t, runRoutes(acceptedRun, "running", ""))
	out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--dry-run", "--set", "domain=acme.com", "--set", "n=3", "--idempotency-key", "k1")
	if err != nil || !strings.Contains(out, "Started wfr_a1b2c3. Follow it with `zuano workflow logs wfr_a1b2c3 --follow`") {
		t.Fatalf("%q %v", out, err)
	}
	b := ps.body("POST /api/v1/workflows/wfi_k3j2h1/runs", 0)
	in, _ := b["input"].(map[string]any)
	if b["mode"] != "dry_run" || in["domain"] != "acme.com" || in["n"] != float64(3) {
		t.Errorf("dry-run body = %v", b)
	}
	file := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(file, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--test", "--yes", "--input", file, "--set", "b=2"); err != nil {
		t.Fatal(err)
	}
	b = ps.body("POST /api/v1/workflows/wfi_k3j2h1/runs", 1)
	in, _ = b["input"].(map[string]any)
	if b["mode"] != "test" || in["a"] != float64(1) || in["b"] != float64(2) {
		t.Errorf("test body = %v", b)
	}
	if _, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1"); err != nil {
		t.Fatal(err)
	}
	if b = ps.body("POST /api/v1/workflows/wfi_k3j2h1/runs", 2); b["mode"] != "live" {
		t.Errorf("live body = %v", b)
	}
	if out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--json"); err != nil || !strings.Contains(out, "run_id") {
		t.Errorf("json: %q %v", out, err)
	}
	before := ps.count("POST /api/v1/workflows/wfi_k3j2h1/runs")
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("nope"), 0o600)
	for name, args := range map[string][]string{
		"both modes":   {"run", "wfi_k3j2h1", "--dry-run", "--test"},
		"test no yes":  {"run", "wfi_k3j2h1", "--test"},
		"bad id":       {"run", "no way"},
		"bad wait":     {"run", "wfi_k3j2h1", "--wait=soon"},
		"missing file": {"run", "wfi_k3j2h1", "--input", filepath.Join(t.TempDir(), "none")},
		"not json":     {"run", "wfi_k3j2h1", "--input", bad},
		"bad set":      {"run", "wfi_k3j2h1", "--set", "novalue"},
	} {
		if _, err := runCommand(t, WorkflowCmd(), args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if ps.count("POST /api/v1/workflows/wfi_k3j2h1/runs") != before {
		t.Error("a run started although the command was refused")
	}
}

func TestWorkflowRun_SetNeedsAnObjectInput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(file, []byte(`[1]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowInput(file, []string{"a=1"}); err == nil {
		t.Error("--set on an array input was accepted")
	}
	if in, err := workflowInput(file, nil); err != nil || in == nil {
		t.Errorf("array input: %v %v", in, err)
	}
}

func TestWorkflowRun_FinishedWithinTheRequest(t *testing.T) {
	finished := `{"run":{"id":"wfr_a1b2c3","status":"succeeded"}}`
	workflowGateway(t, runRoutes(finished, "succeeded", `{"path":"check","status":"succeeded"}`))
	out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--wait=30s")
	if err != nil || strings.TrimSpace(out) != `{"ok":true}` {
		t.Errorf("%q %v", out, err)
	}
}

func TestWorkflowRun_DryRunShowsTheStepsNotTheOutput(t *testing.T) {
	workflowGateway(t, routeMap{
		"POST /api/v1/workflows/wfi_k3j2h1/runs": jsonReply(200, `{"run":{"id":"wfr_a1b2c3","status":"succeeded"}}`),
		"GET /api/v1/workflows/runs/wfr_a1b2c3":  jsonReply(200, `{"run":{"id":"wfr_a1b2c3","status":"succeeded","mode":"dry_run","output":{"ok":true}},"steps":[{"path":"post","status":"succeeded"}]}`),
	})
	out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--dry-run")
	if err != nil || !strings.Contains(out, "Run wfr_a1b2c3: succeeded") || !strings.Contains(out, "post") || strings.Contains(out, `"ok"`) {
		t.Errorf("dry run: %q %v", out, err)
	}
}

func TestWorkflowRun_WaitSendsTheServerWait(t *testing.T) {
	var query string
	workflowGateway(t, routeMap{
		"POST /api/v1/workflows/wfi_k3j2h1/runs": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, acceptedRun)
		},
		"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, `{"run":{"id":"wfr_a1b2c3","status":"succeeded"},"steps":[]}`),
	})
	if _, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--wait=10m"); err != nil {
		t.Fatal(err)
	}
	if query != "wait=55" {
		t.Errorf("query = %q", query)
	}
}

func TestWorkflowRun_WaitExitCodes(t *testing.T) {
	held := `{"seq":2,"path":"post","status":"held","approval_id":"apr_1"}`
	for name, c := range map[string]struct {
		status, steps string
		exit          int
		want          string
	}{
		"succeeded": {"succeeded", `{"path":"check","status":"succeeded"}`, 0, `"ok":true`},
		"failed":    {"failed", `{"path":"check","status":"failed","error":"bad"}`, 20, "step_failed"},
		"dead":      {"dead", `{"path":"check","status":"failed"}`, 20, "boom"},
		"denied":    {"failed", `{"path":"post","status":"denied"}`, 10, "failed"},
		"expired":   {"failed", `{"path":"post","status":"expired"}`, 11, "failed"},
		"held":      {"running", held, 12, "/approvals/apr_1"},
		"cancelled": {"cancelled", ``, 1, "cancelled"},
		"skipped":   {"skipped", ``, 1, "skipped"},
	} {
		workflowGateway(t, runRoutes(acceptedRun, c.status, c.steps))
		out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--wait")
		if exitCodeOf(err) != c.exit || !strings.Contains(out+errText(err), c.want) {
			t.Errorf("%s: exit %d, %v\n%s", name, exitCodeOf(err), err, out)
		}
	}
	workflowGateway(t, runRoutes(acceptedRun, "running", `{"path":"a","status":"running"}`))
	_, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--wait=1ms")
	if exitCodeOf(err) != 17 || !strings.Contains(err.Error(), "workflow logs wfr_a1b2c3 --follow") {
		t.Errorf("timeout: exit %d, %v", exitCodeOf(err), err)
	}
	workflowGateway(t, runRoutes(acceptedRun, "failed", ``))
	if out, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1", "--wait", "--json"); exitCodeOf(err) != 20 || !strings.Contains(out, `"status":"failed"`) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("json failed: %d %v\n%s", exitCodeOf(err), err, out)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestWorkflowRun_RefusalsMapToTheRegistry(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
		exit   int
		want   string
	}{
		"input":  {422, `{"error":"input_invalid","message":"The input is not valid.","issues":[{"pointer":"/domain","message":"is required"}]}`, 18, "/domain: is required"},
		"absent": {404, `{"error":"installation_not_found","message":"No such workflow installation."}`, 1, "No such workflow installation"},
	} {
		workflowGateway(t, routeMap{"POST /api/v1/workflows/wfi_k3j2h1/runs": jsonReply(c.status, c.body)})
		_, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1")
		if exitCodeOf(err) != c.exit || !strings.Contains(errText(err), c.want) {
			t.Errorf("%s: exit %d, %v", name, exitCodeOf(err), err)
		}
	}
}

func TestWorkflowRun_HeldStartIsReported(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/wfi_k3j2h1/runs": jsonReply(202, `{"status":"pending_approval","approvalId":"apr_9","approvalUrl":"/approvals/apr_9"}`)})
	_, err := runCommand(t, WorkflowCmd(), "run", "wfi_k3j2h1")
	if err == nil {
		t.Error("a held start returned no error")
	}
}

func TestWorkflowRun_WaitStopsWhenTheContextEnds(t *testing.T) {
	workflowGateway(t, runRoutes(acceptedRun, "running", `{"path":"a","status":"running"}`))
	c, err := approvalsClient("")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := followWorkflowRun(ctx, c, "wfr_a1b2c3", time.Hour, io.Discard, true); err == nil {
		t.Error("a cancelled wait returned no error")
	}
}

func TestWorkflowRuns(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/wfi_k3j2h1/runs": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"runs":[{"id":"wfr_a1b2c3","status":"failed","mode":"live","outcome":"unknown","finished_at":"2026-10-20T10:00:00Z"}],"next_cursor":null}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "runs", "wfi_k3j2h1", "--status", "failed")
	if err != nil || q != "status=failed" || !strings.Contains(out, "wfr_a1b2c3") || !strings.Contains(out, "failed") {
		t.Fatalf("%q %q %v", q, out, err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "runs", "wfi_k3j2h1", "--json"); err != nil || !strings.Contains(out, `"runs"`) {
		t.Errorf("json: %q %v", out, err)
	}
}

func TestWorkflowRuns_Empty(t *testing.T) {
	workflowGateway(t, routeMap{"GET /api/v1/workflows/wfi_k3j2h1/runs": jsonReply(200, `{"runs":[],"next_cursor":null}`)})
	if out, err := runCommand(t, WorkflowCmd(), "runs", "wfi_k3j2h1"); err != nil || !strings.Contains(out, "No runs.") {
		t.Errorf("%q %v", out, err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "logs", "wfi_k3j2h1"); err != nil || !strings.Contains(out, "No runs yet.") {
		t.Errorf("logs: %q %v", out, err)
	}
}

func TestWorkflowLogs(t *testing.T) {
	var afters []string
	n := 0
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/wfi_k3j2h1/runs": jsonReply(200, `{"runs":[{"id":"wfr_a1b2c3"}]}`),
		"GET /api/v1/workflows/runs/wfr_a1b2c3": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			afters = append(afters, r.URL.Query().Get("after"))
			n++
			if n == 1 {
				_, _ = io.WriteString(w, `{"run":{"id":"wfr_a1b2c3","status":"running","mode":"live"},"steps":[{"path":"a","seq":0,"status":"succeeded"},{"path":"b","seq":1,"status":"held","approval_id":"apr_2"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"run":{"id":"wfr_a1b2c3","status":"failed","mode":"live","error":"boom"},"steps":[{"path":"b","seq":2,"status":"failed","attempt":2,"error":"bad"}]}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "logs", "wfi_k3j2h1", "--follow")
	if exitCodeOf(err) != 20 {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, out)
	}
	for _, want := range []string{"Run wfr_a1b2c3 (live): running", "/approvals/apr_2", "(attempt 2): bad"} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %q:\n%s", want, out)
		}
	}
	if len(afters) != 2 || afters[0] != "" || afters[1] != "1" {
		t.Errorf("after cursors = %v", afters)
	}
	n = 0
	out, err = runCommand(t, WorkflowCmd(), "logs", "wfr_a1b2c3", "--step", "b")
	if err != nil || strings.Contains(out, "  a ") {
		t.Errorf("step filter: %q %v", out, err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "logs", "wfr_a1b2c3", "--json"); err != nil || !strings.Contains(out, `"run"`) {
		t.Errorf("json: %q %v", out, err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "logs", "no way"); err == nil {
		t.Error("a bad reference was accepted")
	}
}

func TestWorkflowCancel(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/runs/wfr_a1b2c3/cancel": jsonReply(200, `{"id":"wfr_a1b2c3","status":"cancelled"}`)})
	if out, err := runCommand(t, WorkflowCmd(), "cancel", "wfr_a1b2c3"); err != nil || !strings.Contains(out, "wfr_a1b2c3: cancelled.") {
		t.Errorf("%q %v", out, err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "cancel", "wfr_a1b2c3", "--json"); err != nil || !strings.Contains(out, `"cancelled"`) {
		t.Errorf("json: %q %v", out, err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "cancel", "wfi_k3j2h1"); err == nil {
		t.Error("an installation id was accepted as a run")
	}
}

func TestWorkflowBrowserCommandsOpenPagesAndRefuseAgents(t *testing.T) {
	ps := workflowGateway(t, routeMap{})
	out, err := runCommand(t, WorkflowCmd(), "enable", "wfi_k3j2h1")
	if err != nil || !strings.Contains(out, ps.URL+"/workflows/wfi_k3j2h1") {
		t.Errorf("enable: %q %v", out, err)
	}
	out, err = runCommand(t, WorkflowCmd(), "preapprove", "wfi_k3j2h1", "--step", "post")
	if err != nil || !strings.Contains(out, "/workflows/wfi_k3j2h1?step=post") {
		t.Errorf("preapprove: %q %v", out, err)
	}
	if out, err = runCommand(t, WorkflowCmd(), "preapprove", "wfi_k3j2h1"); err != nil || strings.Contains(out, "step=") {
		t.Errorf("preapprove all: %q %v", out, err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "enable", "bad id"); err == nil {
		t.Error("bad id accepted")
	}
	t.Setenv("ZUANO_AGENT", "1")
	for _, args := range [][]string{{"enable", "wfi_k3j2h1"}, {"preapprove", "wfi_k3j2h1"}, {"uninstall", "wfi_k3j2h1", "--yes"}, {"upgrade", "wfi_k3j2h1", "--acknowledge-major"}} {
		_, err := runCommand(t, WorkflowCmd(), args...)
		if exitCodeOf(err) != 14 || !strings.Contains(err.Error(), "passkey") {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if len(ps.hits) != 0 {
		t.Errorf("an agent refusal still reached the gateway: %v", ps.hits)
	}
}

func TestWorkflowPauseDisableUninstall(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/installations/wfi_k3j2h1/pause":   jsonReply(200, `{"id":"wfi_k3j2h1","status":"paused"}`),
		"POST /api/v1/workflows/installations/wfi_k3j2h1/disable": jsonReply(200, `{"id":"wfi_k3j2h1","status":"disabled"}`),
		"DELETE /api/v1/workflows/installations/wfi_k3j2h1":       jsonReply(204, ``),
	})
	for verb, want := range map[string]string{"pause": "wfi_k3j2h1: paused.", "disable": "wfi_k3j2h1: disabled."} {
		if out, err := runCommand(t, WorkflowCmd(), verb, "wfi_k3j2h1"); err != nil || !strings.Contains(out, want) {
			t.Errorf("%s: %q %v", verb, out, err)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "uninstall", "wfi_k3j2h1"); err == nil {
		t.Error("uninstall without confirmation succeeded")
	}
	if ps.count("DELETE /api/v1/workflows/installations/wfi_k3j2h1") != 0 {
		t.Error("uninstall reached the gateway without confirmation")
	}
	if out, err := runCommand(t, WorkflowCmd(), "uninstall", "wfi_k3j2h1", "--yes"); err != nil || !strings.Contains(out, "Uninstalled wfi_k3j2h1.") {
		t.Errorf("uninstall: %q %v", out, err)
	}
}

func TestWorkflowUpgrade(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/installations/wfi_k3j2h1/upgrade": jsonReply(200, `{"installation":{"id":"wfi_k3j2h1"},"classification":"minor","from":"1.2.0","to":"1.3.0"}`),
	})
	out, err := runCommand(t, WorkflowCmd(), "upgrade", "wfi_k3j2h1", "--to", "1.3.0")
	if err != nil || !strings.Contains(out, "Upgraded 1.2.0 to 1.3.0 (minor).") || !strings.Contains(out, "zuano workflow preapprove wfi_k3j2h1") {
		t.Fatalf("%q %v", out, err)
	}
	if b := ps.body("POST /api/v1/workflows/installations/wfi_k3j2h1/upgrade", 0); b["version"] != "1.3.0" || b["acknowledgeMajor"] != nil {
		t.Errorf("body = %v", b)
	}
	if _, err := runCommand(t, WorkflowCmd(), "upgrade", "wfi_k3j2h1", "--acknowledge-major"); err != nil {
		t.Fatal(err)
	}
	if b := ps.body("POST /api/v1/workflows/installations/wfi_k3j2h1/upgrade", 1); b["acknowledgeMajor"] != true {
		t.Errorf("major body = %v", b)
	}
	workflowGateway(t, routeMap{
		"POST /api/v1/workflows/installations/wfi_k3j2h1/upgrade": jsonReply(200, `{"installation":{},"classification":"patch","from":"1.3.0","to":"1.3.0"}`),
	})
	if out, err := runCommand(t, WorkflowCmd(), "upgrade", "wfi_k3j2h1"); err != nil || !strings.Contains(out, "Already on that version.") {
		t.Errorf("current: %q %v", out, err)
	}
}

func writeManifest(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkflowValidate(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/validate": func(ps *packServer, w http.ResponseWriter, r *http.Request) {
			if ps.count("POST /api/v1/workflows/validate") == 1 {
				_, _ = io.WriteString(w, `{"valid":true,"manifest_digest":"d1","analysis":{"maxRisk":"outward","permissions":[{"service":"slack","tool":"post_message","risk":"outward"}],"preapprovableSteps":["post"],"requiredFeatures":["workflows.enabled"]}}`)
				return
			}
			_, _ = io.WriteString(w, `{"valid":false,"issues":[{"pointer":"/steps/0/id","message":"duplicate id"},{"pointer":"","message":"no steps"}]}`)
		},
	})
	dir := writeManifest(t, `{"format":"zuano.workflow/v1","steps":[]}`)
	out, err := runCommand(t, WorkflowCmd(), "validate", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"is valid.", "highest risk outward", "slack.post_message", "needs: workflows.enabled", "steps that can be pre-approved: post"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate lacks %q:\n%s", want, out)
		}
	}
	if b := ps.body("POST /api/v1/workflows/validate", 0); b["manifest"].(map[string]any)["format"] != "zuano.workflow/v1" {
		t.Errorf("body = %v", b)
	}
	out, err = runCommand(t, WorkflowCmd(), "validate", filepath.Join(dir, "workflow.json"))
	if exitCodeOf(err) != 16 || !strings.Contains(err.Error(), "2 issue(s)") {
		t.Errorf("invalid: exit %d, %v\n%s", exitCodeOf(err), err, out)
	}
}

func TestWorkflowValidate_JSONAndUnreadableInput(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/validate": jsonReply(200, `{"valid":false,"issues":[{"pointer":"/x","message":"bad"}]}`)})
	dir := writeManifest(t, `{}`)
	out, err := runCommand(t, WorkflowCmd(), "validate", dir, "--json")
	if exitCodeOf(err) != 16 || !strings.Contains(out, `"valid":false`) {
		t.Errorf("json: exit %d %v\n%s", exitCodeOf(err), err, out)
	}
	for name, path := range map[string]string{"missing": filepath.Join(t.TempDir(), "none"), "not an object": writeManifest(t, `[1]`), "not json": writeManifest(t, `nope`)} {
		if _, err := runCommand(t, WorkflowCmd(), "validate", path); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestWorkflowDraft(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/draft": jsonReply(201, `{"custom_manifest_id":"cwf_1","manifest_digest":"d","analysis":{"maxRisk":"write","permissions":[]},"installation":{"id":"wfi_d1"}}`),
	})
	dir := writeManifest(t, `{"format":"zuano.workflow/v1"}`)
	out, err := runCommand(t, WorkflowCmd(), "draft", dir, "--name", "Triage", "--param", "channel=C1")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	b := ps.body("POST /api/v1/workflows/draft", 0)
	install, _ := b["install"].(map[string]any)
	if install["name"] != "Triage" || install["params"].(map[string]any)["channel"] != "C1" {
		t.Errorf("body = %v", b)
	}
	for _, want := range []string{"Saved workflow cwf_1.", "Draft installation wfi_d1.", ps.URL + "/workflows/wfi_d1"} {
		if !strings.Contains(out, want) {
			t.Errorf("draft lacks %q:\n%s", want, out)
		}
	}
	workflowGateway(t, routeMap{"POST /api/v1/workflows/draft": jsonReply(201, `{"custom_manifest_id":"cwf_2","analysis":{},"installation":null}`)})
	if out, err := runCommand(t, WorkflowCmd(), "draft", dir); err != nil || !strings.Contains(out, "zuano workflow draft") {
		t.Errorf("manifest only: %q %v", out, err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "draft", dir, "--param", "a=1"); err == nil {
		t.Error("--param without --name was accepted")
	}
}

func TestWorkflowDraft_InvalidManifestExits16(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/draft": jsonReply(422, `{"error":"manifest_invalid","message":"The manifest is not valid.","issues":[{"pointer":"/steps/0","message":"unknown kind"}]}`)})
	_, err := runCommand(t, WorkflowCmd(), "draft", writeManifest(t, `{}`))
	if exitCodeOf(err) != 16 || !strings.Contains(err.Error(), "/steps/0: unknown kind") {
		t.Errorf("exit %d, %v", exitCodeOf(err), err)
	}
}

func TestWorkflowSchema(t *testing.T) {
	var version string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/installations/wfi_k3j2h1": jsonReply(200, installationJSON),
		"GET /api/v1/workflows/installations/wfi_c1c1":   jsonReply(200, `{"id":"wfi_c1c1","name":"C","status":"draft","custom_manifest_id":"cwf_1"}`),
		"GET /api/v1/workflows/templates/res_invoice": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			version = r.URL.Query().Get("version")
			_, _ = io.WriteString(w, `{"input_schema":{"type":"object"},"output_schema":null}`)
		},
	})
	for args, want := range map[string]string{
		"--input":  `{"type":"object"}`,
		"--output": `null`,
		"":         `{"input":{"type":"object"},"output":null}`,
	} {
		argv := []string{"schema", "wfi_k3j2h1"}
		if args != "" {
			argv = append(argv, args)
		}
		out, err := runCommand(t, WorkflowCmd(), argv...)
		if err != nil || strings.TrimSpace(out) != want {
			t.Errorf("%q: %q %v", args, out, err)
		}
	}
	if version != "1.2.0" {
		t.Errorf("version = %q", version)
	}
	if _, err := runCommand(t, WorkflowCmd(), "schema", "wfi_c1c1"); err == nil {
		t.Error("a custom workflow has no published schema")
	}
	if _, err := runCommand(t, WorkflowCmd(), "schema", "res_invoice", "--input", "--output"); err == nil {
		t.Error("both --input and --output were accepted")
	}
}

func TestDoctorWorkflows(t *testing.T) {
	var body string
	status := 200
	ps := workflowGateway(t, routeMap{})
	ps.replace(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	c, err := approvalsClient("")
	if err != nil {
		t.Fatal(err)
	}
	check := func() (bool, string) {
		var ok bool
		var d string
		doctorWorkflows(context.Background(), c, func(name string, o bool, detail string) { ok, d = o, detail })
		return ok, d
	}
	body = `{"installations":[` + installationJSON + `]}`
	if ok, d := check(); !ok || !strings.Contains(d, "1 workflow(s), none paused") {
		t.Errorf("healthy: %v %q", ok, d)
	}
	body = `{"installations":[` + installationJSON + `,` + pausedJSON + `]}`
	if ok, d := check(); ok || !strings.Contains(d, "wfi_p4u5s6 (installer_removed)") || !strings.Contains(d, "workflow show wfi_p4u5s6") {
		t.Errorf("paused: %v %q", ok, d)
	}
	status = 404
	if ok, d := check(); !ok || !strings.Contains(d, "not available") {
		t.Errorf("unavailable: %v %q", ok, d)
	}
}
