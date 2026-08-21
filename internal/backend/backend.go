// Package backend — the two state.Backend implementations for Grafeio:
// a scripted demo (demo.go) and the live opencode+agentmemory backend
// (opencode.go, events.go, agentmemory.go). Ports of node-legacy/src/backend/*.
package backend

import (
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/state"
)

// NewLive creates the live backend (port of createLiveBackend).
// baseURL empty -> env OPENCODE_SERVER -> spawn `opencode serve --port 0`.
func NewLive(baseURL, directory string) state.Backend {
	return newLiveBackend(baseURL, directory)
}

// NewDemo creates the scripted demo backend (port of createDemoBackend).
func NewDemo() state.Backend {
	return newDemoBackend()
}

// flow — shared lifecycle plumbing for both backends: the stopped flag, the
// emit callback, tracked timers (setTimeout) and ticker goroutines
// (setInterval). All members are guarded by mu; user callbacks run unlocked.
type flow struct {
	mu      sync.Mutex
	stopped bool
	closed  bool
	timers  map[*time.Timer]struct{}
	done    chan struct{}
	emitRef func(state.Event)
}

func newFlow() *flow {
	return &flow{timers: make(map[*time.Timer]struct{}), done: make(chan struct{})}
}

func (f *flow) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *flow) setEmit(fn func(state.Event)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emitRef = fn
}

// emit no-ops after stop(). The callback runs WITHOUT the lock so the
// receiver (e.g. tea.Program.Send) can never deadlock the backend.
func (f *flow) emit(e state.Event) {
	f.mu.Lock()
	stopped, emit := f.stopped, f.emitRef
	f.mu.Unlock()
	if !stopped && emit != nil {
		emit(e)
	}
}

// at is a tracked setTimeout: fires once unless stopped first.
func (f *flow) at(d time.Duration, fn func()) {
	f.mu.Lock()
	var t *time.Timer
	t = time.AfterFunc(d, func() {
		f.mu.Lock()
		delete(f.timers, t)
		stopped := f.stopped
		f.mu.Unlock()
		if !stopped {
			fn()
		}
	})
	f.timers[t] = struct{}{}
	f.mu.Unlock()
}

// every is a tracked setInterval: ticks until stop() closes done.
func (f *flow) every(d time.Duration, fn func()) {
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-t.C:
				if f.isStopped() {
					return
				}
				fn()
			}
		}
	}()
}

// stop is idempotent: kills pending timers, stops tickers, seals emit.
func (f *flow) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.stopped = true
	for t := range f.timers {
		t.Stop()
	}
	f.timers = make(map[*time.Timer]struct{})
	close(f.done)
}

// nowMs is the Date.now() of the TS codebase.
func nowMs() int64 { return time.Now().UnixMilli() }
