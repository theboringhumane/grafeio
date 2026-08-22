// netwatch_test.go — the transition contract: a scripted probe drives
// step() directly (no goroutines, no clocks) for the full online/offline
// decision table, and Start over a tiny interval proves emit-on-transitions
// + ctx termination. The HTTP/TCP probe legs run against hermetic
// httptest/listener fixtures — real network is never touched.
package netwatch

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// seqProbe answers scripted rounds in order, then repeats its last answer.
type seqProbe struct {
	rounds []bool
	calls  atomic.Int64
}

func (p *seqProbe) probe(context.Context) bool {
	n := int(p.calls.Add(1))
	if n > len(p.rounds) {
		return p.rounds[len(p.rounds)-1]
	}
	return p.rounds[n-1]
}

// TestStepTransitions drives step() directly — the whole decision table:
// initial states, the 2-miss flap guard both ways (one stray miss covered
// by a success must NOT flip the office), and re-confirm silence.
func TestStepTransitions(t *testing.T) {
	probe := &seqProbe{rounds: []bool{
		false, // 1: first miss — unconfirmed, silent
		true,  // 2: initial confirm ONLINE
		false, // 3: miss 1 — guarded, silent
		true,  // 4: recovery inside the guard — nothing ever flipped, silent
		false, // 5: miss 1 — guarded
		false, // 6: miss 2 — OFFLINE adopted
		false, // 7: re-confirm offline — silent
		true,  // 8: first success — ONLINE adopted
		true,  // 9: re-confirm online — silent
	}}
	w := New(probe.probe, time.Millisecond)
	want := []struct {
		online bool
		fresh  bool
	}{
		{false, false}, // still unconfirmed: Current() degrades open
		{true, true},
		{true, false},
		{true, false},
		{true, false},
		{false, true},
		{false, false},
		{true, true},
		{true, false},
	}
	for i, wv := range want {
		online, fresh := w.step(context.Background())
		if online != wv.online || fresh != wv.fresh {
			t.Fatalf("round %d: got (%v,%v), want (%v,%v)", i+1, online, fresh, wv.online, wv.fresh)
		}
	}
	if !w.Current() {
		t.Fatalf("Current after confirmed online: got false")
	}
	// The probe ran once per round (nothing retried under the hood).
	if got := probe.calls.Load(); got != int64(len(want)) {
		t.Fatalf("probe calls: got %d, want %d", got, len(want))
	}
}

// TestCurrentDegradesOpen: before the first round confirms (one miss is
// not a confirmation), the state reads as the steady default ONLINE.
func TestCurrentDegradesOpen(t *testing.T) {
	probe := &seqProbe{rounds: []bool{false}}
	w := New(probe.probe, time.Millisecond)
	if !w.Current() {
		t.Fatalf("fresh watcher must read online, got offline")
	}
	w.step(context.Background()) // miss 1 of the guard — still unconfirmed
	if !w.Current() {
		t.Fatalf("one guarded miss must still read online, got offline")
	}
}

func recvOnline(t *testing.T, ch chan bool) bool {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never emitted")
		return false
	}
}

// TestStartEmitsOnlyTransitions: over a tiny interval, emits arrive
// EXACTLY for the three adoptions (boot online -> offline -> online) and
// nothing else across the scripted rounds.
func TestStartEmitsOnlyTransitions(t *testing.T) {
	probe := &seqProbe{rounds: []bool{
		true,  // boot confirm
		false, // guarded
		false, // OFFLINE
		false, // silent
		true,  // ONLINE
	}}
	w := New(probe.probe, 2*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emits := make(chan bool, 16)
	go w.Start(ctx, func(online bool) { emits <- online })

	if v := recvOnline(t, emits); v != true {
		t.Fatalf("first emit (initial confirm): got %v, want true", v)
	}
	if v := recvOnline(t, emits); v != false {
		t.Fatalf("second emit (offline): got %v, want false", v)
	}
	if v := recvOnline(t, emits); v != true {
		t.Fatalf("third emit (recovery): got %v, want true", v)
	}
	// Every further round re-confirms online — silent. Bounded quiet window.
	select {
	case v := <-emits:
		t.Fatalf("re-confirmed online must not emit, got %v", v)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestBootOfflineConfirmsAfterTwoMisses: a watcher born offline emits its
// initial state (false) once MissesToOffline rounds have failed — the
// office learns "no internet at boot" without a single successful round.
func TestBootOfflineConfirmsAfterTwoMisses(t *testing.T) {
	probe := &seqProbe{rounds: []bool{false}}
	w := New(probe.probe, 2*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emits := make(chan bool, 4)
	go w.Start(ctx, func(online bool) { emits <- online })
	if v := recvOnline(t, emits); v != false {
		t.Fatalf("boot-offline confirm: got %v, want false", v)
	}
	if w.Current() {
		t.Fatalf("confirmed offline must read Current()==false")
	}
}

// TestStartStopsWithContext: cancelling the ctx terminates the loop (the
// backend relies on this for goroutine-free Stop).
func TestStartStopsWithContext(t *testing.T) {
	probe := &seqProbe{rounds: []bool{true}}
	w := New(probe.probe, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Start(ctx, func(bool) {}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
}

// TestHTTP204Statuses: 2xx AND 3xx count as online (captive redirects are
// still a working path); 5xx and dead sockets are offline.
func TestHTTP204Statuses(t *testing.T) {
	okStatuses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer okStatuses.Close()
	if !http204(context.Background(), okStatuses.URL) {
		t.Fatalf("204 must count as online")
	}
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", okStatuses.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirected.Close()
	if !http204(context.Background(), redirected.URL) {
		t.Fatalf("302 must count as online")
	}
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if http204(context.Background(), broken.URL) {
		t.Fatalf("500 must count as offline")
	}
	if http204(context.Background(), "http://127.0.0.1:0/none") {
		t.Fatalf("unreachable host must count as offline")
	}
}

// TestDialTCP: open port answers, closed port does not (both hermetic).
func TestDialTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if !dialTCP(context.Background(), ln.Addr().String()) {
		t.Fatalf("listening socket must count as online")
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := closed.Addr().String()
	_ = closed.Close()
	if dialTCP(context.Background(), addr) {
		t.Fatalf("refused socket must count as offline")
	}
}
