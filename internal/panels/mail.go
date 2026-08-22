// mail.go — MAIL tab (port of node-legacy panels/mailbox.tsx):
// newest first, kind letter in kind color (B cyan, R green, N gray,
// U white), sender name in the sender's role color, then ">to subject".
package panels

import (
	"sort"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// Mail is the mailbox tab panel.
type Mail struct {
	vp   viewport.Model
	st   state.OfficeState
	w, h int
	rev  string
}

// NewMail builds the mailbox panel.
func NewMail() *Mail {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(5))
	vp.MouseWheelEnabled = true
	return &Mail{vp: vp}
}

// Title implements Tab.
func (m *Mail) Title() string { return "mail" }

// SetSize implements Tab.
func (m *Mail) SetSize(w, h int) {
	m.w, m.h = w, h
	m.vp.SetWidth(w)
	if h-1 > 0 {
		m.vp.SetHeight(h - 1)
	}
	m.SetState(m.st)
}

// SetState implements Tab.
func (m *Mail) SetState(st state.OfficeState) {
	m.st = st
	content := m.render()
	if content != m.rev {
		m.rev = content
		m.vp.SetContent(content)
	}
}

// Update implements Interactive (scroll).
func (m *Mail) Update(msg tea.Msg) tea.Cmd {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return cmd
	}
	return nil
}

// View implements Tab.
func (m *Mail) View() string {
	return fit(chrome.Header.Render("MAIL")+"\n"+m.vp.View(), m.h)
}

func kindLetter(k state.MailKind) string {
	switch k {
	case state.MailBrief:
		return "B"
	case state.MailReturn:
		return "R"
	case state.MailNotice:
		return "N"
	case state.MailUser:
		return "U"
	default:
		return "?"
	}
}

func kindStyle(k state.MailKind) func(string) string {
	return func(s string) string {
		switch k {
		case state.MailBrief:
			return chrome.Fg(chrome.Info, s)
		case state.MailReturn:
			return chrome.Fg(chrome.OK, s)
		case state.MailNotice:
			return chrome.Fg(chrome.Dim, s)
		default: // user
			return chrome.Fg(chrome.White, s)
		}
	}
}

// render — newest first; rows built from plain parts, clipped, then colored.
func (m *Mail) render() string {
	if len(m.st.Mails) == 0 {
		return chrome.DimText.Render("- empty -")
	}
	rows := make([]state.MailItem, len(m.st.Mails))
	copy(rows, m.st.Mails)
	sort.Slice(rows, func(i, j int) bool { return rows[i].At > rows[j].At })

	var b strings.Builder
	for _, it := range rows {
		// plain skeleton first for width math
		head := "[" + kindLetter(it.Kind) + "] " + it.From + ">" + it.To + " " + it.Subject
		head = clipPlain(head, m.w)
		styleFn := kindStyle(it.Kind)
		// re-color the letter + sender inside the clipped row
		line := styleMailRow(it, head, styleFn)
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// styleMailRow colors the [K] letter and the sender name of an already
// clipped plain row.
func styleMailRow(it state.MailItem, row string, styleFn func(string) string) string {
	letter := kindLetter(it.Kind)
	if len(row) < len(letter)+2 {
		return row // clipped into the "[K] " prefix (width 0 before first SetSize)
	}
	rest := row[len(letter)+2:] // strip "[K] " — machine layout, not NL
	styled := "[" + styleFn(letter) + "] "
	if strings.HasPrefix(rest, it.From) && it.From != "" {
		styled += chrome.Fg(chrome.RoleColor(it.From), it.From) + rest[len(it.From):]
	} else {
		styled += rest
	}
	return styled
}
