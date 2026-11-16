package cli

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The documents below have the shape of the gateway's network admin routes
// (lib/api/contract/networks.ts); the contract test checks them against the
// OpenAPI document, so the commands are exercised against what it publishes.
const networkDoc = `{"id":"net_1","orgId":"org_1","slug":"dc1","name":"Datacentre 1","description":null,"status":"active","dataClass":"internal","settings":{"autoUpdate":"patch","requireAgentApproval":true,"keyRotationDays":30,"offlineAlertMinutes":10,"offlineRetryMinutes":5},"offlineSince":null,"createdBy":"usr_1","createdAt":"2026-10-16T09:00:00Z","updatedAt":"2026-10-16T09:00:00Z","deletedAt":null}`

const networkSummaryDoc = `{"network":` + networkDoc + `,"agents":{"total":2,"online":1,"pending":1},"bindings":2,"health":"online","lastSeenAt":"2026-10-16T10:00:00Z"}`

const networkList = `{"networks":[` + networkSummaryDoc + `],"entitled":true}`

const networkAgents = `{"agents":[{"id":"nag_1","name":"erp-a","status":"active","connected":true,"version":"1.4.2","os":"linux","arch":"amd64","lastSeenAt":"2026-10-16T10:00:00Z","lastIpPrefix":"203.0.113.0/24","allowlistHash":"h1","rotateRequestedAt":null,"enrolledAt":"2026-10-01T09:00:00Z","keyNotAfter":"2026-11-01T09:00:00Z"},{"id":"nag_2","name":"erp-b","status":"pending","connected":false,"version":null,"os":null,"arch":null,"lastSeenAt":null,"lastIpPrefix":null,"allowlistHash":null,"rotateRequestedAt":null,"enrolledAt":"2026-10-02T09:00:00Z","keyNotAfter":null}],"live":true}`

const networkAllowlist = `{"targets":[{"name":"erp","kind":"http","agents":["erp-a"],"methods":["GET"],"pathGlobs":["/invoices/**"],"mcpTools":[],"bindings":["erp-private"],"drift":true},{"name":"hr","kind":"mcp_stdio","agents":["erp-a"],"methods":[],"pathGlobs":[],"mcpTools":["employees_*"],"bindings":[],"drift":false}],"unserved":["crm"],"drift":true}`

const networkBindings = `{"bindings":[{"id":"nbd_1","orgId":"org_1","networkId":"net_1","kind":"connector","service":"erp-private","target":"erp","credentialsMode":"agent","createdBy":"usr_1","createdAt":"2026-10-16T09:00:00Z"},{"id":"nbd_2","orgId":"org_1","networkId":"net_1","kind":"mcp_server","service":"hr","target":"hr","credentialsMode":"gateway","createdBy":"usr_1","createdAt":"2026-10-16T09:00:00Z"}]}`

const networkEvents = `{"events":[{"id":"nev_1","orgId":"org_1","networkId":"net_1","agentId":"nag_1","type":"agent.connected","details":{"os":"linux"},"ts":"2026-10-16T09:30:00Z"}],"next":"2026-10-16T09:30:00Z"}`

func networksGateway(t *testing.T, extra map[string]func(*packServer, http.ResponseWriter, *http.Request)) *packServer {
	t.Helper()
	routes := map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks": jsonReply(200, networkList),
	}
	for k, v := range extra {
		routes[k] = v
	}
	syncHome(t)
	return packGateway(t, routes)
}

