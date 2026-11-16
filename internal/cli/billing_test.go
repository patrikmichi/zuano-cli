package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBillingStatus_PrintsThePlanPeriodAndCredit(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/status": reply(200, contractFixture(t, "billing-status.json")),
	})
	out, err := runCommand(t, BillingCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Pro — subscription", "trialing, trial ends 2026-10-30T12:00:00.000Z",
		"2026-10-16T12:00:00.000Z to 2026-11-16T12:00:00.000Z",
		"Verified:  yes", "Overage:   off", "Credit:    5.00 EUR (plan), until 2026-11-16T12:00:00.000Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Cancels:") {
		t.Errorf("a plan that is not cancelling printed a cancel line:\n%s", out)
	}
}

func TestBillingStatus_SimulatedPlanPaymentFailingWithSpendCap(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/status": reply(200, contractFixture(t, "billing-status-simulated.json")),
	})
	out, err := runCommand(t, BillingCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Business — simulated", "payment failing since 2026-11-01T00:00:00.000Z (day 4)", "Cancels:   at the end of the period", "Moves to:  free", "Verified:  no", "Overage:   on, spend cap 25.00 EUR", "Over limit: members since 2026-11-02T00:00:00.000Z, until 2026-11-09T00:00:00.000Z", "Pay:       https://gateway.example/admin/billing?pay=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Credit:") {
		t.Errorf("an organisation without credit printed a credit line:\n%s", out)
	}
}

func TestBillingStatus_JSONIsTheBodyUnchanged(t *testing.T) {
	body := contractFixture(t, "billing-status.json")
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/status": reply(200, body),
	})
	out, err := runCommand(t, BillingCmd(), "status", "--json")
	if err != nil || strings.TrimSpace(out) != strings.TrimSpace(body) {
		t.Fatalf("--json = %q, %v", out, err)
	}
}

func TestBillingUsage_PrintsATableWithPercentAndProjectedCost(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/usage": reply(200, contractFixture(t, "billing-usage.json")),
	})
	out, err := runCommand(t, BillingCmd(), "usage")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"METER", "PROJECTED COST", "tool_calls", "41000", "200000", "21%", "62000", "observability_events", "unlimited", "Spend:  0.00 EUR (credit left 5.00 EUR)", "Tool calls by source (organization):", "  mcp  41000"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestBillingUsage_ByServiceMatchesTheFixtureAndSendsTheQuery(t *testing.T) {
	body := contractFixture(t, "billing-usage-by-service.json")
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/usage?by=service&meter=tool_calls": reply(200, body),
	})
	out, err := runCommand(t, BillingCmd(), "usage", "--by", "service", "--meter", "tool_calls")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"  stripe  30000", "  google-workspace  11000"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, err = runCommand(t, BillingCmd(), "usage", "--by", "service", "--meter", "tool_calls", "--json")
	if err != nil || strings.TrimSpace(out) != strings.TrimSpace(body) {
		t.Fatalf("--json = %q, %v", out, err)
	}
}

func TestBillingUsage_RejectsBadFlagsWithoutDialing(t *testing.T) {
	_, bodies := approvalsGateway(t, nil)
	for _, args := range [][]string{{"usage", "--by", "color"}} {
		if _, err := runCommand(t, BillingCmd(), args...); err == nil || !strings.Contains(err.Error(), "invalid --") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
	if len(*bodies) != 0 {
		t.Errorf("reached the gateway: %v", *bodies)
	}
}

func TestBilling_AnAgentSessionMayRunStatusAndUsage(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/status": reply(200, contractFixture(t, "billing-status.json")),
		"GET /api/v1/billing/usage":  reply(200, contractFixture(t, "billing-usage.json")),
	})
	t.Setenv("ZUANO_AGENT", "1")
	for _, verb := range []string{"status", "usage"} {
		if _, err := runCommand(t, BillingCmd(), verb); err != nil {
			t.Errorf("%s as an agent: %v", verb, err)
		}
	}
}

func TestBilling_APlanLimitOnStatusIsExit30WithTheLink(t *testing.T) {
	refusingGateway(t, 402, nil, `{"error":"plan_required","message":"billing details need an admin","upgrade_url":"https://gateway.example/settings/billing/plans"}`)
	_, err := runCommand(t, BillingCmd(), "status")
	if got := exitCodeOf(err); got != 30 || !strings.Contains(err.Error(), "https://gateway.example/settings/billing/plans") {
		t.Fatalf("exit %d, err %v", got, err)
	}
}

func TestBillingOpen_PrintsTheLinkWithoutATerminalAndOpensTheBrowserWithOne(t *testing.T) {
	srv, _ := approvalsGateway(t, nil)
	opened := stubBrowserWith(t, nil)

	out, err := runCommand(t, BillingCmd(), "open")
	if err != nil || strings.TrimSpace(out) != srv.URL+"/admin/billing" {
		t.Fatalf("non-TTY: %q, %v", out, err)
	}
	if len(*opened) != 0 {
		t.Errorf("opened %v without a terminal", *opened)
	}

	out, err = runWithInput(t, BillingCmd(), "", "open")
	if err != nil || !strings.Contains(out, "Opened "+srv.URL+"/admin/billing") || len(*opened) != 1 {
		t.Fatalf("TTY: %q, %v, opened %v", out, err, *opened)
	}

	out, err = runWithInput(t, BillingCmd(), "", "open", "--no-browser")
	if err != nil || strings.TrimSpace(out) != srv.URL+"/admin/billing" || len(*opened) != 1 {
		t.Fatalf("--no-browser: %q, %v, opened %v", out, err, *opened)
	}
}

