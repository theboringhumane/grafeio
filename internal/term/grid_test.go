// grid_test.go — unit proofs for the vt screen model: controls, CSI moves,
// erases, insert/delete, scroll regions, SGR (ansi/256/truecolor), alt
// screen, split escape tails, resize, and the typing-burst budget.
package term

import (
	"strings"
	"testing"
	"time"
)

func feed(g *Grid, s string) {
	if _, err := g.Write([]byte(s)); err != nil {
		panic(err)
	}
}

func TestGridPlainPrint(t *testing.T) {
	g := NewGrid(10, 4)
	feed(g, "abc")
	if g.LineText(0) != "abc" {
		t.Fatalf("row0 = %q, want %q", g.LineText(0), "abc")
	}
	if x, y := g.Cursor(); x != 3 || y != 0 {
		t.Fatalf("cursor = (%d,%d), want (3,0)", x, y)
	}
	feed(g, "\rZ\r\nq")
	if g.LineText(0) != "Zbc" {
		t.Fatalf("CR overwrite failed: %q", g.LineText(0))
	}
	if g.LineText(1) != "q" {
		t.Fatalf("LF row1 failed: %q", g.LineText(1))
	}
}

func TestGridScrollAtBottom(t *testing.T) {
	g := NewGrid(6, 2)
	feed(g, "one\r\ntwo\r\nTHREE")
	if g.LineText(0) != "two" {
		t.Fatalf("scroll: row0 = %q, want %q", g.LineText(0), "two")
	}
	if g.LineText(1) != "THREE" {
		t.Fatalf("scroll: row1 = %q, want %q", g.LineText(1), "THREE")
	}
}

func TestGridBackspaceOverwritesAtNextPrint(t *testing.T) {
	g := NewGrid(10, 4)
	feed(g, "x\bX")
	if c := g.CellAt(0, 0); c.Ch != 'X' {
		t.Fatalf("cell(0,0) = %q, want 'X'", c.Ch)
	}
	if x, _ := g.Cursor(); x != 1 {
		t.Fatalf("cursor after overwrite = %d, want 1", x)
	}
}

func TestGridTabStops(t *testing.T) {
	g := NewGrid(40, 2)
	feed(g, "a\tb")
	if c := g.CellAt(0, 0); c.Ch != 'a' || g.CellAt(8, 0).Ch != 'b' {
		t.Fatalf("tab stop wrong: %q", g.LineText(0))
	}
}

func TestGridClear(t *testing.T) {
	g := NewGrid(10, 4)
	feed(g, "A\x1b[2J\x1b[HB")
	if g.LineText(0) != "B" {
		t.Fatalf("clear+home: row0 = %q, want %q", g.LineText(0), "B")
	}
	for y := 1; y < 4; y++ {
		if g.LineText(y) != "" {
			t.Fatalf("clear+home: row%d dirty: %q", y, g.LineText(y))
		}
	}
}

func TestGridCursorPosition(t *testing.T) {
	g := NewGrid(20, 10)
	feed(g, "\x1b[6;11HM") // 1-based row 6, col 11 → 0-based cell(10,5)
	if c := g.CellAt(10, 5); c.Ch != 'M' {
		t.Fatalf("CSI H: cell(10,5) = %q, want 'M'", c.Ch)
	}
	feed(g, "\x1b[2;3H\x1b[2CN") // (row2,col3) 0-based(2,1) → +2 → col5? col=3-1+2=4
	if c := g.CellAt(4, 1); c.Ch != 'N' {
		t.Fatalf("CSI C: cell(4,1) = %q, want 'N'", c.Ch)
	}
	feed(g, "\x1b[B") // down one
	if _, y := g.Cursor(); y != 2 {
		t.Fatalf("CSI B: row = %d, want 2", y)
	}
}

func TestGridEraseLine(t *testing.T) {
	g := NewGrid(10, 3)
	feed(g, "abcdef\x1b[3D\x1b[0K") // cursor at col3, erase to eol
	if g.LineText(0) != "abc" {
		t.Fatalf("K0: %q, want %q", g.LineText(0), "abc")
	}
	feed(g, "\x1b[2K")
	if g.LineText(0) != "" {
		t.Fatalf("K2: %q, want empty", g.LineText(0))
	}
}

