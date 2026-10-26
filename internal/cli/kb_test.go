package cli

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	kbPageJSON   = `{"id":"kpg_abc12","spaceId":"ksp_1","spaceSlug":"eng","spaceKind":"org","slug":"runbook","title":"Runbook","frontmatter":{},"version":2,"status":"published","ownerUserId":null,"reviewBy":null,"verifiedAt":"2026-10-20T00:00:00.000Z","verifiedBy":null,"stale":false,"role":"editor","updatedAt":"2026-10-25T10:00:00.000Z","markdown":"# Runbook\nDeploy on Fridays.","contentHash":"h1","versionCreatedAt":"2026-10-25T10:00:00.000Z"}`
	kbSearchJSON = `{"hits":[{"pageId":"kpg_abc12","sourceDocId":null,"url":null,"spaceSlug":"eng","slug":"runbook","title":"Runbook","version":2,"heading":"Deploy","snippet":"Deploy on\nFridays.","score":0.81,"verifiedAt":null,"stale":true}],"mode":"full_text","reason":"The organisation has no embedding key"}`
)

// kbGateway answers every knowledge base route with a document of its kind and
// records the query of each request.
func kbGateway(t *testing.T) (*packServer, func() map[string][]url.Values) {
	t.Helper()
	gw := workflowGateway(t, routeMap{})
	var mu sync.Mutex
	queries := map[string][]url.Values{}
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		mu.Lock()
		queries[route] = append(queries[route], r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/pages/fresh"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found","message":"Page not found."}`)
		case strings.HasSuffix(path, "/export"):
			_, _ = io.WriteString(w, `{"filename":"eng.zip","contentBase64":"UEsDBA=="}`)
		case strings.HasSuffix(path, "/import"):
			_, _ = io.WriteString(w, `{"created":1,"updated":0,"unchanged":0,"skipped":[]}`)
		case strings.HasSuffix(path, "/git/push") || strings.HasSuffix(path, "/push"):
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_, _ = io.WriteString(w, `{"enabled":true}`)
		case r.Method == http.MethodDelete || r.Method == http.MethodPut || strings.HasSuffix(path, "/run-links"):
			w.WriteHeader(http.StatusNoContent)
		case path == "/api/v1/kb/search":
			_, _ = io.WriteString(w, kbSearchJSON)
		case path == "/api/v1/kb/search/status":
			_, _ = io.WriteString(w, `{"mode":"full_text","status":"full_text","activeModel":null,"targetModel":"openai/text-embedding-3-small","reason":"No key","pendingPassages":12}`)
		case path == "/api/v1/kb/usage":
			_, _ = io.WriteString(w, `{"spaces":[{"space":"eng","name":"Engineering","pages":4,"passages":20,"storageBytes":900}],"personal":{"spaces":1,"pages":2,"storageBytes":10},"embeddingTokensThisMonth":5000}`)
		case path == "/api/v1/kb/search/reindex" || strings.HasSuffix(path, "/sync"):
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"queued":true}`)
		case strings.HasPrefix(path, "/api/v1/kb/documents/"):
			_, _ = io.WriteString(w, `{"id":"ksd_abc12","title":"Design doc","url":"https://notion.example/d","space":"eng","text":"Body","syncedAt":"2026-10-25"}`)
		case path == "/api/v1/kb/spaces" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[{"id":"ksp_1","slug":"eng","name":"Engineering","kind":"org","defaultAccess":"reader","reviewDays":90,"deleteAfter":null,"role":"admin","createdAt":"2026-10-01"}]}`)
		case strings.HasSuffix(path, "/pages") && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[`+kbPageJSON+`]}`)
		case strings.HasSuffix(path, "/members") || strings.HasSuffix(path, "/grants") || strings.HasSuffix(path, "/versions") && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[{"principalKind":"user","principal":"a@b.c","role":"editor","version":2,"authorUserId":"usr_1","createdAt":"2026-10-25","contentHash":"h1"}]}`)
		case strings.HasSuffix(path, "/backlinks"):
			_, _ = io.WriteString(w, `{"items":[{"pageId":"kpg_zz","spaceSlug":"eng","slug":"index","title":"Index","kind":"wikilink"}]}`)
		case path == "/api/v1/kb/sources" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"items":[{"id":"src_1","kind":"notion","name":"Wiki","space":"eng","config":{},"schedule":"daily","status":"active","lastSyncedAt":null,"lastError":null,"documents":7,"createdAt":"2026-10-01"}]}`)
		case strings.HasSuffix(path, "/documents"):
			_, _ = io.WriteString(w, `{"items":[{"id":"ksd_abc12","title":"Design doc","url":"u","syncedAt":"2026-10-25","orgWide":false,"readerCount":3,"unmappedReaders":1,"adminOnly":false}]}`)
		default:
			_, _ = io.WriteString(w, kbPageJSON)
		}
	})
	return gw, func() map[string][]url.Values {
		mu.Lock()
		defer mu.Unlock()
		return queries
	}
}