func TestBillingFixturesAreValidJSON(t *testing.T) {
	for _, name := range []string{"billing-status.json", "billing-status-simulated.json", "billing-usage.json", "billing-usage-by-service.json", "billing-credits.json", "run-accepted.json", "job.json", "jobs.json"} {
		var v any
		if err := json.Unmarshal([]byte(contractFixture(t, name)), &v); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	var buf bytes.Buffer
	printBillingUsage(&buf, billingUsage{}, "")
	if !strings.Contains(buf.String(), "No metered usage") {
		t.Errorf("empty usage printed %q", buf.String())
	}
}

func TestUsage_PrintsEveryMeterFromTheBillingUsageRoute(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/usage": reply(200, contractFixture(t, "billing-usage.json")),
	})
	out, err := runCommand(t, UsageCmd())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"METRIC", "tool_calls", "call", "41000", "200000", "observability_events", "unlimited", "2026-11-16T12:00:00.000Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "trace_spans") || strings.Contains(out, "at the limit") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestPrintOrgUsage_FlagsMetersAtOrOverTheirAllowance(t *testing.T) {
	var u billingUsage
	body := `{"period":{"end":"2026-11-16T12:00:00.000Z"},"meters":[
		{"meter":"seats","unit":"seat","used":3,"included":3,"overage":"none"},
		{"meter":"tool_calls","unit":"call","used":300,"included":200,"overage":"bill"},
		{"meter":"agent_runs","unit":"run","used":1,"included":50,"overage":"bill"}]}`
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	printOrgUsage(&buf, u)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("output = %q", buf.String())
	}
	for i, want := range []string{"seats seat 3 3 2026-11-16T12:00:00.000Z at the limit", "tool_calls call 300 200 2026-11-16T12:00:00.000Z over the allowance", "agent_runs run 1 50 2026-11-16T12:00:00.000Z"} {
		if got := strings.Join(strings.Fields(lines[i+1]), " "); got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
}

func TestUsage_PrintsJSONUnchangedAndTakesNoDateRange(t *testing.T) {
	body := contractFixture(t, "billing-usage.json")
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/usage": reply(200, body),
	})
	out, err := runCommand(t, UsageCmd(), "--json")
	if err != nil || strings.TrimSpace(out) != strings.TrimSpace(body) {
		t.Fatalf("--json = %q, %v", out, err)
	}
	if _, err := runCommand(t, UsageCmd(), "--from", "2026-10-01"); err == nil {
		t.Error("--from is not a flag of the billing usage route")
	}
}

func TestUsage_AQuotaRefusalIsExit31(t *testing.T) {
	refusingGateway(t, 429, nil, `{"error":"quota_exceeded","metric":"api_requests","limit":1000,"resets_at":"2026-10-17T00:00:00Z"}`)
	_, err := runCommand(t, UsageCmd())
	if got := exitCodeOf(err); got != 31 {
		t.Fatalf("exit %d (%v), want 31", got, err)
	}
}

func TestBillingCredits_PrintsBalanceBucketsAndHistory(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/credits": reply(200, contractFixture(t, "billing-credits.json")),
	})
	out, err := runCommand(t, BillingCmd(), "credits")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Balance:  19.00 EUR", "Held:     0.25 EUR",
		"mcl_a1  grant     1.50 EUR   2.00 EUR   2026-11-01T00:00:00.000Z",
		"mcl_b2  purchase  17.50 EUR  20.00 EUR  never",
		"mcl_c3  grant  2.00 EUR   2026-10-21T09:00:00.000Z", "mcl_d4  topup  20.00 EUR  2026-10-20T09:00:00.000Z",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestBillingCredits_EmptyAndJSON(t *testing.T) {
	empty := `{"balance_nano":0,"held_nano":0,"buckets":[],"entries":[]}`
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){"GET /api/v1/billing/credits": reply(200, empty)})
	out, err := runCommand(t, BillingCmd(), "credits")
	if err != nil || !strings.Contains(out, "Balance:  0.00 EUR") || !strings.Contains(out, "No credit left") || strings.Contains(out, "Held:") {
		t.Fatalf("empty: %q, %v", out, err)
	}
	out, err = runCommand(t, BillingCmd(), "credits", "--json")
	if err != nil || strings.TrimSpace(out) != empty {
		t.Fatalf("--json = %q, %v", out, err)
	}
}

func TestBillingCredits_AMemberIsRefusedAndTheSentenceShown(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/billing/credits": reply(403, `{"error":"forbidden","message":"Only owners and admins can see the credits."}`),
	})
	if _, err := runCommand(t, BillingCmd(), "credits"); err == nil || !strings.Contains(err.Error(), "Only owners and admins") {
		t.Fatalf("err = %v", err)
	}
}

func TestFormatNanoEuros(t *testing.T) {
	for nano, want := range map[int64]string{0: "0.00 EUR", 1: "0.000000001 EUR", 4_215_000: "0.004215 EUR", 19_000_000_000: "19.00 EUR", 1_234_500_000: "1.2345 EUR", -500_000_000: "-0.50 EUR"} {
		if got := formatNanoEuros(nano); got != want {
			t.Errorf("formatNanoEuros(%d) = %s, want %s", nano, got, want)
		}
	}
}
