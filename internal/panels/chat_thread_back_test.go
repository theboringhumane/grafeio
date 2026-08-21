// chat_thread_back_test.go — behavior proofs for the "back one thread"
// chord in the chat tab: with one or more subagent threads EXPANDED, esc
// and ↑ each collapse the MOST RECENTLY expanded thread (a one-step
// backstack through the expand history, like cmd+z for thread zoom); with
// ZERO threads expanded both keys keep their pre-feature meaning exactly —
// ↑ scrolls the conversation viewport up one line and releases the
// bottom-follow; esc falls through to the textarea's default arm (a
// panel-level no-op). Threads here are COMPLETED (idle sprites) so they
// are collapsed by default: the only thing that expands one is a real
// toggle gesture (ToggleThread — the same seam the app's click path runs,
// internal/app/model.go). No clocks, no sleeps: every timestamp is a
// literal.
package panels

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/grafeio/internal/state"
)

// tbAssertExpanded pins ONE thread's effective expansion (the exact rule
// renderWorkerGroup renders by, via the same seam ToggleThread consults)
// at one state-walk step.
func tbAssertExpanded(t *testing.T, c *Chat, name string, want bool, walk string) {
	t.Helper()
	if got := c.threadExpandedNow(name); got != want {
		t.Fatalf("state walk %s: thread %q expanded = %v, want %v", walk, name, got, want)
	}
}

// newBackTestChat builds the deterministic two-thread fixture behind every
// back-one test: bossTurns boss replies padding the conversation, then one
// completed 2-tool thread per agent — X (tekton-1, "Fix Portuguese proof")
// born at At 1000, Y (tekton-2, "Wire the tests") born at At 3000.
// Collapsed summaries read "… (· 2 tool calls ✓ done)". Callers pick the
// size + padding: the back-one tests use a short conversation in a tall
// panel (every thread row inside the viewport, so the whole backstack is
// on screen at once); the fall-through scroll test uses a long one that
// OUTGROWS the viewport (so ↑ has an offset to move). No clocks, no
// sleeps: every timestamp is a literal.
func newBackTestChat(t *testing.T, bossTurns, w, h int) *Chat {
	t.Helper()
	c := NewChat(nil)
	c.SetSize(w, h)

	msgs := []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "first request", At: 100},
	}
	for i := 1; i <= bossTurns; i++ {
		msgs = append(msgs, state.ChatMsg{
			ID: "boss-" + itoa(i), From: "boss", Kind: "boss",
			Text: "progress update " + itoa(i), At: int64(100 + i*200),
		})
	}
	msgs = append(msgs,
		// thread X — tekton-1, born between boss-4 and boss-5 when padded
		state.ChatMsg{ID: "x1", From: "tekton-1", Kind: wtoolKind, Text: "read · proof.go", Meta: "done\x1f2", At: 1_000},
		state.ChatMsg{ID: "x2", From: "tekton-1", Kind: wtoolKind, Text: "edit · proof.go", Meta: "done\x1f3", At: 1_400},
		// thread Y — tekton-2, born after boss-14 when padded
		state.ChatMsg{ID: "y1", From: "tekton-2", Kind: wtoolKind, Text: "read · wire.go", Meta: "done\x1f5", At: 3_000},
		state.ChatMsg{ID: "y2", From: "tekton-2", Kind: wtoolKind, Text: "bash · go test", Meta: "done\x1f6", At: 3_400},
	)
	c.SetState(state.OfficeState{
		Tick: 50,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Fix Portuguese proof"},
			{ID: "dev-2", Name: "tekton-2", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Wire the tests"},
		},
		Chat: msgs,
	})
	return c
}

// tbAssertTwoCollapsed pins BOTH completed threads collapsed at the state
// seam AND in the render: both "✓ done" summaries on screen, no "┌"
// expanded header anywhere. Use only on the SHORT fixture — a padded
// (scrollable) conversation keeps the early thread above the viewport
// window, and off-screen is not collapsed.
func tbAssertTwoCollapsed(t *testing.T, c *Chat, walk string) {
	t.Helper()
	tbAssertExpanded(t, c, "tekton-1", false, walk)
	tbAssertExpanded(t, c, "tekton-2", false, walk)
	view := ansi.Strip(c.View())
	for _, want := range []string{
		"tekton-1 · Fix Portuguese proof (· 2 tool calls ✓ done)",
		"tekton-2 · Wire the tests (· 2 tool calls ✓ done)",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("state walk %s: collapsed summary %q missing:\n%s", walk, want, view)
		}
	}
	if strings.Contains(view, "┌") {
		t.Fatalf("state walk %s: an expanded thread header leaked into the collapsed render:\n%s", walk, view)
	}
}

