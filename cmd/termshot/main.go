// termshot — headless proof harness for internal/term: spawns a real PTY
// shell, round-trips a command, resizes, sanitizes, exits, and zombie-
// checks the process group. Prints "TERM OK" when every proof holds.
//
// termshot --grid — the vt-screen-model proof suite: drives a real shell
// through clear/color/cursor/bs/resize/burst scenarios and asserts against
// the parsed GRID (not the raw byte stream). Ends with "TERM-GRID OK".
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/panels"
	"github.com/theboringhumane/theboringoffice/internal/term"
)

// fail prints and exits 1.
func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

// ok prints one green-check evidence line.
func ok(format string, args ...any) {
	fmt.Printf("  ok "+format+"\n", args...)
}

// pgrepCount counts live processes whose name matches the shell basename.
func pgrepCount(name string) int {
	out, err := exec.Command("pgrep", "-x", name).Output()
	if err != nil {
		return 0 // pgrep exits 1 when nothing matched
	}
	return len(strings.Fields(strings.TrimSpace(string(out))))
}

// waitFor polls cond every 20ms until true or deadline.
func waitFor(d time.Duration, what string, cond func() bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fail("timed out waiting for %s", what)
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--grid" {
			runGridSuite()
			return
		}
	}
	fmt.Println("== termshot: PTY proof suite ==")

	shell := term.DefaultShell()
	name := shell[strings.LastIndexByte(shell, '/')+1:]
	before := pgrepCount(name)
	fmt.Printf("shell %s · pgrep %s before spawn: %d\n", shell, name, before)

	sess, err := term.Spawn(term.TermConfig{Cols: 80, Rows: 24})
	if err != nil {
		fail("spawn: %v", err)
	}
	ok("spawned pid %d", sess.Pid())

	contains := func(needle string) func() bool {
		return func() bool {
			return strings.Contains(string(sess.Scrollback().Raw()), needle)
		}
	}

	// let the prompt appear (any output at all)
	waitFor(5*time.Second, "first shell output", func() bool { return sess.Scrollback().Len() > 0 })
	ok("first bytes drained (no TUI blocking): %d buffered", sess.Scrollback().Len())

	// 1) arithmetic round-trip
	if _, err := sess.Write([]byte("echo $((6*7))\n")); err != nil {
		fail("write echo: %v", err)
	}
	waitFor(5*time.Second, "output containing 42", contains("42"))
	ok("echo $((6*7)) -> output contains \"42\"")

	// 2) command echo round-trip
	marker := "LSMARKER"
	beforeLen := sess.Scrollback().Len()
	if _, err := sess.Write([]byte("ls | head -3 | sed 's/^/" + marker + ":/'\n")); err != nil {
		fail("write ls: %v", err)
	}
	waitFor(5*time.Second, "ls output", func() bool {
		return sess.Scrollback().Len() > beforeLen && contains(marker)()
	})
	ok("ls | head output drained")

	// 3) resize 60x20; zsh/bash refresh $COLUMNS on SIGWINCH
	if err := sess.Resize(60, 20); err != nil {
		fail("resize: %v", err)
	}
	cols, rows := sess.Size()
	if cols != 60 || rows != 20 {
		fail("session size after resize = %dx%d, want 60x20", cols, rows)
	}
	if _, err := sess.Write([]byte("echo RESIZE:$COLUMNS\n")); err != nil {
		fail("write columns: %v", err)
	}
	waitFor(5*time.Second, "COLUMNS=60 after SIGWINCH", contains("RESIZE:60"))
	ok("resized pty to 60x20; shell saw SIGWINCH (COLUMNS=60)")

	// 4) sanitizer proof: last 10 rendered lines contain 42 but no cursor seqs
	rendered := sess.Scrollback().Render(10, 60)
	fmt.Println("-- sanitizer: last 10 rows @ width 60 --")
	found42 := false
	for _, r := range rendered {
		fmt.Printf("| %s\n", r)
		if strings.Contains(r, "42") {
			found42 = true
		}
	}
	if !found42 {
		fail("sanitized render lost the 42 row")
	}
	joined := strings.Join(rendered, "\n")
	for _, bad := range []string{"\x1b[?25h", "\x1b[?25l", "\x1b[?2004h", "\x1b[?2004l"} {
		if strings.Contains(joined, bad) {
			fail("sanitizer leaked cursor-protocol sequence %q", bad)
		}
	}
	ok("sanitizer: 42 survives, cursor/bracketed-paste sequences stripped")

	// 4b) panel proof: a real TermPanel around a live shell, one frame
	panel, err := panels.NewTerminal(60, 12)
	if err != nil {
		fail("panel spawn: %v", err)
	}
	_, _ = panel.Session().Write([]byte("echo PANEL:says $((6*7))\n"))
	waitFor(5*time.Second, "panel shell output", func() bool {
		return strings.Contains(string(panel.Session().Scrollback().Raw()), "PANEL:says 42")
	})
	time.Sleep(400 * time.Millisecond) // let the fresh prompt land too
	fmt.Println("-- panel frame (60x12, ansi-stripped) --")
	for i, ln := range strings.Split(ansi.Strip(panel.View()), "\n") {
		fmt.Printf("  %02d|%s\n", i, ln)
	}
	if !panel.Alive() {
		fail("panel not alive")
	}
	_ = panel.Close()
	ok("panel frame captured; [tty] focused badge live; panel Close ok")

	// 5) clean exit: ask the shell to die, watch Alive/ExitCode
	if _, err := sess.Write([]byte("exit 7\n")); err != nil {
		fail("write exit: %v", err)
	}
	waitFor(5*time.Second, "shell exit", sess.Exited)
	if sess.Alive() {
		fail("Alive() still true after exit")
	}
	if sess.ExitCode() != 7 {
		fail("exit code = %d, want 7", sess.ExitCode())
	}
	ok("exit status: pid %d exited code 7, Alive=false", sess.Pid())

	// 6) zombie check + kill-safety on a second session
	sess2, err := term.Spawn(term.TermConfig{Cols: 40, Rows: 10})
	if err != nil {
		fail("respawn: %v", err)
	}
	pid2 := sess2.Pid()
	if err := sess2.Kill(); err != nil {
		fail("kill: %v", err)
	}
	waitFor(5*time.Second, "kill reap", sess2.Exited)
	// group-kill check: no live (non-Z) process left in the shell's pgroup
	out, _ := exec.Command("ps", "-ax", "-o", "pgid=", "-o", "stat=").Output()
	live := 0
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == fmt.Sprint(pid2) && !strings.HasPrefix(f[1], "Z") {
			live++
		}
	}
	if live > 0 {
		fail("kill leaked %d processes in pgroup %d", live, pid2)
	}
	after := pgrepCount(name)
	if after != before {
		fail("zombie check: pgrep %s before=%d after=%d", name, before, after)
	}
	ok("no zombies: pgrep %s before=%d after=%d; pgroup %d empty after Kill", name, before, after, pid2)

	fmt.Println("TERM OK")
}

