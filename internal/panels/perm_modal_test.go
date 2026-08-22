// perm_modal_test.go — proofs for the opencode-style permission popover:
// (1) the card renders over a LIVE textarea (title, description, three
// options, hint, queue badge) without changing the row budget; (2)
// up/down/tab walk the menu cursor and enter confirms the highlighted
// option with the exact response string; (3) a mouse click on an option
// row answers with the same string its key does; (4) esc defers; (5)
// every unreserved key keeps typing into the textarea underneath; (6) an
// open question modal hides the popover (the ask stays queued).
package panels

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// permHarness — a chat with a queued boss permission over a 60x30 panel
// (tall enough that the card can't splash over the textarea rows) plus
// the answer/defer recorders.
func permHarness() (c *Chat, answers *[]string, later *int) {
	c = NewChat(nil)
	c.SetSize(60, 30)
	answers = &[]string{}
	later = new(int)
	c.SetPermissionHandlers(func(response string) tea.Cmd {
		*answers = append(*answers, response)
		// mirror the real app: the handlers close over a tea.Msg-bearing
		// cmd (permAnswerMsg), never a bare nil
		return func() tea.Msg { return nil }
	}, func() tea.Cmd {
		(*later)++
		return func() tea.Msg { return nil }
	})
	c.SetPermission(&PermissionView{
		ID:       "perm-1",
		ToolName: "Write",
		Summary:  "/tmp/x",
		Agent:    "boss",
		Index:    1,
		Total:    3,
	})
	return c, answers, later
}

// key feeds one key press to the panel.
func permKey(c *Chat, code rune, text string) {
	c.Update(tea.KeyPressMsg(tea.Key{Code: code, Text: text}))
}

// TestPermPopoverRendersCardOverLiveTextarea — the card floats centered
// (rails, title, description, the three options, the hint footer, the
// "1/3" queue badge) and the view's row budget is EXACTLY what SetSize
// handed out — the overlay replaces cells, never rows.
func TestPermPopoverRendersCardOverLiveTextarea(t *testing.T) {
	c, _, _ := permHarness()
	view := ansi.Strip(c.View())
	want := []string{
		"PERMISSION REQUIRED",
		"boss wants Write · /tmp/x",
		"Allow once", "Allow always", "Reject",
		"1/3",
		"↑/↓ select · enter confirm · y/a/n quick · esc later",
		"╭", "╰", "│",
	}
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Fatalf("popover frame missing %q:\n%s", w, view)
		}
	}
	rows := strings.Split(view, "\n")
	if len(rows) != 30 {
		t.Fatalf("the overlay must not change the row budget: got %d rows, want 30:\n%s", len(rows), view)
	}
	// the textarea is STILL the bottom region (its prompt marker survives
	// below the card, which centers well above the input row).
	bottom := strings.Join(rows[len(rows)-3:], "\n")
	if !strings.Contains(bottom, "›") {
		t.Fatalf("the textarea prompt must still render under the overlay:\n%s", bottom)
	}
	if strings.Contains(bottom, "PERMISSION REQUIRED") {
		t.Fatalf("the card must be centered, not docked over the textarea:\n%s", bottom)
	}
}

// TestPermPopoverKeys — up/down/tab move the cursor (wrapping both
// ends), enter confirms the highlighted option with its exact response
// string, and y/a/n answer directly no matter where the cursor sits.
func TestPermPopoverKeys(t *testing.T) {
	c, answers, later := permHarness()

	// fresh ask → cursor on row 0, › marker on "Allow once"
	view := ansi.Strip(c.View())
	if !strings.Contains(view, "› Allow once") {
		t.Fatalf("cursor should open on Allow once:\n%s", view)
	}
	permKey(c, tea.KeyDown, "")
	view = ansi.Strip(c.View())
	if !strings.Contains(view, "› Allow always") {
		t.Fatalf("down should move the cursor to Allow always:\n%s", view)
	}
	permKey(c, tea.KeyEnter, "")
	if got := (*answers)[len(*answers)-1]; got != "always" {
		t.Fatalf("enter on row 2 want \"always\", got %q", got)
	}

	permKey(c, tea.KeyTab, "") // tab walks forward too
	if !strings.Contains(ansi.Strip(c.View()), "› Reject") {
		t.Fatalf("tab should move the cursor to Reject:\n%s", ansi.Strip(c.View()))
	}
	permKey(c, tea.KeyTab, "") // wraps back to the top
	if !strings.Contains(ansi.Strip(c.View()), "› Allow once") {
		t.Fatalf("tab on the bottom row should wrap to Allow once:\n%s", ansi.Strip(c.View()))
	}
	permKey(c, tea.KeyUp, "") // up wraps to the bottom
	if !strings.Contains(ansi.Strip(c.View()), "› Reject") {
		t.Fatalf("up on the top row should wrap to Reject:\n%s", ansi.Strip(c.View()))
	}
	permKey(c, tea.KeyEnter, "")
	if got := (*answers)[len(*answers)-1]; got != "reject" {
		t.Fatalf("enter on row 3 want \"reject\", got %q", got)
	}

	// quick keys answer regardless of the cursor position; the draft
	// never sees them.
	permKey(c, 'y', "y")
	permKey(c, 'a', "a")
	permKey(c, 'n', "n")
	want := []string{"always", "reject", "once", "always", "reject"}
	if len(*answers) != len(want) {
		t.Fatalf("answers = %v, want %v", *answers, want)
	}
	for i, w := range want {
		if (*answers)[i] != w {
			t.Fatalf("answers[%d] = %q, want %q (all answered: %v)", i, (*answers)[i], w, *answers)
		}
	}
	if *later != 0 {
		t.Fatalf("later must stay 0 without esc, got %d", *later)
	}
	if c.ta.Value() != "" {
		t.Fatalf("choice keys must never reach the textarea, got %q", c.ta.Value())
	}
}

