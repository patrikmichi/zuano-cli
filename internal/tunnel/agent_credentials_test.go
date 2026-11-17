package tunnel

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The gateway refuses a token whose id it has seen within the token's lifetime
// and a signature or token part that is not the canonical encoding of its bytes.

func TestMintJWT_EveryTokenHasItsOwnIDAndCanonicalParts(t *testing.T) {
	k, _ := GenerateKey(time.Now(), time.Hour)
	now := time.Now()
	seen := map[string]bool{}
	for range 2000 {
		tok, err := MintJWT(k, "nag_1", "https://broker.example", "net_1", now)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			t.Fatalf("token has %d parts", len(parts))
		}
		var decoded [3][]byte
		for i, p := range parts {
			if strings.ContainsAny(p, "=+/") {
				t.Fatalf("part %d is not unpadded base64url: %q", i, p)
			}
			raw, err := base64.RawURLEncoding.Strict().DecodeString(p)
			if err != nil || base64.RawURLEncoding.EncodeToString(raw) != p {
				t.Fatalf("part %d is not the canonical encoding of its bytes: %v", i, err)
			}
			decoded[i] = raw
		}
		if len(decoded[2]) != ed25519.SignatureSize || !ed25519.Verify(k.SignPub(), []byte(parts[0]+"."+parts[1]), decoded[2]) {
			t.Fatal("the signature does not verify over the encoded header and claims")
		}
		var claims struct {
			Jti string `json:"jti"`
		}
		if err := json.Unmarshal(decoded[1], &claims); err != nil {
			t.Fatal(err)
		}
		jti, err := base64.RawURLEncoding.Strict().DecodeString(claims.Jti)
		if err != nil || len(jti) < 12 {
			t.Fatalf("jti %q is not at least 96 random bits: %v", claims.Jti, err)
		}
		if seen[claims.Jti] {
			t.Fatalf("jti %q was used twice", claims.Jti)
		}
		seen[claims.Jti] = true
	}
}

