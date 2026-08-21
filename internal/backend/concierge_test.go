// concierge_test.go — the office concierge's mapping contract, server-free:
// child-of-concierge hires like a primary child, concierge text deltas ride
// EvChatOffice ("office-"+messageID) while the boss lane stays EvChatBoss
// (one lane per message), concierge reasoning stays suppressed, and an
// interrupted concierge stream flushes on the office lane too.
package backend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/theboringhumane/grafeio/internal/state"
)

func TestChildOfConciergeHiresLikePrimaryChild(t *testing.T) {
	ctx := newNormCtx(nil)
	primary := "ses-primary"
	ctx.registerConcierge("ses-conc")
	raw := ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
		`{"info":{"id":"ses-kid","parentID":"ses-conc","title":"write the file (developer)","time":{"created":1,"updated":1}}}`)}
	evs := mapOCEvent(raw, ctx, primary, 100)
	var hire, dispatch bool
	for _, e := range evs {
		if e.Kind == state.EvHire && e.Employee.ID == "ses-kid" {
			hire = true
		}
		if e.Kind == state.EvDispatch && e.EmployeeID == "ses-kid" {
			dispatch = true
		}
	}
	if !hire || !dispatch {
		t.Fatalf("child-of-concierge must hire+dispatch like a primary child, got %v", evs)
	}
	// A foreign root session (parentID "") still never hires.
	raw2 := ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
		`{"info":{"id":"ses-foreign","parentID":"","title":"stray","time":{"created":2,"updated":1}}}`)}
	if evs := mapOCEvent(raw2, ctx, primary, 100); len(evs) != 0 {
		t.Fatalf("foreign root session must not hire, got %v", evs)
	}
}

func TestConciergeTextStreamRidesOfficeLane(t *testing.T) {
	ctx := newNormCtx(nil)
	primary := "ses-primary"
	ctx.registerConcierge("ses-conc")
	if evs := mapTextPart(ocPart{
		ID: "prt-1", SessionID: "ses-conc", MessageID: "msg-1", Type: "text",
	}, ctx, 50); len(evs) != 0 {
		t.Fatalf("mapTextPart must emit nothing, got %v", evs)
	}
	evs := mapTextDelta(ocPartDelta{
		SessionID: "ses-conc", MessageID: "msg-1", PartID: "prt-1", Field: "text", Delta: "hello ",
	}, ctx)
	if len(evs) != 1 || evs[0].Kind != state.EvChatOffice {
		t.Fatalf("concierge text delta must emit ONE EvChatOffice, got %v", evs)
	}
	if evs[0].Msg.ID != "office-msg-1" || evs[0].Msg.From != "office" || evs[0].Msg.Kind != "office" || !evs[0].Msg.Pending {
		t.Fatalf("office bubble contract broken: %+v", evs[0].Msg)
	}
	evs = mapTextDelta(ocPartDelta{
		SessionID: "ses-conc", MessageID: "msg-1", PartID: "prt-1", Field: "text", Delta: "boss",
	}, ctx)
	if evs[0].Msg.Text != "hello boss" || evs[0].Msg.ID != "office-msg-1" {
		t.Fatalf("office bubble must accumulate in place, got %+v", evs[0].Msg)
	}

	// Same message on the PRIMARY stays on the boss lane — one lane per
	// message, prefix-disjoint identities.
	if evs := mapTextPart(ocPart{
		ID: "prt-2", SessionID: primary, MessageID: "msg-2", Type: "text",
	}, ctx, 60); len(evs) != 0 {
		t.Fatalf("mapTextPart (primary) must emit nothing, got %v", evs)
	}
	evs = mapTextDelta(ocPartDelta{
		SessionID: primary, MessageID: "msg-2", PartID: "prt-2", Field: "text", Delta: "counting...",
	}, ctx)
	if len(evs) != 1 || evs[0].Kind != state.EvChatBoss || evs[0].Msg.ID != "bossmsg-msg-2" {
		t.Fatalf("primary text delta must emit EvChatBoss bossmsg-, got %v", evs)
	}
}