func TestKbCommandsSendWhatTheOpenAPIDescribes(t *testing.T) {
	doc := loadOpenAPI(t)
	gw, queries := kbGateway(t)
	sourceConfig := jsonFile(t, `{"workspace":"w1"}`)
	importZip := filepath.Join(t.TempDir(), "in.zip")
	if err := os.WriteFile(importZip, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, inv := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"search", "deploy", "runbook", "--space", "eng", "--limit", "5"}},
		{"", []string{"get", "eng/runbook", "--version", "2"}},
		{"", []string{"get", "kpg_abc12"}},
		{"", []string{"get", "ksd_abc12"}},
		{"# Runbook\nDeploy.", []string{"write", "eng/runbook", "--file", "-", "--title", "Runbook"}},
		{"# Fresh", []string{"write", "eng/fresh", "--file", "-"}},
		{"", []string{"link", "eng/runbook", "--kind", "runs", "--target", "skill:invoice"}},
		{"", []string{"link", "eng/runbook", "--kind", "runs", "--target", "skill:invoice", "--remove"}},
		{"", []string{"spaces", "list"}},
		{"", []string{"spaces", "show", "eng"}},
		{"", []string{"spaces", "create", "docs", "--name", "Docs", "--default-access", "reader", "--review-days", "90"}},
		{"", []string{"spaces", "update", "eng", "--name", "Engineering", "--default-access", "editor", "--review-days", "30"}},
		{"", []string{"spaces", "graph", "eng"}},
		{"", []string{"spaces", "routines", "eng"}},
		{"", []string{"spaces", "members", "list", "eng"}},
		{"", []string{"spaces", "members", "set", "eng", "--user", "a@b.c", "--role", "editor"}},
		{"", []string{"spaces", "members", "remove", "eng", "--group", "staff"}},
		{"", []string{"pages", "list", "eng"}},
		{"", []string{"pages", "versions", "kpg_abc12"}},
		{"", []string{"pages", "backlinks", "kpg_abc12"}},
		{"", []string{"pages", "runs", "eng/runbook"}},
		{"", []string{"pages", "verify", "kpg_abc12"}},
		{"", []string{"pages", "status", "kpg_abc12", "archived"}},
		{"", []string{"pages", "promote", "kpg_abc12", "--space", "eng"}},
		{"", []string{"pages", "grants", "kpg_abc12"}},
		{"", []string{"pages", "grant", "kpg_abc12", "--user", "a@b.c", "--role", "none"}},
		{"", []string{"pages", "ungrant", "kpg_abc12", "--group", "staff"}},
		{"", []string{"sources", "list"}},
		{"", []string{"sources", "show", "src_1"}},
		{"", []string{"sources", "create", "--kind", "notion", "--name", "Wiki", "--space", "eng", "--schedule", "daily", "--config-file", sourceConfig}},
		{"", []string{"sources", "update", "src_1", "--name", "Wiki 2", "--schedule", "manual", "--status", "paused", "--config-file", sourceConfig}},
		{"", []string{"sources", "delete", "src_1", "--yes"}},
		{"", []string{"sources", "sync", "src_1"}},
		{"", []string{"sources", "documents", "src_1"}},
		{"", []string{"sources", "push", "enable", "src_1"}},
		{"", []string{"sources", "push", "disable", "src_1"}},
		{"", []string{"spaces", "git", "show", "eng"}},
		{"", []string{"spaces", "git", "set", "eng", "--repo", "acme/handbook", "--branch", "main", "--path", "docs"}},
		{"", []string{"spaces", "git", "remove", "eng"}},
		{"", []string{"spaces", "git", "sync", "eng"}},
		{"", []string{"spaces", "git", "push", "enable", "eng"}},
		{"", []string{"spaces", "git", "push", "disable", "eng"}},
		{"", []string{"export", "eng", "--out", filepath.Join(t.TempDir(), "eng.zip")}},
		{"", []string{"import", "eng", importZip}},
		{"", []string{"status"}},
		{"", []string{"reindex"}},
		{"", []string{"usage"}},
	} {
		before := requestCount(gw)
		if out, err := runWithStdin(t, KbCmd(), inv.stdin, inv.args...); err != nil {
			t.Errorf("%v: %v\n%s", inv.args, err, out)
		}
		if requestCount(gw) == before {
			t.Errorf("%v sent no request", inv.args)
		}
	}

	seen := queries()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	covered := map[string]bool{}
	for route, hits := range gw.hits {
		method, path, _ := strings.Cut(route, " ")
		op, template, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered[method+" "+template] = true
		for i := 0; i < hits; i++ {
			var body map[string]any
			if i < len(gw.bodies[route]) {
				body = gw.bodies[route][i]
			}
			var query url.Values
			if i < len(seen[route]) {
				query = seen[route][i]
			}
			for _, p := range op.problems(query, body) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
	}
	// The commands cover every knowledge base operation but these two.
	notCommands := map[string]bool{"POST /kb/captures": true, "GET /kb/pages/{id}/html": true}
	for template, ops := range doc.Paths {
		if !strings.HasPrefix(template, "/kb/") {
			continue
		}
		for method := range ops {
			key := strings.ToUpper(method) + " " + template
			if !covered[key] && !notCommands[key] {
				t.Errorf("no command exercised %s", key)
			}
		}
	}
}

