package signing

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Headers of a signed JSON response body.
const (
	HeaderContentSignature = "X-Content-Signature"
	HeaderContentKeyID     = "X-Content-Signature-Key-Id"
	// ExitContentSignature is the process exit status of a signed document that
	// does not verify.
	ExitContentSignature = 4
)

// ContentError is a signed document that failed verification: nothing from
// it was used.
type ContentError struct{ Reason string }

func (e *ContentError) Error() string { return "signature check failed: " + e.Reason }

// ExitCode is the process exit status for the failure.
func (*ContentError) ExitCode() int { return ExitContentSignature }

// PolicyKeysPath is where a gateway publishes the keys that sign firewall
// sessions and snapshots, a ring separate from the download-signing keys.
const PolicyKeysPath = "/.well-known/zuano-policy-keys"

// VerifyPurpose is VerifyContent for a document that carries the purpose it
// was signed for: the purpose is inside the signed body, and a document made
// for another use is refused.
func VerifyPurpose(keys []Key, h http.Header, body []byte, purpose string) error {
	if err := VerifyContent(keys, h, body); err != nil {
		return err
	}
	var doc struct {
		Purpose string `json:"purpose"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.Purpose != purpose {
		return &ContentError{Reason: fmt.Sprintf("the document is not signed for %s", purpose)}
	}
	return nil
}

// VerifyContent checks the signature a gateway puts on a JSON response: an
// ed25519 signature over the canonical JSON of the body, by a pinned key. An
// unsigned body is refused, since the caller is about to act on it.
func VerifyContent(keys []Key, h http.Header, body []byte) error {
	sig := strings.TrimSpace(h.Get(HeaderContentSignature))
	kid := strings.TrimSpace(h.Get(HeaderContentKeyID))
	if sig == "" || kid == "" {
		return &ContentError{Reason: "the response is not signed"}
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return &ContentError{Reason: "the signature is not base64"}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return &ContentError{Reason: "the signed body is not JSON"}
	}
	msg, err := CanonicalJSON(doc)
	if err != nil {
		return &ContentError{Reason: fmt.Sprintf("the signed body cannot be canonicalised: %v", err)}
	}
	if err := VerifyMessage(keys, kid, msg, raw); err != nil {
		return &ContentError{Reason: err.Error()}
	}
	return nil
}
