// statusbar.go — one-line status bar, full width (port of node-legacy
// statusbar.tsx), plus the static keymap hint segment for non-devs:
//
//	left:  <statusLine>   (heuristic color: blocked|failed|offline → red,
//	                       live → green, demo → yellow, else dim)
//	right: <hint> · <n> agents | board p/i/d | <mode>
//	                       (agents cyan, p yellow, i cyan, d green,
//	                        mode yellow|green, hint gray)
package chrome

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// statusLineColor — neutral/attention color for the free-text status line.
// NOTE: this is a machine chrome heuristic on a UI string, not member NL
// (same as the TS heuristic). Machine prefixes (“blocked[”, status words).
func statusLineColor(line string) (c color.Color, dim bool) {
	s := strings.ToLower(line)
	switch {
	case strings.Contains(s, "blocked"), strings.Contains(s, "failed"), strings.Contains(s, "offline"):
		return Err, false
	case strings.Contains(s, "live"):
		return OK, false
	case strings.Contains(s, "demo"):
		return Accent, false
	default:
		return Dim, true
	}
}

// StatusBarZen — the /zen fullscreen-floor status line: a minimal bar with
// just the zen marker, the office clock and the exit hint (any key leaves
// zen; ctrl+q quits the app).
func StatusBarZen(st state.OfficeState, width int) string {
	left := OnBar(Dim, " zen · "+OfficeClock(st.Tick)+" ")
	right := OnBar(Dim, "any key exits · ctrl+q quits ")
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	return Bar.Width(width).Render(line)
}

// StatusBar renders the full-width status bar for one frame.
// hint is the pre-rendered keymap segment (gray), e.g. from
// app keys: "tab:panels · ↑↓:scroll · enter:send · q:quit".
// queueN > 0 appends an amber queue badge ("· qN") — messages enqueued
// while the boss reply is pending.
func StatusBar(st state.OfficeState, hint string, queueN, width int) string {
	var pending, doing, done int
	for _, t := range st.Tasks {
		switch t.Status {
		case state.TaskPending:
			pending++
		case state.TaskInProgress:
			doing++
		case state.TaskDone:
			done++
		}
	}
	agents := fmt.Sprintf("%d", len(st.Employees))

	c, dim := statusLineColor(st.StatusLine)
	leftText := " " + st.StatusLine
	var left string
	if dim {
		left = OnBar(Dim, leftText)
	} else {
		left = OnBar(c, leftText)
	}

	counts := OnBar(Info, agents) +
		OnBar(White, " agents | board ") +
		OnBar(Accent, fmt.Sprintf("%d", pending)) +
		OnBar(White, "/") +
		OnBar(Info, fmt.Sprintf("%d", doing)) +
		OnBar(White, "/") +
		OnBar(OK, fmt.Sprintf("%d", done))
	// The usage tag rides inside `counts`, immediately before the mode
	// segment, so every existing truncation rule keeps working untouched
	// (hint drops first, then the left status line shrinks; counts always
	// survives). Empty while no REAL usage has been reported — the bar is
	// byte-identical to before in that case.
	if tag := usageTag(st); tag != "" {
		counts += OnBar(White, " | ") + OnBar(Dim, tag)
	}
	counts += OnBar(White, " | ") +
		OnBar(ModeColor(st.Mode), string(st.Mode)) +
		OnBar(White, " ")

	segs := []string{counts}
	if queueN > 0 {
		segs = append([]string{OnBarBold(Warn, fmt.Sprintf("q%d", queueN))}, segs...)
	}
	if hint != "" {
		segs = append([]string{OnBar(Dim, hint)}, segs...)
	}
	right := strings.Join(segs, OnBar(Dim, " · "))

	// shrink from the left edge: trim the status line first, then the hint
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	if leftW+1+rightW > width {
		avail := width - 1 - rightW
		if avail < 0 {
			avail = 0
		}
		if lipgloss.Width(leftText) > avail {
			short := leftText
			if len(short) > avail {
				short = short[:avail]
			}
			left = OnBar(c, short)
			leftW = lipgloss.Width(left)
		}
	}
	if leftW+1+lipgloss.Width(right) > width {
		right = counts // hint drops first on narrow terminals
	}

	gap := width - leftW - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	return Bar.Width(width).Render(line)
}

// usageTag renders the cost/token segment ("$0.0042 · 12.4k tok") from the
// conversation's REAL, opencode-reported totals (state.OfficeState
// TokensIn/TokensOut/CostUSD, accumulated from EvUsage deltas). "" while
// both counters are zero — the segment hides itself entirely rather than
// show an estimated or fabricated number. The $ figure leads and is
// dropped on its own when only tokens are known (real cost data absent);
// theboringoffice NEVER prices tokens itself.
func usageTag(st state.OfficeState) string {
	toks := st.TokensIn + st.TokensOut
	var parts []string
	if st.CostUSD > 0 {
		parts = append(parts, costUSD(st.CostUSD))
	}
	if toks > 0 {
		parts = append(parts, humanTokens(toks)+" tok")
	}
	return strings.Join(parts, " · ")
}

// humanTokens compacts a token count: whole numbers under 1000, one-decimal
// k/M above (999 → "999", 1234 → "1.2k", 2_500_000 → "2.5M").
func humanTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// costUSD formats the opencode-reported bill: 4 decimals under $1 (the
// interesting per-turn range), 2 above ("$0.0042", "$1.23").
func costUSD(usd float64) string {
	if usd < 1 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}
