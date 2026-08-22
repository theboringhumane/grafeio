// question_modal.go — the opencode-style QUESTION popover: a floating,
// centered, yellow-bordered card spliced OVER the assembled chat view
// (the same floating-popover mechanics as the permission card in
// perm_modal.go — same geometry helper, same cell-wise splice — so the
// row budget SetSize computed NEVER changes when a question opens).
// One card renders one PAGE of a multi-question request (the "N/N"
// badge shows once Total > 1); the app swaps pages in one at a time.
//
// Four input kinds (opencode's question tool surface):
//
//	TEXT     — free text: the question plus a 3-row echo box. enter
//	           inserts a newline, ctrl+enter submits the text.
//	RADIO    — options, pick exactly one: enter on a row submits its
//	           original wire label; a trailing "› Type your own
//	           answer…" row is a 1-line input while the cursor sits on
//	           it (typing edits, enter submits the text instead).
//	CHECKBOX — options, pick several (wire multiple=true): space or
//	           enter toggles [x]/[ ], a trailing "Submit" row submits
//	           every pick, then the same custom-answer row as radio.
//	CONFIRM  — exactly two yes/no options: enter submits the cursor
//	           row, y/n quick-answer (y = first label, n = second),
//	           no custom-answer row.
//
// Unlike the permission popover (the textarea stays live under it),
// the question popover OWNS EVERY KEY while open — the turn is parked
// at the question reply API, so the main textarea is disabled until
// the answer goes through onQuestionAnswer (or esc defers through
// onQuestionLater → /question re-opens). Clicks: option rows behave
// like their keys (radio/confirm submit, checkbox toggles), the Submit
// row submits, the text box and the custom-answer row just grab the
// cursor; ClickRow still swallows the whole card frame so nothing
// leaks into the thread hit-map. And the APP never branches between
// the two floats: a question card answers through PermClick whenever
// it is the popover on top (see the loud note on PermClick).
package panels

