package cli

import (
	"net/http"
	"strings"
	"testing"
)

const (
	pendingDomainJSON  = `{"id":"dom_1","domain":"example.com","status":"pending","verification":{"type":"TXT","name":"_zuano-verify.example.com","value":"zuano-site-verification=abc"},"verified_at":null,"checked_at":null,"created_at":"2026-10-25T10:00:00.000Z"}`
	verifiedDomainJSON = `{"id":"dom_1","domain":"example.com","status":"verified","verification":{"type":"TXT","name":"_zuano-verify.example.com","value":"zuano-site-verification=abc"},"verified_at":"2026-10-25T11:00:00.000Z","checked_at":"2026-10-25T11:00:00.000Z","created_at":"2026-10-25T10:00:00.000Z"}`
)

func TestOrgDomainsListAddVerifyRemove(t *testing.T) {
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/domains":               jsonReply(200, `{"domains":[`+pendingDomainJSON+`]}`),
		"POST /api/v1/domains":              jsonReply(201, `{"domain":`+pendingDomainJSON+`}`),
		"POST /api/v1/domains/dom_1/verify": jsonReply(200, `{"domain":`+verifiedDomainJSON+`}`),
		"DELETE /api/v1/domains/dom_1":      func(_ *packServer, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	out, err := runCommand(t, OrgCmd(), "domains", "list")
	if err != nil || !strings.Contains(out, "example.com") || !strings.Contains(out, "pending") {
		t.Fatalf("list = %v\n%s", err, out)
	}
	out, err = runCommand(t, OrgCmd(), "domains", "add", " Example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"example.com (pending)", "TXT record at _zuano-verify.example.com", "zuano-site-verification=abc", "zuano org domains verify example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("add lacks %q:\n%s", want, out)
		}
	}
	if body := gw.body("POST /api/v1/domains", 0); body["domain"] != "example.com" {
		t.Errorf("add body = %v", body)
	}
	if out, err = runCommand(t, OrgCmd(), "domains", "verify", "EXAMPLE.com"); err != nil || !strings.Contains(out, "example.com (verified)") || strings.Contains(out, "Publish") {
		t.Fatalf("verify = %v\n%s", err, out)
	}
	if _, err = runCommand(t, OrgCmd(), "domains", "remove", "dom_1"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("a release without --yes: %v", err)
	}
	if gw.count("DELETE /api/v1/domains/dom_1") != 0 {
		t.Error("the domain was released without --yes")
	}
	if out, err = runCommand(t, OrgCmd(), "domains", "remove", "dom_1", "--yes"); err != nil || !strings.Contains(out, "Released example.com") {
		t.Fatalf("remove = %v\n%s", err, out)
	}
	if out, err = runCommand(t, OrgCmd(), "domains", "list", "--json"); err != nil || !strings.HasPrefix(out, `{"domains"`) {
		t.Errorf("list --json = %v %q", err, out)
	}
}

func TestOrgDomainsRejectBadInputAndUnknownDomains(t *testing.T) {
	gw := workflowGateway(t, routeMap{"GET /api/v1/domains": jsonReply(200, `{"domains":[`+pendingDomainJSON+`]}`)})
	for _, args := range [][]string{{"domains", "add", "a@b.com"}, {"domains", "add", "https://example.com"}, {"domains", "add", " "}} {
		if _, err := runCommand(t, OrgCmd(), args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
	if gw.count("POST /api/v1/domains") != 0 {
		t.Error("a bad domain was sent")
	}
	if _, err := runCommand(t, OrgCmd(), "domains", "verify", "other.org"); err == nil || !strings.Contains(err.Error(), "not one of your organisation's domains") {
		t.Errorf("verify of an unknown domain: %v", err)
	}
}

func TestOrgDomainRefusalsEndWithTheirRegisteredExit(t *testing.T) {
	for _, tc := range []struct {
		method, route string
		args          []string
		status        int
		code          string
		exit          int
	}{
		{"POST", "/api/v1/domains/dom_1/verify", []string{"domains", "verify", "dom_1"}, 409, "domain_unverified", exitVerificationFailed},
		{"POST", "/api/v1/domains/dom_1/verify", []string{"domains", "verify", "dom_1"}, 409, "domain_verified_elsewhere", 1},
		{"POST", "/api/v1/domains/dom_1/verify", []string{"domains", "verify", "dom_1"}, 503, "dns_unavailable", 1},
		{"POST", "/api/v1/domains", []string{"domains", "add", "example.com"}, 409, "domain_exists", 1},
		{"POST", "/api/v1/domains", []string{"domains", "add", "example.com"}, 409, "domain_limit", 1},
		{"POST", "/api/v1/domains", []string{"domains", "add", "example.com"}, 400, "invalid_domain", 1},
		{"POST", "/api/v1/domains", []string{"domains", "add", "example.com"}, 403, "forbidden", exitDenied},
	} {
		t.Run(tc.code, func(t *testing.T) {
			workflowGateway(t, routeMap{
				"GET /api/v1/domains":      jsonReply(200, `{"domains":[`+pendingDomainJSON+`]}`),
				tc.method + " " + tc.route: jsonReply(tc.status, `{"error":"`+tc.code+`","message":"`+tc.code+` message."}`),
			})
			_, err := runCommand(t, OrgCmd(), tc.args...)
			if got := ExitCode(err); got != tc.exit {
				t.Fatalf("exit %d, want %d (%v)", got, tc.exit, err)
			}
			if !strings.Contains(err.Error(), tc.code+" message.") {
				t.Errorf("the gateway's sentence is lost: %v", err)
			}
		})
	}
}
