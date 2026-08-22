// question_modal_test.go — proofs for the opencode-style QUESTION
// popover: (a) the TEXT page renders its 3-row echo box, multi-line
// typing works (enter newline), ctrl+enter submits Text; (b) RADIO
// renders options + dim descriptions + the custom-answer row, enter on
// the second row submits Picks:[label], the custom row types+submits
// Text, and a mouse click answers like enter; (c) CHECKBOX space/enter
// toggle [x], the Submit row sends every pick; (d) CONFIRM renders two
// capitalized rows, y/n quick-submit the original wire labels; (e) esc
// defers through onQuestionLater for EVERY kind; (f) the card splices
// as an overlay — the View row count never exceeds the SetSize budget
// and the (disabled) textarea still renders under it; (g)
// ClassifyQuestion's kind rules.
package panels

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// questHarness — a chat with an open question popover over a 60x30
// panel plus the answer/defer recorders (mirroring the app's real
// seams: the handlers close over a tea.Msg-bearing cmd, not bare nil).
func questHarness(q *QuestionView) (c *Chat, answers *[]QuestionAnswer, later *int) {
	c = NewChat(nil)
	c.SetSize(60, 30)
	answers = &[]QuestionAnswer{}
	later = new(int)
	c.SetQuestionHandlers(func(a QuestionAnswer) tea.Cmd {
		*answers = append(*answers, a)
		return func() tea.Msg { return nil }
	}, func() tea.Cmd {
		(*later)++
		return func() tea.Msg { return nil }
	})
	c.SetQuestion(q)
	return c, answers, later
}

// questType feeds printable text one rune at a time.
func questType(c *Chat, s string) {
	for _, r := range s {
		permKey(c, r, string(r))
	}
}

// TestQuestTextPage — (a) the TEXT page: question + 3-row echo box +
// "ctrl+enter answer · esc later" footer, enter inserts a newline,
// ctrl+enter submits the multi-line Text, and not one key ever touches
// the main textarea underneath.
func TestQuestTextPage(t *testing.T) {
	c, answers, _ := questHarness(&QuestionView{
		ID: "que-1", Kind: QuestionKindText, Index: 1, Total: 1,
		Question: "what should the release notes say?",
	})
	view := ansi.Strip(c.View())
	for _, want := range []string{
		"QUESTION",
		"what should the release notes say?",
		"type your answer…",
		questHintText,
		"╭", "╰",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("TEXT page missing %q:\n%s", want, view)
		}
	}
	// a blank ctrl+enter is a no-op
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	if len(*answers) != 0 {
		t.Fatalf("an empty buffer must not submit, got %v", *answers)
	}
	questType(c, "hello boss")
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})) // newline, NOT submit
	questType(c, "line two")
	if got := c.qText; got != "hello boss\nline two" {
		t.Fatalf("enter must newline, buffer = %q", got)
	}
	if !strings.Contains(ansi.Strip(c.View()), "hello boss") ||
		!strings.Contains(ansi.Strip(c.View()), "line two") {
		t.Fatalf("the multi-line echo must render in the box:\n%s", ansi.Strip(c.View()))
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	if len(*answers) != 1 || (*answers)[0].Text != "hello boss\nline two" {
		t.Fatalf("ctrl+enter must submit the multi-line Text, got %v", *answers)
	}
	if got := c.ta.Value(); got != "" {
		t.Fatalf("the popover owns every key — the textarea must stay empty, got %q", got)
	}
}

