package tunnel

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestStreamEndsAtTheFinalChunk(t *testing.T) {
	gw := newTestGateway(t)
	agent, _ := GenerateKey(time.Now(), time.Hour)
	open := func() (*Request, *sealedRequest) {
		r := gw.seal(RequestHeader{Target: "erp"}, [][]byte{[]byte("one"), []byte("two")}, agent)
		q, err := OpenRequest(r.env, []AgentKey{agent}, gw.keys)
		if err != nil {
			t.Fatal(err)
		}
		return q, r
	}

	q, r := open()
	for _, c := range r.chunks {
		if _, err := q.Chunk(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Final(r.final); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Chunk(r.lateData); !errors.Is(err, ErrStreamEnded) {
		t.Errorf("data after the final chunk: %v", err)
	}
	if err := q.Final(r.lateFinal); !errors.Is(err, ErrStreamEnded) {
		t.Errorf("a second final chunk: %v", err)
	}

	// A final chunk that does not verify still ends the stream.
	q, r = open()
	for _, c := range r.chunks {
		_, _ = q.Chunk(c)
	}
	q.gateway = newTestGateway(t).keys[gw.kid]
	if err := q.Final(r.final); !errors.Is(err, ErrSignature) {
		t.Fatalf("a final chunk that was not signed by the gateway: %v", err)
	}
	if _, err := q.Chunk(r.lateData); !errors.Is(err, ErrStreamEnded) {
		t.Errorf("data after a failed final chunk: %v", err)
	}

	// A chunk that fails to open ends the stream: the rest of it is not read on.
	q, r = open()
	bad := append([]byte(nil), r.chunks[0]...)
	bad[0] ^= 1
	if _, err := q.Chunk(bad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("a flipped chunk: %v", err)
	}
	if _, err := q.Chunk(r.chunks[0]); !errors.Is(err, ErrStreamEnded) {
		t.Errorf("a chunk after a failed one: %v", err)
	}
	if err := q.Final(r.final); !errors.Is(err, ErrStreamEnded) {
		t.Errorf("the final chunk after a failed one: %v", err)
	}
}

func TestGuardIsSafeForConcurrentUse(t *testing.T) {
	now := time.Now()
	g := NewGuard("org_1", "net_1")
	const workers, each = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := g.Check(baseHeader(RequestHeader{Target: "erp"}), now); err != nil {
					t.Errorf("a fresh request was refused: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	// One request presented by many callers at once passes exactly once.
	h := baseHeader(RequestHeader{Target: "erp"})
	var accepted, replayed atomic.Int32
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := g.Check(h, now); {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrReplay):
				replayed.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 || replayed.Load() != workers-1 {
		t.Errorf("accepted %d, replayed %d", accepted.Load(), replayed.Load())
	}
}

func TestGuardRefusesWhatWasIssuedBeforeTheAgentStarted(t *testing.T) {
	started := time.Now()
	g := NewGuard("org_1", "net_1")
	g.SetStart(started)
	issuedAt := func(at time.Time) RequestHeader {
		return baseHeader(RequestHeader{Target: "erp", IssuedAtMs: at.UnixMilli()})
	}
	// A request inside the freshness window but older than this process could be a copy from before a restart.
	if err := g.Check(issuedAt(started.Add(-30*time.Second)), started.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("issued 30 s before the start: %v", err)
	}
	if err := g.Check(issuedAt(started.Add(-restartSkew-time.Second)), started.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("issued just outside the skew: %v", err)
	}
	if err := g.Check(issuedAt(started.Add(-restartSkew/2)), started.Add(time.Second)); err != nil {
		t.Errorf("issued within the skew of the start: %v", err)
	}
	if err := g.Check(issuedAt(started.Add(time.Second)), started.Add(2*time.Second)); err != nil {
		t.Errorf("issued after the start: %v", err)
	}
	// The gateway's clock is read through the offset the broker reported.
	g = NewGuard("org_1", "net_1")
	g.SetStart(started)
	g.SetOffset(10 * time.Minute)
	gatewayNow := started.Add(time.Second).Add(10 * time.Minute)
	if err := g.Check(issuedAt(gatewayNow), started.Add(time.Second)); err != nil {
		t.Errorf("a request issued after the start by a gateway whose clock is ahead: %v", err)
	}
	if err := g.Check(issuedAt(started.Add(10*time.Minute-30*time.Second)), started.Add(time.Second)); !errors.Is(err, ErrExpired) {
		t.Errorf("the same before-start request seen through the offset: %v", err)
	}
	// Without a start there is no floor, as in a guard built for a single check.
	if err := NewGuard("org_1", "net_1").Check(issuedAt(started.Add(-30*time.Second)), started); err != nil {
		t.Errorf("no start: %v", err)
	}
}