func TestSignedRequestsNeverReuseATokenAndBytesAreCanonicalStandardBase64(t *testing.T) {
	app := newFakeApp(t)
	dir := t.TempDir()
	id, err := Enroll(t.Context(), EnrollOptions{AppURL: app.srv.URL, Token: testToken, StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.key = id.Current()
	app.mu.Unlock()
	now := time.Now()
	for range 3 {
		if _, err := ConfigHints(t.Context(), http.DefaultClient, id, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := RotateKeys(t.Context(), http.DefaultClient, id, dir, now); err != nil {
		t.Fatal(err)
	}

	app.mu.Lock()
	defer app.mu.Unlock()
	seen := map[string]bool{}
	for _, tok := range app.tokens {
		if seen[tok] {
			t.Fatal("a signed request reused an earlier token")
		}
		seen[tok] = true
	}
	if len(app.tokens) < 5 {
		t.Fatalf("only %d signed requests were seen", len(app.tokens))
	}

	// Every byte field of an enrollment and a rotation is padded standard base64 of exactly the bytes sent.
	if len(app.raw) != 2 {
		t.Fatalf("%d bodies recorded", len(app.raw))
	}
	for i, fields := range [][]string{{"sign_pub", "enc_pub", "proof"}, {"new_sign_pub", "new_enc_pub", "nonce", "signature"}} {
		var body map[string]any
		if err := json.Unmarshal(app.raw[i], &body); err != nil {
			t.Fatal(err)
		}
		for _, f := range fields {
			s, _ := body[f].(string)
			raw, err := base64.StdEncoding.Strict().DecodeString(s)
			if s == "" || err != nil || base64.StdEncoding.EncodeToString(raw) != s || strings.ContainsAny(s, "-_") {
				t.Errorf("%s = %q is not canonical padded standard base64: %v", f, s, err)
			}
		}
	}
}

func TestSigningKey_RefusesAnExpiredOrRetiredKey(t *testing.T) {
	now := time.Now()
	live, _ := GenerateKey(now.Add(-time.Hour), 30*24*time.Hour)
	expired, _ := GenerateKey(now.Add(-31*24*time.Hour), 30*24*time.Hour)
	retired := live
	retired.RetiredAt = &now

	if k, err := (&Identity{Keys: []AgentKey{live}}).SigningKey(now); err != nil || k.KID() != live.KID() {
		t.Errorf("a live key: %v", err)
	}
	for name, id := range map[string]*Identity{
		"expired":                {Keys: []AgentKey{expired}},
		"expired exactly now":    {Keys: []AgentKey{{SignPriv: live.SignPriv, EncPriv: live.EncPriv, CreatedAt: live.CreatedAt, NotAfter: now}}},
		"retired":                {Keys: []AgentKey{retired}},
		"current expired, older": {Keys: []AgentKey{expired, live}},
	} {
		_, err := id.SigningKey(now)
		if !errors.Is(err, ErrKeyExpired) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), BinaryName+" enroll") {
			t.Errorf("%s: the error does not say how to enroll again: %v", name, err)
		}
	}
}

func TestSignedRequestsWithAnExpiredKeySendNothing(t *testing.T) {
	app := newFakeApp(t)
	now := time.Now()
	expired, _ := GenerateKey(now.Add(-31*24*time.Hour), 30*24*time.Hour)
	app.key = expired
	id := &Identity{AgentID: "nag_1", NetworkID: "net_1", AppURL: app.srv.URL, Keys: []AgentKey{expired}, KeyValidity: 30 * 24 * time.Hour}

	if err := RotateKeys(t.Context(), http.DefaultClient, id, t.TempDir(), now); !errors.Is(err, ErrKeyExpired) || len(id.Keys) != 1 {
		t.Errorf("rotation with an expired key: %v", err)
	}
	if _, err := ConfigHints(t.Context(), http.DefaultClient, id, now); !errors.Is(err, ErrKeyExpired) {
		t.Errorf("hints with an expired key: %v", err)
	}
	if len(app.tokens) != 0 {
		t.Errorf("%d requests were signed with an expired key", len(app.tokens))
	}
}

func TestRotateKeys_KeepsTheNewKeyWhenTheIdentityCannotBeSaved(t *testing.T) {
	app := newFakeApp(t)
	now := time.Now()
	old, _ := GenerateKey(now.Add(-21*24*time.Hour), 30*24*time.Hour)
	app.key = old
	id := &Identity{AgentID: "nag_1", NetworkID: "net_1", AppURL: app.srv.URL, Keys: []AgentKey{old}, KeyValidity: 30 * 24 * time.Hour}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := RotateKeys(t.Context(), http.DefaultClient, id, filepath.Join(blocker, "state"), now)
	if err == nil {
		t.Fatal("the save into a path under a file succeeded")
	}
	if id.Current().KID() == old.KID() || id.Keys[1].RetiredAt == nil {
		t.Error("the gateway rotated the key, so the identity must hold the new one even if it could not be saved")
	}
}

func TestAgentSwitchesToTheNewKeyEvenWhenSavingItFails(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	app := newFakeApp(t)
	var state string
	r := newRig(t, erpTarget(up.URL, ""), func(o *AgentOptions) {
		state = o.StateDir
		id, err := LoadIdentity(state)
		if err != nil {
			t.Fatal(err)
		}
		id.AppURL = app.srv.URL
		if err := id.Save(state); err != nil {
			t.Fatal(err)
		}
	})
	app.mu.Lock()
	app.key = r.key
	app.mu.Unlock()
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0o700) })
	if f, err := os.CreateTemp(state, ".probe-*"); err == nil {
		_ = f.Close()
		t.Skip("the state directory stays writable here, so a failed save cannot be provoked")
	}

	r.a.RotateNow()
	r.waitFor("the rotation", func() bool { return r.out.has("rotated") != nil })
	if r.out.has("identity_save_failed") == nil {
		t.Error("the failed save was not reported")
	}
	if kid := r.out.has("rotated")["kid"]; kid == r.key.KID() || kid == nil {
		t.Errorf("the agent keeps signing with the key the gateway rotated out: %v", kid)
	}
}

func TestAgentStopsWhenItsKeyIsPastExpiry(t *testing.T) {
	var offset atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()
	r := newRig(t, erpTarget(up.URL, ""), func(o *AgentOptions) {
		o.Now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	})
	offset.Store(int64(40 * 24 * time.Hour))
	_ = r.conn.ws.Close()
	select {
	case err := <-r.done:
		if !errors.Is(err, ErrKeyExpired) || !strings.Contains(err.Error(), BinaryName+" enroll") {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent kept reconnecting with an expired key")
	}
}
