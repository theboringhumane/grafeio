// Package netwatch — pure-stdlib internet connectivity watching for the
// theboringoffice office. When connectivity drops the office enters OFFLINE mode:
// the live backend parks its network loops (no reconnect churn) and the
// floor shows an "[offline]" badge; when connectivity returns the office
// resumes on its own. NO third-party reachability libraries, NO cgo — the
// probe below is net+net/http only.
package netwatch

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	// PollInterval — the steady cadence between probe rounds.
	PollInterval = 5 * time.Second
	// MissesToOffline — consecutive failed rounds before the watcher
	// declares OFFLINE (the flap guard: one transient packet loss must not
	// flip the office). ONLINE is declared on the FIRST successful round.
	MissesToOffline = 2
)

// publicDialer bounds every probe dial at 2s; probeHTTPClient bounds the
// captive-portal GET at 3s.
var publicDialer = &net.Dialer{Timeout: 2 * time.Second}
var probeHTTPClient = &http.Client{Timeout: 3 * time.Second}

// ProbeFunc answers "is the internet reachable right now" for one round.
// A nil probe (New) is replaced with PublicProbe.
type ProbeFunc func(ctx context.Context) bool

// dialTCP reports whether addr answers a TCP open within the dialer's 2s
// bound (and the caller's ctx). No payload is needed: SYN/ACK through NAT
// and captive middleboxes is enough to count as reachable.
func dialTCP(ctx context.Context, addr string) bool {
	c, err := publicDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// http204 reports whether url answers GET with ANY 2xx/3xx within the 3s
// client bound (and the caller's ctx). The status is the verdict — bodies
// are never read.
func http204(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	res, err := probeHTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode >= 200 && res.StatusCode < 400
}

// PublicProbe is the production probe: online when ANY of — a 2s TCP dial
// to 8.8.8.8:53, a 2s TCP dial to 1.1.1.1:53, or an HTTP GET of
// http://clients3.google.com/generate_204 (Chrome's captive-portal check)
// answering 2xx/3xx — succeeds. The dials pass NAT-only paths; the GET
// passes HTTP-only middleboxes that would refuse an open socket.
func PublicProbe(ctx context.Context) bool {
	if dialTCP(ctx, "8.8.8.8:53") {
		return true
	}
	if dialTCP(ctx, "1.1.1.1:53") {
		return true
	}
	return http204(ctx, "http://clients3.google.com/generate_204")
}

// Watcher polls connectivity on a fixed cadence and emits ONLY on
// transitions. OFFLINE is adopted after MissesToOffline consecutive failed
// rounds (the flap guard); ONLINE is adopted on the first successful round.
// The first emit carries the initial state, fired as soon as the first
// round is CONFIRMED — one success, or MissesToOffline misses.
type Watcher struct {
	probe  ProbeFunc
	interv time.Duration

	mu     sync.Mutex
	online bool // the ADOPTED state — meaningful only once known
	known  bool // first round confirmed
	misses int  // consecutive failed rounds toward the flap guard
}

// New builds a Watcher. A nil probe becomes PublicProbe; a non-positive
// interv becomes PollInterval (tests inject a scripted probe and a tiny
// interval, and can also drive step directly — no real sleeps needed).
func New(probe ProbeFunc, interv time.Duration) *Watcher {
	if probe == nil {
		probe = PublicProbe
	}
	if interv <= 0 {
		interv = PollInterval
	}
	return &Watcher{probe: probe, interv: interv}
}

// Current reports the last ADOPTED state: true while online — and also
// before the first round confirms, since unknown degrades open to the
// office's steady state (boots are assumed connected until refuted).
func (w *Watcher) Current() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.known || w.online
}

// Start runs probe rounds until ctx dies, emitting adopted transitions as
// they confirm. emit runs synchronously on the Start goroutine, NEVER
// under the watcher lock.
func (w *Watcher) Start(ctx context.Context, emit func(online bool)) {
	for {
		if online, fresh := w.step(ctx); fresh && emit != nil {
			emit(online)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.interv):
		}
	}
}

// step runs ONE probe round against the transition state machine
// (recordLocked). fresh is true when the round ADOPTED a state that has
// not been emitted yet — the caller's cue to emit it.
func (w *Watcher) step(ctx context.Context) (online bool, fresh bool) {
	ok := w.probe(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.recordLocked(ok)
}

// recordLocked folds one probe answer into the state machine. A success
// adopts online IMMEDIATELY (fresh only the first time, or on the
// offline→online flip); a fail counts toward the flap guard and adopts
// offline only at MissesToOffline. Re-confirming an adopted state is
// never fresh. Caller holds w.mu.
func (w *Watcher) recordLocked(ok bool) (bool, bool) {
	if ok {
		w.misses = 0
		if !w.known || !w.online {
			w.known, w.online = true, true
			return true, true
		}
		return true, false
	}
	if w.online {
		// online so far: count the miss; only lose the state at the guard
		w.misses++
		if w.misses >= MissesToOffline {
			w.online = false
			return false, true
		}
		return true, false
	}
	if !w.known {
		w.misses++
		if w.misses >= MissesToOffline {
			w.known = true // boots offline: confirmed, adopted
			return false, true
		}
	}
	// already offline (or guarding): re-confirming emits nothing
	return false, false
}
