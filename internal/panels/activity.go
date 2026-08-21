// activity.go — ACTIVITY tab (new in v2): a rolling log of every processed
// office event, one line each, capped at 50, with dim timestamps drawn from
// the office tick clock. Newest at the bottom; the pane follows the tail.
package panels

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

// ActivityCap bounds the rolling log.
const ActivityCap = 50

// Activity is the event-log tab panel.
type Activity struct {
	vp    viewport.Model
	lines []string
	w, h  int
}

// NewActivity builds the log panel.
func NewActivity() *Activity {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(5))
	vp.MouseWheelEnabled = true
	return &Activity{vp: vp}
}

// Title implements Tab.
func (a *Activity) Title() string { return "activity" }

// SetSize implements Tab.
func (a *Activity) SetSize(w, h int) {
	a.w, a.h = w, h
	a.vp.SetWidth(w)
	if h-1 > 0 {
		a.vp.SetHeight(h - 1)
	}
	a.SetState(state.OfficeState{})
}

// SetState implements Tab (activity is driven by Add, state push is a no-op
// refresh).
func (a *Activity) SetState(_ state.OfficeState) {
	a.vp.SetContent(a.render())
	a.vp.GotoBottom()
}

// Add appends one pre-formatted log line ("[09:12] dispatch — …"),
// enforcing the cap.
func (a *Activity) Add(line string) {
	a.lines = append(a.lines, line)
	if len(a.lines) > ActivityCap {
		a.lines = a.lines[len(a.lines)-ActivityCap:]
	}
	a.SetState(state.OfficeState{})
}

// Update implements Interactive (scroll).
func (a *Activity) Update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		var cmd tea.Cmd
		a.vp, cmd = a.vp.Update(msg)
		return cmd
	}
	return nil
}

// View implements Tab.
func (a *Activity) View() string {
	return fit(chrome.Header.Render("ACTIVITY")+"\n"+a.vp.View(), a.h)
}

// render — dim "[HH:MM]" timestamps, default-text event text.
func (a *Activity) render() string {
	if len(a.lines) == 0 {
		return chrome.DimText.Render("- quiet floor -")
	}
	var b strings.Builder
	for _, line := range a.lines {
		row := clipPlain(line, a.w)
		if end := strings.Index(row, "]"); strings.HasPrefix(row, "[") && end > 0 {
			b.WriteString(chrome.DimText.Render(row[:end+1]) + row[end+1:])
		} else {
			b.WriteString(row)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
