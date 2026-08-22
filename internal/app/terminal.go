// terminal.go — the sidebar's REAL OS-shell tab: a lazy-spawning adapter
// around the parallel-built terminal panel.
//
// CONTRACT (the terminal dev builds this exactly; cmd/theboringoffice wires it):
//
//	package panels
//	NewTerminal(width, height int) (*TermPanel, error)
//	TermPanel: Title "terminal", SetSize(w,h), SetState, View() string,
//	Update(msg) tea.Cmd, Close() error, Alive() bool
//
// The app compiles/behere before that panel lands: SpawnTerminal is a
// factory seam (same pattern as SoundBus — nil = not linked). When nil or
// when a spawn fails, the app posts an office notice "terminal spawn
// failed: <err>" and falls back to the chat tab — NEVER a crash.
//
// Lifecycle rules (GOAL 1):
//   - lazy-spawn: the PTY is deferred until the member first visits the tab
//     (battery). Every tab switch passes through maybeSpawnTerminal.
//   - respawn: a dead shell renders a respawn prompt; pressing r respawns.
//   - quit: Close() runs on every app quit path (closeTerminal).
//
// Key contract (from internal/term's keyboard doc): while the terminal tab
// is focused the ONLY keys the app keeps are 1..6/tab/shift+tab (tab
// switch), ctrl+o (release badge) and ctrl+q (app quit) — everything else,
// q and ctrl+c included, forwards to the shell (term maps ctrl+c to the
// 0x03 byte, i.e. SIGINT to the foreground process).
package app

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/theboringoffice/internal/panels"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// terminalIndex is the terminal tab's position in the sidebar strip
// (chat | terminal | agents | board | mail | activity).
const terminalIndex = 1

// TerminalTab — the seam panels.TermPanel must satisfy. Same shape as the
// other panels (Tab + Interactive) plus the lifecycle pair Close/Alive.
type TerminalTab interface {
	panels.Tab         // Title "terminal" · SetSize · SetState · View
	panels.Interactive // Update(msg) tea.Cmd
	Close() error
	Alive() bool
}

// SpawnTerminal — the factory cmd/theboringoffice wires to the real panels.NewTerminal
// (the PTY panel landed at internal/panels/terminal.go; cmd/uishot still pins
// its own stub so shot frames stay deterministic). Contract:
// SpawnTerminal(cols, rows) returns a RUNNING shell panel; errors mean
// "spawn failed" and land on the chat tab as an office notice.
var SpawnTerminal func(cols, rows int) (TerminalTab, error)

// termTabWrap — the lazy-spawning sidebar adapter. It is a panels.Tab from
// birth (so the strip can list it) and only materializes the real PTY panel
// on first activation. All terminal key forwarding goes through its Update.
type termTabWrap struct {
	w, h   int
	inner  TerminalTab // nil until the first successful spawn
	tried  bool        // a spawn attempt happened (success or recorded failure)
	err    error       // last spawn/respawn failure (rendered in View)
	closed bool        // Close ran — no more spawns (app is quitting)
}

func newTermTabWrap() *termTabWrap { return &termTabWrap{} }

// Title implements panels.Tab — the strip's canonical name ("terminal").
func (t *termTabWrap) Title() string { return "terminal" }

// SetSize implements panels.Tab; forwarded to the spawned panel.
func (t *termTabWrap) SetSize(w, h int) {
	t.w, t.h = w, h
	if t.inner != nil {
		t.inner.SetSize(w, h)
	}
}

// SetState implements panels.Tab; forwarded to the spawned panel.
func (t *termTabWrap) SetState(st state.OfficeState) {
	if t.inner != nil {
		t.inner.SetState(st)
	}
}

// ensure spawns the PTY panel if this is the first visit. Errors do NOT
// panic — triggerSpawn (model.go) turns them into an office notice + chat
// fallback. nil factory = the parallel panel isn't linked yet.
func (t *termTabWrap) ensure() error {
	if t.inner != nil || t.closed {
		return nil
	}
	t.tried = true
	if SpawnTerminal == nil {
		t.err = fmt.Errorf("terminal panel not linked yet (parallel build — cmd/theboringoffice wires panels.NewTerminal)")
		return t.err
	}
	cols, rows := t.w, t.h
	if cols < 8 {
		cols = 8
	}
	if rows < 3 {
		rows = 3
	}
	tp, err := SpawnTerminal(cols, rows)
	if err != nil {
		t.err = fmt.Errorf("term: %w", err)
		return t.err
	}
	t.err = nil
	t.inner = tp
	return nil
}

// close kills the shell (idempotent; the app quit path calls it once).
func (t *termTabWrap) close() {
	t.closed = true
	if t.inner != nil {
		_ = t.inner.Close()
	}
}

// alive reports whether a spawned shell is still running.
func (t *termTabWrap) alive() bool {
	return t.inner != nil && t.inner.Alive()
}

// View implements panels.Tab: the shell surface while alive; otherwise a
// small instruction card (spawning / spawn-failed / respawn prompt).
func (t *termTabWrap) View() string {
	if t.inner != nil && t.inner.Alive() {
		return t.inner.View()
	}
	var card string
	switch {
	case t.closed:
		card = "shell stopped"
	case t.inner != nil && !t.inner.Alive():
		card = "shell exited\n\nr respawn · ctrl+o release to panels"
	case !t.tried:
		card = "spawning terminal…"
	case t.err != nil:
		// a failed spawn is also reported on the chat tab via the office
		// notice (model.maybeSpawnTerminal); this card is what the member
		// sees if they revisit the tab afterwards.
		card = "spawn failed:\n" + clipRunes(t.err.Error(), 200) +
			"\n\nr respawn · ctrl+o release to panels"
	default:
		card = "shell exited\n\nr respawn · ctrl+o release to panels"
	}
	lines := strings.Split(card, "\n")
	w := t.w
	if w < 1 {
		w = 1
	}
	for i, ln := range lines {
		lines[i] = fitTermPlain(ln, w)
	}
	h := t.h
	if h < 1 {
		h = 1
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines, "\n")
}

// fitTermPlain clips a plain (no-ANSI) line to w cells and right-pads with
// spaces (the wrap's instruction cards are plain text).
func fitTermPlain(s string, w int) string {
	if w < 0 {
		w = 0
	}
	n := 0
	for i := range s {
		if n >= w {
			s = s[:i]
			break
		}
		n++
	}
	for n < w {
		s += " "
		n++
	}
	return s
}

// Update implements panels.Interactive:
//   - alive shell → every byte goes to the PTY (the app's handleKey already
//     filtered the tab-switch / ctrl+o / ctrl+q keys).
//   - dead/failed → r respawns; every other key is swallowed (writing to a
//     dead PTY is an error, and the member's intent is obvious).
func (t *termTabWrap) Update(msg tea.Msg) tea.Cmd {
	if t.alive() {
		return t.inner.Update(msg)
	}
	if kp, ok := msg.(tea.KeyPressMsg); ok && kp.String() == "r" {
		// respawn: drop the dead panel and spawn a fresh one. An error here
		// stays on the respawn card (View) — the member is ON the terminal
		// tab already, so the card is the honest surface (no tab fallback:
		// they explicitly asked to respawn from inside it).
		if t.inner != nil {
			_ = t.inner.Close()
			t.inner = nil
		}
		_ = t.ensure()
	}
	return nil
}