func TestKbSearchAndGetPrintWhatTheGatewayReturned(t *testing.T) {
	_, queries := kbGateway(t)
	out, err := runCommand(t, KbCmd(), "search", "deploy", "--space", "eng", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"eng/runbook@2", "Runbook", "0.81", "stale", "Deploy on Fridays.", "Ranked by words only: The organisation has no embedding key"} {
		if !strings.Contains(out, want) {
			t.Errorf("search lacks %q:\n%s", want, out)
		}
	}
	if q := queries()["GET /api/v1/kb/search"][0]; q.Get("q") != "deploy" || q.Get("space") != "eng" || q.Get("limit") != "5" {
		t.Errorf("query = %v", q)
	}
	out, err = runCommand(t, KbCmd(), "get", "eng/runbook")
	if err != nil || !strings.Contains(out, "Page:     eng/runbook@2 (kpg_abc12)") || !strings.Contains(out, "Deploy on Fridays.") {
		t.Fatalf("get = %v\n%s", err, out)
	}
	if out, err = runCommand(t, KbCmd(), "get", "eng/runbook", "--json"); err != nil || !strings.HasPrefix(out, `{"id":"kpg_abc12"`) {
		t.Errorf("get --json = %v %q", err, out)
	}
}

func TestKbRejectsBadInputBeforeAnyRequest(t *testing.T) {
	gw, _ := kbGateway(t)
	empty := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(empty, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"get", "not a page"},
		{"get", "Eng/Runbook"},
		{"get", "eng/runbook", "--version", "-1"},
		{"search", "x", "--space", "Bad Space"},
		{"search", "x", "--limit", "99"},
		{"write", "eng/runbook"},
		{"write", "kpg_abc12", "--file", "-"},
		{"write", "eng/runbook", "--file", empty},
		{"link", "eng/runbook", "--kind", "other", "--target", "t"},
		{"link", "eng/runbook", "--kind", "runs"},
		{"spaces", "create", "docs"},
		{"spaces", "create", "docs", "--name", "Docs", "--default-access", "everyone"},
		{"spaces", "update", "eng"},
		{"spaces", "members", "set", "eng", "--role", "editor"},
		{"spaces", "members", "set", "eng", "--user", "a", "--group", "b", "--role", "editor"},
		{"spaces", "members", "set", "eng", "--user", "a", "--role", "owner"},
		{"pages", "status", "kpg_abc12", "gone"},
		{"pages", "promote", "kpg_abc12"},
		{"sources", "create", "--kind", "dropbox", "--name", "n", "--space", "eng", "--config-file", "-"},
		{"sources", "delete", "src_1"},
		{"import", "eng", filepath.Join(t.TempDir(), "missing.zip")},
	} {
		if out, err := runWithStdin(t, KbCmd(), "", args...); err == nil {
			t.Errorf("%v succeeded:\n%s", args, out)
		}
	}
	if n := requestCount(gw); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

