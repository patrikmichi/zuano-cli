package evidence_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/evidence"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/signing/signingtest"
)

// ZUANO_GATEWAY_BUNDLE lists the volume archives of a bundle the gateway built
// (separated like a PATH), and ZUANO_GATEWAY_EVIDENCE_KEYS is the key document
// the gateway publishes at /.well-known/zuano-evidence-keys. Together they
// check this verifier against the gateway's own writer.
func TestVerify_AcceptsABundleTheGatewayBuilt(t *testing.T) {
	list, keyFile := os.Getenv("ZUANO_GATEWAY_BUNDLE"), os.Getenv("ZUANO_GATEWAY_EVIDENCE_KEYS")
	if list == "" || keyFile == "" {
		t.Skip("ZUANO_GATEWAY_BUNDLE and ZUANO_GATEWAY_EVIDENCE_KEYS are not set")
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Keys []signing.Key `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	res := verifyPaths(t, filepath.SplitList(list), doc.Keys, evidence.Options{})
	if !res.OK() {
		t.Fatalf("the gateway's own bundle does not verify:\n%s", failures(res))
	}
	t.Logf("%d files in %d volumes, %d audit rows, %d hours (%d verified, %d unsigned)", res.Files, res.Volumes, res.Events, res.Hours, res.VerifiedHours, len(res.UnsignedHours))
}

// testdata holds a bundle, a copy with one evidence file edited and the public
// evidence key, all written by the gateway's fixture script from synthetic data
// and a key generated for that run. The private key is not kept.
func fixtureKeys(t *testing.T) []signing.Key {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "evidence-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Keys []signing.Key `json:"keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Keys) == 0 {
		t.Fatalf("keys file: %v", err)
	}
	return doc.Keys
}

func TestVerify_GatewayFixtureBundle(t *testing.T) {
	keys := fixtureKeys(t)
	other := signingtest.New(keys[0].KID)
	cases := []struct {
		name    string
		archive string
		keys    []signing.Key
		wantOK  bool
		failure string
	}{
		{"the bundle with the published key", "bundle.zip", keys, true, ""},
		{"a copy with an evidence file edited", "tampered.zip", keys, false, "evidence/"},
		{"the bundle with a different key under the same id", "bundle.zip", []signing.Key{other.Key}, false, "manifest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := verifyPaths(t, []string{filepath.Join("testdata", c.archive)}, c.keys, evidence.Options{})
			if res.OK() != c.wantOK {
				t.Fatalf("OK = %v, want %v:\n%s", res.OK(), c.wantOK, failures(res))
			}
			if c.wantOK && (res.Files == 0 || res.VerifiedHours == 0 || len(res.UnsignedHours) != 0) {
				t.Errorf("accepted without checking the content: %d files, %d verified hours, %d unsigned", res.Files, res.VerifiedHours, len(res.UnsignedHours))
			}
			if c.failure != "" {
				wantFailure(t, res, c.failure)
			}
		})
	}
}
