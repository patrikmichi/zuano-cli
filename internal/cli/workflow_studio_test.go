package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeDoc(t *testing.T, s string) manifestDoc {
	t.Helper()
	var m manifestDoc
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func opNames(ops []map[string]any) string {
	var names []string
	for _, o := range ops {
		names = append(names, o["op"].(string))
	}
	return strings.Join(names, ",")
}

func TestOpsFromManifestDiff(t *testing.T) {
	base := `{"format":"f","name":"a","input":{"type":"object"},"params":[{"key":"p1"},{"key":"p2"}],
	 "invocations":{"mcp":{"on":true}},
	 "steps":[{"id":"s1","kind":"tool","tool":"x"},{"id":"loop","kind":"foreach","steps":[{"id":"inner","kind":"tool","tool":"y"}]}]}`
	cases := []struct {
		name, next, want string
	}{
		{"same", base, ""},
		{"meta", strings.Replace(base, `"name":"a"`, `"name":"b"`, 1), "setMeta"},
		{"input", strings.Replace(base, `"type":"object"`, `"type":"object","required":[]`, 1), "setInput"},
		{"param removed and added", strings.Replace(base, `{"key":"p2"}`, `{"key":"p3"}`, 1), "removeParam,setParam"},
		{"invocation", strings.Replace(base, `"mcp":{"on":true}`, `"mcp":{"on":false},"api":{}`, 1), "setInvocation,setInvocation"},
		{"step setting", strings.Replace(base, `"tool":"x"`, `"tool":"z"`, 1), "updateStep"},
		{"nested step setting", strings.Replace(base, `"tool":"y"`, `"tool":"w"`, 1), "updateStep"},
		{"step added", strings.Replace(base, `"steps":[{"id":"s1"`, `"steps":[{"id":"s0","kind":"tool"},{"id":"s1"`, 1), "removeStep,removeStep,addStep,addStep,addStep,addStep"},
	}
	for _, c := range cases {
		ops, err := opsFromManifestDiff(decodeDoc(t, base), decodeDoc(t, c.next))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := opNames(ops); got != c.want {
			t.Errorf("%s: ops = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestOpsFromManifestDiff_PatchPaths(t *testing.T) {
	prev := decodeDoc(t, `{"steps":[{"id":"a","kind":"tool","args":{"x":1,"y":2}}]}`)
	next := decodeDoc(t, `{"steps":[{"id":"a","kind":"tool","args":{"x":3},"on_error":"stop"}]}`)
	ops, err := opsFromManifestDiff(prev, next)
	if err != nil || len(ops) != 1 {
		t.Fatalf("%v %v", ops, err)
	}
	raw, _ := json.Marshal(ops[0]["patch"])
	if string(raw) != `[{"op":"remove","path":"/args/y"},{"op":"replace","path":"/args/x","value":3},{"op":"add","path":"/on_error","value":"stop"}]` {
		t.Errorf("patch = %s", raw)
	}
}

func TestOpsFromManifestDiff_Refusals(t *testing.T) {
	good := decodeDoc(t, `{"steps":[]}`)
	for name, doc := range map[string]string{
		"duplicate id": `{"steps":[{"id":"a","kind":"tool"},{"id":"a","kind":"tool"}]}`,
		"bad id":       `{"steps":[{"id":"A b","kind":"tool"}]}`,
		"bad field":    `{"steps":[],"bad field":1}`,
	} {
		if _, err := opsFromManifestDiff(good, decodeDoc(t, doc)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestOpsFromManifestDiff_ParallelAndMove(t *testing.T) {
	prev := decodeDoc(t, `{"steps":[{"id":"p","kind":"parallel","branches":{"a":{"steps":[{"id":"x","kind":"tool"}]},"b":{"steps":[]}}}]}`)
	next := decodeDoc(t, `{"steps":[{"id":"p","kind":"parallel","branches":{"a":{"steps":[]},"b":{"steps":[{"id":"x","kind":"tool"}]}}}]}`)
	ops, err := opsFromManifestDiff(prev, next)
	if err != nil {
		t.Fatal(err)
	}
	var branchAdd map[string]any
	for _, o := range ops {
		if o["op"] == "addStep" && o["step"].(map[string]any)["id"] == "x" {
			branchAdd = o["at"].(map[string]any)
		}
	}
	if branchAdd["parentId"] != "p" || branchAdd["branch"] != "b" {
		t.Errorf("ops = %v", ops)
	}
}

const draftJSON = `{"id":"wfd_1","workflow_id":"wcm_abc12","revision":3,"manifest":{"format":"f","steps":[{"id":"s1","kind":"tool","tool":"x"}]},"validation":{"valid":true,"analysis":{"maxRisk":"read","permissions":[{"service":"slack","tool":"post","risk":"read"}]}}}`

func TestWorkflowPullAndPush(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"GET /api/v1/workflows/studio/drafts/wcm_abc12":              jsonReply(200, draftJSON),
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/samples":      jsonReply(200, `{"samples":[{"id":"s","name":"Big one/2","invocation":"api","payload":{"n":1}}],"next":""}`),
		"PUT /api/v1/workflows/studio/drafts/wcm_abc12":              jsonReply(200, `{"draft":`+strings.Replace(strings.Replace(draftJSON, `"revision":3`, `"revision":4`, 1), `"tool":"x"`, `"tool":"z"`, 1)+`,"inverse":[]}`),
		"POST /api/v1/workflows/studio/drafts/wcm_abc12/checkpoints": jsonReply(201, `{}`),
	})
	dir := filepath.Join(t.TempDir(), "wf")
	out, err := runCommand(t, WorkflowCmd(), "pull", "wcm_abc12", "--dir", dir)
	if err != nil || !strings.Contains(out, "revision 3") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "samples", "Big_one_2.json")); err != nil {
		t.Errorf("sample file: %v", err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "push", dir); err != nil {
		t.Fatal(err)
	}
	if out, err := runCommand(t, WorkflowCmd(), "push", dir); err != nil || !strings.Contains(out, "No changes since revision 3") {
		t.Fatalf("unchanged push: %q %v", out, err)
	}
	local := decodeDoc(t, `{"format":"f","steps":[{"id":"s1","kind":"tool","tool":"z"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), marshalManifest(local), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runCommand(t, WorkflowCmd(), "push", dir, "--message", "tuned")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	body := ps.body("PUT /api/v1/workflows/studio/drafts/wcm_abc12", 0)
	if body["baseRevision"] != float64(3) || body["source"] != "json" || len(body["ops"].([]any)) != 1 {
		t.Errorf("body = %v", body)
	}
	if cp := ps.body("POST /api/v1/workflows/studio/drafts/wcm_abc12/checkpoints", 0); cp["name"] != "tuned" || cp["baseRevision"] != float64(4) {
		t.Errorf("checkpoint = %v", cp)
	}
	for _, want := range []string{"Saved wcm_abc12 as revision 4.", "slack.post"} {
		if !strings.Contains(out, want) {
			t.Errorf("push lacks %q:\n%s", want, out)
		}
	}
	st, err := readStudioState(dir)
	if err != nil || st.Revision != 4 {
		t.Errorf("state = %+v %v", st, err)
	}
}

func TestWorkflowPull_KeepsUnpushedEdits(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/studio/drafts/wcm_abc12":         jsonReply(200, draftJSON),
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/samples": jsonReply(200, `{"samples":[],"next":""}`),
	})
	dir := t.TempDir()
	if _, err := runCommand(t, WorkflowCmd(), "pull", "wcm_abc12", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(`{"format":"changed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "pull", "wcm_abc12", "--dir", dir); err == nil || !strings.Contains(err.Error(), "not pushed") {
		t.Errorf("err = %v", err)
	}
	if _, err := runCommand(t, WorkflowCmd(), "pull", "wcm_abc12", "--dir", dir, "--force"); err != nil {
		t.Errorf("force: %v", err)
	}
}

func TestWorkflowPush_ConflictExits25(t *testing.T) {
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/studio/drafts/wcm_abc12":         jsonReply(200, draftJSON),
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/samples": jsonReply(200, `{"samples":[],"next":""}`),
		"PUT /api/v1/workflows/studio/drafts/wcm_abc12":         jsonReply(409, `{"error":"draft_conflict","message":"The draft is at revision 5; reload before saving.","issues":[{"pointer":"/revision","message":"5"}]}`),
	})
	dir := t.TempDir()
	if _, err := runCommand(t, WorkflowCmd(), "pull", "wcm_abc12", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(`{"format":"g","steps":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runCommand(t, WorkflowCmd(), "push", dir)
	if exitCodeOf(err) != 25 || !strings.Contains(err.Error(), "revision 5") {
		t.Errorf("exit %d, %v", exitCodeOf(err), err)
	}
}

func TestWorkflowPush_NewDraftAndInvalid(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/studio/drafts": jsonReply(201, `{"id":"wfd_9","workflow_id":"wcm_new01","revision":0,"manifest":{"format":"f"},"validation":{"valid":false,"issues":[{"pointer":"/steps","message":"needs a step"}]}}`),
	})
	dir := writeManifest(t, `{"format":"f"}`)
	out, err := runCommand(t, WorkflowCmd(), "push", dir)
	if exitCodeOf(err) != 16 || !strings.Contains(out, "/steps: needs a step") {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, out)
	}
	if b := ps.body("POST /api/v1/workflows/studio/drafts", 0); b["source"] != "import" {
		t.Errorf("body = %v", b)
	}
	if st, err := readStudioState(dir); err != nil || st.WorkflowID != "wcm_new01" {
		t.Errorf("state = %+v %v", st, err)
	}
}

func TestWorkflowDiffCommand(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/diff": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"from":"v:1.2.0","to":"draft","diff":{"stepsAdded":["s2"],"permissionsAdded":[{"service":"slack","tool":"post","risk":"outward"}],"connectorsAdded":["slack"],"inputChanged":false,"outputChanged":false,"invocationsChanged":false},"input_change":"none","output_change":"compatible","upgrade_class":"minor","reasons":["a step was added"]}`)
		},
	})
	out, err := runCommand(t, WorkflowCmd(), "diff", "wcm_abc12", "--from", "v:1.2.0")
	if err != nil || q != "from=v%3A1.2.0&to=draft" {
		t.Fatalf("%q %q %v", q, out, err)
	}
	for _, want := range []string{"steps added", "s2", "permission added     outward slack.post", "version bump         minor", "a step was added"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "diff", "wcm_abc12"); err == nil {
		t.Error("--from is required")
	}
}

func TestWorkflowSimulate(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"POST /api/v1/workflows/studio/drafts/wcm_abc12/simulate": jsonReply(202, `{"batch":"b","runs":[{"name":"case","run_id":"wfr_1","error":null},{"name":"bad","run_id":null,"error":{"code":"input_invalid","message":"n is required"}}]}`),
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/simulate":  jsonReply(200, `{"runs":[{"id":"wfr_1","status":"succeeded","steps":2}],"coverage":{"edges":[{"step_id":"s2","label":"else","exercised":false}],"exercised":1,"total":2}}`),
	})
	dir := t.TempDir()
	in := filepath.Join(dir, "case.json")
	if err := os.WriteFile(in, []byte(`{"n":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runCommand(t, WorkflowCmd(), "simulate", "wcm_abc12", "--input", in, "--sample", "saved")
	if exitCodeOf(err) != 18 {
		t.Fatalf("exit %d, %v\n%s", exitCodeOf(err), err, out)
	}
	for _, want := range []string{"case", "wfr_1", "succeeded", "Branches covered: 1 of 2", "not taken: s2 else", "bad: refused (input_invalid)"} {
		if !strings.Contains(out, want) {
			t.Errorf("simulate lacks %q:\n%s", want, out)
		}
	}
	b := ps.body("POST /api/v1/workflows/studio/drafts/wcm_abc12/simulate", 0)
	if b["samples"].([]any)[0] != "saved" || b["inputs"].([]any)[0].(map[string]any)["name"] != "case" {
		t.Errorf("body = %v", b)
	}
	if _, err := runCommand(t, WorkflowCmd(), "simulate", "wcm_abc12"); err == nil {
		t.Error("no input was accepted")
	}
}

func TestWorkflowSimulate_FailedAndTimeout(t *testing.T) {
	start := jsonReply(202, `{"batch":"b","runs":[{"name":"fixture","run_id":"wfr_1","error":null}]}`)
	workflowGateway(t, routeMap{
		"POST /api/v1/workflows/studio/drafts/wcm_abc12/simulate": start,
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/simulate":  jsonReply(200, `{"runs":[{"id":"wfr_1","status":"failed","steps":1}],"coverage":null}`),
	})
	if _, err := runCommand(t, WorkflowCmd(), "simulate", "wcm_abc12", "--fixtures"); exitCodeOf(err) != 20 {
		t.Errorf("failed run: exit %d, %v", exitCodeOf(err), err)
	}
	workflowGateway(t, routeMap{
		"POST /api/v1/workflows/studio/drafts/wcm_abc12/simulate": start,
		"GET /api/v1/workflows/studio/drafts/wcm_abc12/simulate":  jsonReply(200, `{"runs":[{"id":"wfr_1","status":"running","steps":0}],"coverage":null}`),
	})
	if _, err := runCommand(t, WorkflowCmd(), "simulate", "wcm_abc12", "--fixtures", "--wait", "1ms"); exitCodeOf(err) != 17 {
		t.Errorf("timeout: exit %d, %v", exitCodeOf(err), err)
	}
}

func TestWorkflowPublish(t *testing.T) {
	ps := workflowGateway(t, routeMap{
		"GET /api/v1/workflows/studio/drafts/wcm_abc12":          jsonReply(200, draftJSON),
		"POST /api/v1/workflows/studio/drafts/wcm_abc12/publish": jsonReply(201, `{"resource_id":"r","version_id":"v","version":"1.3.0","upgrade_class":"minor","gates":[{"gate":"manifest","status":"passed"},{"gate":"evals","status":"skipped"}]}`),
	})
	out, err := runCommand(t, WorkflowCmd(), "publish", "wcm_abc12", "--version", "1.3.0", "--notes", "n")
	if err != nil {
		t.Fatal(err)
	}
	b := ps.body("POST /api/v1/workflows/studio/drafts/wcm_abc12/publish", 0)
	if b["revision"] != float64(3) || b["version"] != "1.3.0" || b["channel"] != "stable" || b["notes"] != "n" {
		t.Errorf("body = %v", b)
	}
	for _, want := range []string{"Published wcm_abc12 1.3.0 from revision 3", "manifest", "evals"} {
		if !strings.Contains(out, want) {
			t.Errorf("publish lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, WorkflowCmd(), "publish", "wcm_abc12", "--revision", "2", "--version", "1.3.1", "--override-eval-gate"); err != nil {
		t.Fatal(err)
	}
	if b := ps.body("POST /api/v1/workflows/studio/drafts/wcm_abc12/publish", 1); b["revision"] != float64(2) || b["override_eval_gate"] != true {
		t.Errorf("body = %v", b)
	}
}