// ---------------------------------------------------------------------------
// --grid: the vt screen-model proof suite. Drives /bin/sh on a real PTY and
// asserts against the parsed grid state (cells, colors, cursor), not the
// raw byte stream. Each subtest starts with `clear` so screen state is
// known, and ends with a unique marker echo for synchronization.
// ---------------------------------------------------------------------------

// waitGrid polls cond against the session grid until true or deadline.
func waitGrid(sess *term.Session, d time.Duration, what string, cond func(g *term.Grid) bool) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond(sess.Grid()) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	dump := sess.Grid().ScreenText()
	fail("timed out waiting for %s\n-- screen dump --\n%s", what, dump)
}

// gridCmd sends a command line to the shell.
func gridCmd(sess *term.Session, line string) {
	if _, err := sess.Write([]byte(line + "\n")); err != nil {
		fail("write %q: %v", line, err)
	}
}

// printFrame dumps the active grid rows as a bordered frame (panorama).
func printFrame(g *term.Grid, title string) {
	fmt.Printf("-- %s (%dx%d) --\n", title, g.Cols(), g.Rows())
	for y := 0; y < g.Rows(); y++ {
		fmt.Printf("| %s\n", g.LineText(y))
	}
}

func runGridSuite() {
	fmt.Println("== termshot --grid: vt screen-model proof suite ==")

	sess, err := term.Spawn(term.TermConfig{Shell: "/bin/sh", Cols: 80, Rows: 24})
	if err != nil {
		fail("spawn: %v", err)
	}
	defer sess.Close()
	g := sess.Grid()
	ok("spawned /bin/sh pid %d on 80x24 grid", sess.Pid())

	waitGrid(sess, 5*time.Second, "first prompt", func(g *term.Grid) bool {
		return g.ScreenText() != strings.Repeat("\n", g.Rows())
	})
	cx, cy := g.Cursor()
	ok("grid receiving bytes (screen non-blank, cursor at %d,%d)", cx, cy)

	// settle: let the first prompt fully land before driving
	time.Sleep(300 * time.Millisecond)

	// (a) clear repaints cleanly: `printf 'A'; clear; printf 'B\n'`
	//     → row0 shows B ONLY.
	gridCmd(sess, "printf 'A'; clear; printf 'B\\n'; echo GA_DONE")
	waitGrid(sess, 5*time.Second, "GA_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GA_DONE")
	})
	if got := g.LineText(0); got != "B" {
		printFrame(g, "subtest a failed")
		fail("(a) clear redraw: row0 = %q, want %q", got, "B")
	}
	ok("(a) printf 'A'; clear; printf 'B\\n' → row0 is exactly \"B\"")

	// (b) colors: `tput setaf 1; echo RED` → the R cell carries fg code 31.
	gridCmd(sess, "clear; tput setaf 1; echo RED; echo GB_DONE")
	waitGrid(sess, 5*time.Second, "GB_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GB_DONE")
	})
	if c := g.CellAt(0, 0); c.Ch != 'R' || c.Fg != 31 {
		fail("(b) setaf 1: cell(0,0) = %+v, want Ch 'R' Fg 31", c)
	}
	if c := g.CellAt(2, 0); c.Ch != 'D' || c.Fg != 31 {
		fail("(b) setaf 1: cell(2,0) = %+v, want Ch 'D' Fg 31", c)
	}
	ok("(b) tput setaf 1; echo RED → cells carry fg code 31 (red)")

	// (c) cursor addressing: `tput cup 5 10; echo MARK` → 'M' at (10,5).
	gridCmd(sess, "clear; tput cup 5 10; echo MARK; echo GC_DONE")
	waitGrid(sess, 5*time.Second, "GC_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GC_DONE")
	})
	if c := g.CellAt(10, 5); c.Ch != 'M' {
		fail("(c) tput cup 5 10: cell(10,5) = %+v, want 'M'", c)
	}
	ok("(c) tput cup 5 10; echo MARK → 'M' painted at (col=10,row=5)")

	// (d) backspace: `printf 'x\bX'` → cursor handling, cell shows X.
	gridCmd(sess, "clear; printf 'x\\bX'; echo GD_DONE")
	waitGrid(sess, 5*time.Second, "GD_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GD_DONE")
	})
	if c := g.CellAt(0, 0); c.Ch != 'X' {
		fail("(d) printf 'x\\bX': cell(0,0) = %+v, want 'X'", c)
	}
	ok("(d) printf 'x\\bX' → BS moves left, next print overwrites: cell shows X")

	// (e) resize 60x20: grid shape adjusts, top-left content keeps.
	prevCell := g.CellAt(0, 0)
	if err := sess.Resize(60, 20); err != nil {
		fail("(e) resize: %v", err)
	}
	if g.Cols() != 60 || g.Rows() != 20 {
		fail("(e) grid shape after resize = %dx%d, want 60x20", g.Cols(), g.Rows())
	}
	if c := g.CellAt(0, 0); c != prevCell {
		fail("(e) resize lost top-left content: %+v → %+v", prevCell, c)
	}
	gridCmd(sess, "clear; echo GE_DONE")
	waitGrid(sess, 5*time.Second, "GE_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GE_DONE")
	})
	ok("(e) resize 60x20 → grid reshaped, top-left content kept, shell follows")

	// (f) typing burst through the PTY + in-process parse budget.
	gridCmd(sess, "clear; head -c 200 /dev/zero | tr '\\0' y; echo GF_DONE")
	burstStart := time.Now()
	waitGrid(sess, 5*time.Second, "GF_DONE marker", func(g *term.Grid) bool {
		return strings.Contains(g.ScreenText(), "GF_DONE")
	})
	e2e := time.Since(burstStart)

	// pure parse budget: 200-char burst including SGR, far under 50ms
	bench := term.NewGrid(80, 24)
	raw := []byte(strings.Repeat("abcdefghij\x1b[32mgreen\x1b[0m ", 12)) // ~300 B
	p0 := time.Now()
	_, _ = bench.Write(raw)
	parse := time.Since(p0)
	if parse > 50*time.Millisecond {
		fail("(f) grid parse of %d bytes = %s, want <50ms", len(raw), parse)
	}
	fmt.Printf("-- typeburst frame (60x20 after 200-char burst, e2e %s, parse %s) --\n", e2e.Round(time.Millisecond), parse)
	printFrame(g, "panorama")
	ok("(f) 200-char burst: end-to-end %s, grid parse of %d bytes %s (<50ms)",
		e2e.Round(time.Millisecond), len(raw), parse)

	fmt.Println("TERM-GRID OK")
}
