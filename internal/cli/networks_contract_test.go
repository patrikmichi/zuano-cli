package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
)

// openAPIResponseSchema reads the JSON schema a response status of an operation declares.
func openAPIResponseSchema(t *testing.T, op openAPIOperation, status string) map[string]any {
	t.Helper()
	res, ok := op.Responses[status]
	if !ok {
		t.Fatalf("%s declares no %s response", op.ID, status)
	}
	return res.Content["application/json"].Schema
}

// schemaProblems lists how a decoded JSON value departs from a schema: a
// field the schema does not declare where it is closed, a missing required
// field, a value of another type or outside an enum.
func schemaProblems(schema map[string]any, v any, at string) []string {
	if alts, ok := schema["anyOf"].([]any); ok {
		var first []string
		for i, alt := range alts {
			problems := schemaProblems(alt.(map[string]any), v, at)
			if len(problems) == 0 {
				return nil
			}
			if i == 0 {
				first = problems
			}
		}
		return append([]string{at + " matches none of the alternatives"}, first...)
	}
	if types := schemaTypes(schema["type"]); len(types) > 0 && !valueHasType(v, types) {
		return []string{at + " has the wrong type"}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || e == v
		}
		if !found {
			return []string{at + " is outside the enum"}
		}
	}
	var out []string
	switch x := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for k, val := range x {
			sub, declared := props[k].(map[string]any)
			switch {
			case declared:
				out = append(out, schemaProblems(sub, val, at+"."+k)...)
			case schema["additionalProperties"] == false:
				out = append(out, at+"."+k+" is not in the schema")
			}
		}
		if required, ok := schema["required"].([]any); ok {
			for _, r := range required {
				if _, present := x[r.(string)]; !present {
					out = append(out, at+" lacks the required field "+r.(string))
				}
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range x {
				out = append(out, schemaProblems(items, e, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	}
	return out
}

func schemaTypes(raw any) []string {
	switch t := raw.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, len(t))
		for i, e := range t {
			out[i], _ = e.(string)
		}
		return out
	}
	return nil
}

func valueHasType(v any, types []string) bool {
	for _, t := range types {
		switch t {
		case "null":
			if v == nil {
				return true
			}
		case "object":
			if _, ok := v.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := v.([]any); ok {
				return true
			}
		case "string":
			if _, ok := v.(string); ok {
				return true
			}
		case "boolean":
			if _, ok := v.(bool); ok {
				return true
			}
		case "integer", "number":
			if f, ok := v.(float64); ok && (t == "number" || f == float64(int64(f))) {
				return true
			}
		}
	}
	return false
}

func TestContract_SchemaProblemsAreDetected(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"a":{"type":"string","enum":["x"]},"b":{"type":["integer","null"]},"c":{"type":"array","items":{"type":"object","properties":{"d":{"type":"boolean"}},"additionalProperties":false}}},"required":["a"],"additionalProperties":false}`), &schema); err != nil {
		t.Fatal(err)
	}
	decode := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := schemaProblems(schema, decode(`{"a":"x","b":null,"c":[{"d":true}]}`), "$"); len(got) != 0 {
		t.Errorf("a matching value has problems: %v", got)
	}
	got := strings.Join(schemaProblems(schema, decode(`{"a":"y","b":1.5,"e":1,"c":[{"z":1}]}`), "$"), "; ")
	for _, want := range []string{"$.a is outside the enum", "$.b", "$.e is not in the schema", "is not in the schema"} {
		if !strings.Contains(got, want) {
			t.Errorf("problems %q lack %q", got, want)
		}
	}
	if got := strings.Join(schemaProblems(schema, decode(`{}`), "$"), "; "); !strings.Contains(got, "lacks the required field a") {
		t.Errorf("a missing field: %q", got)
	}
}

// networkFixtures are the documents the network command tests answer with,
// keyed by operation and status.
var networkFixtures = []struct{ operation, status, body string }{
	{"networks.list", "200", networkList},
	{"networks.get", "200", networkSummaryDoc},
	{"networks.create", "201", `{"network":` + networkDoc + `}`},
	{"networks.delete", "200", `{"deleted":true}`},
	{"networks.agents.list", "200", networkAgents},
	{"networks.agents.approve", "200", `{"ok":true}`},
	{"networks.allowlist", "200", networkAllowlist},
	{"networks.bindings.list", "200", networkBindings},
	{"networks.bindings.delete", "200", `{"deleted":true}`},
	{"networks.events.list", "200", networkEvents},
}

func TestContract_NetworkFixturesMatchTheOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	byID := map[string]openAPIOperation{}
	for _, ops := range doc.Paths {
		for _, op := range ops {
			byID[op.ID] = op
		}
	}
	for _, f := range networkFixtures {
		op, ok := byID[f.operation]
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", f.operation)
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(f.body), &v); err != nil {
			t.Fatalf("%s: %v", f.operation, err)
		}
		for _, p := range schemaProblems(openAPIResponseSchema(t, op, f.status), v, "$") {
			t.Errorf("%s %s: %s", f.operation, f.status, p)
		}
	}
}

// networkInvocations runs every network command whose route the gateway publishes.
var networkInvocations = [][]string{
	{"list"},
	{"show", "dc1"},
	{"create", "HQ", "--description", "head office", "--data-class", "confidential"},
	{"delete", "dc1", "--yes"},
	{"token", "create", "dc1", "--uses", "3", "--ttl", "2h", "--require-approval"},
	{"agents", "dc1"},
	{"approve", "dc1", "erp-a"},
	{"rotate", "dc1", "erp-a"},
	{"revoke", "dc1", "erp-a", "--reason", "retired", "--yes"},
	{"allowlist", "dc1"},
	{"events", "dc1", "--limit", "20", "--before", "2026-10-17T00:00:00Z"},
	{"bind", "erp-private", "--network", "dc1", "--target", "erp", "--credentials", "agent"},
	{"unbind", "erp-private", "--network", "dc1"},
}

// networkOperationsWithoutACommand are published operations no command sends:
// changing a network needs a passkey confirmation of that exact change, and
// revoking a token needs the token's id, which no route lists.
var networkOperationsWithoutACommand = []string{"networks.update", "networks.tokens.revoke"}

func TestContract_NetworkRequestsMatchTheOpenAPI(t *testing.T) {
	doc := loadOpenAPI(t)
	var mu sync.Mutex
	queries := map[string][]url.Values{}
	record := func(route string, h func(*packServer, http.ResponseWriter, *http.Request)) func(*packServer, http.ResponseWriter, *http.Request) {
		return func(s *packServer, w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			queries[route] = append(queries[route], r.URL.Query())
			mu.Unlock()
			h(s, w, r)
		}
	}
	base := "/api/admin/networks"
	reply := map[string]func(*packServer, http.ResponseWriter, *http.Request){}
	for route, h := range map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET " + base + "/net_1":                              jsonReply(200, networkSummaryDoc),
		"POST " + base:                                        jsonReply(201, `{"network":`+networkDoc+`}`),
		"DELETE " + base + "/net_1":                           jsonReply(200, `{"deleted":true}`),
		"POST " + base + "/net_1/enrollment-tokens":           jsonReply(201, `{"id":"ent_1","token":"nxt_ABCDEFGH23456","maxUses":3,"requireApproval":true,"expiresAt":"2026-10-16T11:00:00Z","install":{"linux":"enroll","debian":"apt"}}`),
		"GET " + base + "/net_1/agents":                       jsonReply(200, networkAgents),
		"POST " + base + "/net_1/agents/nag_1/approve":        jsonReply(200, `{"ok":true}`),
		"POST " + base + "/net_1/agents/nag_1/rotate":         jsonReply(200, `{"ok":true}`),
		"POST " + base + "/net_1/agents/nag_1/revoke":         jsonReply(200, `{"ok":true}`),
		"GET " + base + "/net_1/allowlist":                    jsonReply(200, networkAllowlist),
		"GET " + base + "/net_1/events":                       jsonReply(200, networkEvents),
		"GET " + base + "/net_1/bindings":                     jsonReply(200, networkBindings),
		"POST " + base + "/net_1/bindings":                    jsonReply(201, `{}`),
		"DELETE " + base + "/net_1/bindings/nbd_1":            jsonReply(200, `{"deleted":true}`),
		"GET " + base:                                         jsonReply(200, networkList),
	} {
		reply[route] = record(route, h)
	}
	gw := networksGateway(t, reply)

	for _, args := range networkInvocations {
		before := requestCount(gw)
		if _, err := runCommand(t, NetworksCmd(), args...); err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if requestCount(gw) == before {
			t.Errorf("%v sent no request", args)
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
		op, _, ok := doc.operationFor(method, path)
		if !ok {
			t.Errorf("%s is not in the gateway's OpenAPI document", route)
			continue
		}
		covered[op.ID] = true
		for i := 0; i < gw.hits[route]; i++ {
			var body map[string]any
			if i < len(gw.bodies[route]) {
				body = gw.bodies[route][i]
			}
			var query url.Values
			mu.Lock()
			if i < len(queries[route]) {
				query = queries[route][i]
			}
			mu.Unlock()
			for _, p := range op.problems(query, body) {
				t.Errorf("%s (%s): %s", route, op.ID, p)
			}
		}
	}
	skipped := map[string]bool{}
	for _, id := range networkOperationsWithoutACommand {
		skipped[id] = true
	}
	for _, ops := range doc.Paths {
		for _, op := range ops {
			if strings.HasPrefix(op.ID, "networks.") && !covered[op.ID] && !skipped[op.ID] {
				t.Errorf("%s is published and no command sends it", op.ID)
			}
		}
	}
}
