// offline_test.go — the OFFLINE-mode contract, end to end: netwatch
// transitions park the pump at the connectivity gate (ONE EvOffline pair
// per outage, zero stream attempts while parked), recovery re-opens the
// gate with a fresh SSE ladder and an IMMEDIATE re-attach (EvOnline pair),
// and Stop unwinds both the waiter and the watcher goroutine. The demo
// backend speaks the same pair through its watcher and the SetOffline
// simulation hook.
package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/netwatch"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// scriptedProbe is a manually-flipped netwatch probe: the rounds a test
// scripts, instant and hermetic.
type scriptedProbe struct {
	mu     sync.Mutex
	online bool
}

func (p *scriptedProbe) probe(context.Context) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.online
}

func (p *scriptedProbe) set(online bool) {
	p.mu.Lock()
	p.online = online
	p.mu.Unlock()
}

// eventLog captures a backend's emit stream with a tiny wait helper.
type eventLog struct {
	mu  sync.Mutex
	evs []state.Event
}

func (l *eventLog) emit(e state.Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *eventLog) count(k state.EventKind) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.evs {
		if e.Kind == k {
			n++
		}
	}
	return n
}

func (l *eventLog) textCount(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.evs {
		if strings.Contains(e.Text, sub) {
			n++
		}
	}
	return n
}

func (l *eventLog) kinds() []state.EventKind {
	l.mu.Lock()
	defer l.mu.Unlock()
	ks := make([]state.EventKind, len(l.evs))
	for i, e := range l.evs {
		ks[i] = e.Kind
	}
	return ks
}

// waitFor polls pred until it holds or the deadline runs out (no goroutine
// choreography, no flake).
func (l *eventLog) waitFor(t *testing.T, d time.Duration, pred func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (log: %v)", what, l.kinds())
}

// TestOfflineGateWaitOnline drives the gate directly (netGateFlip, the
// same plumbing the watcher callback uses): the boot gate passes at once,
// the offline flip parks a waiter, the online flip wakes it with the
// bumped generation, and Stop unwinds a parked waiter.
func TestOfflineGateWaitOnline(t *testing.T) {
	b := newLiveBackend("", t.TempDir(), config.Default())

	// Boot gate is OPEN: an immediate pass with generation 0.
	gen, up := b.waitOnline()
	if !up || gen != 0 {
		t.Fatalf("boot gate: got (%d,%v), want (0,true)", gen, up)
	}
	// A redundant online flip at boot (the watcher's first confirm) must be
	// idempotent: no panic on the already-open gate, generation still moves.
	_ = b.netGateFlip(true)

	// Offline flip: the waiter parks.
	_ = b.netGateFlip(false)
	res := make(chan int, 1) // gen at wake, or -1 when unwound by Stop
	go func() {
		g, ok := b.waitOnline()
		if !ok {
			g = -1
		}
		res <- g
	}()
	select {
	case <-res:
		t.Fatal("waitOnline must park while the gate is shut")
	case <-time.After(50 * time.Millisecond):
	}

	// Online flip: the parked waiter wakes immediately, gen re-read fresh.
	_ = b.netGateFlip(true)
	select {
	case g := <-res:
		if g < 2 {
			t.Fatalf("gen at wake: got %d, want >= 2 (two flips behind it)", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitOnline did not wake on the online flip")
	}

	// Stop unwinds a parked waiter via fl.done (no leak).
	_ = b.netGateFlip(false)
	res2 := make(chan int, 1)
	go func() {
		g, ok := b.waitOnline()
		if !ok {
			g = -1
		}
		res2 <- g
	}()
	select {
	case <-res2:
		t.Fatal("waiter left the park before any flip/Stop")
	case <-time.After(50 * time.Millisecond):
	}
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case g := <-res2:
		if g != -1 {
			t.Fatalf("stopped waiter: got gen %d, want the stop path", g)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not unwind the parked waiter")
	}
}

// TestOfflineGateParksAndResumes runs the REAL pump against a stub SSE
// server while a scripted probe drops and restores the internet: during
// the outage the reconnect churn freezes at exactly one attempt, exactly
// ONE EvOffline/status pair is emitted; on recovery the parked pump
// re-attaches at once (no ladder beat) behind one EvOnline/status pair.
func TestOfflineGateParksAndResumes(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/event") {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK) // empty body: streamOnce EOFs at once
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	probe := &scriptedProbe{online: true}
	b := newLiveBackend("", t.TempDir(), config.Default())
	b.mu.Lock()
	b.baseURL = srv.URL
	b.mu.Unlock()
	b.net = netwatch.New(probe.probe, 2*time.Millisecond)
	log := &eventLog{}
	b.fl.setEmit(log.emit)
	ctx, cancel := context.WithCancel(context.Background())
	b.netCancel = cancel
	go b.net.Start(ctx, b.onNetTransition)
	go b.pump()

	// Settle: the pump made its first stream attempt and the boot round
	// confirmed online (EvOnline + "back online" pair #1).
	log.waitFor(t, 2*time.Second, func() bool { return attempts.Load() >= 1 }, "first SSE attempt")
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOnline) >= 1 }, "boot online confirm")

	// ---- the internet drops: OFFLINE after the flap guard ----
	probe.set(false)
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOffline) == 1 }, "EvOffline")

	// The pump was mid-backoff when the outage was confirmed; let that tail
	// elapse so it reaches the parked select, then hold a stability window:
	// OFFLINE means the reconnect churn is FROZEN.
	time.Sleep(1200 * time.Millisecond)
	frozen := attempts.Load()
	time.Sleep(150 * time.Millisecond)
	if got := attempts.Load(); got != frozen {
		t.Fatalf("pump churned while OFFLINE: attempts moved %d -> %d", frozen, got)
	}
	// Anti-spam: a long outage is still exactly ONE offline pair.
	if n := log.count(state.EvOffline); n != 1 {
		t.Fatalf("EvOffline must fire once per outage, got %d", n)
	}
	if n := log.textCount("[theboringoffice] offline — office waiting for internet…"); n != 1 {
		t.Fatalf("offline status note must fire once, got %d", n)
	}

	// ---- the internet returns: re-attach WITHOUT waiting a ladder beat ----
	probe.set(true)
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOnline) >= 2 }, "recovery EvOnline")
	reattached := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if attempts.Load() > frozen {
			reattached = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !reattached {
		t.Fatalf("recovery must re-attach promptly (still %d attempts after 500ms)", attempts.Load())
	}
	if n := log.textCount("[theboringoffice] back online — resumed"); n != 2 {
		t.Fatalf("resume note: want boot+recovery = 2, got %d", n)
	}

	// Stop kills the watcher: a later probe flip emits nothing.
	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	probe.set(false)
	time.Sleep(50 * time.Millisecond)
	if n := log.count(state.EvOffline); n != 1 {
		t.Fatalf("event after Stop: EvOffline count %d, want 1", n)
	}
}