// TestThreadBackEscCollapsesMostRecentFirst: expand X then Y (the real
// toggle gesture seam), then walk the backstack with esc: the FIRST esc
// collapses Y and leaves X expanded (state + render); the SECOND esc
// collapses X — both threads back to their collapsed summaries.
func TestThreadBackEscCollapsesMostRecentFirst(t *testing.T) {
	c := newBackTestChat(t, 4, 60, 30)
	tbAssertTwoCollapsed(t, c, "[]")

	// expand X, then Y — Y is the most recently expanded
	c.ToggleThread("tekton-1")
	c.ToggleThread("tekton-2")
	tbAssertExpanded(t, c, "tekton-1", true, "[X,Y]")
	tbAssertExpanded(t, c, "tekton-2", true, "[X,Y]")

	// esc #1: Y (most recent) collapses, X survives — rendered as X's
	// expanded "┌" header beside Y's collapsed summary
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	tbAssertExpanded(t, c, "tekton-2", false, "[X,Y] --esc--> [X]")
	tbAssertExpanded(t, c, "tekton-1", true, "[X,Y] --esc--> [X]")
	view := ansi.Strip(c.View())
	fmt.Println("---- CHAT PANEL (60 cols, after esc #1: X expanded, Y collapsed) ----")
	fmt.Print(view)
	fmt.Println("---- END PANEL ----")
	if !strings.Contains(view, "┌ tekton-1") {
		t.Fatalf("esc #1: X's expanded header must survive:\n%s", view)
	}
	if strings.Contains(view, "┌ tekton-2") {
		t.Fatalf("esc #1: Y must be collapsed, still shows an expanded header:\n%s", view)
	}
	if !strings.Contains(view, "tekton-2 · Wire the tests (· 2 tool calls ✓ done)") {
		t.Fatalf("esc #1: Y must render its collapsed summary:\n%s", view)
	}

	// esc #2: X collapses too — the backstack is empty
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	tbAssertTwoCollapsed(t, c, "[X,Y] --esc--> [X] --esc--> []")
}

// TestThreadBackUpCollapsesMostRecentFirst: the same backstack walk with
// the up-arrow — the FIRST ↑ collapses Y only, the SECOND ↑ collapses X.
func TestThreadBackUpCollapsesMostRecentFirst(t *testing.T) {
	c := newBackTestChat(t, 4, 60, 30)
	tbAssertTwoCollapsed(t, c, "[]")

	// expand X, then Y — Y is the most recently expanded
	c.ToggleThread("tekton-1")
	c.ToggleThread("tekton-2")
	tbAssertExpanded(t, c, "tekton-1", true, "[X,Y]")
	tbAssertExpanded(t, c, "tekton-2", true, "[X,Y]")

	// up #1: Y (most recent) collapses, X survives
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	tbAssertExpanded(t, c, "tekton-2", false, "[X,Y] --up--> [X]")
	tbAssertExpanded(t, c, "tekton-1", true, "[X,Y] --up--> [X]")
	view := ansi.Strip(c.View())
	if !strings.Contains(view, "┌ tekton-1") {
		t.Fatalf("up #1: X's expanded header must survive:\n%s", view)
	}
	if strings.Contains(view, "┌ tekton-2") {
		t.Fatalf("up #1: Y must be collapsed, still shows an expanded header:\n%s", view)
	}

	// up #2: X collapses too
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	tbAssertTwoCollapsed(t, c, "[X,Y] --up--> [X] --up--> []")
}

// TestThreadBackFallThroughWhenNoExpandedThreads pins the pre-feature
// meaning of both keys when the backstack is EMPTY: ↑ keeps scrolling the
// conversation viewport up one line and releases the bottom-follow; esc
// falls through untouched (no error, no panic, no panel-state drift).
// Crucially, neither key collapses or expands any thread — the collapse
// claim is made only when an expanded thread actually exists.
func TestThreadBackFallThroughWhenNoExpandedThreads(t *testing.T) {
	// ---- up: viewport scroll, exactly like before the feature ----
	// (PADDED fixture: the conversation outgrows the viewport so ↑ has an
	// offset to move — collapse claims are made at the state seam here,
	// since the early thread's summary legitimately lives above the
	// viewport window.)
	c := newBackTestChat(t, 16, 60, 24)
	tbAssertExpanded(t, c, "tekton-1", false, "[]")
	tbAssertExpanded(t, c, "tekton-2", false, "[]")
	if !c.follow {
		t.Fatal("fixture must start bottom-following")
	}
	yoff0 := c.vp.YOffset()
	if yoff0 == 0 {
		t.Fatal("fixture must outgrow the viewport so up can scroll (YOffset 0 at bottom)")
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if c.follow {
		t.Fatal("up with no expanded threads: follow must release (viewport scroll path)")
	}
	if got := c.vp.YOffset(); got != yoff0-1 {
		t.Fatalf("up with no expanded threads: viewport must scroll up one line, YOffset %d -> %d", yoff0, got)
	}
	// the scroll consumed the key for SCROLLING, not collapse: both
	// threads are still collapsed and no expanded header appeared anywhere
	tbAssertExpanded(t, c, "tekton-1", false, "[] --up--> (scroll up one line)")
	tbAssertExpanded(t, c, "tekton-2", false, "[] --up--> (scroll up one line)")
	if view := ansi.Strip(c.View()); strings.Contains(view, "┌") {
		t.Fatalf("up with no expanded threads must open nothing:\n%s", view)
	}
	if got := c.ta.Value(); got != "" {
		t.Fatalf("up must never touch the draft, got %q", got)
	}

	// ---- esc: falls through, panel-state consistent ----
	c2 := newBackTestChat(t, 4, 60, 30)
	tbAssertTwoCollapsed(t, c2, "[]")
	before := ansi.Strip(c2.View())
	follow0 := c2.follow
	draft0 := c2.ta.Value()
	c2.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if c2.follow != follow0 {
		t.Fatalf("esc with no expanded threads must not move follow (%v -> %v)", follow0, c2.follow)
	}
	if got := c2.ta.Value(); got != draft0 {
		t.Fatalf("esc with no expanded threads must not touch the draft (%q -> %q)", draft0, got)
	}
	if after := ansi.Strip(c2.View()); after != before {
		t.Fatalf("esc with no expanded threads must leave the render untouched:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
	tbAssertTwoCollapsed(t, c2, "[] --esc--> []")
}
