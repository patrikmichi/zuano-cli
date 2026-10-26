package cli

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDropOpen_PreviewFallsBackToTheReleaseAddress(t *testing.T) {
	var g *dropGW
	g = newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/artifacts/sites":                                    fixtureReply(t, func() *dropGW { return g }, 200, "artifact-sites.json"),
		"POST /api/artifacts/releases/arl_r1e2l3e4a5/preview-session": reply(404, `{"error":"not_found","message":"Not found."}`),
		"GET /api/artifacts/releases/arl_r1e2l3e4a5":                  fixtureReply(t, func() *dropGW { return g }, 200, "artifact-release-ready.json"),
	})
	out, err := runCommand(t, DropCmd(), "open", "docs", "--preview", "arl_r1e2l3e4a5", "--no-browser")
	if err != nil || strings.TrimSpace(out) != "https://r1e2l3e4a5--preview--acme.sites.example/" {
		t.Fatalf("open preview: %q, %v", out, err)
	}
}

func TestDropPublish_ListsEveryRefusedPath(t *testing.T) {
	newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/artifacts/uploads": reply(400, contractFixture(t, "artifact-path-invalid.json")),
	})
	root := writeSite(t, map[string]string{"index.html": "x"})
	_, err := runCommand(t, DropCmd(), "publish", root, "--site", "docs")
	if err == nil {
		t.Fatal("a refused publish succeeded")
	}
	for _, want := range []string{"3 paths cannot be published.", "a%2fb.html: encoded_separator", "keys/server.pem: denied (key or certificate store)", "Docs/A.md: case (docs/a.md and Docs/A.md)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestDropActivateAndRollback_MapTheirRefusals(t *testing.T) {
	var g *dropGW
	status, body := 200, ""
	g = newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/artifacts/sites": fixtureReply(t, func() *dropGW { return g }, 200, "artifact-sites.json"),
		"POST /api/artifacts/sites/ast_s1i2t3e4a5/activate": func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(status)
			if body == "" {
				body = g.fill(contractFixture(t, "artifact-release-ready.json"))
			}
			_, _ = w.Write([]byte(body))
		},
	})
	out, err := runCommand(t, DropCmd(), "activate", "docs", "arl_r1e2l3e4a5")
	if err != nil || !strings.Contains(out, "arl_r1e2l3e4a5 is live at "+g.srv.URL+"/site/") {
		t.Fatalf("activate: %q, %v", out, err)
	}
	if got := g.calls("POST", "/api/artifacts/sites/ast_s1i2t3e4a5/activate")[0].body; got["release_id"] != "arl_r1e2l3e4a5" || got["expected_content_revision"] != float64(4) {
		t.Errorf("activate body = %v", got)
	}
	for _, tc := range []struct {
		status int
		body   string
		code   int
	}{
		{409, `{"error":"scan_pending","message":"The data protection scan has not finished."}`, 47},
		{412, `{"error":"revision_conflict","message":"The site changed."}`, 25},
		{409, `{"error":"audience_changed","message":"Who can open the site changed."}`, 1},
	} {
		status, body = tc.status, tc.body
		for _, verb := range []string{"activate", "rollback"} {
			_, err := runCommand(t, DropCmd(), verb, "docs", "arl_o1l2d3e4r5")
			if got := exitCodeOf(err); got != tc.code || err == nil || !strings.Contains(err.Error(), "HTTP") {
				t.Errorf("%s %s: exit %d (%v), want %d", verb, tc.body, got, err, tc.code)
			}
		}
	}
}

func TestDropShare_AViewerCannotChangeAndSeesTheirRole(t *testing.T) {
	var g *dropGW
	viewer := strings.Replace(contractFixture(t, "artifact-site-detail.json"), `"role":"owner"}`, `"role":"viewer"}`, 1)
	g = newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/artifacts/sites":                fixtureReply(t, func() *dropGW { return g }, 200, "artifact-sites.json"),
		"GET /api/artifacts/sites/ast_s1i2t3e4a5": reply(200, viewer),
	})
	out, err := runCommand(t, DropCmd(), "share", "docs")
	if err != nil || !strings.Contains(out, "Your role: viewer") {
		t.Fatalf("show: %q, %v", out, err)
	}
	_, err = runCommand(t, DropCmd(), "share", "docs", "--org", "--yes")
	if exitCodeOf(err) != exitDenied || !strings.Contains(err.Error(), "you can view this site") {
		t.Errorf("a viewer's change: exit %d, %v", exitCodeOf(err), err)
	}
	if n := len(g.calls("PUT", "/api/artifacts/sites/ast_s1i2t3e4a5/sharing?mode=propose")); n != 0 {
		t.Errorf("a viewer's change sent %d proposals", n)
	}
}

