package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSessions_ListMarksTheCurrentSessionAndPrintsJSONUnchanged(t *testing.T) {
	body := contractFixture(t, "sessions.json")
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/me/sessions": reply(200, body),
	})
	out, err := runCommand(t, SessionsCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ses_browser1 (this one)", "admin", "ses_viewer1", "viewer", "2026-10-23T08:00:00.000Z", "Firefox on Linux 203.0.113.0/24"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, err = runCommand(t, SessionsCmd(), "list", "--json")
	if err != nil || strings.TrimSpace(out) != strings.TrimSpace(body) {
		t.Fatalf("--json = %q, %v", out, err)
	}
}

// The gateway ends one session with DELETE ?id= and every session, refresh
// family and CLI key with a bare DELETE (?include=current also ends this one).
func TestSessions_EndAndEndAllUseDELETEWithTheGatewaysQuery(t *testing.T) {
	_, bodies := approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"DELETE /api/me/sessions?id=ses_browser1": reply(200, `{"ended":1}`),
		"DELETE /api/me/sessions?include=current": reply(200, `{"sessions":2,"families":1,"keys":3}`),
	})
	out, err := runCommand(t, SessionsCmd(), "end", "ses_browser1")
	if err != nil || !strings.Contains(out, "Ended ses_browser1") {
		t.Fatalf("end: %q, %v", out, err)
	}
	out, err = runCommand(t, SessionsCmd(), "end-all")
	if err != nil || !strings.Contains(out, "2 sessions, 1 refresh families and 3 CLI keys ended") {
		t.Fatalf("end-all: %q, %v", out, err)
	}
	if len(*bodies) != 0 {
		t.Errorf("posts = %d, want none", len(*bodies))
	}
	if _, err := runCommand(t, SessionsCmd(), "revoke-all"); err != nil {
		t.Errorf("the old name no longer works: %v", err)
	}
}

func TestSessions_AnExpiredTokenIsReportedAsRevoked(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/me/sessions": reply(401, `{"error":"Unauthorized"}`),
	})
	_, err := runCommand(t, SessionsCmd(), "list")
	if !errors.Is(err, errTokenRevoked) {
		t.Fatalf("err = %v", err)
	}
}

func TestAudit_VerifyReportsAGoodAndABrokenLog(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/admin/audit/verify?from=2026-09-16&to=2026-10-16": reply(200, contractFixture(t, "audit-verify-ok.json")),
		"GET /api/admin/audit/verify":                               reply(200, contractFixture(t, "audit-verify-broken.json")),
	})
	out, err := runCommand(t, AuditCmd(), "verify", "--from", "2026-09-16", "--to", "2026-10-16")
	if err != nil || !strings.Contains(out, "Audit log verified: 18234 events, 30 checkpoints") {
		t.Fatalf("good log: %q, %v", out, err)
	}
	out, err = runCommand(t, AuditCmd(), "verify")
	if got := exitCodeOf(err); got != 1 || err == nil || !strings.Contains(err.Error(), "first broken event is seq 9021") || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("broken log: exit %d, err %v\n%s", got, err, out)
	}
	if !strings.Contains(out, "did NOT verify") {
		t.Errorf("output = %q", out)
	}
	if _, err := runCommand(t, AuditCmd(), "verify", "--from", "soon"); err == nil || !strings.Contains(err.Error(), "invalid --from") {
		t.Errorf("err = %v", err)
	}
}

func TestPolicyExplain_PrintsTheDecisionItsSourceAndWhoCanChangeIt(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		tool    string
		args    []string
		query   string
		want    []string
	}{
		{"policy-preview-off.json", "create_refund", nil, "params=%7B%7D&principal=cli_personal&service=stripe&tool=create_refund", []string{"stripe.create_refund (destructive) is Off for cli_personal", "from the built-in default", "Can be changed by: org admins"}},
		{"policy-preview-approval.json", "create_refund", []string{"--arg-json", "amount=80", "--arg", "currency=EUR"}, "params=%7B%22amount%22%3A80%2C%22currency%22%3A%22EUR%22%7D&principal=cli_personal&service=stripe&tool=create_refund", []string{"is Approval required", "from an organisation tool override ovr_7 when amount <= 100"}},
		{"policy-preview-platform.json", "delete_customer", []string{"--principal", "cli_agent"}, "params=%7B%7D&principal=cli_agent&service=stripe&tool=delete_customer", []string{"Off for cli_agent", "from a platform block", "Can be changed by: platform admins"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
				"GET /api/admin/risk-policy/preview?" + tc.query: reply(200, contractFixture(t, tc.fixture)),
			})
			out, err := runCommand(t, PolicyCmd(), append([]string{"explain", "stripe", tc.tool}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
		})
	}
}

func TestPolicyExplain_AnAgentSessionAsksAsAnAgentAndBadPrincipalsAreRefused(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/admin/risk-policy/preview?params=%7B%7D&principal=cli_agent&service=stripe&tool=create_refund": reply(200, contractFixture(t, "policy-preview-off.json")),
	})
	t.Setenv("ZUANO_AGENT", "1")
	if _, err := runCommand(t, PolicyCmd(), "explain", "stripe", "create_refund"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, PolicyCmd(), "explain", "stripe", "create_refund", "--principal", "root"); err == nil || !strings.Contains(err.Error(), "invalid --principal") {
		t.Errorf("err = %v", err)
	}
}

func TestPolicyExplain_JSONIsTheBodyUnchanged(t *testing.T) {
	body := contractFixture(t, "policy-preview-off.json")
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/admin/risk-policy/preview?params=%7B%7D&principal=cli_personal&service=stripe&tool=create_refund": reply(200, body),
	})
	out, err := runCommand(t, PolicyCmd(), "explain", "stripe", "create_refund", "--json")
	if err != nil || strings.TrimSpace(out) != strings.TrimSpace(body) {
		t.Fatalf("--json = %q, %v", out, err)
	}
}
