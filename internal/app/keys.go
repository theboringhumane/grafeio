// keys.go — the app's keymap, in one place so the statusbar's hint segment
// and the actual key handling can never drift apart. q/ctrl+c quit,
// tab/shift+tab cycle panels, 1..5 jump, ↑↓/pgup/pgdn/wheel scroll,
// enter sends chat.
package app

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
)

// KeyMap — global app bindings.
type KeyMap struct {
	Quit   key.Binding
	Next   key.Binding
	Prev   key.Binding
	Scroll key.Binding
	Send   key.Binding
	Tab1   key.Binding
	Tab2   key.Binding
	Tab3   key.Binding
	Tab4   key.Binding
	Tab5   key.Binding
}

// NewKeyMap returns the default bindings.
func NewKeyMap() KeyMap {
	return KeyMap{
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Next:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "panels")),
		Prev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "panels")),
		Scroll: key.NewBinding(key.WithKeys("up", "down", "pgup", "pgdown"), key.WithHelp("↑↓", "scroll")),
		Send:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		Tab1:   key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "chat")),
		Tab2:   key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "agents")),
		Tab3:   key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "board")),
		Tab4:   key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "mail")),
		Tab5:   key.NewBinding(key.WithKeys("5"), key.WithHelp("5", "activity")),
	}
}

// ShortHelp is the statusbar segment, in display order.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Next, k.Scroll, k.Send, k.Quit}
}

// HintLine renders the static statusbar hint, keymap-driven:
// "tab:panels · ↑↓:scroll · enter:send · q:quit"
func (k KeyMap) HintLine() string {
	parts := make([]string, 0, len(k.ShortHelp()))
	for _, b := range k.ShortHelp() {
		h := b.Help()
		parts = append(parts, h.Key+":"+h.Desc)
	}
	return strings.Join(parts, " · ")
}

// ShortHelpView renders the same ShortHelp set through bubbles/help (the
// "… for non-devs" surface); used anywhere a taller help strip is wanted.
func (k KeyMap) ShortHelpView() string {
	h := help.New()
	h.ShortSeparator = "  "
	return h.ShortHelpView(k.ShortHelp())
}

// TabJump maps a 1..5 keypress to a tab index, or -1.
func (k KeyMap) TabJump(s string) int {
	switch s {
	case "1":
		return 0
	case "2":
		return 1
	case "3":
		return 2
	case "4":
		return 3
	case "5":
		return 4
	}
	return -1
}
