// cto_test.go — the office CTO's contracts, end to end:
//
//	(a) the scripted tour: the CTO is hired in the opening cast, the
//	    architecture brief routes to him, and his return at 6.8s drains the
//	    board into exactly ONE review beat (EvMail notice);
//	(b) the drain latch, synchronously: one beat per drained batch, never a
//	    second without new dispatches;
//	(c) architecture routing: the ONE matcher sends arch/design/review
//	    titles to the CTO (live roleFromSession) while plain briefs don't;
//	(d) live wiring: child-return -> board drain -> one EvMail, latch-held
//	    until the next dispatch;
//	(e)-(h) the LIVE boot pseudo-CTO: seated in the exec suite at Start
//	    (demo parity), fired-ahead-of-hire when an architecture child swaps
//	    in, re-seated exactly once when the last real CTO leaves
//	    (deleteChild / session.deleted), never double-seated by two
//	    overlapping architecture children.
package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/netwatch"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// mailsFrom — every EvMail (kind EvMail only — a return's mail rides
// EvReturned's field, never this kind) sent by `from`.
func mailsFrom(log *eventLog, from string) []state.MailItem {
	log.mu.Lock()
	defer log.mu.Unlock()
	var out []state.MailItem
	for _, e := range log.evs {
		if e.Kind == state.EvMail && e.Mail.From == from {
			out = append(out, e.Mail)
		}
	}
	return out
}

// eventsMatching — the captured events where keep holds.
func eventsMatching(log *eventLog, keep func(state.Event) bool) []state.Event {
	log.mu.Lock()
	defer log.mu.Unlock()
	var out []state.Event
	for _, e := range log.evs {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// (a) The scripted touring day: CTO in the opening cast, the architecture
// brief on his desk, and exactly one review mail when the batch drains.
func TestDemoScriptedTourPostsOneCTOReview(t *testing.T) {
	b := newDemoBackend(nil)
	log := &eventLog{}
	if err := b.Start(log.emit); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	// Opening cast: the CTO hires at t0 with manager + hr.
	log.waitFor(t, 2*time.Second, func() bool {
		return len(eventsMatching(log, func(e state.Event) bool {
			return e.Kind == state.EvHire && e.Employee.ID == "theboringcto" &&
				e.Employee.Role == state.RoleCTO
		})) == 1
	}, "CTO hire in the opening cast")

	// The batch's architecture brief routes to him (t+1s).
	log.waitFor(t, 2*time.Second, func() bool {
		return len(eventsMatching(log, func(e state.Event) bool {
			return e.Kind == state.EvDispatch && e.EmployeeID == "theboringcto" &&
				e.Task.ID == "t4" && state.IsArchitectureBrief(e.Task.Title)
		})) == 1
	}, "architecture brief dispatched to the CTO")

	// t+6.8s: his return drains the board -> ONE review mail.
	log.waitFor(t, 12*time.Second, func() bool {
		return len(mailsFrom(log, "theboringcto")) == 1
	}, "CTO review mail after the drain")
	m := mailsFrom(log, "theboringcto")[0]
	if m.Kind != state.MailNotice || m.To != "manager" {
		t.Fatalf("review mail kind/to = %v/%q, want notice/manager", m.Kind, m.To)
	}
	if m.Subject != "reviewed: 4 tasks — architecture OK" {
		t.Fatalf("review subject = %q, want %q", m.Subject, "reviewed: 4 tasks — architecture OK")
	}
	if !strings.Contains(m.Body, "4 tasks") || !strings.Contains(m.Body, "Architecture OK") {
		t.Fatalf("review body off-brand: %q", m.Body)
	}
	// Past 6.8s the ambient loop runs: no second review, no spam.
	time.Sleep(2500 * time.Millisecond)
	if got := len(mailsFrom(log, "theboringcto")); got != 1 {
		t.Fatalf("review must post exactly once per drained batch, got %d", got)
	}
}

// (b) The drain latch, driven synchronously (no timers): one beat per
// drained batch, singular grammar, re-arm only on fresh dispatches.
func TestDemoDrainLatchNoSpam(t *testing.T) {
	b := newDemoBackend(nil)
	log := &eventLog{}
	b.fl.setEmit(log.emit)
	defer b.Stop()

	b.dispatch("t1", "Wire the SSE stream into the office reducer", "tekton-1")
	b.dispatch("t4", "Design the agentmemory board sync protocol", ctoName)
	b.doReturn("tekton-1", "t1", "return: SSE wiring", "done")
	if got := len(mailsFrom(log, ctoName)); got != 0 {
		t.Fatalf("t4 still open: no review yet, got %d", got)
	}
	b.doReturn(ctoName, "t4", "return: board sync protocol", "done")
	ms := mailsFrom(log, ctoName)
	if len(ms) != 1 || ms[0].Subject != "reviewed: 2 tasks — architecture OK" {
		t.Fatalf("drained batch must post exactly ONE review mail, got %+v", ms)
	}
	status := eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvStatus && strings.Contains(e.Text, "reviewed the drained batch")
	})
	if len(status) != 1 {
		t.Fatalf("the beat is status line + mail: want ONE status note, got %d", len(status))
	}

	// Repeated drains without new work: the latch holds, no second beat.
	b.doReturn("tekton-1", "t9", "return: ghost", "done") // synthesizes + closes a stray row
	if got := len(mailsFrom(log, ctoName)); got != 1 {
		t.Fatalf("no review without a new dispatch: got %d", got)
	}

	// A fresh dispatch re-arms: the next drain reviews again (new mail id).
	b.dispatch("t5", "Draft the demo smoke script", "tekton-2")
	b.doReturn("tekton-2", "t5", "return: smoke script", "done")
	ms = mailsFrom(log, ctoName)
	if len(ms) != 2 || ms[0].ID == ms[1].ID {
		t.Fatalf("second batch must review once more with a distinct mail id, got %+v", ms)
	}
}

