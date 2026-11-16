package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/evidence/evidencetest"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/signing/signingtest"
)

func complianceGateway(t *testing.T, routes routeMap) *packServer {
	t.Helper()
	ps := packGateway(t, routes)
	oldP, oldW := exportPoll, exportWait
	exportPoll = time.Millisecond
	t.Cleanup(func() { exportPoll, exportWait = oldP, oldW })
	return ps
}

func TestCompliancePacksAndControls(t *testing.T) {
	var cq string
	complianceGateway(t, routeMap{
		"GET /api/compliance/packs": jsonReply(200, `{"packs":[{"id":"soc2","name":"SOC 2","version":"1","coverage":{"covered":30,"partial":5,"gaps":3,"total":38}}]}`),
		"GET /api/compliance/packs/soc2/controls": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			cq = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"controls":[{"id":"CC6.1","title":"Access","status":"covered","sources":["access_snapshot"],"records":12},{"id":"CC7.3","title":"Incidents","status":"gap","gapNote":"no incident register entries"}]}`)
		},
	})
	out, err := runCommand(t, ComplianceCmd(), "packs", "--from", "2026-07-01", "--to", "2026-10-01")
	if err != nil || !strings.Contains(out, "SOC 2") || !strings.Contains(out, "38") {
		t.Errorf("packs: %q %v", out, err)
	}
	out, err = runCommand(t, ComplianceCmd(), "controls", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01")
	if err != nil || !strings.Contains(out, "CC6.1") || !strings.Contains(out, "CC7.3") || cq != "from=2026-07-01&to=2026-10-01" {
		t.Errorf("controls: %q %q %v", out, cq, err)
	}
	out, err = runCommand(t, ComplianceCmd(), "controls", "--pack", "soc2", "--gaps")
	if err != nil || strings.Contains(out, "CC6.1") || !strings.Contains(out, "no incident register entries") {
		t.Errorf("gaps: %q %v", out, err)
	}
	for _, args := range [][]string{{"packs", "--json"}, {"controls", "--pack", "soc2", "--json"}} {
		if out, err := runCommand(t, ComplianceCmd(), args...); err != nil || !strings.Contains(out, "{") {
			t.Errorf("%v: %q %v", args, out, err)
		}
	}
	for _, args := range [][]string{{"controls"}, {"controls", "--pack", "soc2", "--from", "July"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestComplianceControlsAllCovered(t *testing.T) {
	complianceGateway(t, routeMap{"GET /api/compliance/packs/soc2/controls": jsonReply(200, `{"controls":[{"id":"CC6.1","status":"covered"}]}`)})
	if out, err := runCommand(t, ComplianceCmd(), "controls", "--pack", "soc2", "--gaps"); err != nil || !strings.Contains(out, "No controls to show.") {
		t.Errorf("%q %v", out, err)
	}
}

func exportRoutes(job string, bundle []byte) routeMap {
	return routeMap{
		"POST /api/compliance/exports": jsonReply(202, `{"export_id":"exp_1","job_id":"job_1"}`),
		"GET /api/v1/jobs/job_1":       jsonReply(200, job),
		"GET /api/compliance/exports/exp_1/download": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"links":[{"volume":1,"url":"http://`+r.Host+`/blob/b.zip","filename":"../../evil/soc2.zip"}]}`)
		},
		"GET /blob/b.zip": func(_ *packServer, w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(bundle) },
	}
}

func TestComplianceExportWaitsAndDownloads(t *testing.T) {
	bundle := []byte("zip bytes")
	ps := complianceGateway(t, exportRoutes(`{"job":{"id":"job_1","status":"succeeded"}}`, bundle))
	dir := t.TempDir()
	out := filepath.Join(dir, "bundle.zip")
	text, err := runCommand(t, ComplianceCmd(), "export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--wait", "--out", out)
	if err != nil || !strings.Contains(text, "Export exp_1 is ready.") || !strings.Contains(text, "compliance verify") {
		t.Fatalf("%q %v", text, err)
	}
	if got, _ := os.ReadFile(out); string(got) != "zip bytes" {
		t.Errorf("bundle = %q", got)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if b := ps.body("POST /api/compliance/exports", 0); b["pack"] != "soc2" || b["from"] != "2026-07-01" || b["to"] != "2026-10-01" {
		t.Errorf("body = %v", b)
	}
	// A directory as --out takes the file name the gateway gave, without its path.
	if _, err := runCommand(t, ComplianceCmd(), "export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--wait", "--out", dir+string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "soc2.zip")); err != nil {
		t.Errorf("the gateway's file name was not used: %v", err)
	}
	if text, err := runCommand(t, ComplianceCmd(), "export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--wait"); err != nil || strings.Contains(text, "Wrote") {
		t.Errorf("no --out: %q %v", text, err)
	}
}

