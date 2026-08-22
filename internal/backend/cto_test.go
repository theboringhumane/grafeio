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
//	    until the next dispatch.
package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/config"
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
