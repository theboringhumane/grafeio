// perm_modal.go — the opencode-style PERMISSION popover: a floating,
// centered, amber-bordered card spliced OVER the assembled chat view (it
// replaced the old hint block that stole the textarea region). Pixels:
// a WarnBold "PERMISSION REQUIRED" title, a "<agent> wants <tool> ·
// <summary>" description line, three option rows (Allow once / Allow
// always / Reject — the highlighted one runs reversed-amber across the
// content column), an "N/N" queue badge once asks stack, and a dim hint
// footer. Keys while open: up/down/tab walk the menu cursor, enter
// confirms the highlighted option, y/a/n quick-answer regardless of the
// cursor, esc defers — every other key falls through to the LIVE
// textarea underneath. Clicks: option rows answer like their keys; the
// whole card frame swallows clicks so nothing leaks through to the
// thread hit-map. The card is a FIXED overlay (no scroll offset): the
// geometry helper feeding the View splice is the same one feeding the
// click hit-map, so a click can never disagree with the pixels.
package panels

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
)

// permOption — one row of the popover's menu: the display label and the
// exact response string its click/enter fires on onPermAnswer (the same
// strings the y/a/n quick keys use).
type permOption struct {
	label    string
	response string
}

// permOptions is the menu order the selection cursor walks (up/down/tab),
// enter confirms the highlighted index, and a mouse click answers with.
var permOptions = []permOption{
	{label: "Allow once", response: "once"},
	{label: "Allow always", response: "always"},
	{label: "Reject", response: "reject"},
}

// permHint is the popover's dim footer (wrapped when the sidebar is
// narrow, never clipped).
const permHint = "↑/↓ select · enter confirm · y/a/n quick · esc later"

// floatCardW — the floating popover card's width, SHARED by the
// permission card and the question card (question_modal.go) so both
// floats land on the same spot: min(60, panel width − 4) with a 24-cell
// floor (on a hairline sidebar the card keeps its floor and the panel
// frame clips it, rather than collapsing into unreadable rails).
func floatCardW(panelW int) int {
	w := panelW - 4
	if w > 60 {
		w = 60
	}
	if w < 24 {
		w = 24
	}
	return w
}

// permCard renders the popover's rows, each EXACTLY cardW display cells
// (rails included), so the View splice replaces whole cells without
// shifting a row. rowOpt[i] is the option index row i answers to on a
// mouse click, or -1 for every chrome row (borders, blanks, text).
func (c *Chat) permCard() (rows []string, rowOpt []int, cardW int) {
	cardW = floatCardW(c.w)
	inner := cardW - 2 // the content column between the │ rails
	push := func(row string, opt int) {
		rows, rowOpt = append(rows, row), append(rowOpt, opt)
	}
	rail := func(s string) string {
		return chrome.WarnText.Render("│") + s + chrome.WarnText.Render("│")
	}
	blank := strings.Repeat(" ", inner)

	push(chrome.WarnText.Render("╭"+strings.Repeat("─", inner)+"╮"), -1)
	push(rail(blank), -1)

	// Title row: WARN-bold header left, dim "N/N" queue badge right —
	// the badge only shows once asks stack (Total > 1).
	title := "PERMISSION REQUIRED"
	badge := ""
	if c.perm.Total > 1 {
		idx := c.perm.Index
		if idx < 1 || idx > c.perm.Total {
			idx = 1 // an out-of-range index clamps to the front
		}
		badge = itoa(idx) + "/" + itoa(c.perm.Total)
	}
	gap := inner - 2 - lipgloss.Width(title) - lipgloss.Width(badge)
	if gap < 1 {
		gap = 1
	}
	push(rail(" "+chrome.WarnBold.Render(title)+strings.Repeat(" ", gap)+
		chrome.DimText.Render(badge)+" "), -1)

	// Description row(s): the amber of the old header without the
	// shouting — "<agent> wants <tool> · <summary>", wrapped, never
	// clipped.
	agent := c.perm.Agent
	if agent == "" {
		agent = defaultBossShort
	}
	desc := agent + " wants " + c.perm.ToolName
	if c.perm.Summary != "" {
		desc += " · " + c.perm.Summary
	}
	for _, ln := range strings.Split(strings.TrimRight(wrapPlain(desc, inner-2), "\n"), "\n") {
		push(rail(" "+fitPlain(ln, inner-2)+" "), -1)
	}
	push(rail(blank), -1)

	// The option rows: each carries its hit-map index; the highlighted
	// one runs reversed-amber across the WHOLE content column (the
	// opencode look), its neighbors stay plain.
	for i, opt := range permOptions {
		mark := "  "
		if i == c.permSel {
			mark = "› "
		}
		text := fitPlain(" "+mark+opt.label, inner)
		if i == c.permSel {
			text = lipgloss.NewStyle().Foreground(chrome.Warn).Reverse(true).Render(text)
		}
		push(rail(text), i)
	}
	push(rail(blank), -1)

	// Footer hint — dim italic, wrapped to the content column.
	for _, ln := range strings.Split(strings.TrimRight(wrapPlain(permHint, inner-2), "\n"), "\n") {
		push(rail(chrome.DimText.Italic(true).Render(" "+fitPlain(ln, inner-2))+" "), -1)
	}
	push(chrome.WarnText.Render("╰"+strings.Repeat("─", inner)+"╯"), -1)
	return rows, rowOpt, cardW
}