// TestPermPopoverEscDefers — esc fires the defer seam (and only it).
func TestPermPopoverEscDefers(t *testing.T) {
	c, answers, later := permHarness()
	permKey(c, tea.KeyEscape, "")
	if *later != 1 {
		t.Fatalf("esc should defer exactly once, got %d", *later)
	}
	if len(*answers) != 0 {
		t.Fatalf("esc must not answer, got %v", *answers)
	}
	// closing from the app side hides the popover entirely.
	c.SetPermission(nil)
	if strings.Contains(ansi.Strip(c.View()), "PERMISSION REQUIRED") {
		t.Fatalf("a nil permission must close the popover")
	}
}

// TestPermPopoverClickAnswers — a mouse click in CHAT CONTENT COORDS on
// the "Allow always" row fires onPermAnswer("always") — the same string
// its key uses; clicks on the card frame answer nothing but are claimed
// (no leak into the thread hit-map), and clicks outside answer nil.
func TestPermPopoverClickAnswers(t *testing.T) {
	c, answers, _ := permHarness()
	view := ansi.Strip(c.View())
	rows := strings.Split(view, "\n")

	rowIdx, colIdx := -1, -1
	for i, r := range rows {
		if strings.Contains(r, "Allow always") {
			rowIdx = i
			colIdx = strings.Index(r, "│") + 2 // inside the left rail
			break
		}
	}
	if rowIdx < 0 {
		t.Fatalf("no Allow always row in the frame:\n%s", view)
	}
	if cmd := c.PermClick(colIdx, rowIdx); cmd == nil {
		t.Fatalf("a click on the Allow always row must return an answer cmd")
	}
	if got := (*answers)[len(*answers)-1]; got != "always" {
		t.Fatalf("clicking Allow always want \"always\", got %q", got)
	}

	// the card frame swallows clicks without answering (title row).
	for i, r := range rows {
		if strings.Contains(r, "PERMISSION REQUIRED") {
			if cmd := c.PermClick(colIdx, i); cmd != nil {
				t.Fatalf("a click on the title row must not answer")
			}
			if !c.ClickRow(colIdx, i) {
				t.Fatalf("a click inside the card frame must be claimed")
			}
			break
		}
	}
	if cmd := c.PermClick(0, 0); cmd != nil {
		t.Fatalf("a click outside the card must return nil")
	}
	if len(*answers) != 1 {
		t.Fatalf("only the option-row click may answer, got %v", *answers)
	}
}

// TestPermPopoverTypingStaysLive — every unreserved key keeps typing
// into the textarea underneath the popover, exactly as before.
func TestPermPopoverTypingStaysLive(t *testing.T) {
	c, answers, _ := permHarness()
	for _, r := range "hi boss" {
		permKey(c, r, string(r))
	}
	if got := c.ta.Value(); got != "hi boss" {
		t.Fatalf("unreserved keys must reach the textarea, got %q", got)
	}
	if len(*answers) != 0 {
		t.Fatalf("typing must not answer the permission, got %v", *answers)
	}
}

// TestPermPopoverHiddenBehindQuestion — an open question popover owns
// the float slot AND the keys: the permission stays queued but does not
// render while the question card is up.
func TestPermPopoverHiddenBehindQuestion(t *testing.T) {
	c, _, _ := permHarness()
	c.SetQuestion(&QuestionView{ID: "q-1", Question: "ship it?", Kind: QuestionKindText, Index: 1, Total: 1})
	view := ansi.Strip(c.View())
	if strings.Contains(view, "PERMISSION REQUIRED") {
		t.Fatalf("the popover must not render behind the question popover:\n%s", view)
	}
	if !strings.Contains(view, "QUESTION") {
		t.Fatalf("the question popover must still render:\n%s", view)
	}
	c.SetQuestion(nil)
	if !strings.Contains(ansi.Strip(c.View()), "PERMISSION REQUIRED") {
		t.Fatalf("closing the question must surface the queued popover")
	}
}
