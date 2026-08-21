// popover.go — the SLASH popover: opencode-style command discoverability
// for the chat textarea, reusing the @ picker's PanelBox pattern.
//
// Two modes, one box:
//
//	cmd    — "/" at a word start opens a list of every slash command with
//	         a one-line description, prefix-filtered live as the fragment
//	         grows ("/th" → /theme /themes /thinking). ↑/↓ move, Enter/Tab
//	         INSERT the command into the draft tail (never auto-sends);
//	         "/theme" pre-fills "/theme " and flips the box into…
//	theme  — the same box lists live theme names, filtered by the tail
//	         after "/theme ". Arrowing through them applies a LIVE PREVIEW
//	         (chrome + floor palette re-point immediately); Enter commits
//	         through the normal /theme slash path (switch + persist +
//	         office notice), esc reverts back to the theme that was active
//	         when the preview session opened.
//
// Esc closes the box keeping the typed fragment; backspacing over the
// lone "/" (or any edit that breaks the tail match) closes it too — the
// same live/die-by-tail contract as the @ picker.
//
// Also hosts two tiny additive helpers other surfaces needed for the
// mouse wave: Tabs.ContentOffset (screen→content geometry for click
// routing) and Agents.SetSelected (roster highlight for floor clicks).
package panels

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/office"
)

// slashCommand — one row of the popover: the command, its one-line
// description, and its argument usage ("" → applied bare).
type slashCommand struct {
	name  string
	desc  string
	usage string
}

// slashCommands — the full local command list, display order (/theme
// before /themes so a "/th" fragment lands on the switcher first).
// Only commands the app actually implements — the box is a menu of REAL
// outcomes, never of wishes.
var slashCommands = []slashCommand{
	{"/help", "this list", ""},
	{"/theme", "switch theme (persists)", "/theme <name>"},
	{"/themes", "list themes", ""},
	{"/thinking", "show/hide thinking blocks", "/thinking on|off"},
	{"/tools", "show/hide tool one-liners", "/tools on|off"},
	{"/status", "office status", ""},
	{"/clear", "empty the chat", ""},
	{"/queue", "show the backlog (clear drops it)", ""},
	{"/route", "force-dispatch the backlog now", ""},
	{"/perm", "re-open an esc'd permission prompt", ""},
	{"/diffs", "expand/collapse file diffs", "/diffs on|off"},
	{"/question", "re-open a deferred boss question", ""},
	{"/power", "power governor", "/power auto|performance|saver"},
	{"/model", "boss model", "/model provider/model"},
	{"/compact", "compact layout this session", "/compact on|off"},
	{"/mode", "layout mode (persists)", "/mode normal|compact"},
	{"/wide", "sidebar width (0 = default)", "/wide 26..100"},
	{"/zen", "fullscreen floor, any key exits", ""},
	{"/focus", "alias of /zen", "/focus floor"},
	{"/quit", "exit grafeio", ""},
}

// slash popover modes.
const (
	slashModeCmd = iota + 1
	slashModeTheme
)

// slashVisibleRows — the popover list window (same budget as the @ picker).
const slashVisibleRows = 8

// ContentOffset — the sidebar's box-chrome inset: the active tab's content
// starts 1 column and 2 rows below the sidebar's outer edge (border col +
// tab-bar row + border row). The app adds its floor width + topbar row for
// screen coords. Geometry of tabs.go's View, kept as ONE exported fact.
func (t *Tabs) ContentOffset() (dx, dy int) { return 1, 2 }

// SetSelected marks a roster entry as floor-selected (empty clears). The
// chat notice names the agent; the agents tab pins the visual — a "▸"
// marker + bold name on its row. Idempotent.
func (a *Agents) SetSelected(name string) {
	if a.selected == name {
		return
	}
	a.selected = name
	a.rev = "" // force re-render past the SetState revision gate
	a.SetState(a.st)
}

// slashFragmentOf extracts the live slash fragment from a draft value —
// the tail-word contract of the @ picker:
//
//	"/th"          → cmd mode, frag "th"
//	"/th*" after a word start (mid-draft) is fine too
//	"/theme "      → theme mode, frag ""
//	"/theme no"    → theme mode, frag "no"
//	anything else  → ok=false (the popover closes)
//
// Theme mode only engages when "/theme" is the FIRST word (it applies a
// live preview to the whole UI — a mid-sentence "/theme" fragment must
// never preview).
func slashFragmentOf(v string) (mode int, frag string, ok bool) {
	r := []rune(v)
	// tail word = runes after the last whitespace
	ws := -1
	for i := len(r) - 1; i >= 0; i-- {
		if unicode.IsSpace(r[i]) {
			ws = i
			break
		}
	}
	tail := string(r[ws+1:])
	if strings.HasPrefix(tail, "/") {
		return slashModeCmd, tail[1:], true
	}
	words := strings.Fields(v)
	if tail == "" {
		if len(words) == 1 && words[0] == "/theme" {
			return slashModeTheme, "", true
		}
		return 0, "", false
	}
	if len(words) == 2 && words[0] == "/theme" {
		return slashModeTheme, words[1], true
	}
	return 0, "", false
}