func TestKbWriteCreatesAPageThatDoesNotExistAndStopsOnAConflict(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/kb/spaces/eng/pages/new-page": jsonReply(404, `{"error":"not_found","message":"Page not found."}`),
		"POST /api/v1/kb/spaces/eng/pages":         jsonReply(201, kbPageJSON),
		"GET /api/v1/kb/spaces/eng/pages/runbook":  jsonReply(200, kbPageJSON),
		"POST /api/v1/kb/pages/kpg_abc12/versions": jsonReply(409, `{"error":"version_conflict","message":"The page moved on."}`),
	})
	out, err := runWithStdin(t, KbCmd(), "# New", "write", "eng/new-page", "--file", "-")
	if err != nil || !strings.Contains(out, "Wrote eng/runbook@2") {
		t.Fatalf("create = %v\n%s", err, out)
	}
	if body := gw.body("POST /api/v1/kb/spaces/eng/pages", 0); body["slug"] != "new-page" || body["markdown"] != "# New" {
		t.Errorf("create body = %v", body)
	}
	_, err = runWithStdin(t, KbCmd(), "# Changed", "write", "eng/runbook", "--file", "-")
	if got := ExitCode(err); got != exitDraftConflict {
		t.Fatalf("a stale write exits %d, want %d (%v)", got, exitDraftConflict, err)
	}
	if body := gw.body("POST /api/v1/kb/pages/kpg_abc12/versions", 0); body["expected_version"] != float64(2) {
		t.Errorf("the write was not based on the version read: %v", body)
	}
}

func TestKbRefusalsEndWithTheirRegisteredExit(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{409, "space_exists", 1},
		{409, "page_exists", 1},
		{409, "version_conflict", exitDraftConflict},
		{409, "under_hold", 1},
		{404, "not_found", 1},
		{400, "validation_error", 1},
		{413, "payload_too_large", 1},
		{403, "forbidden", exitDenied},
		{403, "browser_session_required", exitDenied},
	} {
		t.Run(tc.code, func(t *testing.T) {
			workflowGateway(t, routeMap{
				"GET /api/v1/kb/spaces/eng": jsonReply(tc.status, `{"error":"`+tc.code+`","message":"`+tc.code+` message."}`),
			})
			_, err := runCommand(t, KbCmd(), "spaces", "show", "eng")
			if got := ExitCode(err); got != tc.exit {
				t.Fatalf("exit %d, want %d (%v)", got, tc.exit, err)
			}
			if !strings.Contains(err.Error(), tc.code+" message.") {
				t.Errorf("the gateway's sentence is lost: %v", err)
			}
		})
	}
}

// An agent credential may not use the REST routes: it reaches the knowledge
// base through the tools, and a write is held for a person.
func TestKbAsAnAgentUsesTheToolsAndAHeldWriteExitsPending(t *testing.T) {
	var mu sync.Mutex
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var rpc struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(raw, &rpc)
		if r.URL.Path != "/api/gateway/mcp" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch rpc.Method {
		case "tools/call":
			mu.Lock()
			calls = append(calls, rpc.Params)
			mu.Unlock()
			if rpc.Params["name"] == "kb_write" || rpc.Params["name"] == "kb_link" {
				_, _ = io.WriteString(w, contractFixture(t, "mcp-pending-approval.json"))
				return
			}
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"found it"}]}}`)
		case "initialize":
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":0,"result":{}}`)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(srv.Close)
	withAPIKeyGateway(t, srv)
	t.Setenv("ZUANO_AGENT", "1")

	out, err := runCommand(t, KbCmd(), "search", "deploy", "--space", "eng", "--limit", "3")
	if err != nil || !strings.Contains(out, "found it") {
		t.Fatalf("search = %v\n%s", err, out)
	}
	if out, err = runCommand(t, KbCmd(), "get", "eng/runbook", "--version", "2"); err != nil {
		t.Fatalf("get = %v\n%s", err, out)
	}
	out, err = runWithStdin(t, KbCmd(), "# Runbook", "write", "eng/runbook", "--file", "-", "--title", "Runbook")
	if got := ExitCode(err); got != exitPending {
		t.Fatalf("a held write exits %d, want %d (%v)", got, exitPending, err)
	}
	if !strings.Contains(out, "apr_1") {
		t.Errorf("the approval is not named:\n%s", out)
	}
	if _, err = runCommand(t, KbCmd(), "link", "eng/runbook", "--kind", "conflicts", "--target", "kpg_other"); ExitCode(err) != exitPending {
		t.Errorf("a held link exits %d (%v)", ExitCode(err), err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []map[string]any{
		{"name": "kb_search", "arguments": map[string]any{"query": "deploy", "space": "eng", "limit": float64(3)}},
		{"name": "kb_get", "arguments": map[string]any{"page": "eng/runbook", "version": float64(2)}},
		{"name": "kb_write", "arguments": map[string]any{"space": "eng", "slug": "runbook", "markdown": "# Runbook", "title": "Runbook"}},
		{"name": "kb_link", "arguments": map[string]any{"page": "eng/runbook", "kind": "conflicts", "target": "kpg_other"}},
	}
	if len(calls) != len(want) {
		t.Fatalf("%d tool calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, w := range want {
		got, _ := json.Marshal(map[string]any{"name": calls[i]["name"], "arguments": calls[i]["arguments"]})
		exp, _ := json.Marshal(w)
		if string(got) != string(exp) {
			t.Errorf("call %d = %s, want %s", i, got, exp)
		}
	}
}

func TestKbExportSavesTheArchiveAndImportSendsIt(t *testing.T) {
	zip := []byte("PK\x03\x04archive")
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/kb/spaces/eng/export":  jsonReply(200, `{"filename":"../eng.zip","contentBase64":"`+base64.StdEncoding.EncodeToString(zip)+`"}`),
		"POST /api/v1/kb/spaces/eng/import": jsonReply(200, `{"created":2,"updated":1,"unchanged":3,"skipped":["notes/readme.txt"]}`),
	})
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.zip")
	if out, err := runCommand(t, KbCmd(), "export", "eng", "--out", dest); err != nil || !strings.Contains(out, "Saved") {
		t.Fatalf("export = %v\n%s", err, out)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != string(zip) {
		t.Fatalf("saved %q, %v", got, err)
	}
	if _, err := runCommand(t, KbCmd(), "export", "eng", "--out", dest); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("an existing file was replaced: %v", err)
	}
	if _, err := runCommand(t, KbCmd(), "export", "eng", "--out", dest, "--force"); err != nil {
		t.Errorf("--force: %v", err)
	}

	out, err := runCommand(t, KbCmd(), "import", "eng", dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 created", "1 updated", "3 unchanged", "skipped notes/readme.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("import lacks %q:\n%s", want, out)
		}
	}
	if body := gw.body("POST /api/v1/kb/spaces/eng/import", 0); body["contentBase64"] != base64.StdEncoding.EncodeToString(zip) {
		t.Errorf("import body = %v", body)
	}
}

