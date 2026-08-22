// resume_test.go — resolvePrimary's pin contract against httptest doubles
// (the -s/--session boot flag's backend leg AND the session.json pin share
// this seam):
//
//	(a) override HIT: GET /session/{pin} 200 → the pinned session wins,
//	    the resume note names the pin, /session list+create are untouched;
//	(b) override MISS: a pin the server does not have (404) degrades to
//	    the normal ensurePrimary find-or-create with a GENERALIZED status
//	    note that reads correctly for an explicit pin (never a silent
//	    substitution, never a hard boot failure);
//	(c) no override → straight ensurePrimary (unchanged default).
package backend

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/config"
)

// resolveFixture boots a bare liveBackend (no Start — resolvePrimary is
// called directly) against srv with the event tap attached.
func resolveFixture(t *testing.T, srv *httptest.Server) (*liveBackend, *eventLog) {
	t.Helper()
	b := newLiveBackend(srv.URL, t.TempDir(), config.Default())
	b.mu.Lock()
	b.baseURL = srv.URL // Start normally does this before resolvePrimary
	b.mu.Unlock()
	log := &eventLog{}
	b.fl.setEmit(log.emit)
	return b, log
}

// (a) pin hit: the pinned id returns, exactly one resume note, no
// find-or-create chatter behind it.
func TestResolvePrimaryOverrideHit(t *testing.T) {
	var listed, created atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses-pin":
			w.Write([]byte(`{"id":"ses-pin","title":"theboringoffice office","time":{"created":1,"updated":1}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			listed.Store(true)
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			created.Store(true)
			w.Write([]byte(`{"id":"ses-created","time":{"created":2,"updated":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b, log := resolveFixture(t, srv)
	b.PrimaryOverride("ses-pin")

	s, err := b.resolvePrimary()
	if err != nil {
		t.Fatalf("resolvePrimary: %v", err)
	}
	if s.ID != "ses-pin" {
		t.Fatalf("the pin must win the boss-session choice, got %q", s.ID)
	}
	if listed.Load() || created.Load() {
		t.Fatalf("a pin hit must never list/create (listed=%v created=%v)", listed.Load(), created.Load())
	}
	if n := log.textCount("primary session: resume ses-pin (pinned)"); n != 1 {
		t.Fatalf("the resume note must name the pin once, got %d", n)
	}
}

// (b) pin miss: the server has no ses-gone → ensurePrimary's reuse path
// still picks the newest root session, and the degrade note reads
// correctly for an explicit pin.
func TestResolvePrimaryOverrideMiss(t *testing.T) {
	var listed, created atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			listed.Store(true)
			w.Write([]byte(`[{"id":"ses-newest","time":{"created":2,"updated":2}}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses-newest/message":
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			created.Store(true)
			w.Write([]byte(`{"id":"ses-created","time":{"created":3,"updated":3}}`))
		default:
			w.WriteHeader(http.StatusNotFound) // ses-gone lands here
		}
	}))
	t.Cleanup(srv.Close)
	b, log := resolveFixture(t, srv)
	b.PrimaryOverride("ses-gone")

	s, err := b.resolvePrimary()
	if err != nil {
		t.Fatalf("resolvePrimary must degrade open, got err %v", err)
	}
	if s.ID != "ses-newest" {
		t.Fatalf("the miss must fall through to ensurePrimary's reuse, got %q", s.ID)
	}
	if !listed.Load() {
		t.Fatalf("the miss path must run the find-or-create list")
	}
	if created.Load() {
		t.Fatalf("a fresh root session exists — the reuse path must not create")
	}
	if n := log.textCount("pinned session ses-gone not found server-side — starting normal find-or-create instead"); n != 1 {
		t.Fatalf("the generalized degrade note must fire once, got %d", n)
	}
}

// (c) no override: untouched behavior — straight ensurePrimary.
func TestResolvePrimaryNoOverride(t *testing.T) {
	var pinFetched atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && len(r.URL.Path) > len("/session/"):
			pinFetched.Store(true)
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Write([]byte(`{"id":"ses-created","time":{"created":1,"updated":1}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	b, log := resolveFixture(t, srv)

	s, err := b.resolvePrimary()
	if err != nil {
		t.Fatalf("resolvePrimary: %v", err)
	}
	if s.ID != "ses-created" {
		t.Fatalf("empty list must create a fresh primary, got %q", s.ID)
	}
	if pinFetched.Load() {
		t.Fatalf("no override means NO pin fetch")
	}
	if n := log.textCount("pinned session"); n != 0 {
		t.Fatalf("no override means no pin notes, got %d", n)
	}
}
