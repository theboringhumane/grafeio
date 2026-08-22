// Package panels — the right-hand sidebar tab strip and its six tab
// panels: chat, terminal, agents, board, mail, activity.
//
// tabs.go — the strip itself: a one-row tab bar (active tab accent bg,
// others gray) above a rounded-border panel holding the active tab's
// content. Keys (handled by the app via the keymap): tab/shift+tab cycles,
// 1..6 jumps straight to a tab. A compact display mode shortens the tab
// labels to single letters (/compact — the canonical Title() is untouched,
// so SetActiveByTitle keeps matching the full names).
package panels

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// Tab is one sidebar panel. SetSize hands the panel its content area
// (border already accounted for), SetState pushes the latest office state,
// View renders the content.
type Tab interface {
	Title() string
	SetSize(w, h int)
	SetState(st state.OfficeState)
	View() string
}

// Interactive is the optional key/mouse surface of a Tab (chat needs keys,
// scrollable panels take scroll input).
type Interactive interface {
	Update(msg tea.Msg) tea.Cmd
}

// Tabs is the sidebar strip: tab bar row + active tab in a bordered box.
// compact shortens the bar labels to single letters (display only).
type Tabs struct {
	tabs    []Tab
	active  int
	w, h    int
	compact bool
}

// compactLabels — the /compact sidebar's short tab labels, keyed by the
// canonical Title(). Unknown titles keep their full name.
var compactLabels = map[string]string{
	"chat":     "c",
	"terminal": "t",
	"agents":   "a",
	"board":    "b",
	"mail":     "m",
	"activity": "x",
}

// SetCompact switches the tab-bar label density (the app re-calls it on
// /compact and /mode changes; panel state and titles are untouched).
func (t *Tabs) SetCompact(on bool) { t.compact = on }

// NewTabs wires the strip in tab order. Index 0 is the default active tab.
func NewTabs(tabs ...Tab) *Tabs {
	return &Tabs{tabs: tabs}
}

// ActiveIndex is the selected tab position.
func (t *Tabs) ActiveIndex() int { return t.active }

// SetActive jumps to tab i (clamped), returning false for unknown indexes.
func (t *Tabs) SetActive(i int) bool {
	if i < 0 || i >= len(t.tabs) {
		return false
	}
	t.active = i
	return true
}

// Next / Prev cycle the tab bar (used for tab / shift+tab).
func (t *Tabs) Next() { t.active = (t.active + 1) % len(t.tabs) }
func (t *Tabs) Prev() { t.active = (t.active - 1 + len(t.tabs)) % len(t.tabs) }

// SetActiveByTitle selects a tab by name (case-insensitive); false if absent.
func (t *Tabs) SetActiveByTitle(title string) bool {
	for i, tb := range t.tabs {
		if strings.EqualFold(tb.Title(), title) {
			t.active = i
			return true
		}
	}
	return false
}

// SetSize sizes the whole strip; every tab gets the bordered content area.
func (t *Tabs) SetSize(w, h int) {
	t.w, t.h = w, h
	// 1 row tab bar, box border eats 2 rows + 2 cols
	cw, ch := w-2, h-1-2
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}
	for _, tb := range t.tabs {
		tb.SetSize(cw, ch)
	}
}

// SetState pushes the latest office state into every tab.
func (t *Tabs) SetState(st state.OfficeState) {
	for _, tb := range t.tabs {
		tb.SetState(st)
	}
}

// Update forwards a message to the active tab if it wants keys/mouse.
func (t *Tabs) Update(msg tea.Msg) tea.Cmd {
	if len(t.tabs) == 0 {
		return nil
	}
	if it, ok := t.tabs[t.active].(Interactive); ok {
		return it.Update(msg)
	}
	return nil
}

// View renders: tab bar row + rounded-border box with the active tab.
func (t *Tabs) View() string {
	if len(t.tabs) == 0 {
		return ""
	}

	// tab bar: " 1 chat " segments; active accent bg, others gray. Fall back
	// in three tiers so all six tabs stay readable before we ever clip:
	// numbered " 1 chat " → padded bare " chat " → tight "chat terminal …"
	// (41 cells at six tabs — fits the default 44-col sidebar).
	var barFinal string
	for _, barKind := range []barPad{padNumbered, padBare, padTight} {
		if bar := t.tabBar(barKind); lipgloss.Width(bar) <= t.w {
			barFinal = bar
			break
		}
	}
	if barFinal == "" {
		// narrower still: hard ansi-aware clip (never overflow the strip)
		barFinal = ansi.Truncate(t.tabBar(padTight), t.w, "")
	}
	bar := barFinal

	content := t.tabs[t.active].View()
	ch := t.h - 1
	if ch < 1 {
		ch = 1
	}
	// lipgloss v2: Width/Height INCLUDE the border — pass outer dims.
	box := chrome.PanelBox.Width(t.w).Height(ch).Render(content)
	return lipgloss.JoinVertical(lipgloss.Left, bar, box)
}

// barPad — label density tiers for the tab bar, widest first.
type barPad int

const (
	padNumbered barPad = iota // " 1 chat " — full labels + jump numbers
	padBare                   // " chat "   — padded bare titles
	padTight                  // "chat"     — single-space separators (last resort)
)

// tabBar composes the strip row at the given label density.
func (t *Tabs) tabBar(pad barPad) string {
	var segs []string
	for i, tb := range t.tabs {
		title := tb.Title()
		if t.compact {
			if short, ok := compactLabels[title]; ok {
				title = short
			}
		}
		var label string
		switch pad {
		case padNumbered:
			label = fmt.Sprintf(" %d %s ", i+1, title)
		case padBare:
			label = " " + title + " "
		default:
			label = title
		}
		if i == t.active {
			segs = append(segs, chrome.TabActive.Render(label))
		} else {
			segs = append(segs, chrome.TabInactive.Render(label))
		}
	}
	bar := strings.Join(segs, " ")
	if pad == padTight {
		bar = " " + bar // one leading cell keeps the box border readable
	}
	return bar
}

// --- shared panel helpers -------------------------------------------------

// fit clips/pads a plain-text block to exactly h lines, each at most w
// columns wide (panels truncate their own rows before styling, so this only
// ever sees already-narrow text).
func fit(s string, h int) string {
	if h < 1 {
		h = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// clipPlain truncates a plain (no-ANSI) string to w display cells.
func clipPlain(s string, w int) string {
	if w < 0 {
		w = 0
	}
	n := 0
	for i := range s {
		if n >= w {
			return s[:i]
		}
		n++
	}
	return s
}

// fitPlain pads a plain string to width w with spaces.
func fitPlain(s string, w int) string {
	s = clipPlain(s, w)
	for lipgloss.Width(s) < w {
		s += " "
	}
	return s
}
