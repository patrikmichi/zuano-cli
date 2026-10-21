package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// openAPIEnv names the gateway's OpenAPI document (gateway/openapi/v1.json).
// Unset, the vendored copy under testdata/contracts is used.
const openAPIEnv = "ZUANO_GATEWAY_OPENAPI"

var vendoredOpenAPI, _ = filepath.Abs(filepath.Join("testdata", "contracts", "openapi-v1.json"))

type openAPISchema struct {
	Properties           map[string]json.RawMessage `json:"properties"`
	Required             []string                   `json:"required"`
	AdditionalProperties json.RawMessage            `json:"additionalProperties"`
}

type openAPIOperation struct {
	ID         string `json:"operationId"`
	Parameters []struct {
		Name string `json:"name"`
		In   string `json:"in"`
	} `json:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema openAPISchema `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

type openAPIDocument struct {
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Paths map[string]map[string]openAPIOperation `json:"paths"`
}

func loadOpenAPI(t *testing.T) openAPIDocument {
	t.Helper()
	path := vendoredOpenAPI
	if env := os.Getenv(openAPIEnv); env != "" {
		path = env
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc openAPIDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Servers) == 0 {
		t.Fatal("the OpenAPI document names no server")
	}
	return doc
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

// templateRegexp matches a request path against a path template; a parameter
// is one segment that holds no ":" (the gateway's custom verbs follow it).
func templateRegexp(template string) *regexp.Regexp {
	parts := pathParameter.Split(template, -1)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, "[^/:]+") + "$")
}

// operationFor finds the operation a request addresses.
func (d openAPIDocument) operationFor(method, requestPath string) (openAPIOperation, string, bool) {
	base, err := url.Parse(d.Servers[0].URL)
	if err != nil || !strings.HasPrefix(requestPath, base.Path+"/") {
		return openAPIOperation{}, "", false
	}
	rel := strings.TrimPrefix(requestPath, base.Path)
	for template, ops := range d.Paths {
		if op, ok := ops[strings.ToLower(method)]; ok && templateRegexp(template).MatchString(rel) {
			return op, template, true
		}
	}
	return openAPIOperation{}, "", false
}

// problems lists how a request differs from the operation it addresses.
func (op openAPIOperation) problems(query url.Values, body map[string]any) []string {
	var out []string
	declared := map[string]bool{}
	for _, p := range op.Parameters {
		if p.In == "query" {
			declared[p.Name] = true
		}
	}
	for k := range query {
		if !declared[k] {
			out = append(out, "query parameter "+k+" is not declared")
		}
	}
	if len(body) == 0 {
		return out
	}
	if op.RequestBody == nil {
		return append(out, "sends a body, the operation takes none")
	}
	schema := op.RequestBody.Content["application/json"].Schema
	closed := string(schema.AdditionalProperties) == "false"
	for k := range body {
		if _, ok := schema.Properties[k]; !ok && closed {
			out = append(out, "body field "+k+" is not in the schema")
		}
	}
	for _, k := range schema.Required {
		if _, ok := body[k]; !ok {
			out = append(out, "body lacks the required field "+k)
		}
	}
	return out
}

// builderInvocations runs every route the builder commands call that the
// gateway's OpenAPI document describes.
var builderInvocations = []struct {
	name  string
	stdin string
	cmd   func() *cobra.Command
	args  []string
}{
	{name: "auth-configs list", cmd: AuthConfigsCmd, args: []string{"list", "--service", "github", "--search", "app", "--project", "prj_9", "--show-disabled", "--limit", "50", "--cursor", "n1"}},
	{name: "auth-configs show", cmd: AuthConfigsCmd, args: []string{"show", "ac_1"}},
	{name: "auth-configs fields", cmd: AuthConfigsCmd, args: []string{"fields", "github", "--scheme", "oauth2", "--required-only"}},
	{name: "auth-configs create", stdin: `{"client_id":"x"}`, cmd: AuthConfigsCmd, args: []string{"create", "--service", "github", "--name", "Our app", "--type", "custom", "--scheme", "oauth2", "--scopes", "repo,read:org", "--credential-source", "config", "--redirect-host", "platform", "--project", "prj_9", "--share-with-projects", "--default", "--from-file", "-"}},
	{name: "auth-configs update", cmd: AuthConfigsCmd, args: []string{"update", "ac_1", "--name", "Renamed", "--scopes", "repo", "--default=false", "--share-with-projects", "--redirect-host", "whitelabel"}},
	{name: "auth-configs disable", cmd: AuthConfigsCmd, args: []string{"disable", "ac_1", "--suspend-accounts"}},
	{name: "auth-configs enable", cmd: AuthConfigsCmd, args: []string{"enable", "ac_1"}},
	{name: "auth-configs delete", cmd: AuthConfigsCmd, args: []string{"delete", "ac_1", "--yes"}},
	{name: "auth-configs secrets set", stdin: `{"client_secret":"v"}`, cmd: AuthConfigsCmd, args: []string{"secrets", "set", "ac_1"}},
	{name: "auth-configs secrets rotate", stdin: `{"client_secret":"v"}`, cmd: AuthConfigsCmd, args: []string{"secrets", "rotate", "ac_1", "--grace", "48h"}},
	{name: "accounts list", cmd: AccountsCmd, args: []string{"list", "--user", "user_1", "--service", "gmail", "--auth-config", "ac_1", "--status", "active", "--alias", "work", "--cursor", "c1"}},
	{name: "accounts show", cmd: AccountsCmd, args: []string{"show", "ca_1"}},
	{name: "accounts update", cmd: AccountsCmd, args: []string{"update", "ca_1", "--alias", "personal", "--primary"}},
	{name: "accounts delete", cmd: AccountsCmd, args: []string{"delete", "ca_1", "--yes"}},
	{name: "accounts credentials", stdin: `{"token":"v"}`, cmd: AccountsCmd, args: []string{"credentials", "ca_1"}},
	{name: "accounts import", stdin: `{"refreshToken":"v"}`, cmd: AccountsCmd, args: []string{"import", "--user", "user_1", "--service", "gmail", "--auth-config", "ac_1", "--alias", "imported", "--from-file", "-"}},
	{name: "links create", cmd: LinksCmd, args: []string{"create", "--user", "u1", "--service", "gmail", "--auth-config", "ac_1", "--alias", "work", "--callback-url", "https://app.example/done", "--scopes", "a,b", "--expires-in", "30m"}},
	{name: "links show", cmd: LinksCmd, args: []string{"show", "lnk_1"}},
	{name: "runs list", cmd: RunsCmd, args: []string{"list", "--kind", "agent_run", "--status", "dead", "--cursor", "c1", "--limit", "20"}},
	{name: "runs show", cmd: RunsCmd, args: []string{"show", "job_1"}},
	{name: "runs cancel", cmd: RunsCmd, args: []string{"cancel", "job_1"}},
	{name: "runs retry", cmd: RunsCmd, args: []string{"retry", "job_1"}},
}

func TestContract_BuilderRequestsMatchTheOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	gw := workflowGateway(t, routeMap{})
	var mu sync.Mutex
	queries := map[string][]url.Values{}
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries[r.Method+" "+r.URL.Path] = append(queries[r.Method+" "+r.URL.Path], r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	for _, inv := range builderInvocations {
		before := requestCount(gw)
		_, _ = runWithStdin(t, inv.cmd(), inv.stdin, inv.args...)
		if requestCount(gw) == before {
			t.Errorf("%s: sent no request, so its flags no longer match the command", inv.name)
		}
	}

	gw.mu.Lock()
	defer gw.mu.Unlock()
	routes := make([]string, 0, len(gw.hits))
	for route := range gw.hits {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	covered := map[string]bool{}
	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		op, template, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered[method+" "+template] = true
		mu.Lock()
		for i := 0; i < gw.hits[route]; i++ {
			var body map[string]any
			if i < len(gw.bodies[route]) {
				body = gw.bodies[route][i]
			}
			var query url.Values
			if i < len(queries[route]) {
				query = queries[route][i]
			}
			for _, p := range op.problems(query, body) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
		mu.Unlock()
	}
	if len(covered) < 15 {
		t.Errorf("only %d operations were exercised", len(covered))
	}
}

func TestContract_OpenAPIOperationMatching(t *testing.T) {
	doc := loadOpenAPI(t)
	for _, tc := range []struct {
		method, path, template string
		ok                     bool
	}{
		{"GET", "/api/v1/auth-configs/ac_1", "/auth-configs/{id}", true},
		{"POST", "/api/v1/auth-configs/ac_1:disable", "/auth-configs/{id}:disable", true},
		{"POST", "/api/v1/auth-configs/ac_1/secrets:rotate", "/auth-configs/{id}/secrets:rotate", true},
		{"POST", "/api/v1/connected-accounts:import", "/connected-accounts:import", true},
		{"DELETE", "/api/v1/connected-accounts/ca_1", "/connected-accounts/{id}", true},
		{"POST", "/api/v1/jobs/job_1/retry", "/jobs/{id}/retry", true},
		{"GET", "/api/v1/auth-configs/ac_1:disable", "", false},
		{"GET", "/api/me/sessions", "", false},
	} {
		_, template, ok := doc.operationFor(tc.method, tc.path)
		if ok != tc.ok || template != tc.template {
			t.Errorf("%s %s = %q, %v; want %q, %v", tc.method, tc.path, template, ok, tc.template, tc.ok)
		}
	}
}

func TestContract_OpenAPIProblemsAreDetected(t *testing.T) {
	var op openAPIOperation
	if err := json.Unmarshal([]byte(`{"operationId":"x.create","parameters":[{"name":"limit","in":"query"}],"requestBody":{"content":{"application/json":{"schema":{"properties":{"name":{}},"required":["name"],"additionalProperties":false}}}}}`), &op); err != nil {
		t.Fatal(err)
	}
	if got := op.problems(url.Values{"limit": {"1"}}, map[string]any{"name": "n"}); len(got) != 0 {
		t.Errorf("a matching request has problems: %v", got)
	}
	got := strings.Join(op.problems(url.Values{"page": {"1"}}, map[string]any{"title": "n"}), "; ")
	for _, want := range []string{"query parameter page", "body field title", "required field name"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q lack %q", got, want)
		}
	}
	if got := (openAPIOperation{}).problems(nil, map[string]any{"a": 1}); len(got) != 1 {
		t.Errorf("a body on an operation without one: %v", got)
	}
}

func requestCount(gw *packServer) int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	n := 0
	for _, c := range gw.hits {
		n += c
	}
	return n
}
