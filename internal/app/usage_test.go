// usage_test.go — EvUsage reducer contract: per-message deltas accumulate
// into the conversation's real usage totals; zero deltas are inert.
package app

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

func TestReducerUsageAccumulates(t *testing.T) {
	// Exact-binary costs keep float equality meaningful (0.125 + 0.375 = 0.5).
	st := reducer(state.OfficeState{}, state.Event{
		Kind: state.EvUsage, CallID: "msg-a", TokensIn: 100, TokensOut: 40, CostUSD: 0.125,
	})
	if st.TokensIn != 100 || st.TokensOut != 40 || st.CostUSD != 0.125 {
		t.Fatalf("first usage delta mismatch: %+v", st)
	}
	// The same message growing again adds only its growth (the backend
	// ships deltas); a second message id rides the same += path.
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-a", TokensIn: 20, TokensOut: 9, CostUSD: 0.375})
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-b", TokensIn: 880, TokensOut: 120, CostUSD: 0})
	if st.TokensIn != 1000 || st.TokensOut != 169 || st.CostUSD != 0.5 {
		t.Fatalf("usage totals must accumulate, got in=%d out=%d cost=%v",
			st.TokensIn, st.TokensOut, st.CostUSD)
	}
	// A zero delta (an unchanged re-report swallowed upstream) is inert.
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-b"})
	if st.TokensIn != 1000 || st.TokensOut != 169 || st.CostUSD != 0.5 {
		t.Fatalf("zero delta must be inert, got in=%d out=%d cost=%v",
			st.TokensIn, st.TokensOut, st.CostUSD)
	}
}

func TestReducerUsageAccumulatesCacheTotals(t *testing.T) {
	// Prompt-cache counters ride the same per-message delta += path and
	// NEVER touch the headline totals (the $ figure already prices them).
	st := reducer(state.OfficeState{}, state.Event{
		Kind: state.EvUsage, CallID: "msg-a",
		TokensIn: 100, TokensOut: 40, CostUSD: 0.125, TokensCacheWrite: 4096,
	})
	if st.TokensCacheWrite != 4096 || st.TokensCacheRead != 0 {
		t.Fatalf("first cache delta mismatch: %+v", st)
	}
	if st.TokensIn != 100 || st.TokensOut != 40 || st.CostUSD != 0.125 {
		t.Fatalf("headline totals must ignore cache figures, got in=%d out=%d cost=%v",
			st.TokensIn, st.TokensOut, st.CostUSD)
	}
	// A cache-read-only delta (headline all zero) still lands.
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-a", TokensCacheRead: 50_000})
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-b", TokensIn: 10, TokensCacheRead: 1_000, TokensCacheWrite: 500})
	if st.TokensCacheRead != 51_000 || st.TokensCacheWrite != 4_596 {
		t.Fatalf("cache totals must accumulate, got read=%d write=%d",
			st.TokensCacheRead, st.TokensCacheWrite)
	}
	// The zero-delta path leaves the cache counters inert too.
	st = reducer(st, state.Event{Kind: state.EvUsage, CallID: "msg-b"})
	if st.TokensCacheRead != 51_000 || st.TokensCacheWrite != 4_596 {
		t.Fatalf("zero delta must be inert for cache, got read=%d write=%d",
			st.TokensCacheRead, st.TokensCacheWrite)
	}
}