func TestConciergeReasoningSuppressed(t *testing.T) {
	ctx := newNormCtx(nil)
	ctx.registerConcierge("ses-conc")
	// Both the classification and the deltas stay silent.
	if evs := mapReasoningPart(ocPart{
		ID: "prt-r", SessionID: "ses-conc", MessageID: "msg-9", Type: "reasoning", Text: "thinking",
	}, ctx, "ses-primary"); len(evs) != 0 {
		t.Fatalf("concierge reasoning part must be suppressed, got %v", evs)
	}
	if evs := mapReasoningDelta(ocPartDelta{
		SessionID: "ses-conc", MessageID: "msg-9", PartID: "prt-r", Field: "text", Delta: "more thought",
	}, ctx, "ses-primary"); len(evs) != 0 {
		t.Fatalf("concierge reasoning delta must be suppressed, got %v", evs)
	}
	// ...while a boss thought still emits exactly like before.
	evs := mapReasoningPart(ocPart{
		ID: "prt-b", SessionID: "ses-primary", MessageID: "msg-8", Type: "reasoning", Text: "boss mind",
	}, ctx, "ses-primary")
	if len(evs) != 1 || evs[0].Kind != state.EvThought || evs[0].EmployeeName != "boss" {
		t.Fatalf("boss reasoning must still emit EvThought, got %v", evs)
	}
}

func TestInterruptedConciergeStreamFlushesOfficeLane(t *testing.T) {
	ctx := newNormCtx(nil)
	ctx.registerConcierge("ses-conc")
	mapTextPart(ocPart{ID: "prt-1", SessionID: "ses-conc", MessageID: "msg-1", Type: "text"}, ctx, 50)
	mapTextDelta(ocPartDelta{SessionID: "ses-conc", MessageID: "msg-1", PartID: "prt-1", Field: "text", Delta: "half"}, ctx)
	mapTextPart(ocPart{ID: "prt-2", SessionID: "ses-primary", MessageID: "msg-2", Type: "text"}, ctx, 60)
	mapTextDelta(ocPartDelta{SessionID: "ses-primary", MessageID: "msg-2", PartID: "prt-2", Field: "text", Delta: "essay"}, ctx)

	evs := interruptedStreamEvents(ctx, "[grafeio] stream interrupted")
	if len(evs) != 2 {
		t.Fatalf("both open streams must flush, got %v", evs)
	}
	var office, boss *state.Event
	for i := range evs {
		switch evs[i].Kind {
		case state.EvChatOffice:
			office = &evs[i]
		case state.EvChatBoss:
			boss = &evs[i]
		}
	}
	if office == nil || boss == nil {
		t.Fatalf("need ONE office + ONE boss flush, got %v", evs)
	}
	if office.Msg.ID != "office-msg-1" || office.Msg.Pending || !strings.Contains(office.Msg.Text, "half") {
		t.Fatalf("office flush broke the lane contract: %+v", office.Msg)
	}
	if boss.Msg.ID != "bossmsg-msg-2" || boss.Msg.Pending || !strings.Contains(boss.Msg.Text, "essay") {
		t.Fatalf("boss flush broke the lane contract: %+v", boss.Msg)
	}
	if len(ctx.textSess) != 0 || len(ctx.textAccum) != 0 {
		t.Fatalf("stream state must be fully freed, got sess=%v accum=%v", ctx.textSess, ctx.textAccum)
	}
}

func TestDemoSendConciergeEmitsOnePinnedBubble(t *testing.T) {
	b := newDemoBackend(nil)
	var got []state.Event
	if err := b.Start(func(e state.Event) {
		if e.Kind == state.EvChatOffice {
			got = append(got, e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	if err := b.SendConcierge("what is 6x7 and other long text that should be capped at eighty runes maximum maximum maximum"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("demo concierge must emit exactly ONE office bubble, got %v", got)
	}
	m := got[0].Msg
	if m.Pending || m.From != "office" || m.Kind != "office" || !strings.HasPrefix(m.ID, "office-") {
		t.Fatalf("demo office bubble contract broken: %+v", m)
	}
	if !strings.Contains(m.Text, "(demo) concierge would handle this right away") {
		t.Fatalf("demo office bubble text unexpected: %q", m.Text)
	}
	if len([]rune(m.Text)) > len([]rune("office › (demo) concierge would handle this right away: "))+80 {
		t.Fatalf("demo office bubble must cap the echoed text at 80 runes: %q", m.Text)
	}
}