// (b2) Singular batch: grammar stays clean ("1 task").
func TestCTOReviewSingular(t *testing.T) {
	b := newDemoBackend(nil)
	log := &eventLog{}
	b.fl.setEmit(log.emit)
	defer b.Stop()
	b.dispatch("solo", "Draft the demo smoke script", "tekton-1")
	b.doReturn("tekton-1", "solo", "return: solo", "done")
	ms := mailsFrom(log, ctoName)
	if len(ms) != 1 || ms[0].Subject != "reviewed: 1 task — architecture OK" {
		t.Fatalf("singular batch subject broken: %+v", ms)
	}
}

// (b3) The demo's dynamic path routes architecture-flavored ad-hoc asks to
// the CTO when he's on the roster (the scripted tour hired him at t0; this
// covers the seat-mapping side without timers).
func TestDemoAdHocArchitectureRoutesToCTO(t *testing.T) {
	b := newDemoBackend(nil)
	log := &eventLog{}
	if err := b.Start(log.emit); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	if err := b.SendWith("design the plugin registry for the floor", nil); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, 3*time.Second, func() bool {
		return len(eventsMatching(log, func(e state.Event) bool {
			return e.Kind == state.EvDispatch && e.EmployeeID == ctoName &&
				strings.HasPrefix(e.Task.ID, "adhoc-")
		})) == 1
	}, "ad-hoc architecture ask routed to the CTO")
	// Negative control: a plain ask stays on the dev rotation.
	if err := b.SendWith("wire the SSE stream", nil); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, 3*time.Second, func() bool {
		return len(eventsMatching(log, func(e state.Event) bool {
			return e.Kind == state.EvDispatch && strings.HasPrefix(e.Task.ID, "adhoc-2")
		})) == 1
	}, "second ad-hoc dispatch")
	plain := eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvDispatch && strings.HasPrefix(e.Task.ID, "adhoc-2")
	})[0]
	if plain.EmployeeID == ctoName {
		t.Fatalf("non-architecture ad-hoc must not route to the CTO, got %q", plain.EmployeeID)
	}
}

