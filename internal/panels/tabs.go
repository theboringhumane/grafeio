// Package panels — the right-hand sidebar tab strip and its five tab
// panels: chat, agents, board, mail, activity.
//
// tabs.go — the strip itself: a one-row tab bar (active tab accent bg,
// others gray) above a rounded-border panel holding the active tab's
// content. Keys (handled by the app via the keymap): tab/shift+tab cycles,
// 1..5 jumps straight to a tab.
package panels

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
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
type Tabs struct {
	tabs   []Tab
	active int
	w, h   int
}

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

	// tab bar: " 1 chat " segments; active accent bg, others gray. When the
	// numbered labels overflow, fall back to bare titles so all five fit.
	bar := t.tabBar(true)
	if lipgloss.Width(bar) > t.w {
		bar = t.tabBar(false)
	}
	if lipgloss.Width(bar) > t.w {
		// narrower still: hard ansi-aware clip (never overflow the strip)
		bar = ansi.Truncate(bar, t.w, "")
	}

	content := t.tabs[t.active].View()
	ch := t.h - 1
	if ch < 1 {
		ch = 1
	}
	// lipgloss v2: Width/Height INCLUDE the border — pass outer dims.
	box := chrome.PanelBox.Width(t.w).Height(ch).Render(content)
	return lipgloss.JoinVertical(lipgloss.Left, bar, box)
}

// tabBar composes the strip row; numbered=true prefixes "1 "…"5 ".
func (t *Tabs) tabBar(numbered bool) string {
	var segs []string
	for i, tb := range t.tabs {
		label := " " + tb.Title() + " "
		if numbered {
			label = fmt.Sprintf(" %d %s ", i+1, tb.Title())
		}
		if i == t.active {
			segs = append(segs, chrome.TabActive.Render(label))
		} else {
			segs = append(segs, chrome.TabInactive.Render(label))
		}
	}
	return strings.Join(segs, " ")
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
