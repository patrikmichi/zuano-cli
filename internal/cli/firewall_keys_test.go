package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/config"
	"github.com/patrikmichi/zuano-cli/internal/signing"
)

func firewallReply(s *packServer, body string) gatewayReply {
	h := http.Header{}
	s.fwSigner.Sign(h, []byte(body))
	return gatewayReply{Status: 200, Header: h, Body: []byte(body)}
}

func policyKeysRoute(s *packServer, w http.ResponseWriter, _ *http.Request) {
	raw, _ := json.Marshal(map[string]any{"keys": []signing.Key{s.fwSigner.Key}})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func TestFirewallVerify_ChecksThePurposeAndTheFirewallRing(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{})
	c, err := approvalsClient(gw.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if err := firewallReply(gw, `{"purpose":"fw-session","status":"active"}`).verifyPurpose(ctx, c, "fw-session"); err != nil {
		t.Errorf("a session signed for the session: %v", err)
	}
	if err := firewallReply(gw, `{"purpose":"fw-snapshot","tools":[]}`).verifyPurpose(ctx, c, "fw-snapshot"); err != nil {
		t.Errorf("a snapshot signed for the snapshot: %v", err)
	}
	err = firewallReply(gw, `{"purpose":"fw-snapshot","tools":[]}`).verifyPurpose(ctx, c, "fw-session")
	if exitCodeOf(err) != 4 || !strings.Contains(err.Error(), "fw-session") {
		t.Errorf("a snapshot used as a session: exit %d, %v", exitCodeOf(err), err)
	}
	if err := firewallReply(gw, `{"status":"active"}`).verifyPurpose(ctx, c, "fw-session"); exitCodeOf(err) != 4 {
		t.Errorf("a document with no purpose: exit %d, %v", exitCodeOf(err), err)
	}

	body := `{"purpose":"fw-session"}`
	h := http.Header{}
	gw.signer.Sign(h, []byte(body))
	if err := (gatewayReply{Status: 200, Header: h, Body: []byte(body)}).verifyPurpose(ctx, c, "fw-session"); exitCodeOf(err) != 4 {
		t.Errorf("a document signed with the download key: exit %d, %v", exitCodeOf(err), err)
	}
	if err := (gatewayReply{Status: 200, Header: http.Header{}, Body: []byte(body)}).verifyPurpose(ctx, c, "fw-session"); exitCodeOf(err) != 4 {
		t.Errorf("an unsigned document: exit %d, %v", exitCodeOf(err), err)
	}
}

func TestFirewallVerify_PinsThePolicyKeysOnFirstUse(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{"GET /.well-known/zuano-policy-keys": policyKeysRoute})
	origin, _ := config.NormalizeGatewayURL(gw.URL)
	if err := config.PinFirewallKeys(origin, nil); err != nil {
		t.Fatal(err)
	}
	c, err := approvalsClient(gw.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := firewallReply(gw, `{"purpose":"fw-session"}`).verifyPurpose(context.Background(), c, "fw-session"); err != nil {
		t.Fatal(err)
	}
	if pinned, _ := config.PinnedFirewallKeys(origin); len(pinned) != 1 || pinned[0].KID != "fwkid1" {
		t.Errorf("pinned = %+v", pinned)
	}
	if got := gw.count("GET /.well-known/zuano-policy-keys"); got != 1 {
		t.Errorf("keys fetched %d times", got)
	}
}

func TestFirewallVerify_WithoutPublishedKeysAsksForTrust(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{})
	gw.lenient = true
	origin, _ := config.NormalizeGatewayURL(gw.URL)
	if err := config.PinFirewallKeys(origin, nil); err != nil {
		t.Fatal(err)
	}
	c, err := approvalsClient(gw.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = firewallReply(gw, `{"purpose":"fw-session"}`).verifyPurpose(context.Background(), c, "fw-session")
	if err == nil || !strings.Contains(err.Error(), "config trust-key --firewall") {
		t.Errorf("err = %v", err)
	}
}

func TestConfigTrustKey_Firewall(t *testing.T) {
	syncHome(t)
	gw := packGateway(t, routeMap{"GET /.well-known/zuano-policy-keys": policyKeysRoute})
	origin, _ := config.NormalizeGatewayURL(gw.URL)
	if err := config.PinFirewallKeys(origin, nil); err != nil {
		t.Fatal(err)
	}
	out, err := runCommand(t, configTrustKeyCmd(), "--gateway-url", gw.URL, "--firewall", "--yes")
	if err != nil || !strings.Contains(out, "fwkid1") {
		t.Fatalf("%q %v", out, err)
	}
	if pinned, _ := config.PinnedFirewallKeys(origin); len(pinned) != 1 || pinned[0].KID != "fwkid1" {
		t.Errorf("pinned = %+v", pinned)
	}
	if download, _ := config.PinnedSigningKeys(origin); len(download) != 1 || download[0].KID != "kid1" {
		t.Errorf("the download ring changed: %+v", download)
	}
}
