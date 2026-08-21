// chat_render_test.go — render proofs for the chat panel's two hygiene
// rules: (1) there is no blinking caret ANYWHERE — the typing row lives
// below the divider (above the input) for the WHOLE pending period;
// (2) every chat bubble type WRAPS at the panel width instead of
// overflowing/clipping — markdown fences, unbreakable URLs, tool
// one-liners, workers-thread rows.
package panels

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/grafeio/internal/state"
)

// chatDividerRow — the View row index of the divider (a run of cells that
// is nothing but "─"), -1 when absent.
func chatDividerRow(rows []string) int {
	for i, r := range rows {
		t := strings.TrimSpace(r)
		if t != "" && strings.Trim(t, "─") == "" {
			return i
		}
	}
	return -1
}

// typingRowIdx — the ONLY row carrying needle that is NOT a textarea
// prompt row (the busy placeholder quotes the same "… is typing…" text,
// but always behind the "›" prompt). -1 when absent.
func typingRowIdx(rows []string, needle string) int {
	idx := -1
	for i, r := range rows {
		if !strings.Contains(r, needle) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(r), "›") {
			continue // prompt row quoting the busy text — not the typing row
		}
		if idx >= 0 {
			return -2 // more than one renderer-owned row carries it
		}
		idx = i
	}
	return idx
}

// TestNoCaretTypingRowAboveInput pins the caret eviction + the typing
// row's new home: pending boss (empty text) → row below the divider;
// pending boss WITH streamed text → the row STAYS (whole pending period);
// settled → the row is gone. In every state: no "▌", and the drawn row
// count equals the SetSize budget exactly.
func TestNoCaretTypingRowAboveInput(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(40, 20)

	assertState := func(tag string, wantRow bool) {
		view := ansi.Strip(c.View())
		if strings.Contains(view, "▌") {
			t.Fatalf("%s: the stream caret must not exist in ANY chat state:\n%s", tag, view)
		}
		rows := strings.Split(view, "\n")
		if len(rows) != 20 {
			t.Fatalf("%s: View drew %d rows, want the SetSize budget 20:\n%s", tag, len(rows), view)
		}
		di := chatDividerRow(rows)
		if di < 0 {
			t.Fatalf("%s: no divider row found:\n%s", tag, view)
		}
		ri := typingRowIdx(rows, "is typing…")
		if !wantRow {
			if ri >= 0 {
				t.Fatalf("%s: typing row present after settle:\n%s", tag, view)
			}
			return
		}
		if ri < 0 {
			t.Fatalf("%s: typing row missing below the divider:\n%s", tag, view)
		}
		if ri != di+1 {
			t.Fatalf("%s: typing row must be the FIRST row below the divider (divider at row %d, typing row at %d):\n%s",
				tag, di, ri, view)
		}
	}

	// pending boss, EMPTY text → the typing row speaks for it
	c.SetState(state.OfficeState{Tick: 2, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Kind: "boss", Pending: true},
	}})
	assertState("empty pending", true)

	// streamed text lands — the bubble grows in the viewport but the
	// typing row STAYS (liveness for the whole pending period), and the
	// row budget does not move
	c.SetState(state.OfficeState{Tick: 3, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Kind: "boss", Pending: true, Text: "working on it —"},
	}})
	streamView := ansi.Strip(c.View())
	if !strings.Contains(streamView, "working on it —") {
		t.Fatalf("streaming pending: the partial text must render in the viewport:\n%s", streamView)
	}
	assertState("streaming pending", true)
	fmt.Println("---- CHAT PANEL (40 cols, boss streaming, ansi-stripped) ----")
	fmt.Print(streamView)
	fmt.Println("---- END PANEL ----")

	// settle: typing row gone, budget returns
	c.SetState(state.OfficeState{Tick: 4, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Text: "working on it — done"},
	}})
	assertState("settled", false)
}

// TestChatConvoWrapsAtWidth — 28 columns, the hostile message set: a
// fenced code line of 40 cells, a 35-cell unbreakable URL in prose, a
// long boss tool one-liner and a workers thread with a deep path. Every
// ansi-stripped View row must fit the panel, everything wrapped glyph
// must SURVIVE (never clipped), and the markdown hanging indent aligns
// under the bubble text start (prefix cell width, not byte length).
func TestChatConvoWrapsAtWidth(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(28, 30)

	codeToken := "export TOKEN=" + strings.Repeat("q", 27) // 40 cells, unbreakable tail
	urlToken := "https://x.co/" + strings.Repeat("z", 22) // 35 cells, unbreakable
	deepPath := "internal/panels/some/really/deep/file.go"
	wPath := "internal/components/very/deep/file.go"

	c.SetState(state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteWorking, Task: "Wire it"},
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "show the wire-up"},
			{ID: "b1", From: "boss", Kind: "boss", Text: "**plan** — see " + urlToken + " then run:\n```sh\n" + codeToken + "\n```"},
			{ID: "t1", From: "boss", Kind: "tool", Text: "read · " + deepPath, Meta: "done"},
			{ID: "w1", From: "tekton-1", Kind: "wtool", Text: "read · " + wPath, Meta: "done\x1f3"},
		},
	})

	view := ansi.Strip(c.View())
	rows := strings.Split(view, "\n")
	fmt.Println("---- CHAT PANEL (28 cols, hostile wrap set, ansi-stripped) ----")
	for _, r := range rows {
		fmt.Printf("%2d|%s|\n", len([]rune(r)), r)
	}
	fmt.Println("---- END PANEL ----")

	// (1) every row inside the column budget
	for i, r := range rows {
		if w := len([]rune(r)); w > 28 {
			t.Fatalf("row %d overflows the 28-col budget (%d cells): %q\nfull view:\n%s", i, w, r, view)
		}
	}

	// (2) nothing clipped: squash all whitespace out of the render AND
	// the tokens (fold boundaries legitimately eat their join space,
	// the viewport pads rows) — every long token must reconstruct
	// contiguously
	joined := ""
	for _, r := range rows {
		joined += strings.TrimSpace(r)
	}
	squash := strings.NewReplacer(" ", "", "│", "").Replace
	for _, tok := range []string{urlToken, codeToken, deepPath, wPath} {
		if !strings.Contains(squash(joined), squash(tok)) {
			t.Fatalf("a long token was CLIPPED somewhere (%q not fully present):\n%s", tok, view)
		}
	}

	// (3) the markdown hanging indent is the prefix CELL width (7 for
	// "boss › ", not its 9 bytes) — the folded URL row hangs right under
	// the bubble text start
	if !strings.Contains(view, "\n       https://x.co/") {
		t.Fatalf("the URL continuation must hang under the bubble text (7-cell indent):\n%s", view)
	}

	// (4) the boss tool one-liner keeps its first-line shape and its
	// continuation hangs under "[tool] " (7 spaces)
	if !strings.Contains(view, "[tool] read ·") {
		t.Fatalf("the tool one-liner lost its first-line prefix/symbol shape:\n%s", view)
	}
	if !strings.Contains(view, "\n       internal/panels/some") {
		t.Fatalf("the tool continuation must hang 7 cells in:\n%s", view)
	}

	// (5) the workers thread WRAPS with a "│ " continuation instead of
	// truncating the path
	if !strings.Contains(view, "│ [tool] read ·") {
		t.Fatalf("the workers-thread row lost its shape:\n%s", view)
	}
	if !strings.Contains(view, "\n│ internal/components/very/d") {
		t.Fatalf("a long workers-thread row must continue with a \"│ \" row:\n%s", view)
	}
}