// TestDemoOfflineFlow drives the demo's manual simulation hook: SetOffline
// emits the same ONE-pair-per-flip contract (offline + waiting status,
// online + resumed status), repeats of the same state are silent, and the
// kind order is exact.
func TestDemoOfflineFlow(t *testing.T) {
	b := newDemoBackend(config.Default())
	log := &eventLog{}
	b.fl.setEmit(log.emit)

	b.SetOffline(true)
	ks := log.kinds()
	if len(ks) != 2 || ks[0] != state.EvOffline || ks[1] != state.EvStatus {
		t.Fatalf("offline flip must emit [EvOffline EvStatus], got %v", ks)
	}
	if n := log.textCount("[theboringoffice] offline — office waiting for internet…"); n != 1 {
		t.Fatalf("waiting note missing (%d)", n)
	}

	b.SetOffline(true) // silent repeat — no second pair
	if ks := log.kinds(); len(ks) != 2 {
		t.Fatalf("repeated SetOffline(true) must be silent, log now %v", ks)
	}

	b.SetOffline(false)
	ks = log.kinds()
	if len(ks) != 4 || ks[2] != state.EvOnline || ks[3] != state.EvStatus {
		t.Fatalf("online flip must emit [EvOnline EvStatus] after the pair, got %v", ks)
	}
	if n := log.textCount("[theboringoffice] back online — resumed"); n != 1 {
		t.Fatalf("resumed note missing (%d)", n)
	}
}

// TestDemoOfflineWatcherLifecycle: the demo's Start runs the connectivity
// watcher exactly like the live backend (scripted probe swapped in), and
// Stop kills its goroutine — post-Stop flips stay silent.
func TestDemoOfflineWatcherLifecycle(t *testing.T) {
	probe := &scriptedProbe{online: true}
	b := newDemoBackend(config.Default())
	b.net = netwatch.New(probe.probe, 2*time.Millisecond)
	log := &eventLog{}
	if err := b.Start(log.emit); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOnline) >= 1 }, "boot online confirm")

	probe.set(false)
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOffline) == 1 }, "demo EvOffline")
	probe.set(true)
	log.waitFor(t, 2*time.Second, func() bool { return log.count(state.EvOnline) >= 2 }, "demo recovery EvOnline")

	if err := b.Stop(); err != nil {
		t.Fatal(err)
	}
	probe.set(false)
	time.Sleep(50 * time.Millisecond)
	if n := log.count(state.EvOffline); n != 1 {
		t.Fatalf("watcher survived Stop: EvOffline count %d, want 1", n)
	}
}