func TestComplianceExportWithoutWaitAndExitCodes(t *testing.T) {
	ps := complianceGateway(t, exportRoutes(`{"job":{"id":"job_1","status":"running"}}`, nil))
	args := []string{"export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01"}
	out, err := runCommand(t, ComplianceCmd(), args...)
	if err != nil || !strings.Contains(out, "Export exp_1 started (job_1)") || ps.count("GET /api/v1/jobs/job_1") != 0 {
		t.Errorf("no wait: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), append(args, "--json")...); err != nil || !strings.Contains(out, "export_id") {
		t.Errorf("json: %q %v", out, err)
	}
	if _, err := runCommand(t, ComplianceCmd(), append(args, "--wait", "--wait-timeout", "5ms")...); exitCodeOf(err) != 17 {
		t.Errorf("timeout: exit %d, %v", exitCodeOf(err), err)
	}
	complianceGateway(t, exportRoutes(`{"job":{"id":"job_1","status":"failed","error":"render failed"}}`, nil))
	if _, err := runCommand(t, ComplianceCmd(), append(args, "--wait")...); exitCodeOf(err) != 20 || !strings.Contains(err.Error(), "render failed") {
		t.Errorf("failed: %v", err)
	}
	for name, a := range map[string][]string{
		"no pack":     {"export", "--from", "2026-07-01", "--to", "2026-10-01"},
		"bad day":     {"export", "--pack", "soc2", "--from", "x", "--to", "2026-10-01"},
		"reversed":    {"export", "--pack", "soc2", "--from", "2026-10-01", "--to", "2026-07-01"},
		"out no wait": {"export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--out", "x.zip"},
	} {
		if _, err := runCommand(t, ComplianceCmd(), a...); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	t.Setenv("ZUANO_AGENT", "1")
	if _, err := runCommand(t, ComplianceCmd(), args...); exitCodeOf(err) != 10 {
		t.Errorf("agent: %v", err)
	}
}

func TestComplianceExportRefusesBadDownloads(t *testing.T) {
	ok := `{"job":{"status":"succeeded"}}`
	out := filepath.Join(t.TempDir(), "b.zip")
	args := []string{"export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--wait", "--out", out}

	routes := exportRoutes(ok, []byte("x"))
	routes["GET /api/compliance/exports/exp_1/download"] = jsonReply(200, `{"links":[{"volume":1,"url":"http://evil.example/b.zip","filename":"b.zip"}]}`)
	complianceGateway(t, routes)
	if _, err := runCommand(t, ComplianceCmd(), args...); err == nil || !strings.Contains(err.Error(), "neither https nor on the gateway") {
		t.Errorf("foreign address: %v", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a bundle from a foreign address was kept")
	}

	routes = exportRoutes(ok, []byte("x"))
	routes["GET /api/compliance/exports/exp_1/download"] = jsonReply(200, `{"links":[]}`)
	complianceGateway(t, routes)
	if _, err := runCommand(t, ComplianceCmd(), args...); err == nil || !strings.Contains(err.Error(), "no download address") {
		t.Errorf("no links: %v", err)
	}

	routes = exportRoutes(ok, []byte("x"))
	routes["GET /blob/b.zip"] = jsonReply(404, `{}`)
	complianceGateway(t, routes)
	if _, err := runCommand(t, ComplianceCmd(), args...); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404: %v", err)
	}

	routes = exportRoutes(ok, []byte("x"))
	routes["GET /blob/b.zip"] = func(_ *packServer, w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}
	complianceGateway(t, routes)
	if _, err := runCommand(t, ComplianceCmd(), args...); err == nil {
		t.Error("a redirect was followed")
	}

	routes = exportRoutes(ok, nil)
	routes["POST /api/compliance/exports"] = jsonReply(202, `{}`)
	complianceGateway(t, routes)
	if _, err := runCommand(t, ComplianceCmd(), args...); err == nil || !strings.Contains(err.Error(), "did not say which export") {
		t.Errorf("no id: %v", err)
	}
}

func TestComplianceExportDownloadsEveryVolume(t *testing.T) {
	ok := `{"job":{"status":"succeeded"}}`
	links := func(l string) func(*packServer, http.ResponseWriter, *http.Request) {
		return func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.ReplaceAll(l, "HOST", r.Host))
		}
	}
	routes := exportRoutes(ok, nil)
	routes["GET /api/compliance/exports/exp_1/download"] = links(`{"links":[
	  {"volume":2,"url":"http://HOST/blob/2","filename":"acme-exp-002.zip"},
	  {"volume":1,"url":"http://HOST/blob/1","filename":"acme-exp-001.zip"}]}`)
	routes["GET /blob/1"] = func(_ *packServer, w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "volume one") }
	routes["GET /blob/2"] = func(_ *packServer, w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "volume two") }
	routes["GET /blob/missing"] = jsonReply(404, `{}`)
	complianceGateway(t, routes)
	base := []string{"export", "--pack", "soc2", "--from", "2026-07-01", "--to", "2026-10-01", "--wait"}

	dir := t.TempDir()
	out, err := runCommand(t, ComplianceCmd(), append(base, "--out", dir+string(filepath.Separator))...)
	first, second := filepath.Join(dir, "acme-exp-001.zip"), filepath.Join(dir, "acme-exp-002.zip")
	if err != nil || !strings.Contains(out, "compliance verify "+first+" "+second) {
		t.Fatalf("%q %v", out, err)
	}
	for path, want := range map[string]string{first: "volume one", second: "volume two"} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("%s = %q", path, got)
		}
	}
	if _, err := runCommand(t, ComplianceCmd(), append(base, "--out", filepath.Join(t.TempDir(), "bundle.zip"))...); err == nil || !strings.Contains(err.Error(), "2 volumes") {
		t.Errorf("one file name for two volumes: %v", err)
	}

	for name, body := range map[string]string{
		"a gap":              `{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":"a-001.zip"},{"volume":3,"url":"http://HOST/blob/2","filename":"a-003.zip"}]}`,
		"a repeated volume":  `{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":"a.zip"},{"volume":1,"url":"http://HOST/blob/2","filename":"b.zip"}]}`,
		"a repeated name":    `{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":"a.zip"},{"volume":2,"url":"http://HOST/blob/2","filename":"x/a.zip"}]}`,
		"a foreign volume":   `{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":"a.zip"},{"volume":2,"url":"http://evil.example/2","filename":"b.zip"}]}`,
		"a volume that fails": `{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":"a.zip"},{"volume":2,"url":"http://HOST/blob/missing","filename":"b.zip"}]}`,
	} {
		routes["GET /api/compliance/exports/exp_1/download"] = links(body)
		complianceGateway(t, routes)
		target := t.TempDir()
		if _, err := runCommand(t, ComplianceCmd(), append(base, "--out", target+string(filepath.Separator))...); err == nil {
			t.Errorf("%s was accepted", name)
		}
		if left, _ := os.ReadDir(target); len(left) != 0 {
			t.Errorf("%s left %d file(s) behind", name, len(left))
		}
	}

	// A file name that names a directory is replaced, never joined to the target.
	routes["GET /api/compliance/exports/exp_1/download"] = links(`{"links":[{"volume":1,"url":"http://HOST/blob/1","filename":".."}]}`)
	complianceGateway(t, routes)
	target := t.TempDir()
	if _, err := runCommand(t, ComplianceCmd(), append(base, "--out", target+string(filepath.Separator))...); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "exp_1-001.zip")); err != nil {
		t.Errorf("the replacement name was not used: %v", err)
	}
}

