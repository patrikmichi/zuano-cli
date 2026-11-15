package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type sealVectors struct {
	Request struct {
		Gateway struct {
			KID    string `json:"kid"`
			Public []byte `json:"public"`
		} `json:"gateway"`
		Agents []struct {
			KID      string `json:"kid"`
			SignSeed []byte `json:"sign_seed"`
			EncPriv  []byte `json:"enc_priv"`
		} `json:"agents"`
		IssuedAtMs  int64    `json:"issued_at_ms"`
		Open        []byte   `json:"open"`
		Chunks      [][]byte `json:"chunks"`
		End         []byte   `json:"end"`
		PlainChunks []string `json:"plain_chunks"`
		Header      struct {
			RequestID string `json:"request_id"`
			Target    string `json:"target"`
			Method    string `json:"method"`
			Path      string `json:"path"`
			Query     string `json:"query"`
		} `json:"header"`
	} `json:"request"`
}

func loadSealVectors(t *testing.T) sealVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "seal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v sealVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The request in the vector was sealed by the gateway; every agent key it was
// sealed to must open it.
func TestGatewaySealedRequestVector(t *testing.T) {
	v := loadSealVectors(t).Request
	gw := GatewayKeys{v.Gateway.KID: ed25519.PublicKey(v.Gateway.Public)}
	for _, a := range v.Agents {
		key := AgentKey{SignPriv: ed25519.NewKeyFromSeed(a.SignSeed), EncPriv: a.EncPriv}
		if key.KID() != a.KID {
			t.Fatalf("key id %s, vector says %s", key.KID(), a.KID)
		}
		var env OpenEnvelope
		if err := json.Unmarshal(v.Open, &env); err != nil {
			t.Fatal(err)
		}
		req, err := OpenRequest(env, []AgentKey{key}, gw)
		if err != nil {
			t.Fatalf("agent %s: %v", a.KID, err)
		}
		h := req.Header
		if h.RequestID != v.Header.RequestID || h.Target != v.Header.Target || h.Method != v.Header.Method || h.Path != v.Header.Path || h.Query != v.Header.Query || h.IssuedAtMs != v.IssuedAtMs {
			t.Errorf("agent %s: header %+v", a.KID, h)
		}
		for i, c := range v.Chunks {
			got, err := req.Chunk(c)
			if err != nil || string(got) != v.PlainChunks[i] {
				t.Fatalf("agent %s chunk %d: %q, %v", a.KID, i, got, err)
			}
		}
		if err := req.Final(v.End); err != nil {
			t.Errorf("agent %s: %v", a.KID, err)
		}
	}
}

// ZUANO_GATEWAY_PROTOCOL names the gateway's lib/networks/protocol directory;
// the vendored vectors must equal its copies.
func TestVendoredVectorsEqualTheGatewaysCopies(t *testing.T) {
	dir := os.Getenv("ZUANO_GATEWAY_PROTOCOL")
	if dir == "" {
		t.Skip("ZUANO_GATEWAY_PROTOCOL is not set")
	}
	for local, remote := range map[string]string{"frames.json": "frames.vectors.json", "seal.json": "seal.vectors.json"} {
		want, err := os.ReadFile(filepath.Join(dir, remote))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("testdata", local))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("testdata/%s differs from the gateway's %s; run make gen-network-vectors", local, remote)
		}
	}
}
