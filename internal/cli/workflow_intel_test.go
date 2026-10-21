package cli

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

const metricsJSON = `{"installation_id":"wfi_k3j2h1","window":{"from":"2026-10-15","to":"2026-10-21"},
"total":{"runs":120,"succeeded":100,"failed":15,"dead":5,"cancelled":0,
 "executionSuccess":{"rate":0.833,"low":0.76,"high":0.89,"n":120},
 "outcome":{"verified":60,"failedVerification":10,"unverified":30,"pending":0,"success":0.857,"coverage":0.7},
 "cost":{"totalNano":2500000000,"meanNano":20000000,"p50Nano":1,"p95Nano":50000000,"perVerifiedNano":1},
 "activeMs":{"p50":1200,"p95":9000},"human":{"correct":1,"incorrect":0,"rate":1},
 "intervention":{"runs":6,"rate":0.05},"quality":{"score":0.91,"judge":{"mean":0.9,"n":10},"human":1}},
"groups":[{"key":"1.2.0","summary":{"runs":5,"succeeded":5,"failed":0,"dead":0,"cancelled":0,"executionSuccess":{"rate":1,"low":null,"high":null,"n":5},"outcome":{"verified":0,"failedVerification":0,"unverified":5,"pending":0,"success":null,"coverage":null},"cost":{"totalNano":0,"meanNano":null,"p95Nano":null},"activeMs":{"p50":null,"p95":null},"intervention":{"runs":0,"rate":null},"quality":{"score":null}}}],
"truncated":false}`

func TestWorkflowQuality(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/wfi_k3j2h1/metrics": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, metricsJSON)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "quality", "wfi_k3j2h1", "--window", "14d", "--version", "1.2.0", "--group-by", "version")
	if err != nil || q != "days=14&group_by=version&version=1.2.0" {
		t.Fatalf("%q %q %v", q, out, err)
	}
	for _, want := range []string{"2026-10-15 to 2026-10-21", "execution success  83.3% (76.0% to 89.0%)", "outcome success    85.7% of 70 decided; coverage 70.0%", "quality            91.0%", "mean $0.0200", "total $2.5000", "p95 9.0s", "1.2.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("quality lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "quality", "wfi_k3j2h1", "--window", "soon"); err == nil {
		t.Error("a bad window was accepted")
	}
	if _, err := runCommand(t, WorkflowCmd(), "quality", "wfi_k3j2h1", "--group-by", "colour"); err == nil {
		t.Error("a bad grouping was accepted")
	}
	if out, err := runCommand(t, WorkflowCmd(), "quality", "wfi_k3j2h1", "--json"); err != nil || !strings.Contains(out, `"installation_id"`) {
		t.Errorf("json: %q %v", out, err)
	}
}