func TestComplianceExportsSchedulesAndShares(t *testing.T) {
	ps := complianceGateway(t, routeMap{
		"GET /api/compliance/exports":               jsonReply(200, `{"exports":[{"id":"exp_1","pack":"soc2","from":"2026-07-01","to":"2026-10-01","status":"ready","expiresAt":"2026-11-15"}]}`),
		"GET /api/compliance/exports/exp_1":         jsonReply(200, `{"export":{"id":"exp_1","pack":"soc2","status":"ready","sizeBytes":1234}}`),
		"DELETE /api/compliance/exports/exp_1":      jsonReply(200, `{"ok":true}`),
		"GET /api/compliance/schedules":             jsonReply(200, `{"schedules":[{"id":"sch_1","pack":"soc2","frequency":"monthly","rangeDays":90,"recipients":["a@firm.example"]}]}`),
		"POST /api/compliance/schedules":            jsonReply(201, `{"schedule":{"id":"sch_2"}}`),
		"DELETE /api/compliance/schedules/sch_1":    jsonReply(200, `{}`),
		"POST /api/compliance/exports/exp_1/shares": jsonReply(201, `{"share":{"url":"https://gw/share/compliance/tok","expiresAt":"2026-10-30"}}`),
	})
	if out, err := runCommand(t, ComplianceCmd(), "exports", "list"); err != nil || !strings.Contains(out, "exp_1") || !strings.Contains(out, "ready") {
		t.Errorf("list: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "exports", "show", "exp_1"); err != nil || !strings.Contains(out, "sizeBytes: 1234") {
		t.Errorf("show: %q %v", out, err)
	}
	if _, err := runCommand(t, ComplianceCmd(), "exports", "delete", "exp_1"); err == nil {
		t.Error("deleted without confirmation")
	}
	if out, err := runCommand(t, ComplianceCmd(), "exports", "delete", "exp_1", "--yes"); err != nil || !strings.Contains(out, "Deleted exp_1.") {
		t.Errorf("delete: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "exports", "delete", "exp_1", "--yes", "--json"); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Errorf("delete json: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "schedules", "list"); err != nil || !strings.Contains(out, "a@firm.example") {
		t.Errorf("schedules: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "schedules", "create", "--pack", "soc2", "--frequency", "quarterly", "--to", "a@firm.example"); err != nil || !strings.Contains(out, "Created schedule sch_2.") {
		t.Errorf("create: %q %v", out, err)
	}
	if b := ps.body("POST /api/compliance/schedules", 0); b["frequency"] != "quarterly" || b["rangeDays"] != float64(90) || b["recipients"].([]any)[0] != "a@firm.example" {
		t.Errorf("schedule body = %v", b)
	}
	if _, err := runCommand(t, ComplianceCmd(), "schedules", "create", "--pack", "soc2", "--frequency", "hourly"); err == nil {
		t.Error("bad frequency")
	}
	if _, err := runCommand(t, ComplianceCmd(), "schedules", "create"); err == nil {
		t.Error("no pack")
	}
	if _, err := runCommand(t, ComplianceCmd(), "schedules", "delete", "sch_1", "--yes"); err != nil {
		t.Error(err)
	}
	out, err := runCommand(t, ComplianceCmd(), "share", "exp_1", "--to", "auditor@firm.example", "--expires", "14d")
	if err != nil || !strings.Contains(out, "https://gw/share/compliance/tok") || !strings.Contains(out, "until 2026-10-30") {
		t.Errorf("share: %q %v", out, err)
	}
	if b := ps.body("POST /api/compliance/exports/exp_1/shares", 0); b["to"] != "auditor@firm.example" || b["expiresInDays"] != float64(14) {
		t.Errorf("share body = %v", b)
	}
	for _, args := range [][]string{{"share", "exp_1", "--to", "nobody"}, {"share", "exp_1", "--to", "a@b.c", "--expires", "0d"}, {"share", "exp_1", "--to", "a@b.c", "--expires", "200d"}, {"share", "exp_1", "--to", "a@b.c", "--expires", "soon"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if out, err := runCommand(t, ComplianceCmd(), "share", "exp_1", "--to", "a@b.c", "--json"); err != nil || !strings.Contains(out, "share") {
		t.Errorf("share json: %q %v", out, err)
	}
	t.Setenv("ZUANO_AGENT", "1")
	for _, args := range [][]string{{"share", "exp_1", "--to", "a@b.c"}, {"exports", "delete", "exp_1", "--yes"}, {"schedules", "create", "--pack", "soc2"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); exitCodeOf(err) != 10 {
			t.Errorf("%v under an agent: %v", args, err)
		}
	}
	if _, err := runCommand(t, ComplianceCmd(), "exports", "list"); err != nil {
		t.Errorf("an agent may read: %v", err)
	}
}

func TestComplianceRegisters(t *testing.T) {
	ps := complianceGateway(t, routeMap{
		"GET /api/compliance/incidents":                   jsonReply(200, `{"incidents":[{"id":"inc_1","title":"Leaked key","severity":"high","status":"open","detectedAt":"2026-10-14T08:00:00Z"}]}`),
		"GET /api/compliance/incidents/inc_1":             jsonReply(200, `{"incident":{"id":"inc_1","title":"Leaked key","services":["stripe","gmail"]}}`),
		"POST /api/compliance/incidents":                  jsonReply(201, `{"incident":{"id":"inc_2"}}`),
		"GET /api/compliance/dsr":                         jsonReply(200, `{"requests":[{"id":"dsr_1","kind":"erasure","subject":"x@example.com","status":"open","receivedAt":"2026-10-10","dueAt":"2026-11-09"}]}`),
		"POST /api/compliance/dsr":                        jsonReply(201, `{"request":{"id":"dsr_2"}}`),
		"POST /api/compliance/dsr/dsr_1/execute":          jsonReply(200, `{"ok":true}`),
		"POST /api/compliance/access-reviews":             jsonReply(201, `{"review":{"id":"rev_1"}}`),
		"GET /api/compliance/access-reviews/rev_1":        jsonReply(200, `{"review":{"id":"rev_1","status":"open","progress":{"decided":1}}}`),
		"POST /api/compliance/access-reviews/rev_1/apply": jsonReply(200, `{"ok":true}`),
	})
	if out, err := runCommand(t, ComplianceCmd(), "incidents", "list"); err != nil || !strings.Contains(out, "Leaked key") {
		t.Errorf("incidents: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "incidents", "show", "inc_1"); err != nil || !strings.Contains(out, "services: stripe, gmail") {
		t.Errorf("incident: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "incidents", "create", "--title", "Phishing", "--severity", "critical", "--detected-at", "2026-10-15T09:30:00Z", "--description", "short"); err != nil || !strings.Contains(out, "Recorded incident inc_2.") {
		t.Errorf("create: %q %v", out, err)
	}
	cmd := ComplianceCmd()
	cmd.SetIn(strings.NewReader("long description from stdin"))
	if _, err := runCommand(t, cmd, "incidents", "create", "--title", "T", "--description", "-"); err != nil {
		t.Fatal(err)
	}
	if b := ps.body("POST /api/compliance/incidents", 1); b["description"] != "long description from stdin" || b["severity"] != "medium" || b["detectedAt"] == "" {
		t.Errorf("stdin body = %v", b)
	}
	for _, args := range [][]string{{"incidents", "create"}, {"incidents", "create", "--title", "T", "--severity", "bad"}, {"incidents", "create", "--title", "T", "--detected-at", "yesterday"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if out, err := runCommand(t, ComplianceCmd(), "dsr", "list"); err != nil || !strings.Contains(out, "x@example.com") {
		t.Errorf("dsr: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "dsr", "create", "--kind", "access", "--subject", "x@example.com"); err != nil || !strings.Contains(out, "Recorded request dsr_2.") {
		t.Errorf("dsr create: %q %v", out, err)
	}
	for _, args := range [][]string{{"dsr", "create", "--kind", "gossip", "--subject", "x@example.com"}, {"dsr", "create", "--kind", "access", "--subject", "nobody"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := runCommand(t, ComplianceCmd(), "dsr", "execute", "dsr_1"); err == nil {
		t.Error("executed without confirmation")
	}
	if out, err := runCommand(t, ComplianceCmd(), "dsr", "execute", "dsr_1", "--yes"); err != nil || !strings.Contains(out, "dsr_1: execute done.") {
		t.Errorf("execute: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "access-review", "start", "--scope", "group:grp_1", "--due-days", "7"); err != nil || !strings.Contains(out, "Started review rev_1") {
		t.Errorf("start: %q %v", out, err)
	}
	if b := ps.body("POST /api/compliance/access-reviews", 0); b["scope"] != "group:grp_1" || b["dueDays"] != float64(7) {
		t.Errorf("review body = %v", b)
	}
	if _, err := runCommand(t, ComplianceCmd(), "access-review", "start", "--scope", "everyone"); err == nil {
		t.Error("bad scope")
	}
	if out, err := runCommand(t, ComplianceCmd(), "access-review", "status", "rev_1"); err != nil || !strings.Contains(out, "status: open") {
		t.Errorf("status: %q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "access-review", "apply", "rev_1", "--yes", "--json"); err != nil || !strings.Contains(out, `"ok":true`) {
		t.Errorf("apply: %q %v", out, err)
	}
	t.Setenv("ZUANO_AGENT", "1")
	for _, args := range [][]string{{"incidents", "create", "--title", "T"}, {"dsr", "create", "--kind", "access", "--subject", "a@b.c"}, {"dsr", "execute", "dsr_1", "--yes"}, {"access-review", "start"}, {"access-review", "apply", "rev_1", "--yes"}} {
		if _, err := runCommand(t, ComplianceCmd(), args...); exitCodeOf(err) != 10 {
			t.Errorf("%v under an agent: %v", args, err)
		}
	}
}

func TestComplianceListsWhenEmpty(t *testing.T) {
	complianceGateway(t, routeMap{
		"GET /api/compliance/packs":     jsonReply(200, `{"packs":[]}`),
		"GET /api/compliance/incidents": jsonReply(200, `{"incidents":[]}`),
	})
	if out, err := runCommand(t, ComplianceCmd(), "packs"); err != nil || !strings.Contains(out, "No compliance packs.") {
		t.Errorf("%q %v", out, err)
	}
	if out, err := runCommand(t, ComplianceCmd(), "incidents", "list"); err != nil || !strings.Contains(out, "No incidents.") {
		t.Errorf("%q %v", out, err)
	}
}

func TestFieldTextAndPrintObject(t *testing.T) {
	row := map[string]any{"a": map[string]any{"b": 3.0, "c": []any{"x", "y"}, "d": true, "e": map[string]any{"k": 1.0}}, "s": "", "n": nil}
	for path, want := range map[string]string{"a.b": "3", "a.c": "x, y", "a.d": "true", "a.e": `{"k":1}`, "s": "-", "n": "-", "a.zzz": "-", "a.b.c": "-", "q": "-"} {
		if got := fieldText(row, path); got != want {
			t.Errorf("fieldText(%q) = %q, want %q", path, got, want)
		}
	}
}

func writeKeys(t *testing.T, doc any) string {
	t.Helper()
	raw, _ := json.Marshal(doc)
	p := filepath.Join(t.TempDir(), "keys.json")
	_ = os.WriteFile(p, raw, 0o600)
	return p
}

func TestComplianceVerifyOffline(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{VolumeBytes: 1})
	paths := b.Write()
	keys := writeKeys(t, map[string]any{"evidence": []signing.Key{b.Evidence.Key}})
	args := append([]string{"verify"}, paths...)
	out, err := runCommand(t, ComplianceCmd(), append(args, "--keys", keys)...)
	if err != nil || !strings.Contains(out, "Valid: iso27001 pack for acme") || !strings.Contains(out, "6 audit rows in 3 hours") || !strings.Contains(out, "in "+keys) {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := runCommand(t, ComplianceCmd(), append(args, "--keys", writeKeys(t, map[string]any{"keys": []signing.Key{b.Evidence.Key}}))...); err != nil {
		t.Errorf("generic keys: %v", err)
	}
	withAudit := writeKeys(t, map[string]any{"evidence": []signing.Key{b.Evidence.Key}, "audit": []signing.Key{signingtest.New("audit-1").Key}})
	out, err = runCommand(t, ComplianceCmd(), append(args, "--keys", withAudit)...)
	if exitCodeOf(err) != 45 || !strings.Contains(out, "the checkpoint signature does not verify") {
		t.Errorf("audit keys of the caller: exit %d %q %v", exitCodeOf(err), out, err)
	}

	other := signingtest.New("evidence-1")
	out, err = runCommand(t, ComplianceCmd(), append(args, "--keys", writeKeys(t, map[string]any{"evidence": []signing.Key{other.Key}}))...)
	if exitCodeOf(err) != 45 || !strings.Contains(out, "manifest.json: the signature does not verify") {
		t.Errorf("wrong key: exit %d %q %v", exitCodeOf(err), out, err)
	}
	tampered := evidencetest.New(t, evidencetest.Options{})
	tampered.Files["controls.csv"] = []byte("control_id,title,statuz\r\n")
	out, err = runCommand(t, ComplianceCmd(), "verify", tampered.Write()[0], "--keys", writeKeys(t, map[string]any{"evidence": []signing.Key{tampered.Evidence.Key}}))
	if exitCodeOf(err) != 45 || !strings.Contains(out, "controls.csv: does not match the hash") {
		t.Errorf("tampered: exit %d %q %v", exitCodeOf(err), out, err)
	}
	if _, err := runCommand(t, ComplianceCmd(), "verify", paths[0], "--keys", keys); exitCodeOf(err) != 45 {
		t.Errorf("one volume of several: exit %d", exitCodeOf(err))
	}
	notZip := filepath.Join(t.TempDir(), "x.zip")
	_ = os.WriteFile(notZip, []byte("nope"), 0o600)
	if _, err := runCommand(t, ComplianceCmd(), "verify", notZip, "--keys", keys); exitCodeOf(err) != 45 {
		t.Errorf("not a zip: exit %d", exitCodeOf(err))
	}
	for name, k := range map[string]string{"missing": filepath.Join(t.TempDir(), "none.json"), "garbled": func() string {
		p := filepath.Join(t.TempDir(), "g.json")
		_ = os.WriteFile(p, []byte("{"), 0o600)
		return p
	}(), "empty": writeKeys(t, map[string]any{}), "audit only": writeKeys(t, map[string]any{"audit": []signing.Key{b.Audit.Key}})} {
		if _, err := runCommand(t, ComplianceCmd(), append(args, "--keys", k)...); exitCodeOf(err) != 46 {
			t.Errorf("%s keys: exit %d, %v", name, exitCodeOf(err), err)
		}
	}
}

func TestComplianceVerifyWarnsAboutHoursWithoutACheckpoint(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{SkipCheckpoint: map[int]bool{2: true}})
	keys := writeKeys(t, map[string]any{"evidence": []signing.Key{b.Evidence.Key}})
	args := append([]string{"verify"}, b.Write()...)
	out, err := runCommand(t, ComplianceCmd(), append(args, "--keys", keys)...)
	if err != nil || !strings.Contains(out, "warning: 1 audit hour(s) have rows and no signed checkpoint") || !strings.Contains(out, "2026-10-01T02:00:00.000Z") || !strings.Contains(out, "2 hours checked") {
		t.Errorf("%q %v", out, err)
	}
	out, err = runCommand(t, ComplianceCmd(), append(args, "--keys", keys, "--require-checkpoints")...)
	if exitCodeOf(err) != 45 || !strings.Contains(out, "no signed checkpoint") {
		t.Errorf("required: exit %d %q %v", exitCodeOf(err), out, err)
	}
}

func TestComplianceVerifyFetchesThePublishedEvidenceKeysOnly(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{})
	paths := b.Write()
	// The gateway publishes no audit keys: only the evidence keys are asked for.
	gw := complianceGateway(t, routeMap{
		"GET /.well-known/zuano-evidence-keys": jsonReply(200, mustJSON(map[string]any{"keys": []signing.Key{b.Evidence.Key}})),
	})
	out, err := runCommand(t, ComplianceCmd(), "verify", paths[0])
	if err != nil || !strings.Contains(out, "Valid:") || !strings.Contains(out, "published by "+gw.URL) {
		t.Errorf("%q %v", out, err)
	}

	// A bundle signed by a key the gateway does not publish is refused, whatever key file it carries.
	forged := evidencetest.New(t, evidencetest.Options{})
	if out, err := runCommand(t, ComplianceCmd(), "verify", forged.Write()[0]); exitCodeOf(err) != 45 || !strings.Contains(out, "does not verify") {
		t.Errorf("forged bundle: exit %d %q %v", exitCodeOf(err), out, err)
	}

	complianceGateway(t, routeMap{"GET /.well-known/zuano-evidence-keys": jsonReply(404, `{}`)})
	if _, err := runCommand(t, ComplianceCmd(), "verify", paths[0]); exitCodeOf(err) != 46 {
		t.Errorf("no keys published: exit %d, %v", exitCodeOf(err), err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ZUANO_URL", "")
	t.Setenv("ZUANO_API_KEY", "")
	if _, err := runCommand(t, ComplianceCmd(), "verify", paths[0]); exitCodeOf(err) != 46 || !strings.Contains(err.Error(), "--keys") {
		t.Errorf("no gateway: exit %d, %v", exitCodeOf(err), err)
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
