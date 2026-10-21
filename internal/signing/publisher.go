package signing

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
)

// PublisherVersion is what a publisher signs for one version of a workflow:
// the version's own content and where it lives, never the listing text.
type PublisherVersion struct {
	Handle      string
	Slug        string
	Version     string
	ContentHash string
	// Manifest is the decoded manifest document of the version.
	Manifest any
}

// PublisherPayload is the canonical JSON the gateway verifies a publisher's
// signature against.
func PublisherPayload(v PublisherVersion) ([]byte, error) {
	return CanonicalJSON(map[string]any{
		"content_hash": v.ContentHash,
		"handle":       v.Handle,
		"manifest":     v.Manifest,
		"slug":         v.Slug,
		"version":      v.Version,
	})
}

// SignPublisherVersion signs the version with the publisher's key; the
// signature is standard base64, as the gateway reads it.
func SignPublisherVersion(key ed25519.PrivateKey, v PublisherVersion) (string, error) {
	payload, err := PublisherPayload(v)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload)), nil
}

// PublisherPublicKey is the raw public key in unpadded base64url, the form the
// gateway registers.
func PublisherPublicKey(key ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

// MarshalPublisherKey encodes a private key as a PKCS#8 PEM document.
func MarshalPublisherKey(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParsePublisherKey reads an Ed25519 private key from a PKCS#8 PEM document.
func ParsePublisherKey(raw []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PKCS#8 private key in PEM form")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("read the private key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("the private key is not an Ed25519 key")
	}
	return key, nil
}
