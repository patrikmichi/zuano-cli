package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMarketLifecycle_DeprecateAndYankSendTheVersionRoutes(t *testing.T) {
	_, bodies := approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/v1/market/listings/acme/invoice-check/versions/1.2.0/deprecate": reply(200, `{"listing":"acme/invoice-check","version":"1.2.0","state":"deprecated","installations":4}`),
		"POST /api/v1/market/listings/acme/invoice-check/versions/1.1.0/yank":      reply(200, `{"listing":"acme/invoice-check","version":"1.1.0","state":"suspended","reason":"security","installations":2,"upgrade_to":"1.3.0"}`),
	})
	out, err := runCommand(t, MarketCmd(), "deprecate", "acme/invoice-check@1.2.0", "--note", "use 1.3.0")
	if err != nil || !strings.Contains(out, "Deprecated acme/invoice-check@1.2.0; 4 installations keep running") {
		t.Fatalf("deprecate: %q, %v", out, err)
	}
	out, err = runCommand(t, MarketCmd(), "yank", "acme/invoice-check@1.1.0", "--reason", "security", "--note", "leaks ids")
	if err != nil || !strings.Contains(out, "Yanked acme/invoice-check@1.1.0 (security); 2 installations paused.") || !strings.Contains(out, "offered 1.3.0") {
		t.Fatalf("yank: %q, %v", out, err)
	}
	if (*bodies)[0]["note"] != "use 1.3.0" || (*bodies)[1]["reason"] != "security" || (*bodies)[1]["note"] != "leaks ids" {
		t.Errorf("bodies = %v", *bodies)
	}
}

func TestMarketLifecycle_UsageErrorsSendNothing(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){})
	for _, args := range [][]string{
		{"deprecate", "acme/invoice-check"},
		{"yank", "acme/invoice-check@1.1.0"},
		{"yank", "acme/invoice-check@1.1.0", "--reason", "boring"},
		{"end-of-life", "acme/invoice-check"},
		{"end-of-life", "acme/invoice-check", "--at", "tomorrow"},
		{"reviews", "post", "acme/invoice-check", "--rating", "6"},
		{"reviews", "edit", "acme/invoice-check"},
		{"reviews", "reply", "rvw_1"},
		{"reports", "file", "acme/invoice-check", "--kind", "broken"},
		{"reports", "file", "acme/invoice-check", "--kind", "rude", "--text", "ten characters"},
		{"reports", "list", "--status", "closed"},
		{"reports", "respond", "wlr_1"},
	} {
		if _, err := runCommand(t, MarketCmd(), args...); exitCodeOf(err) != exitUsage {
			t.Errorf("%v: exit %d (%v), want usage", args, exitCodeOf(err), err)
		}
	}
}

func TestMarketLifecycle_RefusalsEndWithRegisteredCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		args   []string
		status int
		code   string
		want   int
	}{
		{"deprecate an unpublished version", "POST", "/api/v1/market/listings/acme/x/versions/1.0.0/deprecate", []string{"deprecate", "acme/x@1.0.0"}, 409, "version_not_published", exitVersionNotWithdrawn},
		{"yank a yanked version", "POST", "/api/v1/market/listings/acme/x/versions/1.0.0/yank", []string{"yank", "acme/x@1.0.0", "--reason", "broken"}, 409, "version_already_yanked", exitVersionNotWithdrawn},
		{"short notice", "POST", "/api/v1/market/listings/acme/x/end-of-life", []string{"end-of-life", "acme/x", "--at", "2030-01-01"}, 422, "notice_too_short", exitNoticeTooShort},
		{"not eligible", "POST", "/api/v1/market/listings/acme/x/reviews", []string{"reviews", "post", "acme/x", "--rating", "5"}, 403, "review_not_eligible", exitReviewNotEligible},
		{"handle taken", "POST", "/api/v1/market/publisher", []string{"publisher", "init", "--handle", "acme", "--name", "Acme", "--support-email", "a@acme.test", "--country", "CZ"}, 409, "handle_taken", exitHandleUnavailable},
		{"wrong code", "POST", "/api/v1/market/publisher/verifications/email/confirm", []string{"publisher", "verify", "email", "--code", "abcdefghijklmnopqrst"}, 400, "invalid_token", exitVerificationFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
				tc.method + " " + tc.path: reply(tc.status, `{"error":"`+tc.code+`","message":"the gateway says no"}`),
			})
			root := MarketCmd()
			_, err := runCommand(t, root, tc.args...)
			if got := exitCodeOf(err); got != tc.want || err == nil || !strings.Contains(err.Error(), "the gateway says no") {
				t.Fatalf("exit %d (%v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestMarketLifecycle_AnUnrelatedInvalidTokenKeepsItsMeaning(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"GET /api/v1/market/publisher": reply(401, `{"error":"invalid_token","message":"expired"}`),
	})
	_, err := runCommand(t, MarketCmd(), "publisher", "status")
	if exitCodeOf(err) == exitVerificationFailed {
		t.Fatalf("a 401 invalid_token exited %d", exitVerificationFailed)
	}
}

