package tunnel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func chunked(body []byte, size int) [][]byte {
	var out [][]byte
	for i := 0; i < len(body); i += size {
		out = append(out, body[i:min(i+size, len(body))])
	}
	return out
}

// countingUpstream is a target that records every request that reaches it.
func countingUpstream(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int32
	var bodyBytes atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		bodyBytes.Add(n)
		_, _ = io.WriteString(w, "stored")
	}))
	t.Cleanup(up.Close)
	return up, &hits, &bodyBytes
}

// A body is handed to the service only once the gateway's signature over the
// whole stream has verified: a write must never be half done because a stream
// was cut or forged.
func TestAgentSendsNoBodyUpstreamBeforeTheStreamIsVerified(t *testing.T) {
	up, hits, bodyBytes := countingUpstream(t)
	r := newRig(t, erpTarget(up.URL, ""), nil)
	big := bytes.Repeat([]byte("z"), 3<<20) // above the 1 MiB an earlier version streamed

	for _, method := range []string{"POST", "PUT", "PATCH"} {
		req := r.seal(RequestHeader{Target: "erp", Method: method, Path: "/invoices"}, chunked(big, 128<<10)...)
		if res := r.conn.run(req, exchangeOpts{tamperFinal: true}); res.Err == nil || res.Err.Code != "decrypt_failed" || res.Err.Dispatched {
			t.Errorf("%s with a tampered final chunk: %+v", method, res.Err)
		}
	}
	r.gw.badEndSig = true
	req := r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices"}, chunked(big, 128<<10)...)
	r.gw.badEndSig = false
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "signature_invalid" || res.Err.Dispatched {
		t.Errorf("a final signature from the wrong key: %+v", res.Err)
	}
	if hits.Load() != 0 || bodyBytes.Load() != 0 {
		t.Errorf("the service saw %d request(s) and %d body bytes of streams that never verified", hits.Load(), bodyBytes.Load())
	}
}

func TestAgentSendsNothingUpstreamWhenTheStreamIsCutBeforeItsEnd(t *testing.T) {
	up, hits, bodyBytes := countingUpstream(t)
	r := newRig(t, erpTarget(up.URL, ""), nil)
	big := bytes.Repeat([]byte("z"), 3<<20)
	req := r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices"}, chunked(big, 128<<10)...)
	id := r.conn.start(req, exchangeOpts{dropFinal: true})
	// Long enough for an executor that streams to have contacted the service by now.
	time.Sleep(400 * time.Millisecond)
	if hits.Load() != 0 || bodyBytes.Load() != 0 {
		t.Errorf("the service saw %d request(s) and %d body bytes before the stream ended", hits.Load(), bodyBytes.Load())
	}
	r.conn.send(Frame{Type: FrameCancel, StreamID: id, Payload: []byte(`{"reason":"cut"}`)})
	r.waitFor("the cut exchange to finish", func() bool { return r.a.inflight.Load() == 0 })
	if hits.Load() != 0 || bodyBytes.Load() != 0 {
		t.Errorf("the service saw %d request(s) and %d body bytes of a stream that was cut", hits.Load(), bodyBytes.Load())
	}
}

func TestAgentSendsAVerifiedBodyWhole(t *testing.T) {
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "stored")
	}))
	defer up.Close()
	r := newRig(t, erpTarget(up.URL, ""), nil)
	body := bytes.Repeat([]byte("0123456789abcdef"), httpBodyLimit/16) // exactly the limit
	req := r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices"}, chunked(body, 64<<10)...)
	if res := r.open(req, r.conn.run(req, exchangeOpts{})); string(res.Body) != "stored" || !bytes.Equal(got, body) {
		t.Errorf("response %q, the service got %d of %d bytes", res.Body, len(got), len(body))
	}
}

func TestAgentRefusesABodyOverTheLimitWithoutContactingTheService(t *testing.T) {
	up, hits, bodyBytes := countingUpstream(t)
	r := newRig(t, erpTarget(up.URL, ""), nil)
	body := bytes.Repeat([]byte("x"), httpBodyLimit+1)
	for _, method := range []string{"POST", "PUT", "PATCH", "GET"} {
		req := r.seal(RequestHeader{Target: "erp", Method: method, Path: "/invoices"}, chunked(body, 128<<10)...)
		res := r.conn.run(req, exchangeOpts{})
		if res.Err == nil || res.Err.Code != "body_too_large" || res.Err.Dispatched {
			t.Errorf("%s: %+v", method, res.Err)
		}
	}
	if hits.Load() != 0 || bodyBytes.Load() != 0 {
		t.Errorf("the service saw %d request(s) and %d body bytes of an oversized body", hits.Load(), bodyBytes.Load())
	}
}

