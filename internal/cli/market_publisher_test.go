package cli

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/signing"
)

// gatewayVector is the document the gateway's own signing code wrote; see
// internal/signing/publisher_test.go.
type gatewayVector struct {
	SeedHex     string         `json:"seed_hex"`
	PublicKey   string         `json:"public_key"`
	Handle      string         `json:"handle"`
	Slug        string         `json:"slug"`
	Version     string         `json:"version"`
	ContentHash string         `json:"content_hash"`
	Manifest    map[string]any `json:"manifest"`
	Signature   string         `json:"signature"`
}

func loadGatewayVector(t *testing.T) (gatewayVector, ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "signing", "testdata", "publisher-signature.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v gatewayVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(v.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	return v, ed25519.NewKeyFromSeed(seed)
}

func writeKeyFile(t *testing.T, path string, key ed25519.PrivateKey) {
	t.Helper()
	pemKey, err := signing.MarshalPublisherKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
}

const listingJSON = `{"category":"finance","outcome":"Checks every invoice before it is paid","samples":[{"name":"one","input":{},"output":{}}],"screenshots":["./shots/a.png"],"support":{"email":"help@acme.test"}}`

type submitFixture struct {
	dir     string
	listing string
	keyFile string
	vector  gatewayVector
	gw      *packServer
}

// submitGateway serves what a signed submission reads and writes, with the
// gateway's own vector as the published version.
func submitGateway(t *testing.T, submit http.HandlerFunc) submitFixture {
	t.Helper()
	v, key := loadGatewayVector(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "shots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shots", "a.png"), []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := filepath.Join(dir, "listing.json")
	if err := os.WriteFile(listing, []byte(listingJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "key.pem")
	writeKeyFile(t, keyFile, key)

	manifest, err := json.Marshal(v.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	template := `{"resource_id":"res_invoice","slug":"` + v.Slug + `","version":"` + v.Version + `","manifest":` + string(manifest) + `,"analysis":{"manifestDigest":"` + v.ContentHash + `"}}`
	gw := workflowGateway(t, routeMap{
		"GET /api/v1/market/publisher":                jsonReply(200, `{"publisher":{"handle":"`+v.Handle+`","tier":"verified"},"standing":{"may_submit":true}}`),
		"GET /api/v1/workflows/templates/res_invoice": jsonReply(200, template),
		"POST /api/v1/market/submissions":             func(_ *packServer, w http.ResponseWriter, r *http.Request) { submit(w, r) },
		"PUT /api/v1/market/publisher/signing-key":    jsonReply(200, `{"signing_public_key":"`+v.PublicKey+`"}`),
		"DELETE /api/v1/market/publisher/signing-key": jsonReply(200, `{"signing_public_key":null}`),
	})
	return submitFixture{dir: dir, listing: listing, keyFile: keyFile, vector: v, gw: gw}
}

func accepted(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, `{"submission":{"id":"wls_1","handle":"acme","slug":"invoice-check","version":"1.2.0","status":"scanning","elevated":true,"elevated_reasons":["first_listing"],"gates":[{"gate":"schema","status":"passed"},{"gate":"ai_scan","status":"pending"}],"problems":[]}}`)
}

func TestMarketSubmit_SendsTheSignatureTheGatewaysOwnCodeProduces(t *testing.T) {
	f := submitGateway(t, accepted)
	out, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing, "--key-file", f.keyFile)
	if err != nil {
		t.Fatalf("submit: %v\n%s", err, out)
	}
	body := f.gw.body("POST /api/v1/market/submissions", 0)
	if body["publisher_signature"] != f.vector.Signature {
		t.Errorf("publisher_signature = %v, want the gateway's %v", body["publisher_signature"], f.vector.Signature)
	}
	if body["resource_version_id"] != "rev_1" {
		t.Errorf("resource_version_id = %v", body["resource_version_id"])
	}
	shots, _ := body["screenshots"].(map[string]any)
	if shots["./shots/a.png"] != "cG5nLWJ5dGVz" {
		t.Errorf("screenshots = %v", shots)
	}
	if listing, _ := body["listing"].(map[string]any); listing["category"] != "finance" {
		t.Errorf("listing = %v", body["listing"])
	}
	for _, want := range []string{"acme/invoice-check@1.2.0", "scanning", "ai_scan", "pending", "first_listing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMarketSubmit_TakesTheKeyFromTheEnvironmentAndTheConfigDirectory(t *testing.T) {
	f := submitGateway(t, accepted)
	t.Setenv(publisherKeyEnv, f.keyFile)
	if _, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing); err != nil {
		t.Fatal(err)
	}
	if got := f.gw.body("POST /api/v1/market/submissions", 0)["publisher_signature"]; got != f.vector.Signature {
		t.Errorf("env key: signature = %v", got)
	}

	t.Setenv(publisherKeyEnv, "")
	_, key := loadGatewayVector(t)
	dir := filepath.Join(os.Getenv("HOME"), ".config", "zuano")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKeyFile(t, filepath.Join(dir, publisherKeyFileName), key)
	if _, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing); err != nil {
		t.Fatal(err)
	}
	if got := f.gw.body("POST /api/v1/market/submissions", 1)["publisher_signature"]; got != f.vector.Signature {
		t.Errorf("default key: signature = %v", got)
	}
}