// TestQuestRadioPage — (b) the RADIO page: three options render with
// dim descriptions and the custom-answer row, the "1/2" page badge
// shows (Total 2), enter on the second option submits Picks:[label2],
// the custom row types + submits Text, and a mouse click on an option
// row answers like enter on it.
func TestQuestRadioPage(t *testing.T) {
	opts := []state.QuestionOption{
		{Label: "merge now", Description: "ships it as-is"},
		{Label: "request review", Description: "one more pair of eyes"},
		{Label: "hold", Description: "leave it parked"},
	}
	c, answers, _ := questHarness(&QuestionView{
		ID: "que-2", Kind: QuestionKindRadio, Options: opts, Index: 1, Total: 2,
		Question: "which branch do I ship?",
	})
	view := ansi.Strip(c.View())
	for _, want := range []string{
		"QUESTION", "1/2",
		"merge now", "ships it as-is",
		"request review", "hold", "Type your own answer…",
		questHintRadio,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("RADIO page missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "› merge now") {
		t.Fatalf("the cursor opens on the first option:\n%s", view)
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	if !strings.Contains(ansi.Strip(c.View()), "› request review") {
		t.Fatalf("down must move the cursor to option 2:\n%s", ansi.Strip(c.View()))
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(*answers) != 1 || len((*answers)[0].Picks) != 1 || (*answers)[0].Picks[0] != "request review" {
		t.Fatalf("enter on option 2 must submit Picks [request review], got %v", *answers)
	}

	// the custom-answer row: cursor onto it, typing edits, enter submits Text
	for i := 0; i < 2; i++ { // down past option 3 onto the custom row
		c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	}
	if c.qSel != c.questCustomIdx() {
		t.Fatalf("cursor must sit on the custom row, got %d", c.qSel)
	}
	questType(c, "only with tests")
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(*answers) != 2 || (*answers)[1].Text != "only with tests" {
		t.Fatalf("the custom row must submit Text, got %v", *answers)
	}

	// a mouse click on the "hold" row answers like enter on it
	view = ansi.Strip(c.View())
	rows := strings.Split(view, "\n")
	rowIdx, colIdx := -1, 0
	for i, r := range rows {
		if strings.Contains(r, "hold") {
			rowIdx = i
			colIdx = strings.Index(r, "│") + 2
			break
		}
	}
	if rowIdx < 0 {
		t.Fatalf("no hold row in the frame:\n%s", view)
	}
	if cmd := c.PermClick(colIdx, rowIdx); cmd == nil {
		t.Fatalf("a click on an option row must fire the answer seam")
	}
	if len(*answers) != 3 || (*answers)[2].Picks[0] != "hold" {
		t.Fatalf("clicking the hold row must submit Picks [hold], got %v", *answers)
	}
}

// TestQuestCheckboxPage — (c) the CHECKBOX page: space (and enter)
// toggle [x], the Submit row sends every pick in option order, and the
// trailing custom row still works after Submit.
func TestQuestCheckboxPage(t *testing.T) {
	opts := []state.QuestionOption{
		{Label: "api", Description: "public endpoints"},
		{Label: "docs", Description: "README + guides"},
		{Label: "deploy", Description: "kubectl apply"},
	}
	c, answers, _ := questHarness(&QuestionView{
		ID: "que-3", Kind: QuestionKindCheckbox, Options: opts, Index: 1, Total: 1,
		Question: "what can I touch?",
	})
	view := ansi.Strip(c.View())
	for _, want := range []string{
		"[ ] api", "[ ] docs", "[ ] deploy",
		"Submit", "Type your own answer…", questHintCheck,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("CHECKBOX page missing %q:\n%s", want, view)
		}
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace})) // toggle api
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))  // → docs
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))  // → deploy
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})) // enter toggles too
	view = ansi.Strip(c.View())
	if !strings.Contains(view, "[x] api") || !strings.Contains(view, "[x] deploy") {
		t.Fatalf("space + enter must toggle two options:\n%s", view)
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})) // → Submit row
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(*answers) != 1 || len((*answers)[0].Picks) != 2 ||
		(*answers)[0].Picks[0] != "api" || (*answers)[0].Picks[1] != "deploy" {
		t.Fatalf("the Submit row must send Picks [api deploy], got %v", *answers)
	}
}

// TestQuestConfirmPage — (d) the CONFIRM page: exactly two capitalized
// rows, no custom-answer row, y submits the FIRST wire label, n the
// second, and enter submits the cursor row's original label.
func TestQuestConfirmPage(t *testing.T) {
	opts := []state.QuestionOption{{Label: "yes"}, {Label: "no"}}
	c, answers, _ := questHarness(&QuestionView{
		ID: "que-4", Kind: QuestionKindConfirm, Options: opts, Index: 1, Total: 1,
		Question: "drop the old schema?",
	})
	view := ansi.Strip(c.View())
	if !strings.Contains(view, "Yes") || !strings.Contains(view, "No") {
		t.Fatalf("CONFIRM must render the capitalized yes/no rows:\n%s", view)
	}
	if strings.Contains(view, "Type your own answer") {
		t.Fatalf("a confirm page has no custom-answer row:\n%s", view)
	}
	if !strings.Contains(view, questHintConfirm) {
		t.Fatalf("confirm footer must hint the y/n quick keys:\n%s", view)
	}
	questType(c, "y")
	if len(*answers) != 1 || (*answers)[0].Picks[0] != "yes" {
		t.Fatalf("y must submit the first wire label, got %v", *answers)
	}
	questType(c, "n")
	if len(*answers) != 2 || (*answers)[1].Picks[0] != "no" {
		t.Fatalf("n must submit the second wire label, got %v", *answers)
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})) // cursor → No
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(*answers) != 3 || (*answers)[2].Picks[0] != "no" {
		t.Fatalf("enter must submit the cursor row's label, got %v", *answers)
	}
}

