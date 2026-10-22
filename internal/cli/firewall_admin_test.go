package cli

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

var (
	upstreamJSON   = `{"id":"ups_1","slug":"files","name":"Files","source":"package","transport":"stdio","status":"active","toolCount":3,"pendingChanges":1,"pendingDefinitions":2}`
	definitionsDoc = `{"definitions":[
{"kind":"tool","name":"read_file","defHash":"` + hashA + `","state":"pinned","risk":"read","needsReview":false},
{"kind":"tool","name":"write_file","defHash":"` + hashB + `","state":"pending","risk":"write","needsReview":true},
{"kind":"tool","name":"delete_file","defHash":"` + hashC + `","state":"pending","risk":"destructive","needsReview":true},
{"kind":"prompt","name":"summarise","defHash":"` + hashD + `","state":"quarantined","risk":null,"needsReview":false}]}`
)

var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
	hashC = strings.Repeat("c", 64)
	hashD = strings.Repeat("d", 64)
)

func stepUpRefusalFor(action string) string {
	return `{"error":"step_up_required","message":"Confirm this change with your passkey.","step_up":{"action":"` + action + `","digest":"d1"}}`
}

// stepUpThen answers the first request with a step-up refusal and the next
// with ok, as the gateway does once the person has confirmed.
func stepUpThen(action string, status int, ok string) func(*packServer, http.ResponseWriter, *http.Request) {
	var n atomic.Int32
	return func(_ *packServer, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(stepUpRefusalFor(action)))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(ok))
	}
}

func TestFirewallAdd_APackageCarriesItsAuthTagsAndFollowsItsBuild(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{
		"POST /api/firewall/upstreams": jsonReply(201, `{"upstream":`+upstreamJSON+`,"jobId":"job_01B"}`),
		"GET /api/v1/jobs/job_01B":     jsonReply(200, `{"job":{"id":"job_01B","kind":"firewall_deploy","status":"succeeded"}}`),
	})
	out, err := runCommand(t, FirewallCmd(), "upstreams", "add", "Files", "--package", "@acme/files-mcp", "--auth", "stored", "--tag", "dev", "--tag", "files")
	if err != nil || !strings.Contains(out, "Added files.") || !strings.Contains(out, "Build started: job_01B") || !strings.Contains(out, "zuano runs show job_01B") {
		t.Fatalf("%q, %v", out, err)
	}
	body := gw.body("POST /api/firewall/upstreams", 0)
	if body["package"] != "@acme/files-mcp" || body["authMode"] != "stored" || body["slug"] != "files" || len(body["tags"].([]any)) != 2 {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["url"]; ok {
		t.Errorf("a package carries no url: %v", body)
	}
	out, err = runCommand(t, FirewallCmd(), "upstreams", "add", "files", "--package", "@acme/files-mcp", "--wait")
	if err != nil || !strings.Contains(out, "job_01B (succeeded)") {
		t.Fatalf("--wait: %q, %v", out, err)
	}
}

func TestFirewallAdd_ReservedAndMalformedSlugsAreRefusedBeforeARequest(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{})
	for _, slug := range []string{"new", "changes", "shadow", "local", "oauth", "mcp", "Changes"} {
		_, err := runCommand(t, FirewallCmd(), "upstreams", "add", slug, "--url", "https://x.example/mcp")
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: %v", slug, err)
		}
	}
	long := strings.Repeat("a", 64)
	if _, err := runCommand(t, FirewallCmd(), "upstreams", "request", long, "--url", "https://x.example/mcp"); err == nil || !strings.Contains(err.Error(), "not a valid slug") {
		t.Errorf("a long slug: %v", err)
	}
}

func TestFirewallUpstreamActions(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{
		"POST /api/firewall/upstreams/files/approve":    jsonReply(200, `{"upstream":`+upstreamJSON+`}`),
		"POST /api/firewall/upstreams/files/quarantine": jsonReply(200, `{"upstream":{"slug":"files","status":"quarantined"}}`),
		"POST /api/firewall/upstreams/files/release":    jsonReply(200, `{"upstream":{"slug":"files","status":"active"}}`),
		"POST /api/firewall/upstreams/files/discover":   jsonReply(200, `{"outcome":"checked","changes":2}`),
		"POST /api/firewall/upstreams/files/deploy":     jsonReply(202, `{"jobId":"job_01C"}`),
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"upstreams", "approve", "files"}, "Approved: files (active)"},
		{[]string{"upstreams", "quarantine", "files"}, "Held: files (quarantined)"},
		{[]string{"upstreams", "release", "files"}, "Released: files (active)"},
		{[]string{"upstreams", "discover", "files"}, "Checked files: 2 change(s) found"},
		{[]string{"upstreams", "deploy", "files"}, "Build started: job_01C"},
	} {
		if out, err := runCommand(t, FirewallCmd(), tc.args...); err != nil || !strings.Contains(out, tc.want) {
			t.Errorf("%v: %q, %v", tc.args, out, err)
		}
	}
}

