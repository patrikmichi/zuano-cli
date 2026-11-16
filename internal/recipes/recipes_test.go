package recipes

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/signing"
)

const (
	fixtureRecipeID = "rcp_gmail_send_email"
	fixtureKeyID    = "rcp-fe812c12f3ab"
)

// The gateway signs its shared fixture with this seed; it is a test key only.
func fixtureSigner() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)) }

// fixture reads the gateway's signed Gmail recipe and its key document: from the
// directory the gateway's own fixtures are in when set, else the vendored copy.
func fixture(t *testing.T) (rec map[string]any, keys []Key) {
	t.Helper()
	dir := os.Getenv("ZUANO_CONTRACT_FIXTURES")
	if dir == "" {
		dir = filepath.Join("..", "cli", "testdata", "contracts")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "recipes", "gmail-send-email.json"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := signing.DecodeStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc := v.(map[string]any)
	keyDoc, _ := json.Marshal(map[string]any{"keys": doc["keys"]})
	if keys, err = ParseKeys(keyDoc); err != nil {
		t.Fatal(err)
	}
	return doc["recipe"].(map[string]any), keys
}

func marshal(t *testing.T, rec map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"recipe": rec})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func steps(rec map[string]any) []any { return rec["steps"].([]any) }

func step(rec map[string]any, i int) map[string]any { return steps(rec)[i].(map[string]any) }

func sigOf(rec map[string]any) map[string]any { return rec["signature"].(map[string]any) }