import (
	"regexp"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// QuestionKind is the input mode of one question popover page.
type QuestionKind int

const (
	QuestionKindText     QuestionKind = iota // free-text (no options on the wire)
	QuestionKindRadio                        // options, pick exactly one
	QuestionKindCheckbox                     // options, pick several (wire multiple=true)
	QuestionKindConfirm                      // exactly two yes/no options
)

// QuestionView is the open question popover (set/cleared via SetQuestion).
// One page of a possibly multi-question request per open card — the app
// advances Index and swaps Question/Options as answers land.
type QuestionView struct {
	ID       string                 // pending wire request id ("que-…")
	Question string                 // the question text of THIS page
	Header   string                 // header chip (may be empty)
	Kind     QuestionKind           // from ClassifyQuestion
	Options  []state.QuestionOption // radio/checkbox/confirm rows
	Index    int                    // 1-based page inside the request
	Total    int                    // total pages in the request
}

// QuestionAnswer is what one page submits: Text for free-text pages and
// the option pages' custom-answer row, Picks for the option labels
// (radio/confirm: exactly one — the ORIGINAL wire label; checkbox:
// every toggled label, in option order).
type QuestionAnswer struct {
	Text  string   // free text (textarea page or custom answer row)
	Picks []string // picked option labels (radio/confirm: 1, checkbox: N)
}

// yesNoLabel — a confirm page's wire labels are exactly "yes"/"no"
// (any case, any padding). ClassifyQuestion routes those to the
// two-row confirm page with its y/n quick keys.
var yesNoLabel = regexp.MustCompile(`^(yes|no)$`)

// ClassifyQuestion picks the popover page kind for one wire question:
// no options → free text; Multiple → checkbox; exactly two yes/no
// options → confirm; every other option set → radio. Exported because
// the app model classifies each arriving question.asked page with it.
func ClassifyQuestion(q state.QuestionItem) QuestionKind {
	if len(q.Options) == 0 {
		return QuestionKindText
	}
	if q.Multiple {
		return QuestionKindCheckbox
	}
	if len(q.Options) == 2 &&
		yesNoLabel.MatchString(strings.ToLower(strings.TrimSpace(q.Options[0].Label))) &&
		yesNoLabel.MatchString(strings.ToLower(strings.TrimSpace(q.Options[1].Label))) {
		return QuestionKindConfirm
	}
	return QuestionKindRadio
}

// Click hit-map row tags (parallel to perm's rowOpt): a clickable row
// carries its action, chrome rows carry qHitNone. >= 0 is an OPTION
// index — radio/confirm clicks submit it, checkbox clicks toggle it.
const (
	qHitNone   = -1 // borders / blanks / text / hint: swallow, no action
	qHitSubmit = -2 // checkbox's trailing "Submit" row
	qHitCustom = -3 // the "› Type your own answer…" row (grab the cursor)
	qHitText   = -4 // a row of the TEXT page's echo box (focus only)
)

// questHint — the dim footer line per page kind.
const (
	questHintText    = "ctrl+enter answer · esc later"
	questHintRadio   = "↑/↓ select · enter answer · esc later"
	questHintCheck   = "space toggle · enter select/submit · esc later"
	questHintConfirm = "↑/↓ select · enter answer · y/n quick · esc later"
)

// questHigh — the cursor row's reversed-yellow run (the opencode look;
// perm's reversed-amber parallel).
var questHigh = lipgloss.NewStyle().Foreground(chrome.Question).Reverse(true)

// questCaret is the reversed-space typing marker appended at the text
// end (the TEXT echo box and the custom-answer row).
var questCaret = lipgloss.NewStyle().Reverse(true).Render(" ")

// questCursorRows is the selectable-row count of the open page (what
// the up/down/tab cursor walks): ZERO for TEXT (the echo box owns every
// key), options + custom row for RADIO, options + Submit + custom row
// for CHECKBOX, exactly the options for CONFIRM.
func (c *Chat) questCursorRows() int {
	q := c.question
	switch q.Kind {
	case QuestionKindRadio:
		return len(q.Options) + 1
	case QuestionKindCheckbox:
		return len(q.Options) + 2
	case QuestionKindConfirm:
		return len(q.Options)
	default: // TEXT — no menu cursor at all
		return 0
	}
}

// questCustomIdx is the cursor index of the custom-answer row on option
// pages (always the LAST selectable row: radio's sits after the
// options, checkbox's after Submit). Only valid on radio/checkbox.
func (c *Chat) questCustomIdx() int { return c.questCursorRows() - 1 }

// questMove wraps the menu cursor by d rows through the page's
// selectable rows — cycling means ↑ on the top row lands on the last
// row and ↓/tab on the bottom row lands back on the first (the
// permission popover's exact wrap). TEXT pages have no cursor (no-op).
func (c *Chat) questMove(d int) {
	n := c.questCursorRows()
	if n == 0 {
		return
	}
	c.qSel = (c.qSel + d%n + n) % n
}

// questSubmit fires one page answer through the app's seam (→
// AnswerQuestion): scroll pins to the bottom so the resolved bubble
// lands in view, and the answer cmd IS the return (nil unwired).
func (c *Chat) questSubmit(a QuestionAnswer) tea.Cmd {
	c.follow = true
	c.vp.GotoBottom()
	if c.onQuestionAnswer != nil {
		return c.onQuestionAnswer(a)
	}
	return nil
}

// questSubmitPick submits option i's ORIGINAL wire label as the page's
// single pick (radio row enter, confirm row enter/y/n, and their clicks).
func (c *Chat) questSubmitPick(i int) tea.Cmd {
	if i < 0 || i >= len(c.question.Options) {
		return nil
	}
	return c.questSubmit(QuestionAnswer{Picks: []string{c.question.Options[i].Label}})
}

// questToggle flips one checkbox option's [ ] / [x].
func (c *Chat) questToggle(i int) {
	if i >= 0 && i < len(c.qPicked) {
		c.qPicked[i] = !c.qPicked[i]
	}
}

// questSubmitPicks submits every toggled option (checkbox Submit row),
// in option order. Nothing toggled at all is a no-op — an empty picks
// page would resolve the whole request with a blank answer.
func (c *Chat) questSubmitPicks() tea.Cmd {
	var picks []string
	for i, on := range c.qPicked {
		if on && i < len(c.question.Options) {
			picks = append(picks, c.question.Options[i].Label)
		}
	}
	if len(picks) == 0 {
		return nil
	}
	return c.questSubmit(QuestionAnswer{Picks: picks})
}

// questSubmitText submits the free-text buffer as the page's answer
// (TEXT ctrl+enter, option pages' custom-answer row enter). Whitespace-
// only text is a no-op — a blank page answer means nothing on the wire.
func (c *Chat) questSubmitText() tea.Cmd {
	text := strings.TrimSpace(c.qText)
	if text == "" {
		return nil
	}
	return c.questSubmit(QuestionAnswer{Text: text})
}

// questKey handles EVERY key while a question popover is open (the chat
// Update routes to it before ANY other arm): up/down/tab walk the
// cursor on option pages, esc defers through onQuestionLater, pgup/
// pgdown still scroll the transcript, and each kind claims the rest
// for its own input — nothing ever reaches the main textarea below.
func (c *Chat) questKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if c.onQuestionLater != nil {
			return c.onQuestionLater()
		}
		return nil
	case "up":
		c.questMove(-1)
		return nil
	case "down", "tab": // both walk the cursor forward
		c.questMove(1)
		return nil
	case "pgup", "pgdown":
		// transcript scrolling survives even under the owned keys (the
		// old region modal allowed it too)
		var cmd tea.Cmd
		c.vp, cmd = c.vp.Update(msg)
		if msg.String() == "pgdown" && c.vp.AtBottom() {
			c.follow = true
		} else {
			c.follow = false
		}
		return cmd
	}
	switch c.question.Kind {
	case QuestionKindText:
		return c.questTextKey(msg)
	case QuestionKindConfirm:
		return c.questConfirmKey(msg)
	default: // radio + checkbox share the option-list key arm
		return c.questOptionsKey(msg)
	}
}