func TestAgentChecksTheBodyDigestTheGatewaySigned(t *testing.T) {
	up, hits, _ := countingUpstream(t)
	r := newRig(t, erpTarget(up.URL, "")+"  - name: crm\n    kind: mcp_http\n    url: "+up.URL+"/mcp\n    allow_loopback: true\n    mcp: { tools: [\"crm_*\"] }\n", nil)
	body := []byte(`{"amount":42}`)
	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	wrong := strings.Repeat("0", 64)

	req := r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices", BodySHA256: good}, body)
	if res := r.open(req, r.conn.run(req, exchangeOpts{})); string(res.Body) != "stored" {
		t.Errorf("a body that matches its digest: %q", res.Body)
	}
	req = r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices", BodySHA256: strings.ToUpper(good)}, body)
	if res := r.open(req, r.conn.run(req, exchangeOpts{})); string(res.Body) != "stored" {
		t.Errorf("an upper-case digest of the same body: %q", res.Body)
	}
	served := hits.Load()

	req = r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices", BodySHA256: wrong}, body)
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "signature_invalid" || res.Err.Dispatched {
		t.Errorf("an HTTP body with a digest of another body: %+v", res.Err)
	}
	other := sha256.Sum256([]byte("another message"))
	mcp := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"crm_find"}}`)
	req = r.seal(RequestHeader{Target: "crm", Kind: KindMCPHTTP, Method: "POST", Path: "/", BodySHA256: hex.EncodeToString(other[:])}, mcp)
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "signature_invalid" || res.Err.Dispatched {
		t.Errorf("an MCP message with a digest of another message: %+v", res.Err)
	}
	if hits.Load() != served {
		t.Error("a body that did not match its digest reached the service")
	}
}

func TestAgentEnforcesTheResponseLimitTheGatewaySigned(t *testing.T) {
	big := bytes.Repeat([]byte("r"), 5000)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "chunked" {
			fl := w.(http.Flusher)
			for i := 0; i < 5; i++ {
				_, _ = w.Write(big[:1000])
				fl.Flush()
			}
			return
		}
		w.Header().Set("Content-Length", "5000")
		_, _ = w.Write(big)
	}))
	defer up.Close()
	r := newRig(t, erpTarget(up.URL, ""), nil)

	// A length the service announces above the limit is refused before any of the response is sent.
	req := r.seal(RequestHeader{Target: "erp", Method: "GET", Path: "/big", MaxResponseBytes: 1000})
	res := r.conn.run(req, exchangeOpts{})
	if res.Err == nil || res.Err.Code != "response_too_large" || res.Header != nil || len(res.Chunks) != 0 {
		t.Errorf("an announced length over the limit: err %+v, header sent %v, %d chunks", res.Err, res.Header != nil, len(res.Chunks))
	}
	// A length the service does not announce is cut when the limit is passed.
	req = r.seal(RequestHeader{Target: "erp", Method: "GET", Path: "/big", Query: "mode=chunked", MaxResponseBytes: 2500})
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "response_too_large" {
		t.Errorf("a response that grew past the limit: %+v", res.Err)
	}
	// Within the limit it passes.
	req = r.seal(RequestHeader{Target: "erp", Method: "GET", Path: "/big", MaxResponseBytes: 5000})
	if got := r.open(req, r.conn.run(req, exchangeOpts{})); len(got.Body) != 5000 {
		t.Errorf("a response at the limit: %d bytes", len(got.Body))
	}
	// A request that sets no limit is not served: the gateway always names one.
	req = r.seal(RequestHeader{Target: "erp", Method: "GET", Path: "/big", MaxResponseBytes: -1})
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "decrypt_failed" || res.Err.Dispatched {
		t.Errorf("no limit: %+v", res.Err)
	}
}

func TestAgentRefusesARequestIssuedBeforeItStarted(t *testing.T) {
	up, hits, _ := countingUpstream(t)
	r := newRig(t, erpTarget(up.URL, ""), nil)
	// Inside the freshness window, but older than this process: a copy from before a restart looks like this.
	req := r.seal(RequestHeader{Target: "erp", Method: "POST", Path: "/invoices", IssuedAtMs: r.a.started.Add(-30 * time.Second).UnixMilli()}, []byte("x"))
	if res := r.conn.run(req, exchangeOpts{}); res.Err == nil || res.Err.Code != "expired" || res.Err.Dispatched {
		t.Errorf("issued before the start: %+v", res.Err)
	}
	if hits.Load() != 0 {
		t.Error("the service saw a request issued before the agent started")
	}
}
