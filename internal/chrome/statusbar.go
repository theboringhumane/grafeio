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

	"github.com/theboringhumane/grafeio/internal/state"
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
		OnBar(OK, fmt.Sprintf("%d", done)) +
		OnBar(White, " | ") +
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
