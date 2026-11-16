package webhooksign

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type gatewayVector struct {
	Secret            string `json:"secret"`
	PreviousSecret    string `json:"previousSecret"`
	ID                string `json:"id"`
	Timestamp         int64  `json:"timestamp"`
	Body              string `json:"body"`
	Signature         string `json:"signature"`
	PreviousSignature string `json:"previousSignature"`
}

// The gateway's own signing vector: ZUANO_CONTRACT_FIXTURES names its fixture
// directory, and the vendored copy is used otherwise.
func loadGatewayVector(t *testing.T) gatewayVector {
	t.Helper()
	path := filepath.Join("testdata", "vectors.json")
	if dir := os.Getenv("ZUANO_CONTRACT_FIXTURES"); dir != "" {
		path = filepath.Join(dir, "webhooks", "vectors.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v gatewayVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func delivery(v gatewayVector, timestamp, signature string) http.Header {
	h := http.Header{}
	h.Set(HeaderID, v.ID)
	h.Set(HeaderTimestamp, timestamp)
	h.Set(HeaderSignature, signature)
	return h
}

func TestSigningMatchesTheGatewaysVector(t *testing.T) {
	v := loadGatewayVector(t)
	at := time.Unix(v.Timestamp, 0)
	for name, c := range map[string]struct{ secret, want string }{
		"current secret":  {v.Secret, v.Signature},
		"previous secret": {v.PreviousSecret, v.PreviousSignature},
	} {
		h, err := Sign(c.secret, v.ID, at, []byte(v.Body))
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Get(HeaderSignature); got != c.want {
			t.Errorf("%s: signature %s, the gateway's is %s", name, got, c.want)
		}
		// The gateway's signature verifies with the secret that made it, and with no other.
		headers := delivery(v, h.Get(HeaderTimestamp), c.want)
		if err := Verify(c.secret, headers, []byte(v.Body), DefaultTolerance, at); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	headers := delivery(v, strconv.FormatInt(v.Timestamp, 10), v.Signature)
	if err := Verify(v.PreviousSecret, headers, []byte(v.Body), DefaultTolerance, at); err == nil {
		t.Error("the previous secret verified the current signature")
	}
	// During a rotation the gateway sends both signatures; either secret accepts the delivery.
	headers.Set(HeaderSignature, v.Signature+" "+v.PreviousSignature)
	for _, secret := range []string{v.Secret, v.PreviousSecret} {
		if err := Verify(secret, headers, []byte(v.Body), DefaultTolerance, at); err != nil {
			t.Errorf("a rotation delivery with %s: %v", secret[:10], err)
		}
	}
}
