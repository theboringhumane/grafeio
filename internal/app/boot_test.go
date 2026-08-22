// boot_test.go — the boot splash contract: frame 0 is deterministic
// (wordmark + subtitle + first cascade line only), the cascade types out
// line by line into green OKs, Done() waits for SetReady (the 4s cap
// overrides), any key skips, Resize re-centers, and every View is an
// exact w×h ANSI frame.
package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// bootFeed runs n synthetic animation ticks through Update (the pacing is
// tick-pure: real tea.Tick wall-clock waits never fire in tests).
func bootFeed(b Boot, n int) Boot {
	for i := 0; i < n; i++ {
		nb, _ := b.Update(bootTickMsg{})
		b = nb
	}
	return b
}

// bootFrameShape asserts the exact w×h frame contract: h rows joined by
// "\n", every row exactly w cells wide (ANSI-aware).
func bootFrameShape(t *testing.T, frame string, w, h int) {
	t.Helper()
	if frame == "" {
		t.Fatalf("frame must not be empty at %dx%d", w, h)
	}
	rows := strings.Split(frame, "\n")
	if len(rows) != h {
		t.Fatalf("frame must be %d rows, got %d", h, len(rows))
	}
	for i, r := range rows {
		if rw := lipgloss.Width(r); rw != w {
			t.Fatalf("row %d must be %d cells wide, got %d: %q", i, w, rw, ansi.Strip(r))
		}
	}
}

// (a) frame 0: wordmark + subtitle + FIRST status line only, exact size,
// and two fresh boots render byte-identical frames (deterministic).
func TestBootFrameZero(t *testing.T) {
	b := NewBoot(120, 35)
	frame := b.View()
	plain := ansi.Strip(frame)

	if !strings.Contains(plain, "█") {
		t.Fatalf("frame 0 must render the THEBORING / OFFICE wordmark blocks")
	}
	if !strings.Contains(plain, "γραφείο · a startup office in your terminal") {
		t.Fatalf("frame 0 must render the brand subtitle")
	}
	// exactly ONE cascade line revealed (the first, mid-typing): its "▸"
	// is the only one, and no later line's text has started.
	if n := strings.Count(plain, "▸"); n != 1 {
		t.Fatalf("frame 0 must reveal exactly one status line, got %d ▸ glyphs", n)
	}
	for _, hidden := range []string{"agents:", "board:", "mail:", "mcp:"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("frame 0 must not show %q yet (cascade is staggered)", hidden)
		}
	}
	if strings.Contains(plain, "OK") {
		t.Fatalf("frame 0 must not show any OK (nothing done yet)")
	}
	bootFrameShape(t, frame, 120, 35)

	if again := NewBoot(120, 35).View(); again != frame {
		t.Fatalf("frame 0 must be deterministic — two fresh boots diverged")
	}
}

// (b) cascade: after enough ticks every line shows OK; Done() stays false
// until SetReady; the 4s cap finishes the show even without it.
func TestBootCascadeAndReady(t *testing.T) {
	b := bootFeed(NewBoot(100, 30), 40) // full cold cascade lands at tick 33
	plain := ansi.Strip(b.View())
	if n := strings.Count(plain, "OK"); n != 5 {
		t.Fatalf("after 40 ticks all 5 lines must show OK, got %d", n)
	}
	if b.Done() {
		t.Fatalf("Done() must wait for SetReady once the cascade is up")
	}
	b.SetReady()
	if !b.Done() {
		t.Fatalf("Done() must flip true: cascade complete AND backend ready")
	}

	// cap path: no SetReady — one tick before the cap the show still runs,
	// the cap tick itself finishes it AND fires bootDoneMsg.
	b2 := bootFeed(NewBoot(100, 30), bootMaxTicks-1)
	if b2.Done() {
		t.Fatalf("one tick before the cap the splash must still be running")
	}
	nb, cmd := b2.Update(bootTickMsg{})
	if !nb.Done() {
		t.Fatalf("the cap tick must finish the splash even without SetReady")
	}
	if cmd == nil {
		t.Fatalf("the crossing tick must return the bootDoneMsg cmd")
	}
	if _, ok := cmd().(bootDoneMsg); !ok {
		t.Fatalf("crossing cmd must fire bootDoneMsg, got %T", cmd())
	}
}

// (c) any key press yields the skip cmd msg.
func TestBootSkipKey(t *testing.T) {
	_, cmd := NewBoot(100, 30).Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil {
		t.Fatalf("a key press must return the skip cmd")
	}
	if _, ok := cmd().(bootSkipMsg); !ok {
		t.Fatalf("a key press must fire bootSkipMsg, got %T", cmd())
	}
}

// (d) Resize re-centers: the wordmark's left margin tracks the new width.
func TestBootResizeRecenters(t *testing.T) {
	b := NewBoot(120, 35)
	row0 := strings.Split(ansi.Strip(b.View()), "\n")[7] // (35-20)/2 = 7
	if got := strings.Index(row0, "█"); got != 39 {      // (120-44)/2 = 38 pad + 1 wordmark space
		t.Fatalf("wordmark must start at col 39 in 120 cols, got col %d", got)
	}

	nb, cmd := b.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if cmd != nil {
		t.Fatalf("resize must not arm extra cmds")
	}
	if nb.w != 60 || nb.h != 20 {
		t.Fatalf("resize must update the boot's size, got %dx%d", nb.w, nb.h)
	}
	row0 = strings.Split(ansi.Strip(nb.View()), "\n")[0] // (20-20)/2 = 0
	if got := strings.Index(row0, "█"); got != 9 {       // (60-44)/2 = 8 + 1
		t.Fatalf("wordmark must re-center to col 9 in 60 cols, got col %d", got)
	}
}

// (e) View is exactly w×h — big window and small-but-fitting window.
func TestBootExactFrame(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {60, 20}} {
		b := bootFeed(NewBoot(size[0], size[1]), 12) // mid-boot: spinners live
		bootFrameShape(t, b.View(), size[0], size[1])
	}
}