func TestMarketSubmit_WithoutAKeySendsNoSignatureAndTheGatewayDecides(t *testing.T) {
	f := submitGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"signature_required","message":"This publisher has a signing key: sign the version and send the signature."}`)
	})
	_, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing)
	if got := exitCodeOf(err); got != exitSignatureRequired || err == nil || !strings.Contains(err.Error(), "sign the version") {
		t.Fatalf("exit %d, err %v", got, err)
	}
	if body := f.gw.body("POST /api/v1/market/submissions", 0); body["publisher_signature"] != nil {
		t.Errorf("sent a signature without a key: %v", body["publisher_signature"])
	}
	if f.gw.count("GET /api/v1/workflows/templates/res_invoice") != 0 {
		t.Error("read the template although nothing is signed")
	}
}

func TestMarketSubmit_EachRefusalHasItsOwnExitCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{422, "signature_required", exitSignatureRequired},
		{422, "signature_invalid", signing.ExitContentSignature},
		{409, "signature_invalid", signing.ExitContentSignature},
		{422, "publisher_key_missing", exitPublisherKeyMissing},
		{409, "publisher_not_verified", exitPublisherNotVerified},
		{403, "publisher_not_verified", exitPublisherNotVerified},
	} {
		t.Run(tc.code, func(t *testing.T) {
			f := submitGateway(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"`+tc.code+`","message":"refused"}`)
			})
			_, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing, "--key-file", f.keyFile)
			if got := exitCodeOf(err); got != tc.exit {
				t.Errorf("%d %s: exit %d, want %d (%v)", tc.status, tc.code, got, tc.exit, err)
			}
		})
	}
}