func TestNetworksReadCommands(t *testing.T) {
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1":           jsonReply(200, networkSummaryDoc),
		"GET /api/admin/networks/net_1/agents":    jsonReply(200, networkAgents),
		"GET /api/admin/networks/net_1/allowlist": jsonReply(200, networkAllowlist),
		"GET /api/admin/networks/net_1/events":    jsonReply(200, networkEvents),
		"GET /api/admin/networks/net_1/logs":      jsonReply(200, `{"logs":[{"ts":"t","agent":"erp-a","target":"erp","method":"GET","status":200,"outcome":"ok","duration_ms":12,"audit_ref":"aud_1"}]}`),
	})
	gw.lenient = true
	out, err := runCommand(t, NetworksCmd(), "list")
	for _, want := range []string{"Datacentre 1", "active", "online", "2026-10-16T10:00:00Z"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("list lacks %q: %q %v", want, out, err)
		}
	}
	out, err = runCommand(t, NetworksCmd(), "show", "dc1")
	for _, want := range []string{"dataClass: internal", "health: online", "bindings: 2", "agents: 1 online, 1 pending, 2 in total"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("show lacks %q: %q %v", want, out, err)
		}
	}
	if out, err = runCommand(t, NetworksCmd(), "show", "net_1", "--json"); err != nil || !strings.Contains(out, `"network"`) {
		t.Errorf("show --json: %q %v", out, err)
	}
	if _, err = runCommand(t, NetworksCmd(), "show", "nope"); err == nil || !strings.Contains(err.Error(), "no network named") {
		t.Errorf("unknown network: %v", err)
	}
	out, err = runCommand(t, NetworksCmd(), "agents", "Datacentre 1")
	for _, want := range []string{"nag_1", "linux", "amd64", "erp-b", "pending", "2026-11-01T09:00:00Z"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("agents lacks %q: %q %v", want, out, err)
		}
	}
	out, err = runCommand(t, NetworksCmd(), "allowlist", "dc1")
	for _, want := range []string{"erp (http)", "/invoices/**", "employees_*", "erp-private", "differ", "crm is bound"} {
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("allowlist lacks %q: %q %v", want, out, err)
		}
	}
	if out, err = runCommand(t, NetworksCmd(), "allowlist", "dc1", "--json"); err != nil || !strings.Contains(out, `"drift":true`) {
		t.Errorf("allowlist --json: %q %v", out, err)
	}
	if out, err = runCommand(t, NetworksCmd(), "events", "dc1", "--limit", "5", "--before", "2026-10-17T00:00:00Z"); err != nil || !strings.Contains(out, "agent.connected") || !strings.Contains(out, "--before 2026-10-16T09:30:00Z") {
		t.Errorf("events: %q %v", out, err)
	}
	if _, err = runCommand(t, NetworksCmd(), "events", "dc1", "--limit", "0"); err == nil {
		t.Error("an events limit of 0 was accepted")
	}
	out, err = runCommand(t, NetworksCmd(), "logs", "dc1", "--agent", "erp-a", "--target", "erp", "--outcome", "ok", "--since", "24h")
	if err != nil || !strings.Contains(out, "aud_1") {
		t.Errorf("logs: %q %v", out, err)
	}
	if _, err = runCommand(t, NetworksCmd(), "logs", "dc1", "--since", "soon"); err == nil {
		t.Error("a bad --since was accepted")
	}
	if out, err = runCommand(t, NetworksCmd(), "logs", "dc1", "--json"); err != nil || !strings.Contains(out, `"logs"`) {
		t.Errorf("logs --json: %q %v", out, err)
	}
}

func TestNetworksAgentsWarnsWhenTheStateIsNotLive(t *testing.T) {
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/agents": jsonReply(200, strings.Replace(networkAgents, `"live":true`, `"live":false`, 1)),
	})
	out, err := runCommand(t, NetworksCmd(), "agents", "dc1")
	if err != nil || !strings.Contains(out, "last stored state") {
		t.Errorf("agents: %q %v", out, err)
	}
}