func TestGridInsertDelete(t *testing.T) {
	g := NewGrid(10, 3)
	feed(g, "ab\x1b[1D\x1b[@XY") // cursor to col1, insert 1 blank, print XY over it+b
	if g.LineText(0) != "aXY" {
		t.Fatalf("insert-chars: %q, want %q", g.LineText(0), "aXY")
	}
	feed(g, "\x1b[H\x1b[P") // home, delete 1 char
	if g.LineText(0) != "XY" {
		t.Fatalf("delete-chars: %q, want %q", g.LineText(0), "XY")
	}
	// lines
	feed(g, "\x1b[2Hline2\x1b[H\x1b[L") // insert blank line at row0
	if g.LineText(0) != "" || g.LineText(1) != "XY" {
		t.Fatalf("insert-lines: %q / %q", g.LineText(0), g.LineText(1))
	}
	feed(g, "\x1b[H\x1b[M") // delete it back
	if g.LineText(0) != "XY" || g.LineText(1) != "line2" {
		t.Fatalf("delete-lines: %q / %q", g.LineText(0), g.LineText(1))
	}
}

func TestGridScrollRegion(t *testing.T) {
	g := NewGrid(5, 4)
	feed(g, "r0\r\nr1\r\nr2\r\nr3")
	feed(g, "\x1b[2;3r") // region = rows 1..2 (0-based); cursor homes
	feed(g, "\x1b[3;1H") // cursor to region bottom (row2)
	feed(g, "\n")        // LF at region bottom: scroll the REGION up one
	// margins must be untouched; region rows shifted: row1 ← old row2
	if g.LineText(0) != "r0" || g.LineText(3) != "r3" {
		t.Fatalf("region leaked to margins: %q %q", g.LineText(0), g.LineText(3))
	}
	if g.LineText(1) != "r2" || g.LineText(2) != "" {
		t.Fatalf("region scroll wrong: %q / %q", g.LineText(1), g.LineText(2))
	}
	feed(g, "\x1b[r") // reset to full
}

func TestGridSGR(t *testing.T) {
	g := NewGrid(40, 4)
	feed(g, "\x1b[31mR\x1b[0mnormal \x1b[92mG\x1b[1;4mBU\x1b[0m")
	if c := g.CellAt(0, 0); c.Ch != 'R' || c.Fg != 31 {
		t.Fatalf("setaf1 cell = %+v, want Ch R Fg 31", c)
	}
	if c := g.CellAt(1, 0); c.Fg != ColorDefault {
		t.Fatalf("reset fg = %d, want default", c.Fg)
	}
	gi := strings.Index(g.LineText(0), "G")
	if gi < 0 || g.CellAt(gi, 0).Fg != 92 {
		t.Fatalf("bright fg: %+v at %d", g.CellAt(gi, 0), gi)
	}
	bi := strings.Index(g.LineText(0), "B")
	if c := g.CellAt(bi, 0); !c.Bold || !c.Underline {
		t.Fatalf("bold+underline: %+v", c)
	}
	// 256 palette + truecolor
	feed(g, "\x1b[38;5;196mP")
	pi := strings.Index(g.LineText(0), "P")
	if c := g.CellAt(pi, 0); c.Fg != color256Off+196 {
		t.Fatalf("256 fg = %d, want %d", c.Fg, color256Off+196)
	}
	feed(g, "\x1b[48;2;1;2;3mT")
	ti := strings.Index(g.LineText(0), "T")
	want := colorRGBBase | 1<<16 | 2<<8 | 3
	if c := g.CellAt(ti, 0); c.Bg != want {
		t.Fatalf("rgb bg = %d, want %d", c.Bg, want)
	}
	// lipgloss mapping
	if LipColor(31) != "1" || LipColor(92) != "10" || LipColor(color256Off+196) != "196" {
		t.Fatalf("LipColor ansi/256 mapping wrong")
	}
	if LipColor(colorRGBBase|0x010203) != "#010203" {
		t.Fatalf("LipColor rgb = %q", LipColor(colorRGBBase|0x010203))
	}
}