// questTextKey — the TEXT page: the 3-row echo box owns everything.
// enter newline, ctrl+enter submits the trimmed buffer, backspace /
// ctrl+u edit, every other printable appends to the buffer.
func (c *Chat) questTextKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+enter":
		return c.questSubmitText()
	case "enter":
		c.qText += "\n"
		return nil
	case "backspace":
		if r := []rune(c.qText); len(r) > 0 {
			c.qText = string(r[:len(r)-1])
		}
		return nil
	case "ctrl+u":
		c.qText = ""
		return nil
	default:
		if msg.Text != "" {
			c.qText += msg.Text
		}
		return nil
	}
}

// questConfirmKey — the CONFIRM page: enter submits the cursor row's
// original wire label, y/n quick-answer no matter where the cursor sits
// (y = first wire label, n = second — usually "yes"/"no" themselves).
// The confirm page has no text input: every other key does nothing.
func (c *Chat) questConfirmKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		sel := c.qSel
		if sel < 0 || sel >= len(c.question.Options) {
			sel = 0
		}
		return c.questSubmitPick(sel)
	case "y":
		return c.questSubmitPick(0)
	case "n":
		return c.questSubmitPick(1)
	default:
		return nil
	}
}

// questOptionsKey — radio + checkbox: enter on an option row submits
// (radio) or toggles (checkbox), space toggles (checkbox option rows),
// the checkbox Submit row submits all picks on enter, and the trailing
// custom-answer row is a 1-line input while the cursor sits on it —
// typing edits there (space included), enter submits the typed text.
func (c *Chat) questOptionsKey(msg tea.KeyPressMsg) tea.Cmd {
	onCustom := c.qSel == c.questCustomIdx()
	switch msg.String() {
	case "enter":
		switch {
		case c.qSel < len(c.question.Options): // an option row
			if c.question.Kind == QuestionKindCheckbox {
				c.questToggle(c.qSel)
				return nil
			}
			return c.questSubmitPick(c.qSel)
		case c.question.Kind == QuestionKindCheckbox && !onCustom:
			return c.questSubmitPicks() // the Submit row
		default: // the custom-answer row
			return c.questSubmitText()
		}
	case "space":
		if c.qSel < len(c.question.Options) && c.question.Kind == QuestionKindCheckbox {
			c.questToggle(c.qSel)
			return nil
		}
		if onCustom {
			c.qText += " " // on the custom input row space is just typing
		}
		return nil
	case "backspace":
		if onCustom {
			if r := []rune(c.qText); len(r) > 0 {
				c.qText = string(r[:len(r)-1])
			}
		}
		return nil
	case "ctrl+u":
		if onCustom {
			c.qText = ""
		}
		return nil
	default:
		// printable keys type ONLY while the cursor sits on the custom
		// row — on the option rows they do nothing (option pages take
		// no quick-letter answers; confirm's y/n live in their own arm)
		if onCustom && msg.Text != "" {
			c.qText += msg.Text
		}
		return nil
	}
}

