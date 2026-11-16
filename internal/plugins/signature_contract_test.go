package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/signing"
)

// gatewaySignature reads the SIGNATURE.json the gateway wrote for its signed
// sample package, from the directory the gateway's own fixtures are in when set,
// else the vendored copy.
func gatewaySignature(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("ZUANO_CONTRACT_FIXTURES")
	if dir == "" {
		dir = filepath.Join("..", "cli", "testdata", "contracts")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plugin-signature.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The gateway signs that package with a fixed test key (seed of 32 sevens), so
// its signature is checked here the way an install does.
func TestSignedMessageVerifiesTheGatewaysSignature(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)).Public().(ed25519.PublicKey)
	keys := []signing.Key{{KID: "2026-10", Alg: "ed25519", PublicKey: base64.StdEncoding.EncodeToString(pub)}}
	raw := gatewaySignature(t)

	message, err := signedMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	var sig Signature
	if err := json.Unmarshal(raw, &sig); err != nil {
		t.Fatal(err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := signing.VerifyMessage(keys, sig.KID, message, sigBytes); err != nil {
		t.Fatalf("the gateway's signature does not verify: %v", err)
	}

	for name, changed := range map[string]string{
		"a file hash":    strings.Replace(string(raw), `"commands/quote.md":"9b77`, `"commands/quote.md":"9b78`, 1),
		"the version":    strings.Replace(string(raw), `"version":"1.2.0"`, `"version":"1.2.1"`, 1),
		"the platform":   strings.Replace(string(raw), `"platform":"claude"`, `"platform":"codex"`, 1),
		"an added field": strings.Replace(string(raw), `"alg":"ed25519"`, `"alg":"ed25519","extra":"x"`, 1),
	} {
		if changed == string(raw) {
			t.Fatalf("%s: the fixture no longer has the text this test changes", name)
		}
		msg, err := signedMessage([]byte(changed))
		if err == nil && signing.VerifyMessage(keys, sig.KID, msg, sigBytes) == nil {
			t.Errorf("%s: a changed signature document verified", name)
		}
	}
}

func TestSignedMessageRefusesARepeatedKey(t *testing.T) {
	raw := gatewaySignature(t)
	repeated := strings.Replace(string(raw), `"kid":"2026-10"`, `"kid":"other","kid":"2026-10"`, 1)
	if repeated == string(raw) {
		t.Fatal("the fixture no longer has the text this test changes")
	}
	if _, err := signedMessage([]byte(repeated)); err == nil {
		t.Error("a signature document with a repeated key was read")
	}
	if _, err := signedMessage([]byte(`[]`)); err == nil {
		t.Error("a signature document that is not an object was read")
	}
}
