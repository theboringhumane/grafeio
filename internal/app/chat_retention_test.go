// chat_retention_test.go — the WhatsApp retention contract on the reducer:
// the transcript is the FULL history, nothing eaten as new messages arrive
// ("all in there even if its old as hell"). These proofs drive the real
// reducer paths: EvChatUser/EvChatBoss turns far past the OLD global cap
// (30) and EvThought/EvTool churn far past the OLD per-kind caps (20); every
// entry must survive, append-ordered oldest → newest (oldest at index 0,
// newest at the tail — the renderer's top → bottom).
package app

import (
	"fmt"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// TestChatRetentionKeepsFullHistory drives 120 user+boss turn pairs (240
// entries — 8× the old 30-entry cap) through the reducer and asserts every
// one is kept, in append order.
func TestChatRetentionKeepsFullHistory(t *testing.T) {
	st := initialState(state.ModeDemo)
	const turns = 120
	for i := 1; i <= turns; i++ {
		st = reducer(st, state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
			ID: fmt.Sprintf("u%d", i), From: "user", Text: fmt.Sprintf("question %d", i), At: int64(i * 2)}})
		st = reducer(st, state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID: fmt.Sprintf("r%d", i), From: "boss", Text: fmt.Sprintf("answer %d", i), At: int64(i*2 + 1)}})
	}
	if len(st.Chat) != 2*turns {
		t.Fatalf("the transcript ate history: kept %d of %d entries", len(st.Chat), 2*turns)
	}
	// oldest at the head, newest at the tail — chronological, nothing
	// dropped or reversed.
	if st.Chat[0].ID != "u1" {
		t.Fatalf("the FIRST message must survive at the head (got %q)", st.Chat[0].ID)
	}
	if st.Chat[len(st.Chat)-1].ID != fmt.Sprintf("r%d", turns) {
		t.Fatalf("the NEWEST message must sit at the tail (got %q)", st.Chat[len(st.Chat)-1].ID)
	}
	for i := 1; i <= turns; i++ {
		u := st.Chat[(i-1)*2]
		b := st.Chat[(i-1)*2+1]
		if u.ID != fmt.Sprintf("u%d", i) || b.ID != fmt.Sprintf("r%d", i) {
			t.Fatalf("turn %d out of order: got %q/%q", i, u.ID, b.ID)
		}
	}
}

// TestChatPerKindChurnIsNotEaten pushes 60 boss thoughts + 60 boss tool
// one-liners (3× the old think/tool caps of 20) through the reducer and
// asserts the whole machine churn survives alongside the conversation —
// oldest of each kind still at its birth slot.
func TestChatPerKindChurnIsNotEaten(t *testing.T) {
	st := initialState(state.ModeDemo)
	const n = 60
	for i := 1; i <= n; i++ {
		st = reducer(st, state.Event{
			Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
			CallID: fmt.Sprintf("call-%d", i), Text: fmt.Sprintf("musing %d", i), Done: true})
		st = reducer(st, state.Event{
			Kind: state.EvTool, EmployeeName: "boss",
			ToolName: "read", ToolSummary: fmt.Sprintf("f%d.go", i),
			ToolState: "done", CallID: fmt.Sprintf("tc-%d", i)})
	}
	thinks, tools := 0, 0
	firstThink, firstTool := "", ""
	for _, m := range st.Chat {
		switch m.Kind {
		case "think":
			if thinks == 0 {
				firstThink = m.ID
			}
			thinks++
		case "tool":
			if tools == 0 {
				firstTool = m.ID
			}
			tools++
		}
	}
	if thinks != n {
		t.Fatalf("think per-kind fuse dropped history: kept %d of %d", thinks, n)
	}
	if tools != n {
		t.Fatalf("tool per-kind fuse dropped history: kept %d of %d", tools, n)
	}
	// the oldest of each kind survived (IDs key off the first CallID)
	if firstThink != "think-call-1" {
		t.Fatalf("the oldest think entry was eaten (oldest kept: %q)", firstThink)
	}
	if firstTool != "tool-tc-1" {
		t.Fatalf("the oldest tool entry was eaten (oldest kept: %q)", firstTool)
	}
}
