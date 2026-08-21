// chat_esc_test.go — the double-esc interrupt in the MAIN chat input: two
// esc presses inside dblEscWindow fire the stop seam (the app's /stop
// path) exactly once; a lone esc is a recorded no-op; an esc a modal or
// picker already consumed never reaches the tracker; a stale first press
// (outside the window) can't pair, and a completed double-esc re-arms.
package panels

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// TestChatDoubleEscStops drives the real key path: esc-esc fires once,
// the re-arm means the third press only OPENS the next pair, and the
// fourth completes it.
func TestChatDoubleEscStops(t *testing.T) {
	fired := 0
	c := NewChat(nil)
	c.SetStopHandler(func() tea.Cmd { fired++; return nil })
	c.SetSize(60, 30)
	esc := func() tea.Cmd { return c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})) }

	esc()
	if fired != 0 {
		t.Fatalf("a lone esc must not fire the stop seam (fired=%d)", fired)
	}
	esc()
	if fired != 1 {
		t.Fatalf("esc-esc inside the window must fire once (fired=%d)", fired)
	}
	esc()
	if fired != 1 {
		t.Fatalf("a completed pair must re-arm — the third press opens a fresh pair (fired=%d)", fired)
	}
	esc()
	if fired != 2 {
		t.Fatalf("a fresh pair must fire again (fired=%d)", fired)
	}
}

// TestChatDoubleEscWindow pins the timing gate: a first press older than
// dblEscWindow can't pair, so the second press is itself a new opener.
func TestChatDoubleEscWindow(t *testing.T) {
	fired := 0
	c := NewChat(nil)
	c.SetStopHandler(func() tea.Cmd { fired++; return nil })
	c.SetSize(60, 30)
	esc := func() tea.Cmd { return c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})) }

	esc()
	c.lastEscAt = time.Now().Add(-dblEscWindow - time.Second) // stale the opener
	esc()
	if fired != 0 {
		t.Fatalf("presses outside %v must not pair (fired=%d)", dblEscWindow, fired)
	}
	esc() // the previous press re-armed as a fresh opener
	if fired != 1 {
		t.Fatalf("the stale press's successor must open+complete a new pair (fired=%d)", fired)
	}
}

// TestChatEscConsumedBySurfaces pins the precedence contract: an esc that
// closes the @ picker (or defers the question modal) is consumed THERE
// and never feeds the double-esc tracker in the main input.
func TestChatEscConsumedBySurfaces(t *testing.T) {
	fired := 0
	c := NewChat(nil)
	c.SetStopHandler(func() tea.Cmd { fired++; return nil })
	c.SetSize(60, 30)
	esc := func() tea.Cmd { return c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})) }

	// @ picker open: esc closes it (consumed), then a main-input esc-esc
	// must open AND complete a pair — the picker esc never counted.
	c.atOpen = true
	esc()
	if c.atOpen {
		t.Fatal("esc must close the open @ picker")
	}
	if !c.lastEscAt.IsZero() {
		t.Fatal("a picker-consumed esc must not feed the double-esc tracker")
	}
	esc()
	esc()
	if fired != 1 {
		t.Fatalf("esc-esc after a picker close must fire exactly once (fired=%d)", fired)
	}

	// question modal open: esc defers (consumed by the modal arm), and
	// a following pair in the main input still needs two of its own.
	c.SetQuestion(&QuestionView{ID: "que-1", Text: "which branch?"})
	esc()
	if !c.lastEscAt.IsZero() {
		t.Fatal("a modal-consumed esc must not feed the double-esc tracker")
	}
	c.SetQuestion(nil)
	esc()
	if fired != 1 {
		t.Fatal("a lone main-input esc after the modal esc must not fire")
	}
	esc()
	if fired != 2 {
		t.Fatalf("the full main-input pair must fire (fired=%d)", fired)
	}
}

// TestChatDoubleEscNilHandler: with the seam unwired an esc-esc is a
// harmless no-op (nothing panics, nothing reaches the textarea).
func TestChatDoubleEscNilHandler(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)
	esc := func() { c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})) }
	esc()
	esc()
	esc()
	if got := c.ta.Value(); got != "" {
		t.Fatalf("esc never types into the textarea, got %q", got)
	}
}
