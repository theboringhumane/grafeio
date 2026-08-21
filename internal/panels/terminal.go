// terminal.go — the TERM tab: an OS-level terminal embedded in the right
// sidebar. Backed by internal/term.Session (a real PTY running the user's
// shell). The panel:
//
//	body    — the sanitized last-N rows of the shell's scrollback
//	         (SGR colors pass through; the live idle prompt included).
//	footer  — a one-row badge: "[tty] focused · ctrl+o to release" while
//	         the terminal owns the keyboard; "[tty] inactive" (dim) when
//	         the app owns keys again; a red "terminal exited (code N) —
//	         press r to respawn" line replaces the whole body when the
//	         shell dies.
//
// Keyboard contract (only while Focused — see internal/term/term.go for
// the full byte-level matrix): chars/enter/backspace/tab/esc/arrows/
// home/end/pgup/pgdown/delete/ctrl+letter all forward to the PTY;
// ctrl+o is RESERVED (releases focus back to the app — never reaches the
// shell). Mouse wheel scrolls the retained scrollback; a click focuses.
package panels

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
	"github.com/theboringhumane/grafeio/internal/term"
)

// TermPanel is the terminal sidebar tab. It satisfies Tab + Interactive
// exactly like chat/agents; the app additionally calls Focus()/Blur() when
// the term tab becomes (in)active and Close() at quit.
type TermPanel struct {
	sess  *term.Session // nil only transiently during respawn
	shell string        // shell path remembered for respawn
	cwd   string
	w, h  int

	focused  bool
	scroll   int // rows up from the bottom (mouse wheel viewing)
	spawnErr error
	rev      uint64 // cheap change detection for View caching
	cached   string
}

// NewTerminal spawns the user's shell NOW on cols=width rows=height-1
// (one row reserved for the badge) and returns the ready panel. If the
// shell can't spawn the panel still comes up, showing the spawn error in
// the dead-shell body (r retries).
func NewTerminal(width, height int) (*TermPanel, error) {
	p := &TermPanel{shell: term.DefaultShell(), focused: true}
	p.SetSize(width, height)
	if err := p.spawn(); err != nil {
		p.spawnErr = err
		return p, err
	}
	return p, nil
}

// spawn starts a fresh session at the current panel geometry. Old sessions
// must be Close()d first (respawn does it).
func (p *TermPanel) spawn() error {
	sess, err := term.Spawn(term.TermConfig{
		Shell: p.shell,
		Cols:  p.w,
		Rows:  p.bodyH(),
		CWD:   p.cwd,
	})
	if err != nil {
		return err
	}
	p.sess = sess
	p.scroll = 0
	p.rev = 0
	p.cached = ""
	return nil
}

// Title implements Tab.
func (p *TermPanel) Title() string { return "term" }

// Focus hands the keyboard to the terminal (app calls when the tab
// activates); the badge flips to the focused hint.
func (p *TermPanel) Focus() { p.focused = true }

// Blur returns the keyboard to the app (app calls on tab switch; ctrl+o
// calls it internally).
func (p *TermPanel) Blur() { p.focused = false }

// Focused reports whether keystrokes are forwarded to the shell.
func (p *TermPanel) Focused() bool { return p.focused }

// Alive reports whether the shell is running.
func (p *TermPanel) Alive() bool { return p.sess != nil && p.sess.Alive() }

// Close kills the shell's whole process group (zombie-proof at app quit).
// Idempotent.
func (p *TermPanel) Close() error {
	if p.sess == nil {
		return nil
	}
	err := p.sess.Close()
	return err
}

// Session exposes the live PTY session (wiring dev: tests, raw access).
func (p *TermPanel) Session() *term.Session { return p.sess }

// bodyH is the terminal viewport height (panel height minus the badge row).
func (p *TermPanel) bodyH() int {
	h := p.h - 1
	if h < 1 {
		h = 1
	}
	return h
}

// SetSize implements Tab: sizes the panel you see AND resizes the
// underlying PTY (SIGWINCH reaches the shell).
func (p *TermPanel) SetSize(w, h int) {
	if w < 4 {
		w = 4
	}
	if h < 2 {
		h = 2
	}
	p.w, p.h = w, h
	p.rev = 0 // invalidate render cache
	if p.sess != nil && p.sess.Alive() {
		cols, rows := p.sess.Size()
		if cols != w || rows != p.bodyH() {
			_ = p.sess.Resize(w, p.bodyH())
		}
	}
}

// SetState implements Tab: the office tick is our refresh clock — every
// push invalidates the render cache if the scrollback moved (rev-check
// keeps the no-op case free).
func (p *TermPanel) SetState(st state.OfficeState) {
	if p.sess != nil && p.sess.Scrollback().Rev() != p.rev {
		p.rev = p.sess.Scrollback().Rev()
		p.cached = ""
	}
}

