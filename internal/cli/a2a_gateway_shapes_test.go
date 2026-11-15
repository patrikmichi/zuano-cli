package cli

import (
	"net/http"
	"strings"
	"testing"
)

// The bodies below are the shapes the gateway's a2a routes answer with
// (lib/a2a/api.ts, app/api/a2a/agents/[slug]/messages/route.ts).

func TestA2ACallHeldAnswerCarriesTheApprovalNested(t *testing.T) {
	a2aGateway(t, routeFns{"POST /api/a2a/agents/acme-billing/messages": jsonReply(202, `{"status":"pending_approval","approval":{"approval_id":"apr_9","service":"a2a:acme-billing","tool":"issue_credit_note","risk":"outward","args_digest":"d","expires_at":"2026-10-17T00:00:00Z"},"message":"Held for approval"}`)})
	out, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "issue_credit_note", "--message", "hi")
	if got := exitCodeOf(err); got != 12 {
		t.Fatalf("exit %d (%v), want 12; output %q", got, err, out)
	}
	if !strings.Contains(out+errText(err), "apr_9") {
		t.Errorf("the approval id is not shown: %q %v", out, err)
	}
}

func TestA2AChainShowReadsTheGatewayHopShape(t *testing.T) {
	a2aGateway(t, routeFns{"GET /api/a2a/chains/chn_1": jsonReply(200, `{"chain":{"id":"chn_1","status":"completed","origin":"user:u1","depth":1,"started_at":"t","audit":{"verified":false,"pending":true,"events":3},"budget":{"limits":{"tokens":10,"cost_eur":1,"tool_calls":5},"used":{"tokens":2,"cost_eur":0.1,"tool_calls":1}},"hops":[{"id":"h1","parent":null,"depth":1,"caller":"a","callee":"b","skill":"s","external":true,"state":"completed","duration_ms":null,"tokens":2,"cost_eur":0.1,"dlp_findings":[{"class":"iban","count":2},{"class":"email","count":1}],"injection_flags":["hidden_text"],"approvals":["apr_3"],"effects":[]}]}}`)})
	out, err := runCommand(t, A2ACmd(), "chains", "show", "chn_1")
	if err != nil {
		t.Fatalf("chains show: %v", err)
	}
	for _, want := range []string{"dlp 3", "injection 1", "apr_3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %q", want, out)
		}
	}
}

func TestA2AExplainReadsCeilingsAsAnObject(t *testing.T) {
	a2aGateway(t, routeFns{"GET /api/a2a/explain": jsonReply(200, `{"decision":{"result":"approval","reasons":["edge rule"],"approvals":["per_call"],"ceilings":{"max_risk":"write","hop_risk":"outward","blocked_classes":["iban"],"blocked_source_categories":[],"rate_per_minute":null},"edge":{"id":"e1","file":null,"line":null}}}`)})
	out, err := runCommand(t, A2ACmd(), "explain", "--from", "idn_1", "--to", "acme-billing", "--skill", "s")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	for _, want := range []string{"max_risk: write", "blocked_classes: iban"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %q", want, out)
		}
	}
	if strings.Contains(out, "rate_per_minute") || strings.Contains(out, "blocked_source_categories") {
		t.Errorf("an empty ceiling is printed: %q", out)
	}
}

func TestA2ACallHeldApprovalWithoutEffectStillNamesTheApproval(t *testing.T) {
	a2aGateway(t, routeFns{"POST /api/a2a/agents/acme-billing/messages": jsonReply(200, `{"task":{"id":"tsk_1","state":"INPUT_REQUIRED","approval":{"approval_id":"apr_2","approval_url":"https://gw.example/approvals/apr_2"},"chain_id":"chn_1"}}`)})
	out, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "s", "--message", "hi")
	if got := exitCodeOf(err); got != 12 {
		t.Fatalf("exit %d (%v)", got, err)
	}
	if strings.Contains(out, "(). is held") || strings.Contains(out, " . is held") || !strings.Contains(out, "apr_2") {
		t.Errorf("output: %q", out)
	}
}

func TestA2ACallLimitsMatchTheGateway(t *testing.T) {
	gw := a2aGateway(t, routeFns{"POST /api/a2a/agents/acme-billing/messages": jsonReply(200, completed(""))})
	if _, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "s", "--message", "m", "--wait", "121"); err == nil {
		t.Error("--wait above the gateway's 120 seconds was accepted")
	}
	if _, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "s", "--message", "m", "--wait", "120"); err != nil {
		t.Errorf("--wait 120: %v", err)
	}
	before := gw.count("POST /api/a2a/agents/acme-billing/messages")
	if _, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "s", "--message", "m", "--file", "doc=@/etc/hostname"); err == nil {
		t.Error("--file was accepted on a call whose body takes no files")
	}
	if gw.count("POST /api/a2a/agents/acme-billing/messages") != before {
		t.Error("a request was sent")
	}
}

func TestA2AStreamFallsBackToTheJSONTask(t *testing.T) {
	a2aGateway(t, routeFns{"POST /api/a2a/agents/acme-billing/messages": func(_ *packServer, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completed("")))
	}})
	out, err := runCommand(t, A2ACmd(), "call", "acme-billing", "--skill", "s", "--message", "hi", "--stream")
	if err != nil || !strings.Contains(out, "CN-42") {
		t.Fatalf("stream against a JSON answer: %q %v", out, err)
	}
}