// resign makes rec carry a signature by priv over an envelope with the given
// parts, so a test can build a recipe whose signature is valid but for the place.
func resign(t *testing.T, rec map[string]any, priv ed25519.PrivateKey, recipeID string, version int, scope string, orgID any) {
	t.Helper()
	hash, err := contentHash(rec)
	if err != nil {
		t.Fatal(err)
	}
	rec["contentHash"] = hash
	envelope, err := signing.CanonicalJSON(map[string]any{"contentHash": hash, "orgId": orgID, "recipeId": recipeID, "scope": scope, "version": json.Number(strconv.Itoa(version))})
	if err != nil {
		t.Fatal(err)
	}
	sig := sigOf(rec)
	sig["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, envelope))
	sig["scope"], sig["orgId"] = scope, orgID
}

func keysFrom(keys []Key) KeySource { return func(string) ([]Key, error) { return keys, nil } }

func open(t *testing.T, rec map[string]any, keys []Key, want Expect) Verification {
	t.Helper()
	r, v := Open(marshal(t, rec), want, keysFrom(keys))
	if !v.Verified && r.ID != "" {
		t.Errorf("a recipe that did not verify was returned: %+v", v)
	}
	return v
}

var asked = Expect{ID: fixtureRecipeID}

func TestOpenVerifiesTheGatewaysSignedRecipe(t *testing.T) {
	rec, keys := fixture(t)
	if got := fixtureSigner().Public().(ed25519.PublicKey); base64.StdEncoding.EncodeToString(got) != keys[0].PublicKey {
		t.Fatal("the fixture was not signed with the shared test key")
	}
	r, v := Open(marshal(t, rec), Expect{ID: fixtureRecipeID, Version: 1}, keysFrom(keys))
	if !v.Verified || v.KeyID != fixtureKeyID || v.Scope != Platform {
		t.Fatalf("verification = %+v", v)
	}
	if r.ID != fixtureRecipeID || len(r.Steps) != 5 || r.Steps[2].FallbackFor != 1 || len(r.Pitfalls[2].ErrorCodes) != 2 || r.Signature.OrgID != nil {
		t.Errorf("recipe = %+v", r)
	}
}

func TestOpenRefusesEveryTampering(t *testing.T) {
	_, keys := fixture(t)
	priv := fixtureSigner()
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	cases := map[string]func(rec map[string]any){
		"step instruction": func(rec map[string]any) { step(rec, 0)["instruction"] = "Send the funds." },
		"step tool":        func(rec map[string]any) { step(rec, 0)["tool"] = "bank.transfer" },
		"step order":       func(rec map[string]any) { steps(rec)[0], steps(rec)[1] = steps(rec)[1], steps(rec)[0] },
		"title":            func(rec map[string]any) { rec["title"] = "Send money" },
		"use case":         func(rec map[string]any) { rec["useCase"] = "Anything" },
		"difficulty":       func(rec map[string]any) { rec["difficulty"].(map[string]any)["level"] = "hard" },
		"primary tools":    func(rec map[string]any) { rec["primaryTools"] = []any{"bank.transfer"} },
		"related tools":    func(rec map[string]any) { rec["relatedTools"] = []any{} },
		"pitfall fix": func(rec map[string]any) {
			rec["pitfalls"].([]any)[0].(map[string]any)["fix"] = "Disable the checks."
		},
		"extra step": func(rec map[string]any) {
			rec["steps"] = append(steps(rec), map[string]any{"order": json.Number("6"), "kind": "required", "tool": "x.y", "instruction": "z"})
		},
		"extra field in a step": func(rec map[string]any) { step(rec, 0)["risk"] = "read" },
		"content hash":          func(rec map[string]any) { rec["contentHash"] = strings.Repeat("0", 64) },
		"content hash dropped":  func(rec map[string]any) { delete(rec, "contentHash") },
		"content field dropped": func(rec map[string]any) { delete(rec, "pitfalls") },
		"content field null":    func(rec map[string]any) { rec["relatedTools"] = nil },
		"version":               func(rec map[string]any) { rec["version"] = json.Number("2") },
		"version as float":      func(rec map[string]any) { rec["version"] = json.Number("1.0") },
		"recipe id":             func(rec map[string]any) { rec["id"] = "rcp_other" },
		"swapped org":           func(rec map[string]any) { sigOf(rec)["orgId"] = "org_b" },
		"scope org":             func(rec map[string]any) { sigOf(rec)["scope"] = Org },
		"scope unknown":         func(rec map[string]any) { sigOf(rec)["scope"] = "global" },
		"scope dropped":         func(rec map[string]any) { delete(sigOf(rec), "scope") },
		"org dropped":           func(rec map[string]any) { delete(sigOf(rec), "orgId") },
		"org of a number":       func(rec map[string]any) { sigOf(rec)["orgId"] = json.Number("7") },
		"signature": func(rec map[string]any) {
			sigOf(rec)["signature"] = base64.StdEncoding.EncodeToString(make([]byte, 64))
		},
		"signature short": func(rec map[string]any) {
			sigOf(rec)["signature"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
		},
		"signature text":   func(rec map[string]any) { sigOf(rec)["signature"] = "%%%" },
		"signature empty":  func(rec map[string]any) { sigOf(rec)["signature"] = "" },
		"unsigned":         func(rec map[string]any) { rec["signature"] = nil },
		"signature absent": func(rec map[string]any) { delete(rec, "signature") },
		"unknown key id":   func(rec map[string]any) { sigOf(rec)["keyId"] = "rcp-000000000000" },
		"key id empty":     func(rec map[string]any) { sigOf(rec)["keyId"] = "" },
		"signed for another version": func(rec map[string]any) {
			resign(t, rec, priv, fixtureRecipeID, 2, Platform, nil)
		},
		"signed for another recipe": func(rec map[string]any) {
			resign(t, rec, priv, "rcp_other", 1, Platform, nil)
		},
		"platform signed with an org": func(rec map[string]any) {
			resign(t, rec, priv, fixtureRecipeID, 1, Platform, "org_a")
		},
		"signed by another key": func(rec map[string]any) {
			resign(t, rec, other, fixtureRecipeID, 1, Platform, nil)
		},
	}
	for name, mutate := range cases {
		rec, _ := fixture(t)
		mutate(rec)
		if v := open(t, rec, keys, asked); v.Verified || v.Reason == "" {
			t.Errorf("%s: verified (%+v)", name, v)
		}
	}
}

func TestOpenRefusesTheWrongRecipeOrVersionAsked(t *testing.T) {
	rec, keys := fixture(t)
	for name, want := range map[string]Expect{
		"another id":      {ID: "rcp_other"},
		"no id":           {},
		"another version": {ID: fixtureRecipeID, Version: 2},
	} {
		if v := open(t, rec, keys, want); v.Verified {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestOpenRefusesDuplicateKeysAndBrokenDocuments(t *testing.T) {
	rec, keys := fixture(t)
	good := string(marshal(t, rec))
	title := `"title":"Send an email to someone"`
	for name, doc := range map[string]string{
		"title twice":      strings.Replace(good, title, title+`,"title":"Send money"`, 1),
		"signature twice":  strings.Replace(good, `"signature":{`, `"signature":{"keyId":"rcp-000000000000",`, 1),
		"recipe twice":     `{"recipe":` + good[len(`{"recipe":`):len(good)-1] + `,"recipe":{}}`,
		"trailing content": good + `{}`,
		"not json":         `<html>`,
		"no recipe":        `{}`,
		"recipe a list":    `{"recipe":[]}`,
	} {
		if _, v := Open([]byte(doc), asked, keysFrom(keys)); v.Verified {
			t.Errorf("%s: verified", name)
		}
	}
	if _, v := Open([]byte(good), asked, keysFrom(keys)); !v.Verified {
		t.Fatalf("the unmodified document does not verify: %+v", v)
	}
}

func TestOpenTakesKeysOnlyFromTheKeySource(t *testing.T) {
	rec, keys := fixture(t)
	// A key set inside the recipe answer is data to refuse, not keys to use.
	raw, _ := json.Marshal(map[string]any{"recipe": rec, "keys": []any{map[string]any{"keyId": fixtureKeyID, "alg": Algorithm, "publicKey": keys[0].PublicKey}}})
	if _, v := Open(raw, asked, func(string) ([]Key, error) { return nil, errors.New("down") }); v.Verified || !strings.Contains(v.Reason, "published keys could not be read") {
		t.Errorf("keys down: %+v", v)
	}
	if _, v := Open(raw, asked, keysFrom(nil)); v.Verified {
		t.Errorf("no keys: %+v", v)
	}
	if _, v := Open(raw, asked, keysFrom([]Key{{KeyID: "rcp-000000000000", Alg: Algorithm, PublicKey: keys[0].PublicKey}})); v.Verified || !strings.Contains(v.Reason, "no published key") {
		t.Errorf("another key: %+v", v)
	}
	var asks []string
	if _, v := Open(marshal(t, rec), asked, func(id string) ([]Key, error) { asks = append(asks, id); return keys, nil }); !v.Verified || len(asks) != 1 || asks[0] != fixtureKeyID {
		t.Errorf("key source asked %v: %+v", asks, v)
	}
	unsigned, _ := fixture(t)
	unsigned["signature"] = nil
	asks = nil
	if _, v := Open(marshal(t, unsigned), asked, func(id string) ([]Key, error) { asks = append(asks, id); return keys, nil }); v.Verified || len(asks) != 0 {
		t.Errorf("an unsigned recipe asked for keys %v", asks)
	}
}

func TestOpenRefusesAKeyWithTheWrongAlgorithmOrMaterial(t *testing.T) {
	rec, keys := fixture(t)
	for name, key := range map[string]Key{
		"rsa label":      {KeyID: fixtureKeyID, Alg: "rsa", PublicKey: keys[0].PublicKey},
		"upper case":     {KeyID: fixtureKeyID, Alg: "ED25519", PublicKey: keys[0].PublicKey},
		"no label":       {KeyID: fixtureKeyID, PublicKey: keys[0].PublicKey},
		"short key":      {KeyID: fixtureKeyID, Alg: Algorithm, PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 31))},
		"key not base64": {KeyID: fixtureKeyID, Alg: Algorithm, PublicKey: "%%%"},
		"another key":    {KeyID: fixtureKeyID, Alg: Algorithm, PublicKey: base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey))},
	} {
		if v := open(t, rec, []Key{key}, asked); v.Verified {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestOpenChecksTheOrganisationOfAnOrganisationRecipe(t *testing.T) {
	_, keys := fixture(t)
	priv := fixtureSigner()
	build := func() map[string]any {
		rec, _ := fixture(t)
		resign(t, rec, priv, fixtureRecipeID, 1, Org, "org_a")
		rec["source"] = "org"
		return rec
	}
	if v := open(t, build(), keys, Expect{ID: fixtureRecipeID, OrgID: "org_a"}); !v.Verified || v.Scope != Org {
		t.Fatalf("own organisation: %+v", v)
	}
	for name, want := range map[string]Expect{
		"another organisation": {ID: fixtureRecipeID, OrgID: "org_b"},
		"organisation unknown": {ID: fixtureRecipeID},
	} {
		if v := open(t, build(), keys, want); v.Verified {
			t.Errorf("%s: verified", name)
		}
	}
	for name, org := range map[string]any{"no organisation": nil, "empty organisation": ""} {
		rec := build()
		resign(t, rec, priv, fixtureRecipeID, 1, Org, org)
		if v := open(t, rec, keys, Expect{ID: fixtureRecipeID, OrgID: "org_a"}); v.Verified {
			t.Errorf("%s: verified", name)
		}
	}
	// An organisation recipe moved to the platform scope, or the reverse, is a different envelope.
	moved := build()
	sigOf(moved)["scope"], sigOf(moved)["orgId"] = Platform, nil
	if v := open(t, moved, keys, Expect{ID: fixtureRecipeID, OrgID: "org_a"}); v.Verified {
		t.Error("an organisation recipe passed as a platform one")
	}
}

func TestContentHashMatchesTheGatewaysCanonicalForm(t *testing.T) {
	rec, _ := fixture(t)
	got, err := contentHash(rec)
	if err != nil || got != rec["contentHash"] {
		t.Fatalf("hash = %s, %v; the gateway sent %v", got, err, rec["contentHash"])
	}
	// Key order and number spelling in the document do not change the hash; text does.
	reordered := map[string]any{}
	for k, v := range rec {
		reordered[k] = v
	}
	if again, _ := contentHash(reordered); again != got {
		t.Error("the hash depends on map order")
	}
	canon, _ := signing.CanonicalJSON(map[string]any{"a": "<&> é", "b": []any{json.Number("1")}})
	if string(canon) != "{\"a\":\"<&> é\",\"b\":[1]}" {
		t.Errorf("canonical = %q", canon)
	}
}

func TestParseKeys(t *testing.T) {
	pub := base64.StdEncoding.EncodeToString(make([]byte, 32))
	good := `{"keys":[{"keyId":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"},{"keyId":"rcp-b","alg":"ed25519","publicKey":"` + pub + `"}]}`
	if keys, err := ParseKeys([]byte(good)); err != nil || len(keys) != 2 || keys[1].KeyID != "rcp-b" {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	for name, doc := range map[string]string{
		"legacy kid only":     `{"keys":[{"kid":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"}]}`,
		"kid beside keyId":    `{"keys":[{"keyId":"rcp-a","kid":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"}]}`,
		"one legacy of two":   `{"keys":[{"keyId":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"},{"kid":"rcp-b","alg":"ed25519","publicKey":"` + pub + `"}]}`,
		"shared id":           `{"keys":[{"keyId":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"},{"keyId":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"}]}`,
		"no algorithm":        `{"keys":[{"keyId":"rcp-a","publicKey":"` + pub + `"}]}`,
		"no key":              `{"keys":[{"keyId":"rcp-a","alg":"ed25519"}]}`,
		"empty":               `{"keys":[]}`,
		"absent":              `{}`,
		"not a list":          `{"keys":{}}`,
		"an entry not object": `{"keys":["rcp-a"]}`,
		"repeated field":      `{"keys":[{"keyId":"rcp-a","keyId":"rcp-b","alg":"ed25519","publicKey":"` + pub + `"}]}`,
		"repeated keys":       `{"keys":[],"keys":[{"keyId":"rcp-a","alg":"ed25519","publicKey":"` + pub + `"}]}`,
	} {
		if _, err := ParseKeys([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMaxAgeFollowsTheGatewaysCacheControl(t *testing.T) {
	for header, want := range map[string]time.Duration{
		"public, max-age=300":    5 * time.Minute,
		"max-age=60, public":     time.Minute,
		"Max-Age=30":             30 * time.Second,
		"max-age=86400":          MaxKeyLifetime,
		"max-age=300, no-store":  0,
		"private, max-age=300":   0,
		"no-cache":               0,
		"max-age=abc":            0,
		"max-age=-5":             0,
		"max-age=99999999999999": 0,
		"public":                 0,
		"":                       0,
	} {
		if got := maxAge(header); got != want {
			t.Errorf("maxAge(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestFetchKeysReadsThePublishedSetAndRefusesRedirects(t *testing.T) {
	_, keys := fixture(t)
	doc, _ := json.Marshal(map[string]any{"keys": keys})
	var path, auth string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(doc)
	}))
	defer gw.Close()
	set, err := FetchKeys(context.Background(), gw.URL)
	if err != nil || len(set.Keys) != 1 || set.MaxAge != 5*time.Minute || path != KeysPath || auth != "" {
		t.Fatalf("set = %+v, %v (path %q, authorization %q)", set, err, path, auth)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, gw.URL+KeysPath, http.StatusFound)
	}))
	defer bad.Close()
	if _, err := FetchKeys(context.Background(), bad.URL); err == nil {
		t.Error("a redirect was followed")
	}
	if _, err := FetchKeys(context.Background(), "http://gateway.example.com"); err == nil {
		t.Error("a plain http gateway outside loopback was asked")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer down.Close()
	if _, err := FetchKeys(context.Background(), down.URL); err == nil {
		t.Error("an error answer gave keys")
	}
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(append([]byte(`{"keys":[],"pad":"`), bytes.Repeat([]byte("a"), maxKeysBody)...))
	}))
	defer big.Close()
	if _, err := FetchKeys(context.Background(), big.URL); err == nil {
		t.Error("an oversized answer gave keys")
	}
}

func TestKeyCacheStopsServingAKeyWithdrawnFromTheSetAfterTheLifetime(t *testing.T) {
	_, keys := fixture(t)
	second := Key{KeyID: "rcp-next", Alg: Algorithm, PublicKey: keys[0].PublicKey}
	current := KeySet{Keys: keys, MaxAge: 5 * time.Minute}
	fetches := 0
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	c := KeyCache{Path: filepath.Join(t.TempDir(), "sub", "keys.json"), Now: func() time.Time { return now },
		Fetch: func(context.Context, string) (KeySet, error) { fetches++; return current, nil }}
	has := func(set []Key, id string) bool {
		for _, k := range set {
			if k.KeyID == id {
				return true
			}
		}
		return false
	}
	for range 3 {
		if got, err := c.Keys(context.Background(), "https://gw", fixtureKeyID); err != nil || !has(got, fixtureKeyID) {
			t.Fatalf("keys = %v, %v", got, err)
		}
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}
	// The gateway withdraws the key. Inside the lifetime the cache still answers; after it, nothing does.
	current = KeySet{Keys: []Key{second}, MaxAge: 5 * time.Minute}
	now = now.Add(4 * time.Minute)
	if got, _ := c.Keys(context.Background(), "https://gw", fixtureKeyID); !has(got, fixtureKeyID) || fetches != 1 {
		t.Errorf("the cache did not answer inside its lifetime (%d fetches)", fetches)
	}
	now = now.Add(2 * time.Minute)
	if got, _ := c.Keys(context.Background(), "https://gw", fixtureKeyID); has(got, fixtureKeyID) || fetches != 2 {
		t.Errorf("a withdrawn key is still served after the lifetime (%d fetches)", fetches)
	}
	// A key the cache lacks is a rotation: fetched again at once.
	current = KeySet{Keys: []Key{second, keys[0]}, MaxAge: 5 * time.Minute}
	if got, _ := c.Keys(context.Background(), "https://gw", fixtureKeyID); !has(got, fixtureKeyID) || fetches != 3 {
		t.Errorf("a rotation: %d fetches", fetches)
	}
	if _, _ = c.Keys(context.Background(), "https://other", ""); fetches != 4 {
		t.Errorf("another gateway shared the cache: %d fetches", fetches)
	}
}

func TestKeyCacheDoesNotKeepWhatTheGatewayDoesNotAllowOrDistrustsItsDates(t *testing.T) {
	_, keys := fixture(t)
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "keys.json")
	fetches := 0
	set := KeySet{Keys: keys}
	c := KeyCache{Path: path, Now: func() time.Time { return now }, Fetch: func(context.Context, string) (KeySet, error) { fetches++; return set, nil }}
	for range 2 {
		if _, err := c.Keys(context.Background(), "https://gw", ""); err != nil {
			t.Fatal(err)
		}
	}
	if fetches != 2 {
		t.Errorf("a set the gateway did not allow to be cached was kept (%d fetches)", fetches)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an uncacheable set was written to disk")
	}

	set.MaxAge = time.Hour
	write := func(fetched, expires time.Time) {
		raw, _ := json.Marshal(cachedKeys{Origin: "https://gw", FetchedAt: fetched, ExpiresAt: expires, Keys: keys})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fetches = 0
	write(now.Add(time.Hour), now.Add(2*time.Hour))
	_, _ = c.Keys(context.Background(), "https://gw", "")
	write(now.Add(-time.Hour), now.Add(23*time.Hour))
	_, _ = c.Keys(context.Background(), "https://gw", "")
	write(now.Add(-time.Hour), now.Add(-time.Minute))
	_, _ = c.Keys(context.Background(), "https://gw", "")
	if fetches != 3 {
		t.Errorf("a future-dated, over-long or expired file was used (%d fetches)", fetches)
	}

	c.Fetch = func(context.Context, string) (KeySet, error) { return KeySet{}, errors.New("down") }
	write(now.Add(-time.Hour), now.Add(-time.Minute))
	if _, err := c.Keys(context.Background(), "https://gw", ""); err == nil {
		t.Error("a failed fetch fell back to an expired cache")
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v", info.Mode().Perm())
	}
}