// Update implements Interactive. While focused every keypress goes to the
// PTY (ctrl+o releases focus); while blurred only viewing keys work
// (pgup/pgdn scroll) plus "r" to respawn a dead shell.
func (p *TermPanel) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		key := msg.String()
		if !p.Alive() {
			if key == "r" {
				_ = p.sess.Close()
				p.spawnErr = p.spawn()
				p.cached = ""
			}
			if key == "ctrl+o" && p.focused {
				p.Blur()
			}
			return nil
		}
		if p.focused {
			if key == "ctrl+o" {
				p.Blur()
				p.cached = ""
				return nil
			}
			if b, ok := keyToBytes(msg); ok {
				_, _ = p.sess.Write(b)
			}
			return nil // every key is consumed while focused
		}
		// blurred: viewing-only
		switch key {
		case "pgup":
			p.scrollView(1)
		case "pgdown":
			p.scrollView(-1)
		}
		return nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			p.scrollView(1)
		case tea.MouseWheelDown:
			p.scrollView(-1)
		}
		return nil
	case tea.MouseClickMsg:
		if !p.focused {
			p.Focus()
			p.cached = ""
		}
		return nil
	}
	return nil
}

// scrollView moves the view offset runes-worth of rows through the
// retained scrollback (clamped).
func (p *TermPanel) scrollView(d int) {
	max := 0
	if p.sess != nil {
		if n := len(p.sess.Scrollback().Lines()) - p.bodyH(); n > 0 {
			max = n
		}
	}
	p.scroll += d
	if p.scroll < 0 {
		p.scroll = 0
	}
	if p.scroll > max {
		p.scroll = max
	}
	p.cached = ""
}

// keyToBytes maps a bubbletea keypress to the byte sequence the shell
// expects. The full matrix lives in internal/term/term.go's header.
func keyToBytes(msg tea.KeyPressMsg) ([]byte, bool) {
	switch msg.String() {
	case "enter":
		return []byte("\r"), true
	case "backspace":
		return []byte{0x7f}, true
	case "tab":
		return []byte{0x09}, true
	case "esc":
		return []byte{0x1b}, true
	case "space":
		return []byte(" "), true
	case "up":
		return []byte("\x1b[A"), true
	case "down":
		return []byte("\x1b[B"), true
	case "right":
		return []byte("\x1b[C"), true
	case "left":
		return []byte("\x1b[D"), true
	case "home":
		return []byte("\x1b[H"), true
	case "end":
		return []byte("\x1b[F"), true
	case "pgup":
		return []byte("\x1b[5~"), true
	case "pgdown":
		return []byte("\x1b[6~"), true
	case "delete":
		return []byte("\x1b[3~"), true
	}
	if k := msg.String(); len(k) == len("ctrl+x") && strings.HasPrefix(k, "ctrl+") {
		c := k[len(k)-1]
		if c >= 'a' && c <= 'z' {
			return []byte{c - 'a' + 1}, true // 0x01..0x1a pass-through
		}
	}
	if msg.Text != "" {
		return []byte(msg.Text), true
	}
	return nil, false
}

// View implements Tab.
func (p *TermPanel) View() string {
	if p.cached != "" {
		return p.cached
	}
	var b strings.Builder

	if !p.Alive() {
		code := ""
		if p.sess != nil {
			code = itoa(p.sess.ExitCode())
		} else if p.spawnErr != nil {
			code = "spawn failed"
		}
		body := "terminal exited"
		if code != "" {
			body += " (code " + code + ")"
		}
		body += " — press r to respawn"
		b.WriteString(chrome.ErrText.Render(fitPlain(body, p.w)))
		for i := 1; i < p.bodyH(); i++ {
			b.WriteString("\n" + fitPlain("", p.w))
		}
	} else {
		rows := p.sess.Scrollback().Render(p.bodyH()+p.scroll, p.w)
		// apply scroll offset: keep the slice ending scroll-rows above the tail
		if p.scroll > 0 && len(rows) > p.scroll {
			rows = rows[:len(rows)-p.scroll]
		}
		if p.scroll > 0 {
			// viewing history: align to bottom of viewport naturally
			for len(rows) > p.bodyH() {
				rows = rows[1:]
			}
		}
		for i, r := range rows {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(r)
		}
		for i := len(rows); i < p.bodyH(); i++ {
			if len(rows) > 0 || i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fitPlain("", p.w))
		}
	}

	// badge row
	b.WriteString("\n")
	var badge string
	switch {
	case p.focused && p.Alive():
		badge = chrome.TabActive.Render(" tty ") + chrome.DimText.Render(" focused · ctrl+o to release")
	case p.Alive():
		badge = chrome.TabInactive.Render(" tty ") + chrome.DimText.Render(" inactive")
	default:
		badge = chrome.TabInactive.Render(" tty ") + chrome.ErrText.Render(" dead")
	}
	if p.scroll > 0 {
		badge += chrome.DimText.Render(" · scrolled up " + itoa(p.scroll))
	}
	b.WriteString(badge)

	p.cached = b.String()
	return p.cached
}