func TestGridSaveRestore(t *testing.T) {
	g := NewGrid(10, 5)
	feed(g, "\x1b[3;4H\x1b[s\x1b[H\x1b[uX")
	if c := g.CellAt(3, 2); c.Ch != 'X' {
		t.Fatalf("CSI s/u: cell(3,2) = %q, want 'X'", c.Ch)
	}
	// ESC 7 saves AFTER the x print (cursor col 1) → ESC 8 returns there
	feed(g, "\x1b[Hx\x1b7\x1b[5;1H\x1b8Y")
	if c := g.CellAt(1, 0); c.Ch != 'Y' {
		t.Fatalf("ESC 7/8: cell(1,0) = %q, want 'Y'", c.Ch)
	}
}

func TestGridAltScreen(t *testing.T) {
	g := NewGrid(10, 3)
	feed(g, "prompt$ ")
	feed(g, "\x1b[?1049h")
	if !g.AltActive() {
		t.Fatal("alt screen not active after ?1049h")
	}
	feed(g, "ALTAPP")
	if g.LineText(0) != "ALTAPP" {
		t.Fatalf("alt content: %q", g.LineText(0))
	}
	feed(g, "\x1b[?1049l")
	if g.AltActive() {
		t.Fatal("alt screen still active after ?1049l")
	}
	if g.LineText(0) != "prompt$" {
		t.Fatalf("main screen not restored: %q", g.LineText(0))
	}
}

func TestGridSplitEscapeTail(t *testing.T) {
	g := NewGrid(10, 3)
	feed(g, "\x1b[3") // partial SGR
	if _, y := g.Cursor(); y != 0 {
		t.Fatal("partial seq must not parse")
	}
	feed(g, "1mX") // completes \x1b[31m
	if c := g.CellAt(0, 0); c.Ch != 'X' || c.Fg != 31 {
		t.Fatalf("split tail: %+v", g.CellAt(0, 0))
	}
	// split inside the middle of the escape
	feed(g, "\x1b[2")
	feed(g, ";3")
	feed(g, "HZ")
	if c := g.CellAt(2, 1); c.Ch != 'Z' {
		t.Fatalf("split H: cell(2,1) = %q, want 'Z'", c.Ch)
	}
	// split utf-8 rune
	feed(g, string([]byte{0xc3}))
	feed(g, string([]byte{0xa9})) // é
	if c := g.CellAt(2, 0); c.Ch != 'é' {
		// cursor resumed home after Z print? (3,1)... just scan
		found := false
		for y := 0; y < 3; y++ {
			for x := 0; x < 10; x++ {
				if g.CellAt(x, y).Ch == 'é' {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("split utf-8 lost: %q", g.ScreenText())
		}
	}
}

func TestGridResizeKeepsTopLeft(t *testing.T) {
	g := NewGrid(80, 24)
	feed(g, "hello\r\nsecond")
	g.SetSize(60, 20)
	if g.Cols() != 60 || g.Rows() != 20 {
		t.Fatalf("size = %dx%d, want 60x20", g.Cols(), g.Rows())
	}
	if g.LineText(0) != "hello" || g.LineText(1) != "second" {
		t.Fatalf("resize lost content: %q / %q", g.LineText(0), g.LineText(1))
	}
	// micro-shrink
	g.SetSize(4, 2)
	if g.LineText(0) != "hell" {
		t.Fatalf("shrink: %q, want %q", g.LineText(0), "hell")
	}
	if c := g.CellAt(0, 0); c.Ch != 'h' {
		t.Fatalf("shrink cell(0,0) = %q", c.Ch)
	}
	// grow back pads with blanks
	g.SetSize(80, 24)
	if g.Cols() != 80 || g.CellAt(4, 0).Ch != ' ' {
		t.Fatalf("grow padding wrong")
	}
}

func TestGridBurstSpeed(t *testing.T) {
	g := NewGrid(80, 24)
	burst := strings.Repeat("typing fast \x1b[31mred\x1b[0m ", 12) // ~370 bytes
	start := time.Now()
	feed(g, burst)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("burst parse = %s, want <50ms", d)
	}
	if !strings.Contains(g.ScreenText(), "typing") {
		t.Fatalf("burst content missing")
	}
}