func TestWorkflowOutcome(t *testing.T) {
	run := func(outcome string) string {
		return `{"run":{"id":"wfr_a1b2c3","status":"succeeded","outcome":"` + outcome + `","verification":{"required":2,"passed":1,"failed":1,"pending":0,"skipped":0,"expired":0,"checks":[{"id":"ck1","kind":"output","required":true,"status":"passed","score":null},{"id":"ck2","kind":"judge","required":false,"status":"failed","score":0.4}]}}}`
	}
	workflowGateway(t, routeMap{"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, run("verified"))})
	out, err := runCommand(t, WorkflowCmd(), "outcome", "wfr_a1b2c3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"outcome verified", "1 passed, 1 failed", "output", "judge", "(optional)", "score 40.0%"} {
		if !strings.Contains(out, want) {
			t.Errorf("outcome lacks %q:\n%s", want, out)
		}
	}
	workflowGateway(t, routeMap{"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, run("failed_verification"))})
	if _, err := runCommand(t, WorkflowCmd(), "outcome", "wfr_a1b2c3"); exitCodeOf(err) != 20 {
		t.Errorf("failed verification: exit %d, %v", exitCodeOf(err), err)
	}
	workflowGateway(t, routeMap{"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, run("unverified"))})
	if _, err := runCommand(t, WorkflowCmd(), "outcome", "wfr_a1b2c3"); exitCodeOf(err) != 1 {
		t.Errorf("unverified: exit %d, %v", exitCodeOf(err), err)
	}
	workflowGateway(t, routeMap{"GET /api/v1/workflows/runs/wfr_a1b2c3": jsonReply(200, run("pending"))})
	if _, err := runCommand(t, WorkflowCmd(), "outcome", "wfr_a1b2c3", "--wait", "1ms"); exitCodeOf(err) != 17 {
		t.Errorf("pending: exit %d, %v", exitCodeOf(err), err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "outcome", "nope"); err == nil {
		t.Error("a bad run id was accepted")
	}
}

func TestWorkflowCompare(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/wfi_k3j2h1/compare": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"breached":[{"metric":"cost_mean","maxDegradation":0.3}],"satisfied":[],"insufficient":[{"metric":"latency_p95","maxDegradation":0.5}],
			 "comparison":{"verdict":"worse","metrics":{"execution_success":{"baseline":0.95,"candidate":0.9,"delta":-0.05,"ci95":[-0.1,0],"samples":[60,55]},"cost_mean":{"baseline":null,"candidate":null,"delta":null,"ci95":null,"samples":[0,0]}}}}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "compare", "wfi_k3j2h1", "--baseline", "1.2.0", "--candidate", "1.3.0")
	if err != nil || q != "baseline=1.2.0&candidate=1.3.0&days=7" {
		t.Fatalf("%q %q %v", q, out, err)
	}
	for _, want := range []string{"1.3.0 vs 1.2.0: worse", "execution_success", "-0.050", "runs 60/55", "breached: cost_mean", "too few runs to judge: latency_p95"} {
		if !strings.Contains(out, want) {
			t.Errorf("compare lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "compare", "wfi_k3j2h1", "--baseline", "1.2.0", "--candidate", "1.3.0", "--fail-on-breach"); exitCodeOf(err) != 21 {
		t.Errorf("fail-on-breach: exit %d, %v", exitCodeOf(err), err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "compare", "wfi_k3j2h1", "--baseline", "1.2.0"); err == nil {
		t.Error("--candidate is required")
	}
}

func TestWorkflowEvalCI(t *testing.T) {
	gate := func(status string, code int) routeMap {
		body := fmt.Sprintf(`{"status":%q,"exit_code":%d,"baseline":"1.2.0","suites":[{"id":"s1","name":"core","verdict":%q,"pass_rate":0.9,"run_id":"r","report":{}}]}`, status, code, status)
		return routeMap{"POST /api/v1/workflows/wfi_k3j2h1/eval-gate": jsonReply(200, body)}
	}
	dir := writeManifest(t, `{"format":"f","steps":[]}`)
	ps := workflowGateway(t, gate("passed", 0))
	out, err := runCommand(t, WorkflowCmd(), "eval", "ci", "wfi_k3j2h1", "--manifest", dir, "--suite", "s1")
	if err != nil || !strings.Contains(out, "Gate passed against version 1.2.0") || !strings.Contains(out, "pass rate 90.0%") {
		t.Fatalf("%q %v", out, err)
	}
	b := ps.body("POST /api/v1/workflows/wfi_k3j2h1/eval-gate", 0)
	if b["manifest"].(map[string]any)["format"] != "f" || b["suite_ids"].([]any)[0] != "s1" {
		t.Errorf("body = %v", b)
	}
	for status, code := range map[string]int{"failed": 20, "regressed": 21} {
		workflowGateway(t, gate(status, code))
		if _, err := runCommand(t, WorkflowCmd(), "eval", "ci", "wfi_k3j2h1", "--manifest", dir); exitCodeOf(err) != code {
			t.Errorf("%s: exit %d, %v", status, exitCodeOf(err), err)
		}
	}
	workflowGateway(t, gate("odd", 7))
	if _, err := runCommand(t, WorkflowCmd(), "eval", "ci", "wfi_k3j2h1", "--manifest", dir); err == nil || exitCodeOf(err) == 7 {
		t.Errorf("an unknown exit code was passed on: %v", err)
	}
	workflowGateway(t, routeMap{"POST /api/v1/workflows/wfi_k3j2h1/eval-gate": jsonReply(422, `{"error":"manifest_invalid","message":"The manifest is not a valid workflow.","issues":[{"pointer":"/steps","message":"bad"}]}`)})
	if _, err := runCommand(t, WorkflowCmd(), "eval", "ci", "wfi_k3j2h1", "--manifest", dir); exitCodeOf(err) != 16 {
		t.Errorf("invalid manifest: exit %d, %v", exitCodeOf(err), err)
	}
	empty := t.TempDir()
	if _, err := runCommand(t, WorkflowCmd(), "eval", "ci", "wfi_k3j2h1", "--manifest", filepath.Join(empty, "none.json")); err == nil {
		t.Error("a missing manifest was accepted")
	}
}

func TestWorkflowReplay(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/runs/wfr_a1b2c3/replay":          jsonReply(201, `{"id":"rpl_1","kind":"replay","run_id":"wfr_new1","status":"succeeded"}`),
		"POST /api/v1/workflows/runs/wfr_a1b2c3/rerun-from-step": jsonReply(201, `{"id":"rpl_2","kind":"rerun_from_step","run_id":"wfr_new2","status":"running"}`),
	})
	out, err := runCommand(t, WorkflowCmd(), "replay", "wfr_a1b2c3", "--reuse-model-outputs", "--model", "a=b")
	if err != nil || !strings.Contains(out, "Replay rpl_1 started as run wfr_new1: succeeded") || !strings.Contains(out, "workflow logs wfr_new1 --follow") {
		t.Fatalf("%q %v", out, err)
	}
	b := ps.body("POST /api/v1/workflows/runs/wfr_a1b2c3/replay", 0)
	if b["reuse_model_outputs"] != true || b["model_map"].(map[string]any)["a"] != "b" {
		t.Errorf("replay body = %v", b)
	}
	if _, err := runCommand(t, WorkflowCmd(), "replay", "wfr_a1b2c3", "--from-step", "pay", "--version", "1.3.0", "--live-reads"); err != nil {
		t.Fatal(err)
	}
	b = ps.body("POST /api/v1/workflows/runs/wfr_a1b2c3/rerun-from-step", 0)
	if b["from_step_path"] != "pay" || b["version"] != "1.3.0" || b["live_reads"] != true {
		t.Errorf("rerun body = %v", b)
	}
	for _, args := range [][]string{
		{"replay", "wfr_a1b2c3", "--version", "1.3.0"},
		{"replay", "wfr_a1b2c3", "--from-step", "p", "--version", "1", "--draft", "d"},
		{"replay", "wfr_a1b2c3", "--model", "nope"},
		{"replay", "bad id"},
	} {
		if _, err := runCommand(t, WorkflowCmd(), args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

func TestWorkflowReplay_IncompatibleListsSteps(t *testing.T) {
	workflowGateway(t, routeMap{"POST /api/v1/workflows/runs/wfr_a1b2c3/rerun-from-step": jsonReply(409, `{"error":"replay_incompatible","message":"That version cannot continue the run.","issues":[{"pointer":"/steps/pay","message":"the output shape changed"}]}`)})
	_, err := runCommand(t, WorkflowCmd(), "replay", "wfr_a1b2c3", "--from-step", "pay", "--version", "2.0.0")
	if err == nil || !strings.Contains(err.Error(), "/steps/pay: the output shape changed") {
		t.Errorf("err = %v", err)
	}
}

func TestWorkflowResume_OpensThePage(t *testing.T) {
	ps := workflowGateway(t, routeMap{})
	out, err := runCommand(t, WorkflowCmd(), "resume", "wfr_a1b2c3", "--no-browser")
	if err != nil || !strings.Contains(out, ps.URL+"/workflows/runs/wfr_a1b2c3") {
		t.Fatalf("%q %v", out, err)
	}
	t.Setenv("ZUANO_AGENT", "1")
	if _, err := runCommand(t, WorkflowCmd(), "resume", "wfr_a1b2c3", "--no-browser"); exitCodeOf(err) != 14 {
		t.Errorf("agent: exit %d, %v", exitCodeOf(err), err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "resume", "bad"); err == nil {
		t.Error("a bad run id was accepted")
	}
}