func TestFirewallPasskeyActionsGoThroughTheStepUpFlow(t *testing.T) {
	syncHome(t)
	for _, tc := range []struct {
		name, action, route string
		args                []string
		reply               string
		want                string
	}{
		{"release", "firewall_release", "POST /api/firewall/upstreams/files/release", []string{"upstreams", "release", "files"}, `{"upstream":{"slug":"files","status":"active"}}`, "Released: files (active)"},
		{"decide", "firewall_pin_approve", "POST /api/firewall/upstreams/files/definitions/decide", []string{"definitions", "decide", "files", "--approve", "write_file"}, `{"approved":1,"rejected":0,"skipped":0}`, "Approved 1, rejected 0, skipped 0."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			routes := routeMap{
				"GET /api/firewall/upstreams/files/definitions": jsonReply(200, definitionsDoc),
				tc.route: stepUpThen(tc.action, 200, tc.reply),
			}
			packGateway(t, routes)
			prompt, opened := stepUpEnv(t, true, personConfirms(nil))
			out, err := runCommand(t, FirewallCmd(), tc.args...)
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("%q, %v", out, err)
			}
			if !strings.Contains(prompt.String(), `"`+tc.action+`"`) || len(*opened) != 1 || !strings.HasSuffix((*opened)[0], "/firewall") {
				t.Errorf("prompt %q opened %v: want the action named and the firewall page opened", prompt, *opened)
			}
		})
	}
}

func TestFirewallPasskeyActionsWithoutAPersonExitFourteen(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{
		"POST /api/firewall/upstreams/files/release": stepUpThen("firewall_release", 200, `{}`),
	})
	stepUpEnv(t, false, personConfirms(nil))
	_, err := runCommand(t, FirewallCmd(), "upstreams", "release", "files")
	if exitCodeOf(err) != 14 || !strings.Contains(err.Error(), "firewall_release") {
		t.Errorf("exit %d: %v", exitCodeOf(err), err)
	}
}

func TestFirewallAnAgentSessionCannotGrantStandingPermissions(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{
		"GET /api/firewall/upstreams/files/definitions":         jsonReply(200, definitionsDoc),
		"POST /api/firewall/upstreams/files/definitions/decide": jsonReply(200, `{"approved":0,"rejected":1,"skipped":0}`),
	})
	t.Setenv("ZUANO_AGENT", "1")
	for _, args := range [][]string{
		{"upstreams", "approve", "files"},
		{"upstreams", "release", "files"},
		{"definitions", "decide", "files", "--approve", "write_file"},
		{"definitions", "accept", "files"},
		{"upstreams", "env", "clear", "files", "--yes"},
	} {
		if _, err := runCommand(t, FirewallCmd(), args...); exitCodeOf(err) != 14 {
			t.Errorf("%v: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
	if _, err := runWithStdin(t, FirewallCmd(), `{"A":"1"}`, "upstreams", "env", "set", "files"); exitCodeOf(err) != 14 {
		t.Errorf("env set: exit %d", exitCodeOf(err))
	}
	_, err := runCommand(t, FirewallCmd(), "definitions", "decide", "files", "--reject", "write_file")
	if exitCodeOf(err) == 14 {
		t.Errorf("rejecting grants nothing and needs no person: %v", err)
	}
}

func TestFirewallDefinitionsDecideSendsTheCurrentHashes(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{
		"GET /api/firewall/upstreams/files/definitions":         jsonReply(200, definitionsDoc),
		"POST /api/firewall/upstreams/files/definitions/decide": jsonReply(200, `{"approved":2,"rejected":1,"skipped":0}`),
	})
	out, err := runCommand(t, FirewallCmd(), "definitions", "decide", "files", "--approve", "write_file", "--approve", "prompt:summarise", "--reject", "delete_file")
	if err != nil || !strings.Contains(out, "Approved 2, rejected 1, skipped 0.") || !strings.Contains(out, "approve tool:write_file (bbbbbbbbbbbb)") {
		t.Fatalf("%q, %v", out, err)
	}
	items := gw.body("POST /api/firewall/upstreams/files/definitions/decide", 0)["items"].([]any)
	want := []string{"approve tool write_file " + hashB, "approve prompt summarise " + hashD, "reject tool delete_file " + hashC}
	var got []string
	for _, it := range items {
		m := it.(map[string]any)
		got = append(got, strings.Join([]string{m["decision"].(string), m["kind"].(string), m["name"].(string), m["defHash"].(string)}, " "))
	}
	if !slices.Equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}

	if _, err := runCommand(t, FirewallCmd(), "definitions", "decide", "files", "--approve-all"); err != nil {
		t.Fatal(err)
	}
	all := gw.body("POST /api/firewall/upstreams/files/definitions/decide", 1)["items"].([]any)
	if len(all) != 2 {
		t.Errorf("--approve-all covers the pending definitions only: %v", all)
	}
}

