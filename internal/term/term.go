// Package term embeds an OS-level terminal: spawn the user's shell on a
// real PTY (github.com/creack/pty — darwin/linux), stream its output into
// a scrollback, forward keystrokes, and resize on layout change.
//
// term.go — the Session: process lifecycle + a non-blocking reader loop.
//
// KEYBOARD CONTRACT (what panels.TermPanel forwards per keystroke):
//
//	printable chars      → msg.Text bytes verbatim (utf-8)
//	enter                → "\r"
//	backspace            → 0x7f  (DEL — what real terminals send)
//	tab                  → 0x09  (completion)
//	esc                  → 0x1b
//	up/down/right/left   → "\x1b[A" … "\x1b[D"  (vt100 cursor keys)
//	home/end             → "\x1b[H" / "\x1b[F"
//	pgup/pgdown          → "\x1b[5~" / "\x1b[6~"
//	delete               → "\x1b[3~"
//	space                → " "
//	ctrl+c/d/x (all ctrl+<letter>) → 0x01..0x1a pass-through
//	ctrl+o               → RESERVED by the panel (releases terminal focus);
//	                       never reaches the shell
//	bracketed paste / F-keys / kitty modifiers → NOT forwarded in v1
//
// Process-group discipline: creack/pty's StartWithSize configures
// Setsid+Setctty, so the shell is a session leader and its own pgroup
// leader — syscall.Kill(-pid, SIGKILL) kills the whole group (shell +
// any pipeline children), and there is never a Quit path that hangs on
// a live child. Caller-callable multiple times; Kill is idempotent.
package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// DefaultShell picks the user's shell: $SHELL, else /bin/zsh (darwin
// default) if present, else /bin/bash.
func DefaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	for _, cand := range []string{"/bin/zsh", "/bin/bash"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/bin/sh"
}

// TermConfig is the spawn-time shape of a Session.
type TermConfig struct {
	Shell string   // default: DefaultShell()
	Cols  int      // default: 80
	Rows  int      // default: 24
	CWD   string   // default: caller's working directory
	Env   []string // default: os.Environ() + TERM + COLORTERM
}

// Session is one live PTY-bound shell process.
type Session struct {
	cfg  TermConfig
	cmd  *exec.Cmd
	mf   *os.File // pty master
	sb   *Scrollback
	grid *Grid  // live screen model (same bytes as sb, parsed)
	name string // shell basename (ps/pgrep proofs)

	mu     sync.Mutex
	alive  bool
	exited bool
	code   int // exit code, -1 while alive / kilt unknown
	closed bool
	err    error
}

// Spawn starts the shell under a new PTY of cfg.Cols x cfg.Rows and
// immediately starts the reader loop; the returned Session is already
// streaming. Spawn never blocks the caller on shell startup.
func Spawn(cfg TermConfig) (*Session, error) {
	if cfg.Shell == "" {
		cfg.Shell = DefaultShell()
	}
	if cfg.Cols < 2 {
		cfg.Cols = 80
	}
	if cfg.Rows < 1 {
		cfg.Rows = 24
	}
	env := cfg.Env
	if env == nil {
		env = os.Environ()
		env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	}

	cmd := exec.Command(cfg.Shell, "-i")
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}
	cmd.Env = env
	mf, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(cfg.Rows),
		Cols: uint16(cfg.Cols),
	})
	if err != nil {
		return nil, fmt.Errorf("term: spawn %s: %w", cfg.Shell, err)
	}

	s := &Session{
		cfg:   cfg,
		cmd:   cmd,
		mf:    mf,
		sb:    NewScrollback(defaultScrollback),
		grid:  NewGrid(cfg.Cols, cfg.Rows),
		name:  filepath.Base(cfg.Shell),
		alive: true,
		code:  -1,
	}
	s.startReader()
	go s.waiter()
	return s, nil
}

// startReader launches the goroutine draining the PTY master into the
// scrollback. It exits when the PTY goes away (shell exit or Kill) and
// MUST never block the TUI: all output lands behind the scrollback mutex.
func (s *Session) startReader() {
	go func() {
		// io.Copy on the master: EOF when every slave closes. Every byte
		// lands in BOTH the raw scrollback (durable history) and the grid
		// (live screen model) — one parse per delta, O(bytes), and the
		// grid nudges the app repaint channel on the way through.
		dst := io.MultiWriter(s.sb, s.grid)
		if _, err := io.Copy(dst, s.mf); err != nil {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
		// reader finishing means the PTY is gone → mark dead so Alive()
		// flips even if cmd.Wait is still reaping.
		s.mu.Lock()
		s.alive = false
		s.mu.Unlock()
	}()
}

// waiter reaps the child so it never zombifies and records the exit code.
func (s *Session) waiter() {
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	s.mu.Lock()
	s.alive, s.exited, s.code = false, true, code
	s.mu.Unlock()
}

// Read drains newly-arrived output accumulated since the last call.
// Non-blocking: returns nil when nothing new. (The scrollback is the
// durable store; Read is for consumers that want deltas.)
func (s *Session) Read() ([]byte, error) {
	b := s.sb.DrainNew()
	if werr := s.errSnapshot(); werr != nil && !s.Alive() {
		return b, werr
	}
	return b, nil
}

func (s *Session) errSnapshot() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(s.err, os.ErrClosed) || s.err == nil {
		return nil
	}
	return s.err
}

// Write forwards raw key bytes to the shell's PTY master.
func (s *Session) Write(p []byte) (int, error) {
	if !s.Alive() {
		return 0, fmt.Errorf("term: write to dead session")
	}
	return s.mf.Write(p)
}

// Resize hands the PTY a new window size (SIGWINCH to the foreground
// process group happens implicitly on darwin/linux).
func (s *Session) Resize(cols, rows int) error {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	s.cfg.Cols, s.cfg.Rows = cols, rows
	s.mu.Unlock()
	// reshape the screen model first so the SIGWINCH reprint paints into
	// the new geometry (top-left content kept by the grid).
	s.grid.SetSize(cols, rows)
	return pty.Setsize(s.mf, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

// Size reports the current PTY geometry.
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Cols, s.cfg.Rows
}

// Kill terminates the whole process group (the shell is a session leader,
// so -pid addresses shell + pipeline children), closes the master, and
// waits briefly for the waiter to reap. Idempotent — safe at app quit.
func (s *Session) Kill() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	pid := s.cmd.Process.Pid
	s.mu.Unlock()

	// Group kill first (best effort), then close the master so the reader
	// unblocks even on the error path, then let the waiter reap.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = s.mf.Close()

	for i := 0; i < 50; i++ {
		if s.Exited() {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// Close is the io.Closer spelling of Kill (app quit path).
func (s *Session) Close() error { return s.Kill() }

// Alive reports whether the shell is believed running (false once the PTY
// output stream ends or the process exits).
func (s *Session) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

// Exited reports whether cmd.Wait has returned (process fully reaped).
func (s *Session) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// ExitCode returns the shell's exit code; -1 while alive or unknown.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Pid is the shell's process id (also its pgroup id — kill -Pid works).
func (s *Session) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return -1
	}
	return s.cmd.Process.Pid
}

// ShellName is the shell binary basename ("zsh", "bash", …) — handy for
// zombie checks via pgrep.
func (s *Session) ShellName() string { return s.name }

// Scrollback exposes the output buffer (last-N rendering, search, etc).
func (s *Session) Scrollback() *Scrollback { return s.sb }

// Grid exposes the live screen model (cursor-positioned, SGR-styled cells)
// panels paint from — the alt screen's content while ?1049 is set.
func (s *Session) Grid() *Grid { return s.grid }
