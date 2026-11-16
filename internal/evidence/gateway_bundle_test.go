package evidence_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/evidence"
	"github.com/patrikmichi/zuano-cli/internal/signing"
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