// questCard renders the popover's rows, each EXACTLY cardW display
// cells (rails included), so the View splice replaces whole cells
// without shifting a row. rowHit[i] tags row i with a qHit* action (or
// option index) for the click hit-map — the geometry helper feeds both
// the View splice and the click code off ONE layout, so a click can
// never disagree with the pixels.
func (c *Chat) questCard() (rows []string, rowHit []int, cardW int) {
	cardW = floatCardW(c.w)
	inner := cardW - 2 // the content column between the │ rails
	push := func(row string, hit int) {
		rows, rowHit = append(rows, row), append(rowHit, hit)
	}
	rail := func(s string) string {
		return chrome.QuestionText.Render("│") + s + chrome.QuestionText.Render("│")
	}
	blank := strings.Repeat(" ", inner)
	q := c.question

	push(chrome.QuestionText.Render("╭"+strings.Repeat("─", inner)+"╮"), qHitNone)
	push(rail(blank), qHitNone)

	// Title row: QUESTION-bold header left, dim "N/N" page badge right —
	// the badge shows once the request pages (Total > 1), and an
	// out-of-range index clamps to the front (perm's same badge rule).
	title := "QUESTION"
	badge := ""
	if q.Total > 1 {
		idx := q.Index
		if idx < 1 || idx > q.Total {
			idx = 1
		}
		badge = itoa(idx) + "/" + itoa(q.Total)
	}
	gap := inner - 2 - lipgloss.Width(title) - lipgloss.Width(badge)
	if gap < 1 {
		gap = 1
	}
	push(rail(" "+chrome.QuestionText.Bold(true).Render(title)+strings.Repeat(" ", gap)+
		chrome.DimText.Render(badge)+" "), qHitNone)

	// Header chip — the wire's optional one-word label, dim.
	if q.Header != "" {
		push(rail(" "+fitLabel(chrome.DimText.Render(q.Header), inner-2)+" "), qHitNone)
	}

	// The question text: the question-tool yellow of the transcript's
	// "boss asks ›" bubbles, wrapped, never clipped.
	for _, ln := range strings.Split(strings.TrimRight(wrapPlain(q.Question, inner-2), "\n"), "\n") {
		push(rail(" "+fitLabel(chrome.QuestionText.Render(ln), inner-2)+" "), qHitNone)
	}
	push(rail(blank), qHitNone)

	if q.Kind == QuestionKindText {
		c.questBodyText(push, rail, inner)
	} else {
		c.questBodyOptions(push, rail, inner)
	}
	push(rail(blank), qHitNone)

	// Footer hint — dim italic, wrapped to the content column.
	hint := questHintText
	switch q.Kind {
	case QuestionKindRadio:
		hint = questHintRadio
	case QuestionKindCheckbox:
		hint = questHintCheck
	case QuestionKindConfirm:
		hint = questHintConfirm
	}
	for _, ln := range strings.Split(strings.TrimRight(wrapPlain(hint, inner-2), "\n"), "\n") {
		push(rail(chrome.DimText.Italic(true).Render(" "+fitPlain(ln, inner-2))+" "), qHitNone)
	}
	push(chrome.QuestionText.Render("╰"+strings.Repeat("─", inner)+"╯"), qHitNone)
	return rows, rowHit, cardW
}