func TestFirewallDefinitionsDecideRefusesWhatItCannotResolve(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{"GET /api/firewall/upstreams/files/definitions": jsonReply(200, definitionsDoc)})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"decide", "files"}, "name what to decide"},
		{[]string{"decide", "files", "--approve-all", "--reject-all"}, "not both"},
		{[]string{"decide", "files", "--approve", "read_file"}, "waits for a decision"},
		{[]string{"decide", "files", "--reject", "summarise"}, "waits for a decision"},
		{[]string{"decide", "files", "--approve", "write_file", "--reject", "write_file"}, "both approved and rejected"},
	} {
		if _, err := runCommand(t, FirewallCmd(), append([]string{"definitions"}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestFirewallDefinitionsAcceptCoversTheWaitingDefinitionsOfSeveralServers(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{
		"GET /api/firewall/upstreams/files":             jsonReply(200, `{"upstream":`+upstreamJSON+`}`),
		"GET /api/firewall/upstreams/docs":              jsonReply(200, `{"upstream":{"id":"ups_2","slug":"docs"}}`),
		"GET /api/firewall/upstreams/idle":              jsonReply(200, `{"upstream":{"id":"ups_3","slug":"idle"}}`),
		"GET /api/firewall/upstreams/files/definitions": jsonReply(200, `{"definitions":[{"kind":"tool","name":"write_file","defHash":"`+hashB+`","state":"pending"},{"kind":"tool","name":"delete_file","defHash":"`+hashC+`","state":"pending"}]}`),
		"GET /api/firewall/upstreams/docs/definitions":  jsonReply(200, `{"definitions":[{"kind":"tool","name":"search","defHash":"`+hashA+`","state":"pending"}]}`),
		"GET /api/firewall/upstreams/idle/definitions":  jsonReply(200, `{"definitions":[]}`),
		"POST /api/firewall/definitions/accept":         jsonReply(200, `{"approved":3,"skipped":0,"upstreams":2,"setHash":"`+hashD+`"}`),
	})
	out, err := runCommand(t, FirewallCmd(), "definitions", "accept", "files", "docs", "idle")
	if err != nil || !strings.Contains(out, "Accepted 3 definition(s) on 2 server(s), skipped 0 (set dddddddddddd).") {
		t.Fatalf("%q, %v", out, err)
	}
	body := gw.body("POST /api/firewall/definitions/accept", 0)
	ids, hashes := body["upstreamIds"].([]any), body["defHashes"].([]any)
	if len(ids) != 2 || ids[0] != "ups_1" || ids[1] != "ups_2" || len(hashes) != 3 {
		t.Errorf("body = %v", body)
	}
	if out, err := runCommand(t, FirewallCmd(), "definitions", "accept", "idle"); err != nil || !strings.Contains(out, "Nothing waits") {
		t.Errorf("nothing waiting: %q, %v", out, err)
	}
	if gw.count("POST /api/firewall/definitions/accept") != 1 {
		t.Error("an accept with nothing waiting sent a request")
	}
}

func TestFirewallEnv(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{
		"GET /api/firewall/upstreams/files/env": jsonReply(200, `{"names":["API_URL","TOKEN"]}`),
		"PUT /api/firewall/upstreams/files/env": jsonReply(200, `{"names":["TOKEN"]}`),
	})
	if out, err := runCommand(t, FirewallCmd(), "upstreams", "env", "list", "files"); err != nil || !strings.Contains(out, "API_URL\nTOKEN") {
		t.Fatalf("list: %q, %v", out, err)
	}
	secret := fakeSecret("tok")
	out, err := runWithStdin(t, FirewallCmd(), `{"TOKEN":"`+secret+`"}`, "upstreams", "env", "set", "files")
	if err != nil || !strings.Contains(out, "Variables set: TOKEN") || strings.Contains(out, secret) || !strings.Contains(out, "zuano firewall upstreams deploy files") {
		t.Fatalf("set: %q, %v", out, err)
	}
	if env := gw.body("PUT /api/firewall/upstreams/files/env", 0)["env"].(map[string]any); env["TOKEN"] != secret || len(env) != 1 {
		t.Errorf("env = %v", env)
	}
	if _, err := runCommand(t, FirewallCmd(), "upstreams", "env", "clear", "files"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("clear without --yes: %v", err)
	}
	if _, err := runCommand(t, FirewallCmd(), "upstreams", "env", "clear", "files", "--yes"); err != nil {
		t.Fatal(err)
	}
	if env := gw.body("PUT /api/firewall/upstreams/files/env", 1)["env"].(map[string]any); len(env) != 0 {
		t.Errorf("clear sent %v", env)
	}
	for _, bad := range []string{`{"1BAD":"x"}`, `{"A":""}`, `{"A":3}`, `{"A":"` + strings.Repeat("x", maxPackageEnvValueBytes+1) + `"}`} {
		if _, err := runWithStdin(t, FirewallCmd(), bad, "upstreams", "env", "set", "files"); err == nil {
			t.Errorf("%.30s accepted", bad)
		}
	}
	if gw.count("PUT /api/firewall/upstreams/files/env") != 2 {
		t.Error("an invalid variable set was sent")
	}
}

func TestFirewallShadowAndDefinitionsList(t *testing.T) {
	syncHome(t)
	packGateway(t, routeMap{
		"GET /api/firewall/shadow":                      jsonReply(200, `{"servers":[{"name":"jira","transport":"http","host":"jira.example","command":null,"devices":4}],"source":"device_reports"}`),
		"GET /api/firewall/upstreams/files/definitions": jsonReply(200, definitionsDoc),
	})
	if out, err := runCommand(t, FirewallCmd(), "shadow"); err != nil || !strings.Contains(out, "jira") || !strings.Contains(out, "jira.example") {
		t.Errorf("shadow: %q, %v", out, err)
	}
	out, err := runCommand(t, FirewallCmd(), "definitions", "list", "files", "--state", "pending")
	if err != nil || !strings.Contains(out, "write_file") || !strings.Contains(out, "bbbbbbbbbbbb") || strings.Contains(out, hashB) {
		t.Errorf("list: %q, %v", out, err)
	}
}

// firewallRefusals are the codes the firewall routes answer with, with the
// HTTP status the gateway gives each.
var firewallRefusals = map[string]int{
	"member_requests_off": 403, "change_not_found": 404, "definition_not_found": 404, "upstream_not_found": 404,
	"definition_not_decidable": 409, "deploy_running": 409, "upstream_exists": 409, "upstream_not_active": 409,
	"upstream_not_pending": 409, "upstream_not_quarantined": 409, "env_name_invalid": 422, "federation_refused": 422,
	"tool_list_too_large": 422, "upstream_source_invalid": 422, "hosting_unavailable": 503,
}

func TestFirewallRefusalsKeepTheGatewaysSentenceAndMapToTheirClass(t *testing.T) {
	syncHome(t)
	for code, status := range firewallRefusals {
		gw := packGateway(t, routeMap{})
		gw.replace(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"` + code + `","message":"The gateway says so."}`))
		})
		_, err := runCommand(t, FirewallCmd(), "upstreams", "discover", "files")
		if err == nil || !strings.Contains(err.Error(), "The gateway says so. (HTTP "+strconv.Itoa(status)+")") {
			t.Errorf("%s: %v", code, err)
			continue
		}
		want := 1
		if code == "member_requests_off" {
			want = exitDenied
		}
		if got := exitCodeOf(err); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
		if _, ok := firewallRefusalHints[code]; !ok {
			t.Errorf("%s has no hint", code)
		} else if !strings.Contains(err.Error(), "; ") {
			t.Errorf("%s: the hint is missing from %q", code, err)
		}
	}
}

func TestFirewallRequestsMatchTheOpenAPI(t *testing.T) {
	syncHome(t)
	doc := loadOpenAPI(t)
	gw := packGateway(t, routeMap{})
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/definitions"):
			_, _ = w.Write([]byte(definitionsDoc))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/env"):
			_, _ = w.Write([]byte(`{"names":[]}`))
		case r.Method == http.MethodGet && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/firewall/upstreams/"), "/") && r.URL.Path != "/api/firewall/upstreams":
			_, _ = w.Write([]byte(`{"upstream":{"id":"ups_1","slug":"files"}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})
	invocations := [][]string{
		{"upstreams", "list", "--status", "pending"},
		{"upstreams", "show", "files"},
		{"upstreams", "add", "files", "--package", "@acme/files-mcp", "--auth", "stored", "--tag", "dev"},
		{"upstreams", "add", "docs", "--url", "https://docs.example.com/mcp", "--auth", "per_user"},
		{"upstreams", "request", "docs", "--url", "https://docs.example.com/mcp", "--reason", "need it"},
		{"upstreams", "approve", "files"},
		{"upstreams", "discover", "files"},
		{"upstreams", "quarantine", "files"},
		{"upstreams", "release", "files"},
		{"upstreams", "deploy", "files"},
		{"upstreams", "env", "list", "files"},
		{"changes", "list", "--status", "open", "--server", "files", "--limit", "20"},
		{"changes", "show", "chg_1"},
		{"definitions", "list", "files", "--state", "pending", "--kind", "tool"},
		{"definitions", "decide", "files", "--approve", "write_file", "--reject", "delete_file"},
		{"definitions", "accept", "files"},
		{"shadow"},
	}
	for _, args := range invocations {
		_, _ = runCommand(t, FirewallCmd(), args...)
	}
	_, _ = runWithStdin(t, FirewallCmd(), `{"TOKEN":"x"}`, "upstreams", "env", "set", "files")

	gw.mu.Lock()
	defer gw.mu.Unlock()
	covered := map[string]bool{}
	for route := range gw.hits {
		method, path, _ := strings.Cut(route, " ")
		op, template, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered[method+" "+template] = true
		for _, body := range gw.bodies[route] {
			for _, p := range op.problems(nil, body) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
	}
	for _, want := range []string{
		"GET /firewall/upstreams", "POST /firewall/upstreams", "POST /firewall/upstream-requests", "GET /firewall/upstreams/{id}",
		"POST /firewall/upstreams/{id}/approve", "POST /firewall/upstreams/{id}/discover", "POST /firewall/upstreams/{id}/quarantine",
		"POST /firewall/upstreams/{id}/release", "POST /firewall/upstreams/{id}/deploy", "GET /firewall/upstreams/{id}/env",
		"PUT /firewall/upstreams/{id}/env", "GET /firewall/changes", "GET /firewall/changes/{id}", "GET /firewall/upstreams/{id}/definitions",
		"POST /firewall/upstreams/{id}/definitions/decide", "POST /firewall/definitions/accept", "GET /firewall/shadow",
	} {
		if !covered[want] {
			t.Errorf("no command exercised %s", want)
		}
	}
}

func TestFirewallQueriesAreDeclaredByTheOpenAPI(t *testing.T) {
	syncHome(t)
	doc := loadOpenAPI(t)
	gw := packGateway(t, routeMap{})
	var seen []url.URL
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, *r.URL)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	for _, args := range [][]string{
		{"upstreams", "list", "--status", "pending"},
		{"changes", "list", "--status", "open", "--server", "files", "--limit", "20"},
		{"definitions", "list", "files", "--state", "pending", "--kind", "tool"},
	} {
		_, _ = runCommand(t, FirewallCmd(), args...)
	}
	if len(seen) != 3 {
		t.Fatalf("requests = %d", len(seen))
	}
	for _, u := range seen {
		op, _, ok := doc.operationFor(http.MethodGet, u.Path)
		if !ok {
			t.Fatalf("%s is not in the OpenAPI document", u.Path)
		}
		if got := op.problems(u.Query(), nil); len(got) != 0 {
			t.Errorf("%s: %v", u.Path, got)
		}
	}
}

func TestFirewallRefusalCodesAreInTheOpenAPI(t *testing.T) {
	raw := vendoredRefusalCodes(t)
	for code := range firewallRefusals {
		if !slices.Contains(raw, code) {
			t.Errorf("%s is not in the OpenAPI refusal codes", code)
		}
	}
	for code := range firewallRefusalHints {
		if _, ok := firewallRefusals[code]; !ok {
			t.Errorf("hint for %s, which the firewall routes do not answer with", code)
		}
	}
	for _, code := range []string{"not_running", "message_limit_reached"} {
		if !slices.Contains(raw, code) {
			t.Errorf("%s is not in the OpenAPI refusal codes", code)
		}
		if _, ok := runRefusalHints[code]; !ok {
			t.Errorf("%s has no hint", code)
		}
	}
}

func TestStepUpPagesForTheFirewallActions(t *testing.T) {
	for action, want := range map[string]string{"firewall_pin_approve": "/firewall", "firewall_release": "/firewall", "policy_change": "/", "": "/"} {
		if got := stepUpPage(action); got != want {
			t.Errorf("%q: %q, want %q", action, got, want)
		}
	}
}