// permCardGeom — where the popover sits, in CHAT CONTENT COORDS (row 0 =
// the viewport's first rendered row): centered over the WHOLE panel on
// both axes, fixed regardless of how the transcript scrolls. The View
// splice and the click hit-map both read this, so the two can never
// disagree about where the card is.
func (c *Chat) permCardGeom() (top, left, cardW int, rows []string, rowOpt []int) {
	rows, rowOpt, cardW = c.permCard()
	top = (c.h - len(rows)) / 2
	if top < 0 {
		top = 0 // a very short panel pins the card to the top edge
	}
	left = (c.w - cardW) / 2
	if left < 0 {
		left = 0
	}
	return top, left, cardW, rows, rowOpt
}

// permVisible reports whether the popover renders: an ask is open AND no
// question popover is up. The question popover OWNS every key (the parked
// turn rule), so a permission arriving under it stays queued in c.perm
// until the question clears — only one float renders at a time.
func (c *Chat) permVisible() bool { return c.perm != nil && c.question == nil }

// permMove wraps the menu cursor by d rows through the three options —
// cycling means ↑ on the top row lands on Reject and ↓/tab on the bottom
// row lands back on Allow once.
func (c *Chat) permMove(d int) {
	n := len(permOptions)
	c.permSel = (c.permSel + d%n + n) % n
}

// permOverlay splices the popover's rows over the assembled background
// lines cell-wise (ANSI-aware): the background pixels outside the card —
// transcript, divider, the LIVE textarea — survive, and the row count
// never changes, so the layout never jumps when a permission opens.
func (c *Chat) permOverlay(bg []string) []string {
	top, left, _, rows, _ := c.permCardGeom()
	for i, row := range rows {
		y := top + i
		if y < 0 || y >= len(bg) {
			continue // a short panel clips the card instead of growing
		}
		bg[y] = permSplice(bg[y], left, row, lipgloss.Width(row), c.w)
	}
	return bg
}

// permSplice writes one card row over the background's cells [x, x+rw),
// keeping whatever the background drew outside that span. ANSI-aware on
// both pieces: styled transcripts (glamour markdown, diff tints, thread
// cards) cut cleanly at cell edges instead of mid-escape.
func permSplice(bg string, x int, row string, rw, w int) string {
	left := ansi.Cut(bg, 0, x)
	if lw := lipgloss.Width(left); lw < x {
		left += strings.Repeat(" ", x-lw) // a short line pads up to the card
	}
	right := ansi.Cut(bg, x+rw, w)
	return left + row + right
}

// PermClick answers WHICHEVER floating popover card is on top — the name
// is historical (the app's one click path calls PermClick + ClickRow and
// stays untouched): while a QUESTION popover is open the permission card
// can't even render (permVisible), so the click belongs to the question
// card and this routes it to questionClick — option rows submit/toggle
// like their keys, the Submit row submits, the custom/text rows just
// grab the cursor, chrome claims. With only a permission open it answers
// the permission from a mouse CLICK at (x, y) in chat content coords
// (the app's click handler translates screen → content exactly like it
// does for ClickRow). It returns the answer cmd — the SAME cmd the key
// path fires — or nil when no popover is visible / the click missed an
// actionable row / no handler is wired. Chrome clicks (borders, title,
// blanks) return nil too: the card swallows them in ClickRow but only
// an option row answers.
func (c *Chat) PermClick(x, y int) tea.Cmd {
	if c.question != nil {
		// the question popover owns the float slot (and every key)
		return c.questionClick(x, y)
	}
	if !c.permVisible() {
		return nil
	}
	top, left, cardW, rows, rowOpt := c.permCardGeom()
	if y < top || y >= top+len(rows) || x < left || x >= left+cardW {
		return nil
	}
	opt := rowOpt[y-top]
	if opt < 0 || c.onPermAnswer == nil {
		return nil
	}
	return c.onPermAnswer(permOptions[opt].response)
}
