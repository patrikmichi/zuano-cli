package cli

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/packs"
)

func packFixture(t *testing.T, name string) string {
	t.Helper()
	return contractFixture(t, "packs/"+name+".json")
}

// The gateway's plan for a pack and the one for taking it out again are signed
// documents the CLI shows; their ops, prompts and counts are what it reads.
func TestContract_PackPlansDecode(t *testing.T) {
	for name, mode := range map[string]string{"plan-install": "install", "plan-uninstall": "uninstall"} {
		var plan packs.Plan
		if err := json.Unmarshal([]byte(packFixture(t, name)), &plan); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if plan.PlanID == "" || plan.Mode != mode || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(plan.PlanHash) || plan.Pack.Slug != "finance-invoicing" || plan.ExpiresAt == "" {
			t.Errorf("%s: header = %+v", name, plan)
		}
		if len(plan.Ops) == 0 || plan.Summary.Apply == 0 {
			t.Errorf("%s: ops %d, summary %+v", name, len(plan.Ops), plan.Summary)
		}
		for _, op := range plan.Ops {
			if op.Kind == "" || op.Status == "" || len(op.Target) == 0 {
				t.Errorf("%s: op %+v lacks a kind, status or target", name, op)
			}
		}
	}
	var plan packs.Plan
	_ = json.Unmarshal([]byte(packFixture(t, "plan-install")), &plan)
	if len(plan.CredentialPrompts) != 1 || plan.CredentialPrompts[0].CLICommand != "zuano connect stripe" || plan.CredentialPrompts[0].Scope != "org" || plan.CredentialPrompts[0].Status != "connected" {
		t.Errorf("prompts = %+v", plan.CredentialPrompts)
	}
}

func TestContract_PackAssignmentsDecode(t *testing.T) {
	var a packs.Assignments
	if err := json.Unmarshal([]byte(packFixture(t, "assignments")), &a); err != nil {
		t.Fatal(err)
	}
	if a.Revision != 5 || len(a.Profiles) != 1 || len(a.ConnectRequired) != 1 || a.ConnectRequired[0].Scope != "personal" {
		t.Fatalf("assignments = %+v", a)
	}
	p := a.Profiles[0]
	if p.Source != "pack" || p.Pack.Slug != "finance-invoicing" || len(p.Rules) != 1 || !p.Rules[0].AlwaysApply || p.Rules[0].Agents[0] != "all" || p.Rules[0].Globs != nil || len(p.Skills) != 1 || p.Skills[0].VersionID != "rev_skill" || len(p.Bundles) != 1 || p.Bundles[0].Channel != "stable" {
		t.Errorf("profile = %+v", p)
	}
}

// Every pack command reads the gateway's documents as the gateway wrote them.
func TestContract_PackCommandsReadTheGatewaysDocuments(t *testing.T) {
	packGateway(t, map[string]func(*packServer, http.ResponseWriter, *http.Request){
		"GET /api/packs":                             jsonReply(200, packFixture(t, "list")),
		"GET /api/packs/finance-invoicing":           jsonReply(200, packFixture(t, "detail")),
		"POST /api/packs/finance-invoicing/plans":    signedReply(200, packFixture(t, "plan-install")),
		"GET /api/packs/installs":                    jsonReply(200, packFixture(t, "installs")),
		"GET /api/packs/installs/pki_FIXTURE":        jsonReply(200, packFixture(t, "install")),
		"POST /api/packs/installs/pki_FIXTURE/plans": signedReply(200, packFixture(t, "plan-uninstall")),
	})
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"list":      {[]string{"list"}, []string{"finance-invoicing", "1.2.0", "finance", "platform", "full"}},
		"show":      {[]string{"show", "finance-invoicing"}, []string{"Finance and invoicing 1.2.0 (finance-invoicing)", "connector stripe (required, org): Payments.", "skills: invoice-reconcile", "bundles: finance-toolkit", "rules: money-safety"}},
		"plan":      {[]string{"plan", "finance-invoicing"}, []string{"install finance-invoicing 1.2.0", "Connectors", "10 to apply"}},
		"status":    {[]string{"status", "finance-invoicing"}, []string{"finance-invoicing", "1.2.0", "installed"}},
		"uninstall": {[]string{"uninstall", "finance-invoicing"}, []string{"uninstall finance-invoicing"}},
	} {
		out, _ := runCommand(t, PackCmd(), tc.args...)
		for _, want := range tc.want {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", name, want, out)
			}
		}
	}
}

// A plan request names a version only when the person did; an empty one is not
// "the newest", the gateway looks for a version with that name and finds none.
func TestPackPlanRequestsLeaveOutAVersionNobodyNamed(t *testing.T) {
	ps := packGateway(t, applyRoutes())
	for _, args := range [][]string{
		{"plan", "finance"},
		{"install", "finance", "--yes", "--keep-conflicts"},
		{"update", "finance", "--yes", "--keep-conflicts"},
	} {
		if _, err := runCommand(t, PackCmd(), args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	sent := ps.count("POST /api/packs/finance/plans")
	if sent != 3 {
		t.Fatalf("%d plan requests, want 3", sent)
	}
	for i := range sent {
		if b := ps.body("POST /api/packs/finance/plans", i); b["version"] != nil {
			t.Errorf("request %d names the version %v nobody asked for: %v", i, b["version"], b)
		}
	}
	if _, err := runCommand(t, PackCmd(), "plan", "finance@1.0.0"); err != nil {
		t.Fatal(err)
	}
	if b := ps.body("POST /api/packs/finance/plans", sent); b["version"] != "1.0.0" {
		t.Errorf("a named version was left out: %v", b)
	}
}