func TestOrgDomainsSendWhatTheOpenAPIDescribes(t *testing.T) {
	doc := loadOpenAPI(t)
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/domains":               jsonReply(200, `{"domains":[`+pendingDomainJSON+`]}`),
		"POST /api/v1/domains":              jsonReply(201, `{"domain":`+pendingDomainJSON+`}`),
		"POST /api/v1/domains/dom_1/verify": jsonReply(200, `{"domain":`+verifiedDomainJSON+`}`),
		"DELETE /api/v1/domains/dom_1":      func(_ *packServer, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	for _, args := range [][]string{{"domains", "list"}, {"domains", "add", "example.com"}, {"domains", "verify", "dom_1"}, {"domains", "remove", "dom_1", "--yes"}} {
		if out, err := runCommand(t, OrgCmd(), args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	covered := 0
	for route, hits := range gw.hits {
		method, path, _ := strings.Cut(route, " ")
		op, _, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered++
		for i := 0; i < hits; i++ {
			var body map[string]any
			if i < len(gw.bodies[route]) {
				body = gw.bodies[route][i]
			}
			for _, p := range op.problems(nil, body) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
	}
	if covered != 4 {
		t.Errorf("%d domain operations were exercised, want 4", covered)
	}
}

func TestApprovalShowAndDecisionNameHowItWasDecided(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/approvals/apr_1": reply(200, `{"approval":{"approval_id":"apr_1","status":"approved","service":"stripe","tool":"create_refund","risk":"destructive","args_digest":"9b2e","requester":"dev@example.com","reason":"risk","created_at":"2026-10-16T12:00:00.000Z","expires_at":"2026-10-16T12:15:00.000Z","decided_at":"2026-10-16T12:01:00.000Z","approver":"boss@example.com","decided_via":{"method":"passkey","session":"approval"}}}`),
	})
	out, err := runCommand(t, ApprovalsCmd(), "show", "apr_1")
	if err != nil || !strings.Contains(out, "Via:       passkey, approval session") {
		t.Fatalf("show = %v\n%s", err, out)
	}
	a := approvalRecord{Status: "denied", Approver: "boss@example.com", DecidedVia: &decidedVia{Method: "passkey", Channel: "slack"}}
	if got := decisionSummary(a); got != "denied by boss@example.com (passkey, slack)" {
		t.Errorf("summary = %q", got)
	}
	if got := decisionSummary(approvalRecord{Status: "approved"}); got != "approved by an approver" {
		t.Errorf("summary without a path = %q", got)
	}
}