func TestNetworksEmptyViews(t *testing.T) {
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/agents":    jsonReply(200, `{"agents":[],"live":true}`),
		"GET /api/admin/networks/net_1/allowlist": jsonReply(200, `{"targets":[],"unserved":[],"drift":false}`),
		"GET /api/admin/networks/net_1/events":    jsonReply(200, `{"events":[],"next":null}`),
		"GET /api/admin/networks/net_1/logs":      jsonReply(200, `{"logs":[]}`),
	})
	for args, want := range map[string]string{"agents dc1": "No agents", "allowlist dc1": "No agent advertises", "events dc1": "No events", "logs dc1": "No log lines"} {
		out, err := runCommand(t, NetworksCmd(), strings.Fields(args)...)
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%s: %q %v", args, out, err)
		}
	}
}

func TestNetworksWriteCommands(t *testing.T) {
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"POST /api/admin/networks":                            jsonReply(201, `{"network":`+networkDoc+`}`),
		"DELETE /api/admin/networks/net_1":                    jsonReply(200, `{"deleted":true}`),
		"GET /api/admin/networks/net_1/agents":                jsonReply(200, networkAgents),
		"POST /api/admin/networks/net_1/agents/nag_1/approve": jsonReply(200, `{"ok":true}`),
		"POST /api/admin/networks/net_1/agents/nag_1/rotate":  jsonReply(200, `{"ok":true}`),
		"POST /api/admin/networks/net_1/agents/nag_1/revoke":  jsonReply(200, `{"ok":true}`),
		"GET /api/admin/networks/net_1/bindings":              jsonReply(200, networkBindings),
		"POST /api/admin/networks/net_1/bindings":             jsonReply(201, `{}`),
		"DELETE /api/admin/networks/net_1/bindings/nbd_1":     jsonReply(200, `{"deleted":true}`),
		"DELETE /api/admin/networks/net_1/bindings/nbd_2":     jsonReply(200, `{"deleted":true}`),
	})
	out, err := runCommand(t, NetworksCmd(), "create", "HQ", "--description", "head office", "--data-class", "confidential")
	if err != nil || !strings.Contains(out, "Created Datacentre 1") {
		t.Fatalf("create: %q %v", out, err)
	}
	if b := gw.body("POST /api/admin/networks", 0); b["name"] != "HQ" || b["description"] != "head office" || b["dataClass"] != "confidential" || len(b) != 3 {
		t.Errorf("create body = %v", b)
	}
	if out, err = runCommand(t, NetworksCmd(), "create", "HQ", "--json"); err != nil || !strings.Contains(out, "net_1") {
		t.Errorf("create --json: %q %v", out, err)
	}
	if _, err = runCommand(t, NetworksCmd(), "delete", "dc1"); err == nil {
		t.Error("delete without a terminal or --yes was accepted")
	}
	if out, err = runCommand(t, NetworksCmd(), "delete", "dc1", "--yes"); err != nil || !strings.Contains(out, "Deleted") {
		t.Errorf("delete: %q %v", out, err)
	}
	for action, want := range map[string]string{"approve": "Approved", "rotate": "Rotation requested", "revoke": "Revoked"} {
		for _, ref := range []string{"nag_1", "erp-a"} {
			args := []string{action, "dc1", ref}
			if action == "revoke" {
				args = append(args, "--yes")
			}
			if out, err = runCommand(t, NetworksCmd(), args...); err != nil || !strings.Contains(out, want) {
				t.Errorf("%s %s: %q %v", action, ref, out, err)
			}
		}
	}
	if _, err = runCommand(t, NetworksCmd(), "approve", "dc1", "nope"); err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Errorf("an unknown agent: %v", err)
	}
	if gw.count("POST /api/admin/networks/net_1/agents/nag_1/approve") != 2 {
		t.Error("the unknown agent reached the gateway")
	}
	if _, err = runCommand(t, NetworksCmd(), "revoke", "dc1", "nag_1", "--reason", "laptop lost", "--yes"); err != nil {
		t.Fatal(err)
	}
	if b := gw.body("POST /api/admin/networks/net_1/agents/nag_1/revoke", 2); b["reason"] != "laptop lost" || len(b) != 1 {
		t.Errorf("revoke body = %v", b)
	}
	if b := gw.body("POST /api/admin/networks/net_1/agents/nag_1/approve", 0); len(b) != 0 {
		t.Errorf("approve body = %v", b)
	}
	if _, err = runCommand(t, NetworksCmd(), "revoke", "dc1", "nag_1"); err == nil {
		t.Error("revoke without --yes and without a terminal was accepted")
	}

	if out, err = runCommand(t, NetworksCmd(), "bind", "erp-private", "--network", "dc1", "--target", "erp", "--credentials", "agent"); err != nil || !strings.Contains(out, "Bound erp-private") {
		t.Errorf("bind: %q %v", out, err)
	}
	if b := gw.body("POST /api/admin/networks/net_1/bindings", 0); b["kind"] != "connector" || b["service"] != "erp-private" || b["target"] != "erp" || b["credentialsMode"] != "agent" || len(b) != 4 {
		t.Errorf("bind body = %v", b)
	}
	if _, err = runCommand(t, NetworksCmd(), "bind", "mcp:hr", "--network", "dc1", "--target", "hr"); err != nil {
		t.Fatal(err)
	}
	if b := gw.body("POST /api/admin/networks/net_1/bindings", 1); b["kind"] != "mcp_server" || b["service"] != "hr" || b["credentialsMode"] != "gateway" {
		t.Errorf("mcp bind body = %v", b)
	}
	if out, err = runCommand(t, NetworksCmd(), "unbind", "mcp:hr", "--network", "dc1"); err != nil || !strings.Contains(out, "Unbound mcp:hr") {
		t.Errorf("unbind: %q %v", out, err)
	}
	if out, err = runCommand(t, NetworksCmd(), "unbind", "erp-private", "--network", "dc1"); err != nil || !strings.Contains(out, "Unbound erp-private") {
		t.Errorf("unbind connector: %q %v", out, err)
	}
	if gw.count("DELETE /api/admin/networks/net_1/bindings/nbd_1") != 1 || gw.count("DELETE /api/admin/networks/net_1/bindings/nbd_2") != 1 {
		t.Error("each binding was not deleted by its id exactly once")
	}
	if _, err = runCommand(t, NetworksCmd(), "unbind", "mcp:erp-private", "--network", "dc1"); err == nil || !strings.Contains(err.Error(), "not routed") {
		t.Errorf("unbinding something that is not bound: %v", err)
	}
	for _, args := range [][]string{
		{"bind", "erp", "--target", "erp"},
		{"bind", "erp", "--network", "dc1"},
		{"bind", "erp", "--network", "dc1", "--target", "erp", "--credentials", "magic"},
		{"bind", "mcp:", "--network", "dc1", "--target", "x"},
		{"unbind", "erp"},
	} {
		if _, err := runCommand(t, NetworksCmd(), args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

func TestNetworksUnbindNeedsTheTargetWhenSeveralBindingsMatch(t *testing.T) {
	two := strings.Replace(networkBindings, `"service":"hr","target":"hr"`, `"service":"erp-private","target":"erp-2"`, 1)
	two = strings.Replace(two, `"kind":"mcp_server"`, `"kind":"connector"`, 1)
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/bindings":          jsonReply(200, two),
		"DELETE /api/admin/networks/net_1/bindings/nbd_2": jsonReply(200, `{"deleted":true}`),
	})
	if _, err := runCommand(t, NetworksCmd(), "unbind", "erp-private", "--network", "dc1"); err == nil || !strings.Contains(err.Error(), "--target") {
		t.Fatalf("an ambiguous unbind: %v", err)
	}
	if _, err := runCommand(t, NetworksCmd(), "unbind", "erp-private", "--network", "dc1", "--target", "erp-2"); err != nil {
		t.Fatal(err)
	}
	if gw.count("DELETE /api/admin/networks/net_1/bindings/nbd_2") != 1 {
		t.Error("the binding for that target was not deleted")
	}
}

