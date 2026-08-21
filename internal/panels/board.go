// board.go — BOARD tab (port of node-legacy panels/taskboard.tsx):
// three columns PENDING | DOING | DONE, headers in status color
// (PENDING yellow, DOING cyan, DONE green) with per-row owner tag in the
// owner's role color; DONE rows dimmed. Rows are sorted by task.At.
package panels

import (
	"image/color"
	"sort"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

// Board is the kanban tab panel.
type Board struct {
	vp   viewport.Model
	st   state.OfficeState
	w, h int
	rev  string
}

// NewBoard builds the board panel.
func NewBoard() *Board {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(5))
	vp.MouseWheelEnabled = true
	return &Board{vp: vp}
}

// Title implements Tab.
func (b *Board) Title() string { return "board" }

// SetSize implements Tab.
func (b *Board) SetSize(w, h int) {
	b.w, b.h = w, h
	b.vp.SetWidth(w)
	if h-1 > 0 {
		b.vp.SetHeight(h - 1)
	}
	b.SetState(b.st)
}

// SetState implements Tab.
func (b *Board) SetState(st state.OfficeState) {
	b.st = st
	content := b.render()
	if content != b.rev {
		b.rev = content
		b.vp.SetContent(content)
	}
}

// Update implements Interactive (scroll).
func (b *Board) Update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		var cmd tea.Cmd
		b.vp, cmd = b.vp.Update(msg)
		return cmd
	}
	return nil
}

// View implements Tab.
func (b *Board) View() string {
	return fit(chrome.Header.Render("BOARD")+"\n"+b.vp.View(), b.h)
}

type boardCol struct {
	title  string
	status state.TaskStatus
	color  color.Color
}

var boardCols = []boardCol{
	{"PENDING", state.TaskPending, chrome.Accent},
	{"DOING", state.TaskInProgress, chrome.Info},
	{"DONE", state.TaskDone, chrome.OK},
}

// render — three tight columns, rows clipped to the column width.
func (b *Board) render() string {
	width := b.w
	gap := 1
	colW := (width - gap*(len(boardCols)-1)) / len(boardCols)
	if colW < 6 {
		colW = width // hopeless narrow: stack single column
	}

	rendered := make([]string, 0, len(boardCols))
	for _, col := range boardCols {
		var rows []state.BoardTask
		for _, t := range b.st.Tasks {
			if t.Status == col.status {
				rows = append(rows, t)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].At < rows[j].At })

		done := col.status == state.TaskDone
		var lines []string
		lines = append(lines, lipgloss.NewStyle().Foreground(col.color).Bold(true).Underline(true).
			Render(clipPlain(col.title, colW)))
		if len(rows) == 0 {
			lines = append(lines, chrome.DimText.Render("-"))
		}
		for _, t := range rows {
			// clip plain parts first, then color the pieces
			lines = append(lines, renderTaskRow(t, col.color, done, colW))
		}
		// pad column to colW so JoinHorizontal doesn't smear
		rendered = append(rendered, lipgloss.NewStyle().Width(colW).Render(strings.Join(lines, "\n")))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, paddedJoin(rendered, gap)...)
}

// renderTaskRow builds one row from plain parts (title then owner), clipped
// as plain text before any styling is applied.
func renderTaskRow(t state.BoardTask, color color.Color, done bool, colW int) string {
	title := clipPlain(t.Title, colW)
	owner := ""
	if t.Owner != "" && len(title)+1 < colW {
		owner = clipPlain(t.Owner, colW-len(title)-1)
	}
	style := lipgloss.NewStyle().Foreground(color)
	if done {
		style = style.Faint(true)
	}
	row := style.Render(title)
	if owner != "" {
		oc := lipgloss.NewStyle().Foreground(chrome.RoleColor(t.Owner))
		if done {
			oc = oc.Faint(true)
		}
		row += " " + oc.Render(owner)
	}
	return row
}

// paddedJoin inserts fixed-width gaps between column blocks.
func paddedJoin(cols []string, gap int) []string {
	out := make([]string, 0, len(cols)*2-1)
	for i, c := range cols {
		if i > 0 {
			out = append(out, strings.Repeat(" ", gap))
		}
		out = append(out, c)
	}
	return out
}