func TestMarketSubmit_ASubmissionThatNeedsChangesFails(t *testing.T) {
	f := submitGateway(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"submission":{"id":"wls_1","handle":"acme","slug":"invoice-check","version":"1.2.0","status":"changes_requested","gates":[{"gate":"secrets","status":"failed"}],"problems":["secrets: a token in step fetch"]}}`)
	})
	out, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing, "--key-file", f.keyFile)
	if err == nil || !strings.Contains(out, "secrets: a token in step fetch") {
		t.Fatalf("err %v\n%s", err, out)
	}
}

func TestMarketSubmit_RefusesWhatItCannotSignOrSend(t *testing.T) {
	f := submitGateway(t, accepted)
	base := []string{"submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", f.listing}
	for name, args := range map[string][]string{
		"no version in the reference": {"submit", "res_invoice", "--version-id", "rev_1", "--listing-file", f.listing},
		"no version id":               {"submit", "res_invoice@1.2.0", "--listing-file", f.listing},
		"no listing":                  {"submit", "res_invoice@1.2.0", "--version-id", "rev_1"},
		"missing named key":           append(append([]string{}, base...), "--key-file", filepath.Join(f.dir, "nope.pem")),
	} {
		if _, err := runCommand(t, MarketCmd(), args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if f.gw.count("POST /api/v1/market/submissions") != 0 {
		t.Error("sent a submission it should have refused")
	}

	loose := filepath.Join(f.dir, "loose.pem")
	_, key := loadGatewayVector(t)
	writeKeyFile(t, loose, key)
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, MarketCmd(), append(append([]string{}, base...), "--key-file", loose)...); err == nil || !strings.Contains(err.Error(), "only its owner") {
		t.Errorf("group-readable key: %v", err)
	}
}

func TestMarketSubmit_ScreenshotsStayBesideTheListing(t *testing.T) {
	f := submitGateway(t, accepted)
	for _, shot := range []string{"../outside.png", "/etc/passwd"} {
		listing := filepath.Join(f.dir, "bad.json")
		if err := os.WriteFile(listing, []byte(`{"screenshots":["`+shot+`"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := runCommand(t, MarketCmd(), "submit", "res_invoice@1.2.0", "--version-id", "rev_1", "--listing-file", listing, "--key-file", f.keyFile)
		if err == nil || !strings.Contains(err.Error(), "beside the listing") {
			t.Errorf("%s: err = %v", shot, err)
		}
	}
	if f.gw.count("POST /api/v1/market/submissions") != 0 {
		t.Error("sent a submission with a screenshot outside the listing directory")
	}
}

func TestSigningKey_GenerateWritesAnOwnerOnlyKeyAndNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "k.pem")
	out, err := runCommand(t, marketSigningKeyCmd(), "generate", "--key-file", path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	key, err := loadPublisherKey(path)
	if err != nil || !strings.Contains(out, "Public key: "+signing.PublisherPublicKey(key)) {
		t.Fatalf("out %q key err %v", out, err)
	}
	if _, err := runCommand(t, marketSigningKeyCmd(), "generate", "--key-file", path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrote: %v", err)
	}
}

func TestSigningKey_SetRegistersThePublicKeyAndRemoveDeletesIt(t *testing.T) {
	f := submitGateway(t, accepted)
	out, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "set", "--key-file", f.keyFile)
	if err != nil || !strings.Contains(out, f.vector.PublicKey) {
		t.Fatalf("set = %q, %v", out, err)
	}
	if got := f.gw.body("PUT /api/v1/market/publisher/signing-key", 0)["public_key"]; got != f.vector.PublicKey {
		t.Errorf("registered %v, want %v", got, f.vector.PublicKey)
	}
	if _, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "set", "--public-key", f.vector.PublicKey); err != nil {
		t.Fatal(err)
	}
	if got := f.gw.body("PUT /api/v1/market/publisher/signing-key", 1)["public_key"]; got != f.vector.PublicKey {
		t.Errorf("--public-key registered %v", got)
	}
	if _, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "set", "--public-key", "x", "--key-file", f.keyFile); exitCodeOf(err) != exitUsage {
		t.Errorf("both flags: %v", err)
	}
	if out, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "remove"); err != nil || !strings.Contains(out, "removed") {
		t.Errorf("remove = %q, %v", out, err)
	}
}

func TestSigningKey_SetNamesTheRefusalForAnUnverifiedPublisher(t *testing.T) {
	f := submitGateway(t, accepted)
	f.gw.replace(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":"publisher_not_verified","message":"Only a verified publisher can sign its versions."}`)
	})
	_, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "set", "--key-file", f.keyFile)
	if exitCodeOf(err) != exitPublisherNotVerified || err == nil || !strings.Contains(err.Error(), "verified publisher") {
		t.Fatalf("exit %d, err %v", exitCodeOf(err), err)
	}
}

func TestSigningKey_SetWithoutAKeyPointsToGenerate(t *testing.T) {
	workflowGateway(t, routeMap{})
	_, err := runCommand(t, MarketCmd(), "publisher", "signing-key", "set")
	if err == nil || !strings.Contains(err.Error(), "signing-key generate") {
		t.Fatalf("err = %v", err)
	}
}