// (c) roleFromSession: architecture titles land on the CTO FIRST
// ("architect"/"design"/"review"), everything else keeps its old mapping.
func TestRoleFromSessionRoutesArchitectureToCTO(t *testing.T) {
	cases := []struct {
		title string
		hint  string
		want  state.EmployeeRole
	}{
		{"design the board sync protocol", "", state.RoleCTO},
		{"architect the next floor", "", state.RoleCTO},
		{"review the diff before merge", "", state.RoleCTO},
		{"Review the reducer", "explore", state.RoleCTO}, // architecture beats scout
		// non-architecture briefs keep their historic seats
		{"write the file (developer)", "", state.RoleDeveloper},
		{"explore the repo map", "", state.RoleScout},
		{"scout the build graph", "", state.RoleScout},
		{"run the migration", "runner", state.RoleRunner},
		{"stabilize the harness", "", state.RoleDeveloper},
	}
	for _, c := range cases {
		if got := roleFromSession(c.title, c.hint); got != c.want {
			t.Errorf("roleFromSession(%q,%q) = %q, want %q", c.title, c.hint, got, c.want)
		}
	}
}

// (d) Live wiring: a child session's dispatch re-arms the latch, its
// return drains the board into ONE CTO EvMail; idles and stray calls
// never re-post, and the next batch reviews again.
func TestLiveBoardDrainPostsOneCTOReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/diff"):
			w.Write([]byte(`[]`))
		case strings.Contains(r.URL.Path, "/message") && strings.Contains(r.URL.Path, "ses-kid2"):
			w.Write([]byte(`[{"info":{"id":"msg-2","sessionID":"ses-kid2","role":"assistant"},"parts":[{"type":"text","text":"wired the stream"}]}]`))
		case strings.Contains(r.URL.Path, "/message") && strings.Contains(r.URL.Path, "ses-kid"):
			w.Write([]byte(`[{"info":{"id":"msg-1","sessionID":"ses-kid","role":"assistant"},"parts":[{"type":"text","text":"designed the sync map"}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	b := newLiveBackend("", t.TempDir(), config.Default())
	b.mu.Lock()
	b.baseURL = srv.URL
	b.primaryID = "ses-primary"
	b.mu.Unlock()
	log := &eventLog{}
	b.fl.setEmit(log.emit)
	defer b.Stop()

	created := func(id, title string) ocSSEEvent {
		return ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
			`{"info":{"id":"` + id + `","parentID":"ses-primary","title":"` + title + `","time":{"created":1,"updated":1}}}`)}
	}
	idle := func(id string) ocSSEEvent {
		return ocSSEEvent{Type: "session.status", Properties: json.RawMessage(
			`{"sessionID":"` + id + `","status":{"type":"idle"}}`)}
	}

	// An architecture child hires AS the CTO (seat mapping prefers him) and
	// arms the review latch.
	if err := b.onEvent(created("ses-kid", "design the board sync")); err != nil {
		t.Fatal(err)
	}
	hired := eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvHire && e.Employee.ID == "ses-kid"
	})
	if len(hired) != 1 || hired[0].Employee.Role != state.RoleCTO || hired[0].Employee.Name != "theboringcto-1" {
		t.Fatalf("architecture child must hire as the CTO, got %+v", hired)
	}
	if got := len(mailsFrom(log, ctoName)); got != 0 {
		t.Fatalf("board open: no review yet, got %d", got)
	}

	// The return drains the one-brief batch -> exactly one review mail.
	if err := b.onEvent(idle("ses-kid")); err != nil {
		t.Fatal(err)
	}
	ms := mailsFrom(log, ctoName)
	if len(ms) != 1 || ms[0].Kind != state.MailNotice || ms[0].Subject != "reviewed: 1 task — architecture OK" {
		t.Fatalf("live drain must post ONE review mail, got %+v", ms)
	}

	// Dedupe + latch: repeated idles and stray return checks post nothing.
	if err := b.onEvent(idle("ses-kid")); err != nil {
		t.Fatal(err)
	}
	b.maybeChildReturned("ses-kid")
	if got := len(mailsFrom(log, ctoName)); got != 1 {
		t.Fatalf("no second review without a new dispatch, got %d", got)
	}

	// A non-architecture second child keeps its developer seat, re-arms the
	// latch; its drain posts the second batch's review (distinct mail id).
	if err := b.onEvent(created("ses-kid2", "write the file (developer)")); err != nil {
		t.Fatal(err)
	}
	hired2 := eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvHire && e.Employee.ID == "ses-kid2"
	})
	if len(hired2) != 1 || hired2[0].Employee.Role != state.RoleDeveloper {
		t.Fatalf("plain brief must keep the developer seat, got %+v", hired2)
	}
	if got := len(mailsFrom(log, ctoName)); got != 1 {
		t.Fatalf("mid-batch: still one review, got %d", got)
	}
	if err := b.onEvent(idle("ses-kid2")); err != nil {
		t.Fatal(err)
	}
	ms = mailsFrom(log, ctoName)
	if len(ms) != 2 || ms[0].ID == ms[1].ID {
		t.Fatalf("second batch must review once more (distinct mail), got %+v", ms)
	}
}

// ---------------------------------------------------------------- boot pseudo-CTO
// (e)–(h): the LIVE floor's idle pseudo-CTO contract — seated at boot
// (demo parity), swapped for the real session-backed CTO on the first
// architecture child, re-seated exactly once when the last real one
// leaves.

// startLiveForTest boots a REAL liveBackend (Start, not a field-pinned
// stub) against a minimal opencode serve double: an empty session list
// (ensurePrimary creates), one creatable primary, an EOF /event SSE (the
// pump ladders silently — no frames ever arrive), and 200 DELETEs for
// deleteChild. Hermetic: the agentmemory probe is pointed at an unroutable
// port (instant refusal) and the internet watcher gets a scripted
// always-online probe.
func startLiveForTest(t *testing.T) (*liveBackend, *eventLog) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session":
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Write([]byte(`{"id":"ses-primary","title":"theboringoffice office","time":{"created":1,"updated":1}}`))
		case strings.HasPrefix(r.URL.Path, "/event"):
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK) // empty body: streamOnce EOFs at once
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/session/"):
			w.Write([]byte(`true`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AGENTMEMORY_URL", "http://127.0.0.1:1") // refuse the lane probe instantly
	probe := &scriptedProbe{online: true}
	b := newLiveBackend(srv.URL, t.TempDir(), config.Default())
	b.net = netwatch.New(probe.probe, 2*time.Millisecond)
	log := &eventLog{}
	if err := b.Start(log.emit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop() }) // registered after srv.Close: runs first (LIFO)
	return b, log
}

// archCreated builds a session.created SSE frame for a primary child.
func archCreated(id, title string) ocSSEEvent {
	return ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
		`{"info":{"id":"` + id + `","parentID":"ses-primary","title":"` + title + `","time":{"created":1,"updated":1}}}`)}
}

// sessDeleted builds a session.deleted SSE frame.
func sessDeleted(id string) ocSSEEvent {
	return ocSSEEvent{Type: "session.deleted", Properties: json.RawMessage(
		`{"info":{"id":"` + id + `","time":{"created":1,"updated":1}}}`)}
}

// ctoWire — the pseudo-CTO's wire events, in emitted order (hires + fires
// of ctoName, hires + fires of the real session-keyed CTOs): the seat
// audit trail.
func ctoWire(log *eventLog, realIDs ...string) []state.Event {
	return eventsMatching(log, func(e state.Event) bool {
		switch e.Kind {
		case state.EvFire:
			if e.EmployeeID == ctoName {
				return true
			}
			for _, id := range realIDs {
				if e.EmployeeID == id {
					return true
				}
			}
		case state.EvHire:
			if e.Employee.ID == ctoName {
				return true
			}
			for _, id := range realIDs {
				if e.Employee.ID == id {
					return true
				}
			}
		}
		return false
	})
}

// wireKinds renders a ctoWire slice compactly for failure output and the
// verbose-proof log: e.g. "EvHire:theboringcto EvFire:theboringcto".
func wireKinds(evs []state.Event) string {
	var parts []string
	for _, e := range evs {
		switch e.Kind {
		case state.EvHire:
			parts = append(parts, "EvHire:"+e.Employee.ID)
		case state.EvFire:
			parts = append(parts, "EvFire:"+e.EmployeeID)
		default:
			parts = append(parts, string(e.Kind))
		}
	}
	return strings.Join(parts, " ")
}

// pseudoState snapshots the pseudo latch + roster facts under the backend
// mutex (the pump goroutine shares ctx, even if the stub never feeds it).
func pseudoState(b *liveBackend) (latched, inEmployees bool, liveCTOs int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, inEmployees = b.ctx.employees[ctoName]
	return b.ctx.pseudoCTO, inEmployees, b.ctx.liveCTOs()
}

// (e) Live Start hires exactly THREE fixed seats — manager, hr, then the
// idle pseudo-CTO at seat "cto" (demo parity) — and the pseudo is a floor
// ghost: latched, EvHire'd, but NEVER keyed into ctx.employees.
func TestLiveStartSeatsPseudoCTO(t *testing.T) {
	b, log := startLiveForTest(t)

	hires := eventsMatching(log, func(e state.Event) bool { return e.Kind == state.EvHire })
	var hireIDs []string
	for _, h := range hires {
		hireIDs = append(hireIDs, h.Employee.ID)
	}
	t.Logf("boot hires: %s", strings.Join(hireIDs, " "))
	if len(hires) != 3 || hireIDs[0] != "ses-primary" || hireIDs[1] != "hr" || hireIDs[2] != ctoName {
		t.Fatalf("boot hires must be exactly [manager hr theboringcto], got %v", hireIDs)
	}
	pseudo := hires[2].Employee
	if pseudo.Name != ctoName || pseudo.Role != state.RoleCTO || pseudo.Seat != "cto" || pseudo.Sprite != state.SpriteAtDesk {
		t.Fatalf("pseudo identity = %+v, want theboringcto/cto/at-desk", pseudo)
	}

	latched, inEmployees, liveCTOs := pseudoState(b)
	if !latched {
		t.Fatal("Start must latch the pseudo-CTO")
	}
	if inEmployees {
		t.Fatal("pseudo must NEVER be keyed into ctx.employees (session-id mappers would adopt him)")
	}
	if liveCTOs != 0 {
		t.Fatalf("no real CTO on a fresh boot, liveCTOs = %d", liveCTOs)
	}
}

// (f) An architecture-titled session.created FIRES the pseudo BEFORE
// hiring the real theboringcto-1 — in-order events, so the reducer frees
// seat "cto" ahead of the real hire's AssignSeat. Exactly once: the latch
// is dropped, so the swap never repeats while the real CTO stands.
func TestLiveArchitectureChildSwapsPseudoCTO(t *testing.T) {
	b, log := startLiveForTest(t)

	if err := b.onEvent(archCreated("ses-arch", "design the floor plan")); err != nil {
		t.Fatal(err)
	}

	wire := ctoWire(log, "ses-arch")
	t.Logf("cto wire: %s", wireKinds(wire))
	if len(wire) != 3 ||
		wire[0].Kind != state.EvHire || wire[0].Employee.ID != ctoName ||
		wire[1].Kind != state.EvFire || wire[1].EmployeeID != ctoName ||
		wire[2].Kind != state.EvHire || wire[2].Employee.ID != "ses-arch" {
		t.Fatalf("swap must read [hire pseudo -> fire pseudo -> hire real], got %s", wireKinds(wire))
	}
	real := wire[2].Employee
	if real.Role != state.RoleCTO || real.Name != "theboringcto-1" {
		t.Fatalf("the real CTO must hire as theboringcto-1, got %+v", real)
	}

	latched, inEmployees, liveCTOs := pseudoState(b)
	if latched {
		t.Fatal("the swap must drop the pseudo latch")
	}
	if inEmployees || liveCTOs != 1 {
		t.Fatalf("exactly one real CTO on the board: inEmployees=%v liveCTOs=%d", inEmployees, liveCTOs)
	}
}

// (g1) deleteChild (the 10s-tidy path) fires the real CTO and re-seats
// the pseudo exactly once — fire first, hire after.
func TestLiveDeleteChildReseatsPseudoCTO(t *testing.T) {
	b, log := startLiveForTest(t)
	if err := b.onEvent(archCreated("ses-arch", "design the retry ladder")); err != nil {
		t.Fatal(err)
	}

	b.deleteChild("ses-arch")

	wire := ctoWire(log, "ses-arch")
	t.Logf("cto wire: %s", wireKinds(wire))
	want := "EvHire:theboringcto EvFire:theboringcto EvHire:ses-arch EvFire:ses-arch EvHire:theboringcto"
	if got := wireKinds(wire); got != want {
		t.Fatalf("re-seat via deleteChild:\nwant %s\ngot  %s", want, got)
	}
	latched, _, liveCTOs := pseudoState(b)
	if !latched || liveCTOs != 0 {
		t.Fatalf("pseudo must be re-seated with zero live CTOs: latched=%v liveCTOs=%d", latched, liveCTOs)
	}

	// A duplicate delete is swallowed by the fired dedupe — no second re-seat.
	b.deleteChild("ses-arch")
	if got := len(eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvHire && e.Employee.ID == ctoName
	})); got != 2 {
		t.Fatalf("pseudo hires must total 2 (boot + one re-seat), got %d", got)
	}
}

// (g2) the SSE session.deleted path plays the same re-seat (mirror of
// deleteChild), and its own fired dedupe stays intact.
func TestLiveSessionDeletedReseatsPseudoCTO(t *testing.T) {
	b, log := startLiveForTest(t)
	if err := b.onEvent(archCreated("ses-arch", "architect the org chart")); err != nil {
		t.Fatal(err)
	}

	if err := b.onEvent(sessDeleted("ses-arch")); err != nil {
		t.Fatal(err)
	}
	wire := ctoWire(log, "ses-arch")
	t.Logf("cto wire: %s", wireKinds(wire))
	want := "EvHire:theboringcto EvFire:theboringcto EvHire:ses-arch EvFire:ses-arch EvHire:theboringcto"
	if got := wireKinds(wire); got != want {
		t.Fatalf("re-seat via session.deleted:\nwant %s\ngot  %s", want, got)
	}
	latched, _, liveCTOs := pseudoState(b)
	if !latched || liveCTOs != 0 {
		t.Fatalf("pseudo must be re-seated with zero live CTOs: latched=%v liveCTOs=%d", latched, liveCTOs)
	}

	// The frame's own dedupe: a repeated session.deleted emits NOTHING.
	before := len(log.kinds())
	if err := b.onEvent(sessDeleted("ses-arch")); err != nil {
		t.Fatal(err)
	}
	if got := len(log.kinds()); got != before {
		t.Fatalf("repeated session.deleted must be silent: %d events -> %d", before, got)
	}
}

// (h) Two overlapping architecture children never double-seat the pseudo:
// the first swap drops the latch, the SECOND child hires plain, the first
// removal re-seats nothing (one real CTO still stands), and only the
// final departure re-seats him — exactly once.
func TestLiveOverlappingCTOChildrenReseatOnce(t *testing.T) {
	b, log := startLiveForTest(t)

	if err := b.onEvent(archCreated("ses-a", "design the floor plan")); err != nil {
		t.Fatal(err)
	}
	if err := b.onEvent(archCreated("ses-b", "review the reducer")); err != nil {
		t.Fatal(err)
	}
	wire := ctoWire(log, "ses-a", "ses-b")
	t.Logf("cto wire after both hires: %s", wireKinds(wire))
	if got := wireKinds(wire); got != "EvHire:theboringcto EvFire:theboringcto EvHire:ses-a EvHire:ses-b" {
		t.Fatalf("second arch child must hire plain (no swap), got %s", got)
	}

	// First removal: ses-b still stands — the pseudo stays off the floor.
	b.deleteChild("ses-a")
	if got := wireKinds(ctoWire(log, "ses-a", "ses-b")); got != "EvHire:theboringcto EvFire:theboringcto EvHire:ses-a EvHire:ses-b EvFire:ses-a" {
		t.Fatalf("first removal must NOT re-seat (ses-b live), got %s", got)
	}

	// Final removal: the last real CTO is gone — re-seat exactly once.
	b.deleteChild("ses-b")
	wire = ctoWire(log, "ses-a", "ses-b")
	t.Logf("cto wire after both removals: %s", wireKinds(wire))
	want := "EvHire:theboringcto EvFire:theboringcto EvHire:ses-a EvHire:ses-b EvFire:ses-a EvFire:ses-b EvHire:theboringcto"
	if got := wireKinds(wire); got != want {
		t.Fatalf("final removal must re-seat exactly once:\nwant %s\ngot  %s", want, got)
	}
	latched, _, liveCTOs := pseudoState(b)
	if !latched || liveCTOs != 0 {
		t.Fatalf("after both removals: latched=%v liveCTOs=%d, want true/0", latched, liveCTOs)
	}
	if n := len(eventsMatching(log, func(e state.Event) bool {
		return e.Kind == state.EvHire && e.Employee.ID == ctoName
	})); n != 2 {
		t.Fatalf("pseudo hires must total exactly 2 (boot + final re-seat), got %d", n)
	}
}