// openSlashPicker opens the popover at the just-typed "/": cmd mode,
// empty fragment, selection at the top. The @ picker (if open) closes —
// one box owns the region at a time.
func (c *Chat) openSlashPicker() tea.Cmd {
	c.closeAttachPicker()
	c.slashOpen = true
	c.slashMode = slashModeCmd
	c.slashFrag = ""
	c.slashSel = 0
	c.slashPrevTheme = ""
	c.refilterSlash()
	return nil
}

// closeSlashPicker closes the popover, keeping the typed fragment in the
// draft (esc semantics). revert=true unwinds a live theme preview back to
// the pre-popover theme; a commit passes false (the preview WAS the pick).
func (c *Chat) closeSlashPicker(revert bool) {
	if !c.slashOpen {
		return
	}
	if revert {
		c.revertThemePreview()
	}
	c.slashOpen = false
	c.slashPrevTheme = ""
	c.SetSize(c.w, c.h)
}

// afterSlashEdit re-derives the popover state from the draft tail after
// the textarea consumed a typed/backspace key — the popover lives only
// while the tail still matches a command/theme fragment. A mode flip
// ("/theme" → space) re-opens in the new mode; leaving theme mode unwinds
// any live preview first.
func (c *Chat) afterSlashEdit() {
	mode, frag, ok := slashFragmentOf(c.ta.Value())
	if !ok {
		c.closeSlashPicker(true)
		return
	}
	if mode != c.slashMode {
		if c.slashMode == slashModeTheme {
			c.revertThemePreview() // backtracking out of "/theme " unwinds any preview
		}
		c.slashMode = mode
		c.slashSel = 0
		c.slashFrag = frag
		if mode == slashModeTheme {
			c.capturePrevTheme()
		}
		c.refilterSlash()
		return
	}
	if frag != c.slashFrag {
		c.slashFrag = frag
		c.slashSel = 0
		c.refilterSlash()
	}
}

// refilterSlash recomputes the visible rows (prefix match on the command
// name / theme name, case-insensitive — predictable while you type) and
// clamps the selection. The popover height follows the count, so the
// layout re-splits.
func (c *Chat) refilterSlash() {
	frag := strings.ToLower(c.slashFrag)
	switch c.slashMode {
	case slashModeTheme:
		c.slashThemes = c.slashThemes[:0]
		for _, n := range chrome.ThemeNames() {
			if frag == "" || strings.HasPrefix(strings.ToLower(n), frag) {
				c.slashThemes = append(c.slashThemes, n)
			}
		}
	default:
		c.slashCmds = c.slashCmds[:0]
		for _, sc := range slashCommands {
			if frag == "" || strings.HasPrefix(sc.name[1:], frag) {
				c.slashCmds = append(c.slashCmds, sc)
			}
		}
	}
	if n := c.slashCount(); c.slashSel >= n {
		c.slashSel = n - 1
	}
	if c.slashSel < 0 {
		c.slashSel = 0
	}
	c.SetSize(c.w, c.h)
}

// slashCount — rows in the active mode's filtered list.
func (c *Chat) slashCount() int {
	if c.slashMode == slashModeTheme {
		return len(c.slashThemes)
	}
	return len(c.slashCmds)
}

// slashMove walks the selection up/down the filtered window (wraps). In
// theme mode the new selection previews LIVE — the chrome palette and the
// floor palette follow the highlight before any commit.
func (c *Chat) slashMove(d int) {
	if n := c.slashCount(); n > 0 {
		c.slashSel = (c.slashSel + d + n) % n
	}
	if c.slashMode == slashModeTheme {
		c.previewTheme()
	}
}

// capturePrevTheme remembers the active theme when a preview session
// starts (so esc/backtrack can unwind it). Captured once per session.
func (c *Chat) capturePrevTheme() {
	if c.slashPrevTheme == "" {
		c.slashPrevTheme = chrome.CurrentTheme().Name
	}
}

// previewTheme applies the highlighted theme immediately (chrome styles +
// floor palette + chat re-render). This is the LIVE preview — arrows are
// the "switch now" gesture; only Enter persists.
func (c *Chat) previewTheme() {
	if len(c.slashThemes) == 0 {
		return
	}
	c.capturePrevTheme()
	name := c.slashThemes[c.slashSel]
	if chrome.SetTheme(name) {
		office.SetTheme(name) // floor palette follows chrome
		c.RefreshTheme()
	}
}

// revertThemePreview unwinds a live preview back to the theme captured at
// session start (esc / backtrack-out-of-theme-mode).
func (c *Chat) revertThemePreview() {
	if c.slashPrevTheme == "" || c.slashPrevTheme == chrome.CurrentTheme().Name {
		return
	}
	if chrome.SetTheme(c.slashPrevTheme) {
		office.SetTheme(c.slashPrevTheme)
		c.RefreshTheme()
	}
}

