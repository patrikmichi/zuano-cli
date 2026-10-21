package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestModelsCmd(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/models": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"mode":{"allowed":["byo","platform"],"default":"byo"},"defaults":{"model":"anthropic/claude-sonnet-5.5","source":"byo"},"balanceNano":2500000000,"models":[{"id":"anthropic/claude-sonnet-5.5","label":"Claude Sonnet 5.5","status":"active","sources":{"platform":{"available":true,"price":{"inputPerMTokNano":3000000000,"outputPerMTokNano":15000000000}},"byo":{"available":false,"reason":"no_key"}}}]}`)
		},
	})
	out, err := runCommand(t, ModelsCmd(), "--surface", "agent")
	if err != nil || q != "surface=agent" {
		t.Fatalf("%q %q %v", q, out, err)
	}
	for _, want := range []string{"Credit: 2.50", "claude-sonnet-5.5", "yes (3.00 / 15.00 per M tokens)", "no (no_key)"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if _, err := runCommand(t, ModelsCmd(), "--surface", "nope"); err == nil {
		t.Error("accepted an unknown surface")
	}
}

func TestMarketSearchAndShow(t *testing.T) {
	var q string
	workflowGateway(t, routeMap{
		"GET /api/v1/market/listings": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			q = r.URL.RawQuery
			_, _ = io.WriteString(w, `{"listings":[{"handle":"acme","slug":"invoice-check","title":"Invoice check","publisherName":"Acme","maxRisk":"write","badges":["evaluated"],"missing":["slack"]}],"total":3}`)
		},
		"GET /api/v1/market/listings/acme/invoice-check":               jsonReply(200, `{"title":"Invoice check"}`),
		"GET /api/v1/market/listings/acme/invoice-check/cost-estimate": jsonReply(200, `{"perRun":1}`),
	})
	out, err := runCommand(t, MarketCmd(), "search", "invoice", "--connectors", "gmail", "--ready", "--sort", "lowest_cost", "--limit", "1")
	if err != nil || q != "connectors=gmail&limit=1&q=invoice&ready=true&sort=lowest_cost" {
		t.Fatalf("%q %q %v", q, out, err)
	}
	for _, want := range []string{"acme/invoice-check", "evaluated", "slack", "1 of 3 shown; use --offset 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if out, err := runCommand(t, MarketCmd(), "show", "acme/invoice-check"); err != nil || !strings.Contains(out, `"title": "Invoice check"`) {
		t.Errorf("show: %q %v", out, err)
	}
	if out, err := runCommand(t, MarketCmd(), "cost", "acme/invoice-check"); err != nil || !strings.Contains(out, `"perRun": 1`) {
		t.Errorf("cost: %q %v", out, err)
	}
	if _, err := runCommand(t, MarketCmd(), "show", "no-slash"); err == nil {
		t.Error("accepted a listing without a handle")
	}
	if _, err := runCommand(t, MarketCmd(), "search", "--sort", "cheapest"); err == nil {
		t.Error("accepted an unknown sort")
	}
}

func TestMarketUpdatePolicy(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"PUT /api/v1/market/installations/wfi_1/update-policy": jsonReply(200, `{"settings":{"policy":"manual","channel":"beta"}}`),
	})
	out, err := runCommand(t, MarketCmd(), "update-policy", "wfi_1", "manual", "--channel", "beta")
	if err != nil || !strings.Contains(out, "now follows manual updates") {
		t.Fatalf("%q %v", out, err)
	}
	if b := gw.body("PUT /api/v1/market/installations/wfi_1/update-policy", 0); b["policy"] != "manual" || b["channel"] != "beta" {
		t.Errorf("body %v", b)
	}
	if _, err := runCommand(t, MarketCmd(), "update-policy", "wfi_1", "sometimes"); err == nil {
		t.Error("accepted an unknown policy")
	}
}

func TestAppsRequests(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"POST /api/v1/apps/requests":            jsonReply(201, `{"request":{"id":"apr_1","name":"Pohoda","status":"open","votes":1},"voted":true}`),
		"POST /api/v1/apps/requests/apr_1/vote": jsonReply(200, `{"request":{"id":"apr_1","name":"Pohoda","status":"open","votes":1},"voted":false}`),
		"GET /api/v1/apps/requests":             jsonReply(200, `{"requests":[{"id":"apr_1","name":"Pohoda","status":"open","votes":4}],"next_cursor":"4.apr_1"}`),
	})
	out, err := runCommand(t, AppsCmd(), "request", "Pohoda", "--url", "https://pohoda.example")
	if err != nil || !strings.Contains(out, "Counted your organisation's vote for Pohoda") {
		t.Fatalf("%q %v", out, err)
	}
	if b := gw.body("POST /api/v1/apps/requests", 0); b["name"] != "Pohoda" || b["url"] != "https://pohoda.example" {
		t.Errorf("body %v", b)
	}
	if out, err := runCommand(t, AppsCmd(), "vote", "apr_1"); err != nil || !strings.Contains(out, "had already voted") {
		t.Errorf("vote: %q %v", out, err)
	}
	out, err = runCommand(t, AppsCmd(), "requests")
	if err != nil || !strings.Contains(out, "Pohoda") || !strings.Contains(out, "--cursor 4.apr_1") {
		t.Errorf("board: %q %v", out, err)
	}
}
