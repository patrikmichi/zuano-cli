package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The vector is written by the gateway's own signing code
// (scripts/publisher-signing-vector.ts), so a drift in the canonical form or
// the signed fields fails here.
type publisherVector struct {
	SeedHex     string         `json:"seed_hex"`
	PublicKey   string         `json:"public_key"`
	Handle      string         `json:"handle"`
	Slug        string         `json:"slug"`
	Version     string         `json:"version"`
	ContentHash string         `json:"content_hash"`
	Manifest    map[string]any `json:"manifest"`
	Payload     string         `json:"payload"`
	Signature   string         `json:"signature"`
}

func loadPublisherVector(t *testing.T) (publisherVector, ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile("testdata/publisher-signature.json")
	if err != nil {
		t.Fatal(err)
	}
	var v publisherVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(v.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	return v, ed25519.NewKeyFromSeed(seed)
}

func (v publisherVector) version() PublisherVersion {
	return PublisherVersion{Handle: v.Handle, Slug: v.Slug, Version: v.Version, ContentHash: v.ContentHash, Manifest: v.Manifest}
}

func TestPublisherPayloadMatchesTheGatewaysBytes(t *testing.T) {
	v, _ := loadPublisherVector(t)
	got, err := PublisherPayload(v.version())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != v.Payload {
		t.Fatalf("payload differs from the gateway's:\n got %s\nwant %s", got, v.Payload)
	}
}

func TestSignPublisherVersionReproducesTheGatewaysSignature(t *testing.T) {
	v, key := loadPublisherVector(t)
	got, err := SignPublisherVersion(key, v.version())
	if err != nil {
		t.Fatal(err)
	}
	if got != v.Signature {
		t.Fatalf("signature = %s, want %s", got, v.Signature)
	}
	if pub := PublisherPublicKey(key); pub != v.PublicKey {
		t.Fatalf("public key = %s, want %s", pub, v.PublicKey)
	}
}

func TestGatewaySignatureVerifiesWithTheRegisteredKey(t *testing.T) {
	v, _ := loadPublisherVector(t)
	pub, err := base64.RawURLEncoding.DecodeString(v.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(v.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, []byte(v.Payload), sig) {
		t.Fatal("the gateway's signature does not verify against its payload")
	}
}

func TestPublisherKeyRoundTripsThroughPEM(t *testing.T) {
	_, key := loadPublisherVector(t)
	pem, err := MarshalPublisherKey(key)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePublisherKey(pem)
	if err != nil || !key.Equal(back) {
		t.Fatalf("round trip = %v, %v", back, err)
	}
	for name, raw := range map[string]string{"empty": "", "wrong block": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"} {
		if _, err := ParsePublisherKey([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
