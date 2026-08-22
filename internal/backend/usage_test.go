// usage_test.go — mapUsage / message.updated usage mapping contract,
// server-free: real AssistantMessage .tokens/.cost counters (required on
// the wire since opencode serve 1.18.19, verified against GET /doc) become
// per-message EvUsage DELTAS; zero frames, repeats, user messages and
// foreign sessions emit nothing.
package backend

import (
	"encoding/json"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

func msgUpdated(t *testing.T, body string) ocSSEEvent {
	t.Helper()
	return ocSSEEvent{Type: "message.updated", Properties: json.RawMessage(body)}
}

func TestMessageUpdatedEmitsUsageDeltas(t *testing.T) {
	ctx := newNormCtx(nil)
	primary := "ses-primary"

	// First frame, counters still zero: nothing to report.
	evs := mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-a","sessionID":"ses-primary","role":"assistant","time":{"created":1},"cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}}`),
		ctx, primary, 100)
	if len(evs) != 0 {
		t.Fatalf("zero counters must emit nothing, got %v", evs)
	}

	// Growth frame: ONE EvUsage carrying the full delta. TokensIn = input;
	// TokensOut = output + reasoning; cache figures stay out of the
	// headline counts.
	evs = mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-a","sessionID":"ses-primary","role":"assistant","time":{"created":1},"cost":0.125,"tokens":{"total":140,"input":100,"output":30,"reasoning":10,"cache":{"read":500,"write":0}}}}`),
		ctx, primary, 100)
	if len(evs) != 1 || evs[0].Kind != state.EvUsage {
		t.Fatalf("usage frame must emit ONE EvUsage, got %v", evs)
	}
	u := evs[0]
	if u.CallID != "msg-a" || u.TokensIn != 100 || u.TokensOut != 40 || u.CostUSD != 0.125 {
		t.Fatalf("usage delta mismatch: %+v", u)
	}

	// An unchanged re-report of the SAME message emits nothing — repeated
	// message.updated frames can never double-count.
	if evs := mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-a","sessionID":"ses-primary","role":"assistant","time":{"created":1},"cost":0.125,"tokens":{"total":140,"input":100,"output":30,"reasoning":10,"cache":{"read":500,"write":0}}}}`),
		ctx, primary, 100); len(evs) != 0 {
		t.Fatalf("unchanged re-report must emit nothing, got %v", evs)
	}

	// The completion frame adds only its growth.
	evs = mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-a","sessionID":"ses-primary","role":"assistant","finish":"stop","time":{"created":1,"completed":2},"cost":0.5,"tokens":{"total":300,"input":200,"output":85,"reasoning":15,"cache":{"read":0,"write":0}}}}`),
		ctx, primary, 100)
	if len(evs) != 1 || evs[0].Kind != state.EvUsage {
		t.Fatalf("completion frame must emit ONE EvUsage, got %v", evs)
	}
	if u := evs[0]; u.TokensIn != 100 || u.TokensOut != 60 || u.CostUSD != 0.375 {
		t.Fatalf("completion must ship only the growth (+%d in / +%d out / +$%v), got %+v",
			100, 60, 0.375, u)
	}

	// Prompt-cache counters ride ALONGSIDE the headline: the write-off
	// frame lands verbatim, and a later frame whose ONLY growth is a cache
	// read still emits (cache.read growing means the provider hit cache).
	evs = mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-c","sessionID":"ses-primary","role":"assistant","time":{"created":1},"cost":0.01,"tokens":{"input":500,"output":10,"reasoning":0,"cache":{"read":0,"write":4096}}}}`),
		ctx, primary, 100)
	if len(evs) != 1 || evs[0].Kind != state.EvUsage {
		t.Fatalf("cache-write frame must emit ONE EvUsage, got %v", evs)
	}
	if u := evs[0]; u.CallID != "msg-c" || u.TokensIn != 500 || u.TokensOut != 10 || u.CostUSD != 0.01 ||
		u.TokensCacheRead != 0 || u.TokensCacheWrite != 4096 {
		t.Fatalf("cache-write delta mismatch: %+v", u)
	}
	evs = mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-c","sessionID":"ses-primary","role":"assistant","time":{"created":1},"cost":0.01,"tokens":{"input":500,"output":10,"reasoning":0,"cache":{"read":50000,"write":4096}}}}`),
		ctx, primary, 100)
	if len(evs) != 1 || evs[0].Kind != state.EvUsage {
		t.Fatalf("cache-read-only growth must still emit ONE EvUsage, got %v", evs)
	}
	if u := evs[0]; u.TokensIn != 0 || u.TokensOut != 0 || u.CostUSD != 0 ||
		u.TokensCacheRead != 50000 || u.TokensCacheWrite != 0 {
		t.Fatalf("cache-read-only delta: headline tokens must stay 0, got %+v", u)
	}

	// User messages never carry usage.
	if evs := mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-u","sessionID":"ses-primary","role":"user","time":{"created":1}}}`),
		ctx, primary, 100); len(evs) != 0 {
		t.Fatalf("user message.updated must emit nothing, got %v", evs)
	}

	// A foreign session (not primary, not a hired child or the concierge)
	// never leaks into the member's tally.
	if evs := mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-x","sessionID":"ses-foreign","role":"assistant","time":{"created":1},"cost":9.99,"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}`),
		ctx, primary, 100); len(evs) != 0 {
		t.Fatalf("foreign session usage must be ignored, got %v", evs)
	}
}

func TestChildAssistantUsageRidesWorkingPulse(t *testing.T) {
	ctx := newNormCtx(nil)
	primary := "ses-primary"
	hired := mapOCEvent(ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
		`{"info":{"id":"ses-kid","parentID":"ses-primary","title":"write the file (developer)","time":{"created":1,"updated":1}}}`)},
		ctx, primary, 100)
	if len(hired) != 2 {
		t.Fatalf("child must hire+dispatch first, got %v", hired)
	}
	// The child's assistant message.updated carries its counters: the
	// EvUsage delta prepends the throttled working pulse the frame already
	// emitted (now clears the 500ms throttle window).
	evs := mapOCEvent(msgUpdated(t,
		`{"info":{"id":"msg-k","sessionID":"ses-kid","role":"assistant","time":{"created":1},"cost":0.25,"tokens":{"input":900,"output":140,"reasoning":0,"cache":{"read":0,"write":0}}}}`),
		ctx, primary, 1000)
	var usage, working bool
	for _, e := range evs {
		switch e.Kind {
		case state.EvUsage:
			if e.CallID != "msg-k" || e.TokensIn != 900 || e.TokensOut != 140 || e.CostUSD != 0.25 {
				t.Fatalf("child usage delta mismatch: %+v", e)
			}
			usage = true
		case state.EvWorking:
			working = true
		}
	}
	if !usage || !working {
		t.Fatalf("child frame must carry usage AND the working pulse, got %v", evs)
	}
}
