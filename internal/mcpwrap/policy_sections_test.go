package mcpwrap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func gatewayPolicy(t *testing.T) *Policy {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "policy-sections.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The keys of one upstream in the gateway's policy-section.ts. A key added
// there must be added to the schema too, or an organisation's own document
// would not load offline.
var gatewayUpstreamKeys = []string{
	"approvals", "dlp", "elicitation", "elicitation_url_hosts", "exposure", "instructions", "logging", "new_tools",
	"on_definition_change", "prompts", "rate", "relay_wait_seconds", "resources", "roots", "roots_allow", "sampling",
	"sampling_max_tokens", "sampling_models", "sampling_tools", "tags", "timeout_seconds", "token_exchange", "tools",
}

func TestSectionsSchemaCoversTheGatewaysUpstreamKeys(t *testing.T) {
	var schema struct {
		Defs struct {
			Upstream struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"upstream"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(SectionsSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range schema.Defs.Upstream.Properties {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(gatewayUpstreamKeys, ",") {
		t.Errorf("schema keys %v, gateway keys %v", got, gatewayUpstreamKeys)
	}
}

func TestGatewayDocumentLoads(t *testing.T) {
	u := gatewayPolicy(t).For("github")
	if u.OnDefinitionChange != "block" || u.NewTools != "hidden" || u.Classify(Definition{"name": "merge_pull_request"}) != "destructive" || !u.Denied("delete_repository") || !u.Denied("rotate_secret_key") || u.Denied("list_issues") {
		t.Errorf("policy = %+v", u)
	}
	if u.Profile == nil || len(u.Profile.Custom) != 1 || !u.NeedsApproval("destructive") || u.NeedsApproval("write") {
		t.Errorf("profile = %+v", u.Profile)
	}
	if other := gatewayPolicy(t).For("other"); other.Profile == nil || other.Profile.Outbound["secret"] != dlpBlock || other.Profile.Inbound["secret"] != dlpRedact {
		t.Errorf("an upstream without a profile gets %+v", other.Profile)
	}
}

func TestPolicyRefusesWhatTheGatewayRefuses(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown section":      "extra: 1\n",
		"unknown upstream key": "firewall:\n  upstreams:\n    a:\n      onDefinitionChange: block\n",
		"camel case":           "firewall:\n  requiredRouting: true\n",
		"bad enum":             "firewall:\n  upstreams:\n    a:\n      elicitation: always\n",
		"timeout too long":     "firewall:\n  upstreams:\n    a:\n      timeout_seconds: 300\n",
		"no exposure":          "firewall:\n  upstreams:\n    a:\n      exposure: []\n",
		"profile missing":      "firewall:\n  upstreams:\n    a:\n      dlp: nowhere\n",
		"rule without match":   "dlp:\n  profiles:\n    p:\n      custom: [{name: n, action: block}]\n",
		"rule with both":       "dlp:\n  profiles:\n    p:\n      custom: [{name: n, action: block, regex: x, dictionary: [y]}]\n",
		"bad category":         "dlp:\n  profiles:\n    p:\n      outbound: {biometric: block}\n",
		"bad pattern":          "dlp:\n  profiles:\n    p:\n      custom: [{name: n, action: block, regex: \"(\"}]\n",
		"external scanner":     "dlp:\n  profiles:\n    p:\n      external: {url: \"https://dlp.example.com/scan\"}\n",
		"auto accepts":         "firewall:\n  upstreams:\n    a:\n      on_definition_change: auto\n",
	} {
		if _, err := ParsePolicy([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func approvedGovernor(t *testing.T, p UpstreamPolicy, defs ...Definition) *OfflineGovernor {
	t.Helper()
	dir := t.TempDir()
	gov := &OfflineGovernor{Name: "gh", Pins: PinStore{Dir: dir}, Policy: p, Logf: func(string, ...any) {}}
	ctx := context.Background()
	if err := gov.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := gov.Tools(ctx, defs, true); err != nil {
		t.Fatal(err)
	}
	set, _ := gov.Pins.Load("gh")
	for _, d := range defs {
		_ = set.Approve(nameOf(d))
	}
	_ = gov.Pins.Save(set)
	return gov
}

func TestOfflineAppliesTheGatewaysSections(t *testing.T) {
	policy := gatewayPolicy(t).For("github")
	gov := approvedGovernor(t, policy, def("list_issues", "Lists"), def("merge_pull_request", "Merges"), def("create_issue", "Creates"))
	ctx := context.Background()
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"

	if d, _ := gov.Call(ctx, "list_issues", json.RawMessage(`{"q":"open"}`)); d.Kind != "allow" {
		t.Errorf("a plain call: %+v", d)
	}
	if d, _ := gov.Call(ctx, "merge_pull_request", json.RawMessage(`{}`)); d.Reason != "approval_unavailable" {
		t.Errorf("a destructive call waits for a person: %+v", d)
	}
	if d, _ := gov.Call(ctx, "create_issue", json.RawMessage(`{"body":"key `+awsKey+`"}`)); d.Reason != "dlp_blocked" || strings.Contains(d.Message, awsKey) {
		t.Errorf("a secret leaving: %+v", d)
	}
	if d, _ := gov.Call(ctx, "create_issue", json.RawMessage(`{"body":"mail ana@example.com"}`)); d.Reason != "approval_unavailable" {
		t.Errorf("personal data needs approval, which offline cannot give: %+v", d)
	}
	d, _ := gov.Call(ctx, "create_issue", json.RawMessage(`{"body":"see PRJ-123456 today"}`))
	if d.Kind != "redact" || strings.Contains(string(d.Arguments), "PRJ-123456") || !strings.Contains(string(d.Arguments), "see [REDACTED] today") {
		t.Errorf("a custom rule redacts: %+v", d)
	}

	rd, _ := gov.Result(ctx, Decision{}, "list_issues", json.RawMessage(`{"content":[{"type":"text","text":"token `+awsKey+`"}]}`), false)
	if rd.Kind != "redact" || strings.Contains(string(rd.Content), awsKey) {
		t.Errorf("a secret coming back: %+v", rd)
	}
	rd, _ = gov.Result(ctx, Decision{}, "list_issues", json.RawMessage(`{"content":[{"type":"text","text":"nothing here"}]}`), false)
	if rd.Kind != "allow" {
		t.Errorf("a clean result: %+v", rd)
	}
}

func TestOfflineDefaultProfileKeepsSecretsInAndRedactsThemOnTheWayBack(t *testing.T) {
	gov := approvedGovernor(t, (&Policy{}).For("gh"), def("create_issue", "Creates"))
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"
	if d, _ := gov.Call(context.Background(), "create_issue", json.RawMessage(`{"body":"`+awsKey+`"}`)); d.Reason != "dlp_blocked" {
		t.Errorf("default profile: %+v", d)
	}
	rd, _ := gov.Result(context.Background(), Decision{}, "create_issue", json.RawMessage(`{"content":[{"type":"text","text":"`+awsKey+`"}]}`), false)
	if rd.Kind != "redact" {
		t.Errorf("default profile result: %+v", rd)
	}
}

func TestOfflineAllowListAndAutoApprovedReadTools(t *testing.T) {
	p, err := ParsePolicy([]byte("firewall:\n  upstreams:\n    gh:\n      new_tools: auto_read_only\n      tools:\n        allow: [\"get_*\", \"list_*\", create_issue]\n"))
	if err != nil {
		t.Fatal(err)
	}
	policy := p.For("gh")
	if policy.Denied("get_user") || !policy.Denied("drop_table") {
		t.Fatalf("allow list: %+v", policy.Tools)
	}
	dir := t.TempDir()
	gov := &OfflineGovernor{Name: "gh", Pins: PinStore{Dir: dir}, Policy: policy, Logf: func(string, ...any) {}}
	ctx := context.Background()
	if err := gov.Start(ctx); err != nil {
		t.Fatal(err)
	}
	served, err := gov.Tools(ctx, []Definition{def("get_user", "Reads"), def("create_issue", "Writes"), def("drop_table", "Drops")}, true)
	if err != nil || len(served) != 1 || nameOf(served[0]) != "get_user" {
		t.Fatalf("served %v, %v: only a read tool on the allow list pins itself", served, err)
	}
	if d, _ := gov.Call(ctx, "create_issue", nil); d.Reason != "tool_not_pinned" {
		t.Errorf("a write tool still waits for approval: %+v", d)
	}
}

func TestFirewallDocsExampleLoads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "firewall.md"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "```yaml\n")
	if len(parts) < 2 {
		t.Fatal("the firewall docs have no YAML example")
	}
	for _, part := range parts[1:] {
		block, _, _ := strings.Cut(part, "```")
		if _, err := ParsePolicy([]byte(block)); err != nil {
			t.Errorf("a documented example does not load: %v\n%s", err, block)
		}
	}
}