// TestQuestEscDefers — (e) esc defers through onQuestionLater for EVERY
// page kind (and answers nothing).
func TestQuestEscDefers(t *testing.T) {
	pages := []QuestionView{
		{ID: "q", Kind: QuestionKindText, Question: "?", Index: 1, Total: 1},
		{ID: "q", Kind: QuestionKindRadio, Question: "?", Index: 1, Total: 1,
			Options: []state.QuestionOption{{Label: "a"}, {Label: "b"}}},
		{ID: "q", Kind: QuestionKindCheckbox, Question: "?", Index: 1, Total: 1,
			Options: []state.QuestionOption{{Label: "a"}, {Label: "b"}}},
		{ID: "q", Kind: QuestionKindConfirm, Question: "?", Index: 1, Total: 1,
			Options: []state.QuestionOption{{Label: "yes"}, {Label: "no"}}},
	}
	for i, page := range pages {
		q := page
		c, answers, later := questHarness(&q)
		c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		if *later != 1 {
			t.Fatalf("kind %d: esc must defer exactly once, got %d", i, *later)
		}
		if len(*answers) != 0 {
			t.Fatalf("kind %d: esc must not answer, got %v", i, *answers)
		}
		// closing from the app side hides the popover entirely
		c.SetQuestion(nil)
		if strings.Contains(ansi.Strip(c.View()), "QUESTION") {
			t.Fatalf("kind %d: a nil question must close the popover", i)
		}
	}
}

// TestQuestPopoverOverlayBudget — (f) the card is a pure overlay: the
// View's row count is EXACTLY the SetSize budget, the center of the
// panel holds the card, and the (disabled) textarea still renders at
// the bottom under the splice. The card frame swallows clicks in its
// rect (ClickRow) while clicks outside return nil everywhere.
func TestQuestPopoverOverlayBudget(t *testing.T) {
	c, _, _ := questHarness(&QuestionView{
		ID: "que-5", Kind: QuestionKindRadio, Index: 1, Total: 1,
		Question: "pick one",
		Options:  []state.QuestionOption{{Label: "a"}, {Label: "b"}},
	})
	view := ansi.Strip(c.View())
	rows := strings.Split(view, "\n")
	if len(rows) != 30 {
		t.Fatalf("the overlay must not change the row budget: got %d rows, want 30:\n%s", len(rows), view)
	}
	bottom := strings.Join(rows[len(rows)-3:], "\n")
	if !strings.Contains(bottom, "›") {
		t.Fatalf("the textarea prompt must still render under the overlay:\n%s", bottom)
	}
	if strings.Contains(bottom, "QUESTION") {
		t.Fatalf("the card must be centered, not docked over the textarea:\n%s", bottom)
	}
	// the card frame swallows clicks; clicks outside it answer nothing
	topRow, midCol := -1, 0
	for i, r := range rows {
		if strings.Contains(r, "QUESTION") {
			topRow = i
			midCol = strings.Index(r, "│") + 2
			break
		}
	}
	if topRow < 0 {
		t.Fatalf("no QUESTION title row in the frame:\n%s", view)
	}
	if !c.ClickRow(midCol, topRow) {
		t.Fatalf("a click inside the question card must be claimed")
	}
	if cmd := c.PermClick(0, 0); cmd != nil {
		t.Fatalf("a click outside the card must return nil")
	}
}

// TestClassifyQuestion — (g) the kind classifier: no options → text,
// Multiple → checkbox (yes/no labels included), exactly two yes/no
// options → confirm (any case), everything else → radio.
func TestClassifyQuestion(t *testing.T) {
	cases := []struct {
		name string
		q    state.QuestionItem
		want QuestionKind
	}{
		{"no options is free text", state.QuestionItem{Question: "?"}, QuestionKindText},
		{"multiple is checkbox", state.QuestionItem{Options: []state.QuestionOption{{Label: "a"}, {Label: "b"}}, Multiple: true}, QuestionKindCheckbox},
		{"yes/no is confirm", state.QuestionItem{Options: []state.QuestionOption{{Label: "yes"}, {Label: "no"}}}, QuestionKindConfirm},
		{"YES/No is confirm (case-insensitive)", state.QuestionItem{Options: []state.QuestionOption{{Label: "YES"}, {Label: "No"}}}, QuestionKindConfirm},
		{"two non-yes/no options are radio", state.QuestionItem{Options: []state.QuestionOption{{Label: "a"}, {Label: "b"}}}, QuestionKindRadio},
		{"three options are radio", state.QuestionItem{Options: []state.QuestionOption{{Label: "yes"}, {Label: "no"}, {Label: "maybe"}}}, QuestionKindRadio},
		{"multiple beats the yes/no shape", state.QuestionItem{Options: []state.QuestionOption{{Label: "yes"}, {Label: "no"}}, Multiple: true}, QuestionKindCheckbox},
	}
	for _, tc := range cases {
		if got := ClassifyQuestion(tc.q); got != tc.want {
			t.Fatalf("%s: got kind %d, want %d", tc.name, got, tc.want)
		}
	}
}