// slashPicked applies the highlighted row. cmd mode INSERTS the command
// into the draft tail (trailing space when it takes an argument; "/theme"
// flips the popover into theme mode instead of closing) — Enter never
// auto-sends. theme mode COMMITS: the draft clears and the pick goes
// through the plain /theme slash path (switch + persist + office notice).
func (c *Chat) slashPicked() tea.Cmd {
	if c.slashCount() == 0 {
		c.closeSlashPicker(false)
		return nil
	}
	if c.slashMode == slashModeTheme {
		name := c.slashThemes[c.slashSel]
		c.ta.SetValue("")
		c.closeSlashPicker(false) // the preview WAS the pick — keep it
		c.SetSize(c.w, c.h)
		if c.onSend != nil {
			return c.onSend("/theme "+name, nil)
		}
		return nil
	}
	item := c.slashCmds[c.slashSel]
	if item.name == "/theme" {
		c.ta.SetValue("/theme ")
		c.slashMode = slashModeTheme
		c.slashFrag = ""
		c.slashSel = 0
		c.capturePrevTheme()
		c.refilterSlash()
		return nil
	}
	// replace the "/frag" tail with the command (cursor-at-tail, same
	// contract as the @ picker's attachPicked)
	r := []rune(c.ta.Value())
	drop := len([]rune(c.slashFrag)) + 1 // fragment + the "/" itself
	if drop <= len(r) {
		c.ta.SetValue(string(r[:len(r)-drop]) + item.name + slashArgSpace(item))
	}
	c.closeSlashPicker(false)
	c.SetSize(c.w, c.h)
	return nil
}

// slashArgSpace — trailing space to type after an applied command: one
// when it takes an argument, none for a bare command.
func slashArgSpace(sc slashCommand) string {
	if sc.usage != "" {
		return " "
	}
	return ""
}

// slashH — rows the open popover consumes (SetSize budget): list window +
// header + footer + the PanelBox border pair. 0 while closed.
func (c *Chat) slashH() int {
	if !c.slashOpen {
		return 0
	}
	return c.slashPopoverRows() + 4
}

// slashPopoverRows — visible list rows for the SetSize layout budget and
// the render window (1 "(no matches)" row when the filter is empty).
func (c *Chat) slashPopoverRows() int {
	rows := c.slashCount()
	if rows > slashVisibleRows {
		rows = slashVisibleRows
	}
	if rows == 0 {
		rows = 1
	}
	return rows
}

// renderSlashPopover draws the box (slashPopoverRows()+4 rows — header,
// footer, border pair). Selected row accented under a "›" marker; the
// footer carries the selected command's usage preview ("/theme <name>")
// or the key hints in theme mode.
func (c *Chat) renderSlashPopover() string {
	inner := c.w - 2 // PanelBox border columns
	if inner < 1 {
		inner = 1
	}
	lines := make([]string, 0, c.slashPopoverRows()+2)
	var names []string
	header := "commands"
	if c.slashMode == slashModeTheme {
		header = "theme preview"
		names = c.slashThemes
	} else {
		names = make([]string, len(c.slashCmds))
		for i, sc := range c.slashCmds {
			names[i] = sc.name
		}
	}
	lines = append(lines, chrome.Header.Render(fitPlain(header, inner)))
	if len(names) == 0 {
		lines = append(lines, chrome.DimText.Render(fitPlain("(no matches)", inner)))
	}
	start := 0
	if c.slashSel >= slashVisibleRows {
		start = c.slashSel - slashVisibleRows + 1
	}
	end := start + slashVisibleRows
	if end > len(names) {
		end = len(names)
	}
	for i := start; i < end; i++ {
		label := names[i]
		if c.slashMode == slashModeCmd {
			// one-line description beside the command (mixed-style string,
			// so the fit is ANSI-aware)
			label = names[i] + "  " + chrome.DimText.Render(c.slashCmds[i].desc)
		}
		if i == c.slashSel {
			lines = append(lines, chrome.AccentText.Render(fitLabel("› "+label, inner)))
		} else {
			lines = append(lines, fitLabel("  "+label, inner))
		}
	}
	footer := "enter: apply · ↑↓: move · esc: close"
	if c.slashMode == slashModeTheme {
		footer = "enter: commit + persist · esc: cancel preview"
	} else if len(c.slashCmds) > 0 && c.slashCmds[c.slashSel].usage != "" {
		// prefill preview subline for argument-taking commands
		footer = c.slashCmds[c.slashSel].usage
	}
	lines = append(lines, chrome.DimText.Render(fitPlain(footer, inner)))
	return chrome.PanelBox.Width(c.w).Render(strings.Join(lines, "\n"))
}

// fitLabel clips/pads an ANSI-styled string to exactly w display cells
// (fitPlain is rune-based and would slice through escape sequences).
func fitLabel(s string, w int) string {
	if w < 0 {
		w = 0
	}
	if lipgloss.Width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	for lipgloss.Width(s) < w {
		s += " "
	}
	return s
}
