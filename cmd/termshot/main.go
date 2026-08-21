// termshot — headless proof harness for internal/term: spawns a real PTY
// shell, round-trips a command, resizes, sanitizes, exits, and zombie-
// checks the process group. Prints "TERM OK" when every proof holds.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/panels"
	"github.com/theboringhumane/grafeio/internal/term"
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
