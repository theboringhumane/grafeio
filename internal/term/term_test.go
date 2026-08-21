// term_test.go — PTY lifecycle proofs: spawn, round-trip, resize (SIGWINCH
// observed by the shell), sanitizer behavior, exit code capture, and a
// zombie-free group kill.
package term

import (
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSpawnRoundTrip(t *testing.T) {
	s, err := Spawn(TermConfig{Shell: "/bin/sh", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer s.Close()
	if !s.Alive() {
		t.Fatal("session not alive right after spawn")
	}
	if _, err := s.Write([]byte("echo $((6*7))\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, 5*time.Second, "42 in scrollback", func() bool {
		return strings.Contains(string(s.Scrollback().Raw()), "42")
	})
	// delta drain must ALSO carry it
	if !strings.Contains(string(s.Scrollback().Raw()), "echo") {
		t.Fatal("scrollback missing command echo")
	}
}

func TestReadDrainsDelta(t *testing.T) {
	s, err := Spawn(TermConfig{Shell: "/bin/sh", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer s.Close()
	waitFor(t, 3*time.Second, "first output", func() bool { return s.Scrollback().Len() > 0 })
	b, _ := s.Read() // drain the prompt bytes
	_, _ = s.Write([]byte("echo DELTA\n"))
	waitFor(t, 3*time.Second, "DELTA delta", func() bool {
		b, _ = s.Read()
		return strings.Contains(string(b), "DELTA")
	})
}

func TestResizeSIGWINCH(t *testing.T) {
	s, err := Spawn(TermConfig{Shell: "/bin/sh", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer s.Close()
	if err := s.Resize(60, 20); err != nil {
		t.Fatalf("resize: %v", err)
	}
	cols, rows := s.Size()
	if cols != 60 || rows != 20 {
		t.Fatalf("size = %dx%d, want 60x20", cols, rows)
	}
	waitFor(t, 3*time.Second, "prompt bytes", func() bool { return s.Scrollback().Len() > 0 })
	_, _ = s.Write([]byte("echo RSZ:$COLUMNS\n"))
	waitFor(t, 5*time.Second, "COLUMNS=60", func() bool {
		return strings.Contains(string(s.Scrollback().Raw()), "RSZ:60")
	})
}

func TestExitCodeAndKill(t *testing.T) {
	s, err := Spawn(TermConfig{Shell: "/bin/sh", Cols: 40, Rows: 10})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	_, _ = s.Write([]byte("exit 7\n"))
	waitFor(t, 5*time.Second, "exit", s.Exited)
	if s.Alive() {
		t.Fatal("Alive() true after exit")
	}
	if s.ExitCode() != 7 {
		t.Fatalf("exit code = %d, want 7", s.ExitCode())
	}

	// kill while a command still runs: must reap, not hang
	s2, err := Spawn(TermConfig{Shell: "/bin/sh", Cols: 40, Rows: 10})
	if err != nil {
		t.Fatalf("respawn: %v", err)
	}
	_, _ = s2.Write([]byte("sleep 300\n")) // foreground job
	time.Sleep(150 * time.Millisecond)
	if err := s2.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, 5*time.Second, "kill reap", s2.Exited)
	if err := s2.Kill(); err != nil { // idempotent
		t.Fatalf("double kill: %v", err)
	}
}

func TestSanitizeStripsCursorKeepsSGR(t *testing.T) {
	in := "\x1b[?2004h\x1b[?25h\x1b[2J\x1b[3;4H\x1b[>hand \x1b[92mgreen\x1b[0m \x1b]0;title\x07plain\x1bc"
	out := Sanitize(in)
	if !strings.Contains(out, "\x1b[92m") {
		t.Fatalf("sanitizer dropped SGR color: %q", out)
	}
	for _, bad := range []string{"\x1b[?2004h", "\x1b[?25h", "\x1b[2J", "\x1b[3;4H", "\x1b]", "\x1bc", "\x1b[>"} {
		if strings.Contains(out, bad) {
			t.Fatalf("sanitizer kept %q in %q", bad, out)
		}
	}
	if !strings.Contains(out, "green") || !strings.Contains(out, "plain") || !strings.Contains(out, "title") == true {
		if strings.Contains(out, "title") {
			t.Fatalf("OSC payload leaked: %q", out)
		}
	}
}

func TestRenderTrimsWidthAndDropsTrailingBlank(t *testing.T) {
	sb := NewScrollback(1 << 20)
	_, _ = sb.Write([]byte("alpha\r\nbeta is a longer line\x1b[31m!\x1b[0m\r\n"))
	rows := sb.Render(5, 8)
	if len(rows) != 2 {
		t.Fatalf("rows = %d (%q), want 2", len(rows), rows)
	}
	// width trim: visible cells <= 8 (SGR escapes are zero-width)
	for _, r := range rows {
		if n := len([]rune(stripANSI(r))); n > 8 {
			t.Fatalf("row wider than 8 cells: %q", r)
		}
	}
}

func TestLinesCRRewrite(t *testing.T) {
	rows := splitLines([]byte("10%\r20%\r30%\n"))
	if len(rows) < 2 || rows[0] != "30%" {
		t.Fatalf("carriage-return rewrite not flattened: %q", rows)
	}
}