// questMenuRow renders one menu row of EXACTLY inner cells: the label
// plain, an optional description dim right after it on the same row,
// ANSI-aware-truncated (fitLabel, never rune-sliced). The highlighted
// row runs reversed-yellow across the WHOLE content column — the desc
// folds into the reversed run because a nested dim style would break
// the reverse mid-row.
func questMenuRow(label, desc string, inner int, on bool) string {
	if on {
		s := label
		if desc != "" {
			s += "  " + desc
		}
		return questHigh.Render(fitLabel(s, inner))
	}
	row := label
	if desc != "" {
		row += chrome.DimText.Render("  " + desc)
	}
	return fitLabel(row, inner)
}

// upperFirst capitalizes a confirm row's first letter for DISPLAY (the
// submitted label stays the original wire spelling).
func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// questBodyText — the TEXT page: a 3-row echo box with its own thin dim
// frame inside the card's rails. The buffer wraps to the box's text
// column minus one cell (so the reversed caret always fits line-end)
// and the TAIL stays visible, opencode-style. An untouched box shows a
// dim "type your answer…" placeholder behind the caret. Every box row
// is qHitText: a click in it just keeps the (already total) focus.
func (c *Chat) questBodyText(push func(string, int), rail func(string) string, inner int) {
	boxW := inner - 2 // the box + one space each side inside the rails
	textW := boxW - 4 // "│ " + text + " │"
	push(rail(" "+chrome.DimText.Render("╭"+strings.Repeat("─", boxW-2)+"╮")+" "), qHitNone)

	wrapped := []string{}
	if c.qText != "" {
		wrapped = strings.Split(strings.TrimRight(wrapPlain(c.qText, textW-1), "\n"), "\n")
	}
	if len(wrapped) > 3 {
		wrapped = wrapped[len(wrapped)-3:] // over budget: keep the caret tail
	}
	for i := 0; i < 3; i++ {
		body := ""
		if i < len(wrapped) {
			body = wrapped[i]
		} else if i == 0 && c.qText == "" {
			body = chrome.DimText.Render("type your answer…")
		}
		// the caret sits after the last shown text (or the placeholder)
		if (len(wrapped) == 0 && i == 0) || i == len(wrapped)-1 {
			body += questCaret
		}
		if pad := textW - lipgloss.Width(body); pad > 0 {
			body += strings.Repeat(" ", pad)
		}
		push(rail(" "+chrome.DimText.Render("│")+" "+fitLabel(body, textW)+" "+
			chrome.DimText.Render("│")+" "), qHitText)
	}
	push(rail(" "+chrome.DimText.Render("╰"+strings.Repeat("─", boxW-2)+"╯")+" "), qHitNone)
}

// questBodyOptions — the option pages: one row per option (label + dim
// description on the same row, truncated), checkbox rows lead with
// [x]/[ ], confirm labels DISPLAY capitalized (the submitted pick is
// always the original wire label). Trailing rows: "Submit" (checkbox
// only, qHitSubmit), then "› Type your own answer…" (radio + checkbox —
// qHitCustom — which renders as a 1-line input with caret once the
// cursor sits on it and text exists). CONFIRM skips both.
func (c *Chat) questBodyOptions(push func(string, int), rail func(string) string, inner int) {
	q := c.question
	for i, opt := range q.Options {
		mark := "  "
		if i == c.qSel {
			mark = "› "
		}
		box := ""
		if q.Kind == QuestionKindCheckbox {
			box = "[ ] "
			if i < len(c.qPicked) && c.qPicked[i] {
				box = "[x] "
			}
		}
		label := " " + mark + box + opt.Label
		if q.Kind == QuestionKindConfirm {
			label = " " + mark + upperFirst(opt.Label)
		}
		push(rail(questMenuRow(label, opt.Description, inner, i == c.qSel)), i)
	}
	if q.Kind == QuestionKindCheckbox {
		// the Submit row: enter/click submits every [x] pick
		onSub := c.qSel == len(q.Options)
		push(rail(questMenuRow(sepMark(onSub)+"Submit", "", inner, onSub)), qHitSubmit)
	}
	if q.Kind == QuestionKindRadio || q.Kind == QuestionKindCheckbox {
		c.questCustomRow(push, rail, inner)
	}
}

// sepMark is an option row's leading " › "/"   " marker pair for the
// one-cell-indented rows built inline above (Submit / custom rows).
func sepMark(on bool) string {
	if on {
		return " › "
	}
	return "   "
}