func TestMarketLifecycle_EndOfLifeSendsUTCTime(t *testing.T) {
	_, bodies := approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/v1/market/listings/acme/x/end-of-life": reply(200, `{"listing":"acme/x","ends_at":"2027-03-31T00:00:00.000Z"}`),
	})
	out, err := runCommand(t, MarketCmd(), "end-of-life", "acme/x", "--at", "2027-03-31")
	if err != nil || !strings.Contains(out, "acme/x ends on 2027-03-31T00:00:00.000Z") || (*bodies)[0]["ends_at"] != "2027-03-31T00:00:00Z" {
		t.Fatalf("%q %v %v", out, err, *bodies)
	}
	if _, err := runCommand(t, MarketCmd(), "end-of-life", "acme/x", "--at", "2027-03-31T02:00:00+02:00"); err != nil || (*bodies)[1]["ends_at"] != "2027-03-31T00:00:00Z" {
		t.Fatalf("offset time: %v %v", err, *bodies)
	}
}

func TestMarketLifecycle_ReviewsAndReports(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/market/listings/acme/x/reviews": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "cursor=c1&limit=5" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"reviews":[{"id":"rvw_1","rating":4,"body":"Solid","version":"1.2.0","live_runs":9,"publisher_reply":"Thanks","publisher_replied_at":null,"created_at":"2026-10-01T00:00:00.000Z","updated_at":"2026-10-01T00:00:00.000Z","mine":true}],"summary":{"count":7,"mean":4.25},"eligible":false,"live_runs":1,"next":"cur2"}`)
		},
		"POST /api/v1/market/listings/acme/x/reviews":  jsonReply(201, `{"review":{"id":"rvw_2","rating":5}}`),
		"PATCH /api/v1/market/listings/acme/x/reviews": jsonReply(200, `{"review":{"id":"rvw_2","rating":3}}`),
		"POST /api/v1/market/reviews/rvw_1/reply":      jsonReply(200, `{"review":{"id":"rvw_1","rating":4}}`),
		"GET /api/v1/market/publisher/reports": func(_ *packServer, w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "status=open" {
				t.Errorf("reports query = %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"reports":[{"id":"wlr_1","kind":"broken","status":"open","listing":"acme/x","body":"It stops at step 3","response":null,"created_at":"2026-10-02T00:00:00.000Z","first_response_at":null}]}`)
		},
		"POST /api/v1/market/listings/acme/x/reports": jsonReply(201, `{"report":{"id":"wlr_2","kind":"security","status":"open"}}`),
		"POST /api/v1/market/reports/wlr_1/respond":   jsonReply(200, `{"report":{"id":"wlr_1","status":"responded"}}`),
	})
	out, err := runCommand(t, MarketCmd(), "reviews", "list", "acme/x", "--cursor", "c1", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"7 reviews, 4.2 of 5", "has 1 live runs; three are needed", "rvw_1", "Solid (yours)", "publisher: Thanks", "--cursor cur2"} {
		if !strings.Contains(out, want) {
			t.Errorf("reviews lack %q:\n%s", want, out)
		}
	}
	for args, want := range map[string]string{
		"reviews post acme/x --rating 5 --text Great":                "Recorded review rvw_2: 5 of 5.",
		"reviews edit acme/x --rating 3 --text":                      "Updated review rvw_2: 3 of 5.",
		"reviews reply rvw_1 --reply Thanks":                         "Answered review rvw_1",
		"reports list --status open":                                 "wlr_1  acme/x",
		"reports file acme/x --kind security --text Leaks-the-token": "Reported as wlr_2 (security).",
		"reports respond wlr_1 --message Fixed":                      "Answered report wlr_1 (responded).",
	} {
		argv := strings.Fields(args)
		if argv[1] == "edit" {
			argv = append(argv, "")
		}
		out, err := runCommand(t, MarketCmd(), argv...)
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("%s: %q, %v", args, out, err)
		}
	}
	gw.mu.Lock()
	edit := gw.bodies["PATCH /api/v1/market/listings/acme/x/reviews"]
	gw.mu.Unlock()
	if len(edit) != 1 || edit[0]["rating"] != float64(3) {
		t.Fatalf("edit bodies = %v", edit)
	}
	if v, ok := edit[0]["body"]; !ok || v != nil {
		t.Errorf("edit body = %v, want an explicit null to clear the text", edit[0])
	}
}

