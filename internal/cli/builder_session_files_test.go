package cli

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const sessionFileJSON = `{"id":"fil_abc123","name":"report.txt","contentType":"text/plain; charset=utf-8","sizeBytes":5,"state":"clean","createdAt":"2026-10-25T10:00:00.000Z","expiresAt":"2026-11-25T10:00:00.000Z"}`

func sessionFilesGateway(t *testing.T) (*packServer, func() map[string][]url.Values, func() string) {
	t.Helper()
	gw := workflowGateway(t, routeMap{})
	var mu sync.Mutex
	queries := map[string][]url.Values{}
	stored := ""
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		mu.Lock()
		queries[route] = append(queries[route], r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch route {
		case "GET /api/v1/sessions/ses_1/files":
			_, _ = io.WriteString(w, `{"items":[`+sessionFileJSON+`],"nextCursor":"c2"}`)
		case "POST /api/v1/sessions/ses_1/files":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"fileId":"fil_abc123","upload":{"url":"`+gw.URL+`/store/up","method":"PUT","headers":{"x-amz-meta":"1"},"expiresAt":"2026-10-25T11:00:00.000Z"}}`)
		case "PUT /store/up":
			raw, _ := io.ReadAll(r.Body)
			mu.Lock()
			stored = string(raw)
			mu.Unlock()
		case "POST /api/v1/sessions/ses_1/files/fil_abc123/complete":
			_, _ = io.WriteString(w, `{"file":`+strings.Replace(sessionFileJSON, `"clean"`, `"scanning"`, 1)+`}`)
		case "GET /api/v1/sessions/ses_1/files/fil_abc123":
			_, _ = io.WriteString(w, `{"file":`+sessionFileJSON+`,"download":{"url":"`+gw.URL+`/store/down","expiresAt":"2026-10-25T11:00:00.000Z"}}`)
		case "GET /store/down":
			_, _ = io.WriteString(w, "hello")
		case "DELETE /api/v1/sessions/ses_1/files/fil_abc123":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", route)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return gw, func() map[string][]url.Values {
			mu.Lock()
			defer mu.Unlock()
			return queries
		}, func() string {
			mu.Lock()
			defer mu.Unlock()
			return stored
		}
}

func TestBuilderSessionFilesRoundTrip(t *testing.T) {
	doc := loadOpenAPI(t)
	gw, queries, stored := sessionFilesGateway(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCommand(t, BuilderCmd(), "sessions", "files", "list", "ses_1", "--limit", "10", "--cursor", "c1")
	if err != nil || !strings.Contains(out, "fil_abc123") || !strings.Contains(out, "report.txt") || !strings.Contains(out, "More: --cursor c2") {
		t.Fatalf("list = %v\n%s", err, out)
	}
	out, err = runCommand(t, BuilderCmd(), "sessions", "files", "upload", "ses_1", src)
	if err != nil || !strings.Contains(out, "fil_abc123  scanning") {
		t.Fatalf("upload = %v\n%s", err, out)
	}
	if stored() != "hello" {
		t.Errorf("the object store received %q", stored())
	}
	body := gw.body("POST /api/v1/sessions/ses_1/files", 0)
	if body["name"] != "report.txt" || body["size"] != float64(5) || body["contentType"] == "" || len(body["sha256"].(string)) != 64 {
		t.Errorf("create body = %v", body)
	}

	dest := filepath.Join(dir, "got")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err = runCommand(t, BuilderCmd(), "sessions", "files", "download", "ses_1", "fil_abc123", "-o", dest)
	if err != nil || !strings.Contains(out, "Saved") {
		t.Fatalf("download = %v\n%s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "report.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("saved %q, %v", got, err)
	}
	if _, err := runCommand(t, BuilderCmd(), "sessions", "files", "download", "ses_1", "fil_abc123", "-o", dest); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("an existing file was replaced: %v", err)
	}
	if out, err = runCommand(t, BuilderCmd(), "sessions", "files", "rm", "ses_1", "fil_abc123"); err != nil || !strings.Contains(out, "Deleted fil_abc123") {
		t.Fatalf("rm = %v\n%s", err, out)
	}

	seen := queries()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	covered := map[string]bool{}
	for route, hits := range gw.hits {
		method, path, _ := strings.Cut(route, " ")
		if strings.HasPrefix(path, "/store/") {
			continue
		}
		op, template, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered[method+" "+template] = true
		for i := 0; i < hits; i++ {
			var b map[string]any
			if i < len(gw.bodies[route]) {
				b = gw.bodies[route][i]
			}
			for _, p := range op.problems(seen[route][i], b) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
	}
	if len(covered) != 5 {
		t.Errorf("exercised %d of the 5 session file operations: %v", len(covered), covered)
	}
}

func TestBuilderSessionFilesRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{409, "conflict", 1},
		{413, "payload_too_large", 1},
		{400, "validation_error", 1},
		{404, "not_found", 1},
		{403, "forbidden", exitDenied},
		{402, "plan_required", 1},
	} {
		t.Run(tc.code, func(t *testing.T) {
			workflowGateway(t, routeMap{"GET /api/v1/sessions/ses_1/files": jsonReply(tc.status, `{"error":"`+tc.code+`","message":"`+tc.code+` message."}`)})
			_, err := runCommand(t, BuilderCmd(), "sessions", "files", "list", "ses_1")
			if err == nil || !strings.Contains(err.Error(), tc.code+" message.") {
				t.Fatalf("the gateway's sentence is lost: %v", err)
			}
			if tc.code != "plan_required" && ExitCode(err) != tc.exit {
				t.Errorf("exit %d, want %d", ExitCode(err), tc.exit)
			}
		})
	}
	if _, err := runCommand(t, BuilderCmd(), "sessions", "files", "list", "sess_1"); err == nil {
		t.Error("a mistyped session id reached the gateway")
	}
}