// questCustomRow — the "› Type your own answer…" trailing row of the
// radio/checkbox pages. Cursorless it reads as a dim italic invite,
// highlighted it joins the reversed run, and once TEXT exists it is a
// 1-line input: the buffer echoes on the row (the head drops as it
// overflows — the caret end stays visible), with no wrap.
func (c *Chat) questCustomRow(push func(string, int), rail func(string) string, inner int) {
	onCustom := c.qSel == c.questCustomIdx()
	mark := "  "
	if onCustom {
		mark = "› "
	}
	if c.qText == "" {
		row := questMenuRow(" "+mark+"Type your own answer…", "", inner, onCustom)
		if !onCustom {
			row = fitLabel(chrome.DimText.Italic(true).Render(" "+mark+"Type your own answer…"), inner)
		}
		push(rail(row), qHitCustom)
		return
	}
	// the live 1-line input: echo the buffer, head-dropped to fit, caret
	// at the very end (the highlight comes from the › marker + caret —
	// reversing typed text reads poorly)
	text := c.qText
	for lipgloss.Width(" "+mark+text)+1 > inner && len([]rune(text)) > 0 {
		text = string([]rune(text)[1:])
	}
	push(rail(fitLabel(" "+mark+text+questCaret, inner)), qHitCustom)
}

// questCardGeom — where the popover sits, in CHAT CONTENT COORDS (row
// 0 = the viewport's first rendered row): centered over the WHOLE
// panel on both axes, fixed regardless of how the transcript scrolls.
// The View splice and the click hit-map both read this, so the two can
// never disagree about where the card is (perm's identical contract).
func (c *Chat) questCardGeom() (top, left, cardW int, rows []string, rowHit []int) {
	rows, rowHit, cardW = c.questCard()
	top = (c.h - len(rows)) / 2
	if top < 0 {
		top = 0 // a very short panel pins the card to the top edge
	}
	left = (c.w - cardW) / 2
	if left < 0 {
		left = 0
	}
	return top, left, cardW, rows, rowHit
}

// questOverlay splices the popover's rows over the assembled background
// lines cell-wise (ANSI-aware): the background pixels outside the card
// — transcript, divider, the (disabled) textarea — survive, and the
// row count never changes, so the layout never jumps when a question
// opens. The splice mechanics are permOverlay's (shared permSplice).
func (c *Chat) questOverlay(bg []string) []string {
	top, left, _, rows, _ := c.questCardGeom()
	for i, row := range rows {
		y := top + i
		if y < 0 || y >= len(bg) {
			continue // a short panel clips the card instead of growing
		}
		bg[y] = permSplice(bg[y], left, row, lipgloss.Width(row), c.w)
	}
	return bg
}

// questionClick acts for a mouse CLICK at (x, y) in CHAT CONTENT
// COORDS on the open question popover: an option row behaves like its
// key (RADIO/CONFIRM submit the row's original wire label on the spot,
// CHECKBOX toggles its [ ]/[x]), the checkbox Submit row submits all
// picks, the custom-answer row moves the cursor onto it (typing then
// starts), and chrome rows / the TEXT echo box claim-but-do-nothing.
// The returned cmd is EXACTLY the one the matching key would fire.
// Called from PermClick (the app has ONE click seam — see its doc).
func (c *Chat) questionClick(x, y int) tea.Cmd {
	if c.question == nil {
		return nil
	}
	top, left, cardW, rows, rowHit := c.questCardGeom()
	if y < top || y >= top+len(rows) || x < left || x >= left+cardW {
		return nil
	}
	hit := rowHit[y-top]
	switch hit {
	case qHitNone, qHitText:
		return nil // frame + echo box are focus-only claims
	case qHitCustom:
		c.qSel = c.questCustomIdx() // grab the cursor; typing starts
		return nil
	case qHitSubmit:
		return c.questSubmitPicks()
	default: // an option row
		if c.question.Kind == QuestionKindCheckbox {
			c.questToggle(hit)
			return nil
		}
		return c.questSubmitPick(hit) // radio + confirm: click answers
	}
}
