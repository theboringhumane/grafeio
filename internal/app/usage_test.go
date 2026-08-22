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