func TestMarketLifecycle_WorkflowMarketAndPublisherAreReachable(t *testing.T) {
	approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/v1/market/listings/acme/x/versions/1.0.0/deprecate": reply(200, `{"listing":"acme/x","version":"1.0.0","state":"deprecated","installations":0}`),
	})
	root := WorkflowCmd()
	if out, err := runCommand(t, root, "market", "deprecate", "acme/x@1.0.0"); err != nil || !strings.Contains(out, "Deprecated acme/x@1.0.0") {
		t.Fatalf("%q %v", out, err)
	}
	var names []string
	for _, c := range PublisherCmd().Commands() {
		names = append(names, c.Name())
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "init") || !strings.Contains(got, "verify") || !strings.Contains(got, "status") {
		t.Errorf("publisher commands = %s", got)
	}
}

func TestPublisher_InitStatusAndVerify(t *testing.T) {
	_, bodies := approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
		"POST /api/v1/market/publisher":                             reply(201, `{"publisher":{"handle":"acme","display_name":"Acme","tier":"community"}}`),
		"GET /api/v1/market/publisher":                              reply(200, `{"publisher":{"handle":"acme","display_name":"Acme","tier":"community","support_email":"help@acme.test","country":"CZ","suspended":false},"standing":{"email_verified":true,"domain_verified":false,"domain":null,"may_submit":false}}`),
		"POST /api/v1/market/publisher/verifications/email":         reply(200, `{"sent_to":"help@acme.test"}`),
		"POST /api/v1/market/publisher/verifications/email/confirm": reply(200, `{"standing":{"email_verified":true,"domain_verified":false,"domain":null,"may_submit":false}}`),
		"POST /api/v1/market/publisher/verifications/domain":        reply(201, `{"domain":"acme.test","token":"t","dns":{"type":"TXT","name":"acme.test","value":"zuano-publisher=t"},"well_known":{"url":"https://acme.test/.well-known/zuano-publisher.txt","body":"t"}}`),
	})
	out, err := runCommand(t, PublisherCmd(), "init", "--handle", "acme", "--name", "Acme", "--support-email", "help@acme.test", "--country", "CZ", "--website", "https://acme.test")
	if err != nil || !strings.Contains(out, "Created publisher acme (community)") {
		t.Fatalf("init: %q, %v", out, err)
	}
	if b := (*bodies)[0]; b["display_name"] != "Acme" || b["support_email"] != "help@acme.test" || b["website"] != "https://acme.test" || b["bio"] != nil {
		t.Errorf("init body = %v", b)
	}
	if _, err := runCommand(t, PublisherCmd(), "init", "--handle", "acme"); exitCodeOf(err) != exitUsage {
		t.Errorf("init without its required flags: %v", err)
	}
	out, err = runCommand(t, PublisherCmd(), "status")
	if err != nil || !strings.Contains(out, "Publisher: acme (Acme), tier community, CZ") || !strings.Contains(out, "Domain verified: no") || !strings.Contains(out, "May submit free listings: no") {
		t.Fatalf("status: %q, %v", out, err)
	}
	if out, err := runCommand(t, PublisherCmd(), "verify", "email"); err != nil || !strings.Contains(out, "Sent a code to help@acme.test") {
		t.Fatalf("verify email: %q, %v", out, err)
	}
	if out, err := runCommand(t, PublisherCmd(), "verify", "email", "--code", "abcdefghijklmnopqrst"); err != nil || !strings.Contains(out, "Email confirmed.") {
		t.Fatalf("confirm: %q, %v", out, err)
	}
	out, err = runCommand(t, PublisherCmd(), "verify", "domain", "acme.test")
	if err != nil || !strings.Contains(out, "a TXT record on acme.test with the value zuano-publisher=t") || !strings.Contains(out, "/.well-known/zuano-publisher.txt containing exactly t") {
		t.Fatalf("verify domain: %q, %v", out, err)
	}
	if _, err := runCommand(t, PublisherCmd(), "verify", "domain"); exitCodeOf(err) != exitUsage {
		t.Errorf("domain without an argument or --check: %v", err)
	}
}

func TestPublisher_DomainCheck(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		want       string
	}{
		{"found", `{"verified":true,"method":"well_known","standing":{"email_verified":true,"domain_verified":true,"domain":"acme.test","may_submit":true}}`, 0, "Domain confirmed by the well-known proof."},
		{"not yet", `{"verified":false,"method":null,"standing":{"email_verified":true,"domain_verified":false,"domain":null,"may_submit":false}}`, exitVerificationFailed, "not found yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approvalsGateway(t, map[string]func(http.ResponseWriter, map[string]any){
				"POST /api/v1/market/publisher/verifications/domain/check": reply(200, tc.body),
			})
			out, err := runCommand(t, PublisherCmd(), "verify", "domain", "--check")
			if exitCodeOf(err) != tc.code || !strings.Contains(out+errText(err), tc.want) {
				t.Fatalf("exit %d, %q, %v", exitCodeOf(err), out, err)
			}
		})
	}
}