func TestNetworksTokenCreate(t *testing.T) {
	token := "nxt_" + "ABCDEFGH23456"
	doc := `{"id":"ent_1","token":"` + token + `","maxUses":1,"requireApproval":false,"expiresAt":"2026-10-16T11:00:00Z","install":{"linux":"sudo zuano-connector enroll --token ` + token + `","debian":"sudo apt install zuano-connector","kubernetes":"helm install zuano-connector"}}`
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"POST /api/admin/networks/net_1/enrollment-tokens": jsonReply(201, doc),
	})
	out, err := runCommand(t, NetworksCmd(), "token", "create", "dc1")
	for _, want := range []string{token, "shown once", "2026-10-16T11:00:00Z", "Linux", "Debian", "Kubernetes", "apt install"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("token create lacks %q: %q %v", want, out, err)
		}
	}
	if strings.Contains(out, "Docker") {
		t.Errorf("a missing install command was printed: %q", out)
	}
	if b := gw.body("POST /api/admin/networks/net_1/enrollment-tokens", 0); b["uses"] != float64(1) || b["ttlSeconds"] != float64(3600) || b["requireApproval"] != nil || len(b) != 2 {
		t.Errorf("single body = %v", b)
	}
	if _, err = runCommand(t, NetworksCmd(), "token", "create", "dc1", "--uses", "3", "--ttl", "2h", "--require-approval"); err != nil {
		t.Fatal(err)
	}
	if b := gw.body("POST /api/admin/networks/net_1/enrollment-tokens", 1); b["uses"] != float64(3) || b["ttlSeconds"] != float64(7200) || b["requireApproval"] != true {
		t.Errorf("multi body = %v", b)
	}
	if out, err = runCommand(t, NetworksCmd(), "token", "create", "dc1", "--json"); err != nil || !strings.Contains(out, token) {
		t.Errorf("--json: %q %v", out, err)
	}
	for _, args := range [][]string{{"--uses", "0"}, {"--uses", "11"}, {"--ttl", "48h"}, {"--ttl", "59s"}, {"--ttl", "0s"}} {
		if _, err := runCommand(t, NetworksCmd(), append([]string{"token", "create", "dc1"}, args...)...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if _, err = runCommand(t, NetworksCmd(), "token", "create", "dc1", "--ttl", "60s"); err != nil {
		t.Errorf("the shortest ttl was refused: %v", err)
	}
	gw.replace(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(networkList))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"token":"leaked"}`))
	})
	if _, err := runCommand(t, NetworksCmd(), "token", "create", "dc1"); err == nil {
		t.Error("a response without an nxt_ token was accepted")
	}
}

func TestNetworksTestCommandExitStatuses(t *testing.T) {
	results := `{"results":[]}`
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"POST /api/admin/networks/net_1/test": func(s *packServer, w http.ResponseWriter, r *http.Request) { jsonReply(200, results)(s, w, r) },
	})
	_, err := runCommand(t, NetworksCmd(), "test", "dc1")
	if exitCodeOf(err) != 34 || !strings.Contains(err.Error(), "offline") {
		t.Errorf("no agent online: exit %d, %v", exitCodeOf(err), err)
	}
	results = `{"results":[{"agent":"erp-a","ok":true,"rtt_ms":42},{"agent":"erp-b","ok":false,"error":"timeout"}]}`
	out, err := runCommand(t, NetworksCmd(), "test", "dc1")
	if err != nil || !strings.Contains(out, "erp-a: ok, 42 ms") || !strings.Contains(out, "erp-b: failed: timeout") {
		t.Errorf("mixed: %q %v", out, err)
	}
	results = `{"results":[{"agent":"erp-b","ok":false,"error":"timeout"}]}`
	if _, err = runCommand(t, NetworksCmd(), "test", "dc1"); exitCodeOf(err) != 1 {
		t.Errorf("all failed: exit %d, %v", exitCodeOf(err), err)
	}
	if out, err = runCommand(t, NetworksCmd(), "test", "dc1", "--json"); exitCodeOf(err) != 1 || !strings.Contains(out, "results") {
		t.Errorf("--json: %q %v", out, err)
	}
}

func TestNetworksTokenRevoke(t *testing.T) {
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"DELETE /api/admin/networks/net_1/enrollment-tokens/ent_1": jsonReply(200, `{"revoked":true}`),
		"DELETE /api/admin/networks/net_1/enrollment-tokens/ent_2": jsonReply(404, `{"error":"not_found","message":"Token not found or already used."}`),
	})
	if _, err := runCommand(t, NetworksCmd(), "token", "revoke", "dc1", "ent_1"); err == nil {
		t.Error("revoke without --yes and without a terminal was accepted")
	}
	if gw.count("DELETE /api/admin/networks/net_1/enrollment-tokens/ent_1") != 0 {
		t.Error("an unconfirmed revoke reached the gateway")
	}
	out, err := runCommand(t, NetworksCmd(), "token", "revoke", "dc1", "ent_1", "--yes")
	if err != nil || !strings.Contains(out, "Revoked enrollment token ent_1") {
		t.Fatalf("revoke: %q %v", out, err)
	}
	if out, err = runCommand(t, NetworksCmd(), "token", "revoke", "dc1", "ent_1", "--yes", "--json"); err != nil || !strings.Contains(out, `"revoked"`) {
		t.Errorf("--json: %q %v", out, err)
	}
	if _, err = runCommand(t, NetworksCmd(), "token", "revoke", "dc1", "ent_2", "--yes"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("a used-up token: %v", err)
	}
}

func TestNetworksMutationsAreRefusedForAgents(t *testing.T) {
	networksGateway(t, nil)
	t.Setenv("ZUANO_AGENT", "1")
	for _, args := range [][]string{
		{"create", "x"}, {"delete", "dc1", "--yes"}, {"token", "create", "dc1"}, {"token", "revoke", "dc1", "ent_1", "--yes"}, {"approve", "dc1", "a"}, {"rotate", "dc1", "a"},
		{"revoke", "dc1", "a", "--yes"}, {"bind", "x", "--network", "dc1", "--target", "t"}, {"unbind", "x", "--network", "dc1"},
	} {
		if _, err := runCommand(t, NetworksCmd(), args...); exitCodeOf(err) != exitDenied {
			t.Errorf("%v by an agent: exit %d, %v", args, exitCodeOf(err), err)
		}
	}
}

func TestOfflineNetworkRefusalExitsThirtyFour(t *testing.T) {
	for _, reason := range []string{"network_offline", "egress_unavailable"} {
		if err := policyRefusal([]byte(`{"reason":"`+reason+`","message":"DC1 is offline"}`), nil); exitCodeOf(err) != 34 {
			t.Errorf("%s: %v", reason, err)
		}
	}
}

func TestNetworksTestNamesTheBrokerAsTheMeasuredHop(t *testing.T) {
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"POST /api/admin/networks/net_1/test": jsonReply(200, `{"results":[{"agent":"erp-a","agent_id":"nag_1","ok":true,"rtt_ms":42}],"via":"broker"}`),
	})
	out, err := runCommand(t, NetworksCmd(), "test", "dc1")
	if err != nil || !strings.Contains(out, "erp-a: ok, 42 ms") || !strings.Contains(out, "gateway to the broker") {
		t.Errorf("the round-trip time is not tied to the gateway and the broker: %q %v", out, err)
	}
}

func TestNetworksTestRefusals(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{503, `{"error":"broker_unavailable","message":"The broker did not answer in time."}`, "The broker did not answer in time."},
		{409, `{"error":"network_disabled","message":"This network is disabled."}`, "This network is disabled."},
	} {
		networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
			"POST /api/admin/networks/net_1/test": jsonReply(tc.status, tc.body),
		})
		out, err := runCommand(t, NetworksCmd(), "test", "dc1")
		if err == nil || exitCodeOf(err) != 1 || !strings.Contains(err.Error(), tc.want) || out != "" {
			t.Errorf("%d: exit %d, %v, output %q", tc.status, exitCodeOf(err), err, out)
		}
	}
}

func TestNetworksLogsFiltersAndCursor(t *testing.T) {
	var query url.Values
	gw := networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/logs": func(s *packServer, w http.ResponseWriter, r *http.Request) {
			query = r.URL.Query()
			jsonReply(200, `{"logs":[{"id":"nal_1","ts":"2026-10-16T09:30:00Z","agent":"erp-a","agent_id":"nag_1","level":"error","event":"exchange","target":"erp","method":"GET","path":null,"status":502,"outcome":"upstream_error","code":null,"duration_ms":12,"audit_ref":null,"message":null}],"next":"2026-10-16T09:30:00Z"}`)(s, w, r)
		},
	})
	out, err := runCommand(t, NetworksCmd(), "logs", "dc1", "--limit", "5", "--before", "2026-10-17T00:00:00.250Z")
	if err != nil || !strings.Contains(out, "--before 2026-10-16T09:30:00Z") {
		t.Fatalf("the next page was not offered: %q %v", out, err)
	}
	if query.Get("limit") != "5" || query.Get("before") != "2026-10-17T00:00:00.25Z" {
		t.Errorf("query = %v", query)
	}
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "201"}, {"--before", "yesterday"}} {
		before := gw.count("GET /api/admin/networks/net_1/logs")
		if _, err := runCommand(t, NetworksCmd(), append([]string{"logs", "dc1"}, args...)...); err == nil {
			t.Errorf("%v was accepted", args)
		}
		if gw.count("GET /api/admin/networks/net_1/logs") != before {
			t.Errorf("%v reached the gateway", args)
		}
	}
	if _, err := runCommand(t, NetworksCmd(), "logs", "dc1", "--before", "2026-10-17T02:00:00+02:00"); err != nil || query.Get("before") != "2026-10-17T00:00:00Z" {
		t.Errorf("an offset timestamp was not sent in UTC: %v (%v)", query, err)
	}
	if _, err := runCommand(t, NetworksCmd(), "logs", "dc1"); err != nil || query.Has("limit") || query.Has("before") {
		t.Errorf("an unfiltered request carried %v (%v)", query, err)
	}
}

func TestNetworksTokenList(t *testing.T) {
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/enrollment-tokens": jsonReply(200, `{"tokens":[{"id":"ent_1","createdAt":"2026-10-16T09:00:00Z","expiresAt":"2026-10-16T10:00:00Z","maxUses":3,"usesLeft":2,"requireApproval":true}]}`),
	})
	out, err := runCommand(t, NetworksCmd(), "token", "list", "dc1")
	for _, want := range []string{"ent_1", "2026-10-16T10:00:00Z", "2 of 3", "yes"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("token list lacks %q: %q %v", want, out, err)
		}
	}
	if strings.Contains(out, "nxt_") {
		t.Errorf("a token secret was printed: %q", out)
	}
	if out, err = runCommand(t, NetworksCmd(), "token", "list", "dc1", "--json"); err != nil || !strings.Contains(out, `"tokens"`) {
		t.Errorf("--json: %q %v", out, err)
	}
	networksGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/admin/networks/net_1/enrollment-tokens": jsonReply(200, `{"tokens":[]}`),
	})
	if out, err = runCommand(t, NetworksCmd(), "token", "list", "dc1"); err != nil || !strings.Contains(out, "No enrollment tokens") {
		t.Errorf("empty: %q %v", out, err)
	}
}
