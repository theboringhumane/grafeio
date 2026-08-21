// agents.go — AGENTS roster tab (port of node-legacy panels/agents.tsx):
// boss pinned first (brain.json boss.name, default "boss (oikonomos)")
// yellow bold; one row per employee:
//
//	<name (role color)>  <glyph> <sprite word (semantic color)>   <task, dim, right>
//
// blocked (at-mailbox) employees show "blocked" in bold red.
package panels

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

// sprite word per SpriteState (port of WORD in agents.tsx).
func spriteWord(s state.SpriteState) string {
	switch s {
	case state.SpriteWorking:
		return "working"
	case state.SpriteToManager, state.SpriteMeeting:
		return "meeting"
	case state.SpriteToCoffee, state.SpriteCoffee:
		return "coffee"
	case state.SpriteAtMailbox:
		return "blocked"
	default:
		return "at desk"
	}
}

// wordStyle — semantic chrome color per sprite word (port of WORD_STYLE).
func wordStyle(word string, s string) string {
	switch word {
	case "working":
		return chrome.Fg(chrome.Info, s)
	case "meeting":
		return chrome.Fg(chrome.Accent, s)
	case "coffee":
		return lipgloss.NewStyle().Foreground(chrome.Accent).Faint(true).Render(s)
	case "blocked":
		return lipgloss.NewStyle().Foreground(chrome.Err).Bold(true).Render(s)
	case "at desk":
		return chrome.Fg(chrome.Dim, s)
	default:
		return chrome.Fg(chrome.White, s)
	}
}

// Agents is the roster tab panel.
type Agents struct {
	vp       viewport.Model
	st       state.OfficeState
	w, h     int
	rev      string
	bossName string // cfg.Boss.Name — the human label pinned first (default below)
	selected string // floor-click selection: a "▸" marker + bold row (popover.go SetSelected)
}

// defaultBossName is the pinned boss label when no config name is set.
const defaultBossName = "boss (oikonomos)"

// NewAgents builds the roster panel.
func NewAgents() *Agents {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(5))
	vp.MouseWheelEnabled = true
	return &Agents{vp: vp}
}

// SetBossName personalizes the pinned boss row's label (brain.json
// boss.name). Empty restores the default. Re-renders immediately.
func (a *Agents) SetBossName(name string) {
	if a.bossName == name {
		return
	}
	a.bossName = name
	a.SetState(a.st)
}

// bossLabel — the pinned first row's human name (config or default).
func (a *Agents) bossLabel() string {
	if a.bossName == "" {
		return defaultBossName
	}
	return a.bossName
}

// Title implements Tab.
func (a *Agents) Title() string { return "agents" }

// SetSize implements Tab.
func (a *Agents) SetSize(w, h int) {
	a.w, a.h = w, h
	a.vp.SetWidth(w)
	if h-1 > 0 {
		a.vp.SetHeight(h - 1) // header row
	}
	a.SetState(a.st)
}

// SetState implements Tab.
func (a *Agents) SetState(st state.OfficeState) {
	a.st = st
	content := a.render()
	if content != a.rev {
		a.rev = content
		a.vp.SetContent(content)
	}
}

// Update implements Interactive (scroll).
func (a *Agents) Update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		var cmd tea.Cmd
		a.vp, cmd = a.vp.Update(msg)
		return cmd
	}
	return nil
}

// View implements Tab.
func (a *Agents) View() string {
	return fit(chrome.Header.Render("AGENTS")+"\n"+a.vp.View(), a.h)
}

// render — boss first, then the rest; task right-aligned when it fits.
func (a *Agents) render() string {
	if len(a.st.Employees) == 0 {
		return chrome.DimText.Render("- empty -")
	}
	ordered := make([]state.Employee, 0, len(a.st.Employees))
	for _, e := range a.st.Employees {
		if e.Role == state.RoleManager {
			ordered = append(ordered, e)
		}
	}
	for _, e := range a.st.Employees {
		if e.Role != state.RoleManager {
			ordered = append(ordered, e)
		}
	}

	var b strings.Builder
	for _, e := range ordered {
		isBoss := e.Role == state.RoleManager
		label := e.Name
		if isBoss {
			label = a.bossLabel()
		}
		word := spriteWord(e.Sprite)
		c := chrome.RoleColor(label)

		// floor-click selection marker: "▸ " + bold row (2 cells of the
		// width budget go to the marker)
		sel := a.selected != "" && e.Name == a.selected
		marker := "  "
		if sel {
			marker = lipgloss.NewStyle().Foreground(c).Bold(true).Render("▸ ")
		}
		leftPlain := label + " " + chrome.RoleGlyph(e.Role) + " " + word
		task := e.Task
		room := a.w - len(leftPlain) - 1 - 2
		if task != "" && room >= 6 && len(task) > room {
			task = task[:room-3] + "..." // ellipsis on TASK TEXT (machine), not NL
		}
		gap := a.w - len(leftPlain) - len(task) - 2
		if gap < 1 {
			gap = 1
		}

		var left string
		if isBoss {
			left = lipgloss.NewStyle().Foreground(c).Bold(true).Render(label) + " " +
				chrome.Fg(c, chrome.RoleGlyph(e.Role)) + " " +
				wordStyle(word, word)
		} else {
			left = chrome.Fg(c, label) + " " +
				chrome.Fg(c, chrome.RoleGlyph(e.Role)) + " " +
				wordStyle(word, word)
		}
		if sel {
			left = lipgloss.NewStyle().Bold(true).Render(left)
		}
		line := marker + left + strings.Repeat(" ", gap)
		if task != "" && gap > 1 {
			line += chrome.DimText.Render(task)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