func TestDropOrg_ShowsAndChangesTheSettings(t *testing.T) {
	g := newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/artifacts/org": reply(200, contractFixture(t, "artifact-org.json")),
		"PUT /api/artifacts/org": reply(200, contractFixture(t, "artifact-org.json")),
	})
	out, err := runCommand(t, DropCmd(), "org")
	for _, want := range []string{"Sites:          on", "Label:          acme", "Public sharing: off", "Storage:        4096 bytes of 1073741824"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("org lacks %q (%v):\n%s", want, err, out)
		}
	}
	if _, err = runCommand(t, DropCmd(), "org", "--public-sharing=true", "--allow-remote=false", "--enabled=true"); err != nil {
		t.Fatal(err)
	}
	body := g.calls("PUT", "/api/artifacts/org")[0].body
	if len(body) != 3 || body["public_sharing"] != true || body["allow_remote"] != false || body["enabled"] != true {
		t.Errorf("org body = %v", body)
	}
	if _, err = runCommand(t, DropCmd(), "org", "--label", "Acme"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("a label without confirmation: %v", err)
	}
	if _, err = runCommand(t, DropCmd(), "org", "--label", "Acme", "--yes"); err != nil {
		t.Fatal(err)
	}
	if body := g.calls("PUT", "/api/artifacts/org")[1].body; len(body) != 1 || body["label"] != "acme" {
		t.Errorf("label body = %v", body)
	}
	if _, err = runCommand(t, DropCmd(), "org", "--label", "no_underscores"); err == nil {
		t.Error("an invalid label was accepted")
	}
	if len(g.calls("PUT", "/api/artifacts/org")) != 2 {
		t.Error("an unconfirmed or invalid label was sent")
	}
}

func TestDropCommandsSendWhatTheOpenAPIDescribes(t *testing.T) {
	doc := loadOpenAPI(t)
	var g *dropGW
	site := func() func(http.ResponseWriter, map[string]any) {
		return fixtureReply(t, func() *dropGW { return g }, 200, "artifact-sites.json")
	}
	g = newDropGW(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/artifacts/uploads":                                  fixtureReply(t, func() *dropGW { return g }, 200, "artifact-upload-create.json"),
		"POST /api/artifacts/uploads/aup_x1y2z3a4b5/finalize":          reply(200, `{"release_id":"arl_r1e2l3e4a5","status":"preparing"}`),
		"GET /api/artifacts/releases/arl_r1e2l3e4a5":                   fixtureReply(t, func() *dropGW { return g }, 200, "artifact-release-ready.json"),
		"GET /api/artifacts/sites":                                     site(),
		"GET /api/artifacts/sites?status=deleted":                      site(),
		"GET /api/artifacts/sites/ast_s1i2t3e4a5":                      fixtureReply(t, func() *dropGW { return g }, 200, "artifact-site-detail.json"),
		"GET /api/artifacts/sites/ast_s1i2t3e4a5/releases":             reply(200, contractFixture(t, "artifact-releases.json")),
		"POST /api/artifacts/sites/ast_s1i2t3e4a5/activate":            fixtureReply(t, func() *dropGW { return g }, 200, "artifact-release-ready.json"),
		"PUT /api/artifacts/sites/ast_s1i2t3e4a5/sharing?mode=propose": reply(200, contractFixture(t, "artifact-share-proposal.json")),
		"PUT /api/artifacts/sites/ast_s1i2t3e4a5/sharing?mode=apply":   reply(200, contractFixture(t, "artifact-share-applied.json")),
		"DELETE /api/artifacts/sites/ast_s1i2t3e4a5":                   reply(204, ``),
		"POST /api/artifacts/sites/ast_s1i2t3e4a5/restore":             reply(200, `{"site":{}}`),
		"GET /api/artifacts/org":                                       reply(200, contractFixture(t, "artifact-org.json")),
		"PUT /api/artifacts/org":                                       reply(200, contractFixture(t, "artifact-org.json")),
	})
	root := writeSite(t, map[string]string{"index.html": "<h1>hi</h1>"})
	for _, args := range [][]string{
		{"publish", root, "--site", "docs", "--title", "Team docs", "--spa", "--entry", "index.html"},
		{"list"},
		{"releases", "docs"},
		{"activate", "docs", "arl_r1e2l3e4a5"},
		{"rollback", "docs", "arl_o1l2d3e4r5"},
		{"share", "docs"},
		{"share", "docs", "--add", "bo@example.com", "--remove", "group:eng", "--public", "--expires", "7d", "--yes"},
		{"delete", "docs", "--yes"},
		{"restore", "docs"},
		{"org"},
		{"org", "--enabled=true", "--public-sharing=false", "--allow-remote=false"},
	} {
		if out, err := runCommand(t, DropCmd(), args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	covered := map[string]bool{}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.requests {
		u, err := url.Parse(r.uri)
		if err != nil {
			t.Fatal(err)
		}
		op, template, ok := doc.operationFor(r.method, u.Path)
		if !ok {
			t.Errorf("%s %s is not in the gateway's OpenAPI document", r.method, r.uri)
			continue
		}
		covered[r.method+" "+template] = true
		for _, p := range op.problems(u.Query(), r.body) {
			t.Errorf("%s %s (%s): %s", r.method, r.uri, op.ID, p)
		}
	}
	for _, want := range []string{
		"POST /uploads", "POST /uploads/{id}/finalize", "GET /releases/{id}", "GET /sites", "GET /sites/{id}", "GET /sites/{id}/releases",
		"POST /sites/{id}/activate", "PUT /sites/{id}/sharing", "DELETE /sites/{id}", "POST /sites/{id}/restore", "GET /org", "PUT /org",
	} {
		if !covered[want] {
			t.Errorf("no drop command exercised %s", want)
		}
	}
}
