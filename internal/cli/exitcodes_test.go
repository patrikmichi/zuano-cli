package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestExitCodeTableInDocsMatchesTheConstants(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "gateway-contracts.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## CLI exit codes\n")
	if !ok {
		t.Fatal("gateway-contracts.md has no CLI exit codes section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	row := regexp.MustCompile("(?m)^\\| (\\d+) \\| `([a-z0-9_]+)` \\|")
	docs := map[int]string{}
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		n, _ := strconv.Atoi(m[1])
		if prev, dup := docs[n]; dup {
			t.Errorf("code %d is listed twice (%s, %s)", n, prev, m[2])
		}
		docs[n] = m[2]
	}
	code := map[int]string{}
	for _, e := range exitRegistry {
		if prev, dup := code[e.code]; dup {
			t.Errorf("code %d is %s and %s in the code", e.code, prev, e.name)
		}
		code[e.code] = e.name
	}
	for n, name := range code {
		if docs[n] != name {
			t.Errorf("code %d is %q in the code and %q in the docs table", n, name, docs[n])
		}
	}
	for n, name := range docs {
		if code[n] != name {
			t.Errorf("the docs table lists %d %q, which the code does not have", n, name)
		}
	}
}

func TestRefusalReasonsMapToRegisteredCodes(t *testing.T) {
	registered := map[int]bool{}
	for _, e := range exitRegistry {
		registered[e.code] = true
	}
	for reason, code := range refusalCodes {
		if !registered[code] {
			t.Errorf("%s maps to unregistered code %d", reason, code)
		}
	}
	for reason, want := range map[string]int{
		"meta_executor_blocked": 10, "agent_not_allowed": 10, "registry_server_deleted": 44,
		"spec_invalid": 60, "spec_unsupported": 61, "connector_slug_taken": 62, "connector_version_conflict": 62,
		"quality_gate_failed": 63, "spec_fetch_blocked": 1, "federated_mode_unsupported": 1,
	} {
		err := policyRefusal([]byte(`{"reason":"`+reason+`","message":"m"}`), nil)
		if exitCodeOf(err) != want {
			t.Errorf("%s exits %d, want %d", reason, exitCodeOf(err), want)
		}
	}
}

// A call through a private network that is too big either way is refused with
// the gateway's sentence and the generic status; the old transfer code is gone.
func TestTooLargeRefusalsKeepTheirSentenceAndExitGenerically(t *testing.T) {
	for _, code := range []string{"request_too_large", "response_too_large"} {
		err := policyRefusal([]byte(`{"error":"`+code+`","message":"The body is over the limit."}`), nil)
		if exitCodeOf(err) != 1 || err == nil || !strings.Contains(err.Error(), "The body is over the limit.") {
			t.Errorf("%s: exit %d, %v", code, exitCodeOf(err), err)
		}
		if refusalCodes[code] != 1 {
			t.Errorf("%s is not listed as a refusal that exits 1", code)
		}
	}
	if _, ok := refusalCodes["transfer_too_large"]; ok {
		t.Error("transfer_too_large is listed; the gateway no longer sends it")
	}
}

// The marketplace and recipe refusals have no status of their own: each ends
// with exit 1 and the gateway's sentence, and the code stays in --json.
func TestMarketAndRecipeRefusalsExitGenerically(t *testing.T) {
	enum := vendoredRefusalCodes(t)
	for _, tc := range []struct {
		status int
		body   string
		code   string
	}{
		{404, `{"error":"purchase_not_found","message":"No such purchase."}`, "purchase_not_found"},
		{409, `{"error":"refund_refused","message":"The refund window has closed."}`, "refund_refused"},
		{409, `{"error":"refund_unavailable","message":"This purchase cannot be refunded."}`, "refund_unavailable"},
		{404, `{"error":"flag_not_found","message":"No such flag."}`, "flag_not_found"},
		{409, `{"error":"conflict","reason":"four_eyes_required","message":"Another organization admin has to activate it."}`, "four_eyes_required"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			err := gatewayStatusError(tc.status, []byte(tc.body))
			if exitCodeOf(err) != 1 || !strings.Contains(err.Error(), "(HTTP ") {
				t.Errorf("exit %d, %v", exitCodeOf(err), err)
			}
			if _, mapped := refusalExit[tc.code]; mapped {
				t.Errorf("%s has an exit status of its own; the registry lists none", tc.code)
			}
			if tc.code != "four_eyes_required" && !slices.Contains(enum, tc.code) {
				t.Errorf("%s is not in the OpenAPI refusal codes", tc.code)
			}
		})
	}
}

func TestRouteRefusalMapsUseRegisteredCodes(t *testing.T) {
	registered := map[int]bool{}
	for _, e := range exitRegistry {
		registered[e.code] = true
	}
	for name, codes := range map[string]map[string]int{"refusalExit": refusalExit, "kb": kbRefusals, "domains": domainRefusals, "firewall": firewallRefusalExit} {
		for refusal, code := range codes {
			if !registered[code] {
				t.Errorf("%s: %s maps to unregistered code %d", name, refusal, code)
			}
		}
	}
	// The knowledge base refusal is in the gateway's document; the domain
	// refusals join it once the core branch is on the integration branch.
	enum := vendoredRefusalCodes(t)
	for refusal := range kbRefusals {
		if !slices.Contains(enum, refusal) {
			t.Errorf("%s is not in the OpenAPI refusal codes", refusal)
		}
	}
	for refusal := range artifactRefusals {
		if !slices.Contains(enum, refusal) {
			t.Errorf("%s is not in the OpenAPI refusal codes", refusal)
		}
	}
	for _, refusal := range []string{"space_exists", "page_exists", "under_hold", "version_conflict", "browser_session_required", "expired", "already_decided", "challenge_invalid", "digest_mismatch", "passkey_verification_failed", "passkey_required"} {
		if !slices.Contains(enum, refusal) {
			t.Errorf("%s is documented here but not in the OpenAPI refusal codes", refusal)
		}
	}
}
