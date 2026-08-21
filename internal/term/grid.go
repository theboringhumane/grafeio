// grid.go — a minimal but correct xterm screen model: the Session's reader
// loop feeds raw PTY bytes in (same bytes the Scrollback retains), and the
// Grid keeps a cols×rows matrix of styled Cells that panels paint directly.
//
// Why: the old sanitizer+scroll path was a byte-stream flatten — typing
// flickered, clear/redraw smeared, and the prompt wandered between frames.
// A real screen model holds the cursor, so the prompt stays put and every
// repaint is a stable full-screen snapshot.
//
// Supported (day-1 surface):
//
//	C0:  CR, LF/VT/FF (+scroll at bottom margin), BS (no auto-erase — the
//	     next print overwrites), TAB (8-stops), BELL ignore, other C0 ignore
//	CSI: A/B/C/D cursor, E/F next/prev line, G/` col, H/f/d position,
//	     J 0/1/2(/3) erase display, K 0/1/2 erase line, L/M insert/delete
//	     lines, P/@/X delete/insert/erase chars, S/T scroll region up/down,
//	     r scroll region (full+partial), s/u save/restore cursor,
//	     m SGR (0,1,2,4,7,22,24,27,39,49; 30-37/90-97 fg; 40-47/100-107 bg;
//	     38/48;5;n palette; 38/48;2;r;g;b truecolor)
//	     private ?h/l: ?1049 alt screen (swap grids), ?25/?2004/?1000/?1006
//	     ignored (bracketed-paste mode set is a no-op here: Session.Write is
//	     raw passthrough, so pasted bytes echo raw, never interpreted)
//	ESC: 7/8 save/restore, c RIS, D IND, M RI, E NEL, =/> ignore;
//	     OSC/DCS/SOS/PM/APC consumed to BEL/ST and ignored
//
// Partial escape sequences split across Read() deltas are held in an
// internal tail buffer until complete — the parser is fully incremental.
// Concurrency: Write runs on the PTY reader goroutine, Render/SetSize on
// the TUI goroutine; everything is mutex-guarded.
package term

import (
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"
)

// Color encoding inside Cell.Fg / Cell.Bg: a single int carries every SGR
// color form so asserts stay trivial (termshot: setaf 1 → Fg == 31).
const (
	ColorDefault = -1 // SGR 39/49 — terminal default

	color256Off  = 1000    // 38;5;n  → color256Off+n      (1000..1255)
	colorRGBBase = 1 << 25 // 38;2;r;g;b → base|r<<16|g<<8|b
)

// Cell is one screen cell: a rune plus its SGR attributes.
type Cell struct {
	Ch rune // ' ' (or 0) for blank
	Fg int  // ColorDefault, literal SGR code (30-37/90-97), color256Off+n, or colorRGBBase|packed-rgb
	Bg int  // ColorDefault, literal SGR code (40-47/100-107), color256Off+n, or colorRGBBase|packed-rgb

	Bold      bool
	Dim       bool
	Underline bool
	Reverse   bool
}

// Row is one screen line of Cells, exactly Grid.Cols() wide.
type Row []Cell

// LipColor converts a Cell color code to a lipgloss-friendly color string
// ("" for default, "N" for ANSI/palette, "#rrggbb" for truecolor).
func LipColor(c int) string {
	switch {
	case c == ColorDefault:
		return ""
	case c >= colorRGBBase:
		v := c &^ colorRGBBase
		return fmt.Sprintf("#%02x%02x%02x", (v>>16)&0xff, (v>>8)&0xff, v&0xff)
	case c >= color256Off:
		return strconv.Itoa(c - color256Off)
	case c >= 30 && c <= 37:
		return strconv.Itoa(c - 30)
	case c >= 90 && c <= 97:
		return strconv.Itoa(c - 90 + 8)
	case c >= 40 && c <= 47:
		return strconv.Itoa(c - 40)
	case c >= 100 && c <= 107:
		return strconv.Itoa(c - 100 + 8)
	}
	return ""
}

// screen is one buffer (main or alt): lines + cursor + pen + scroll region.
type screen struct {
	lines     [][]Cell
	cx, cy    int
	pen       Cell // current attributes (Ch always 0)
	scrollTop int
	scrollBot int

	// CSI s / ESC 7 save slots (cursor + pen)
	savedX, savedY int
	savedPen       Cell
	saved          bool
}

// Grid is the vt screen model. Safe for concurrent use.
type Grid struct {
	mu sync.Mutex

	cols, rows int
	main, alt  screen
	altActive  bool

	// dedicated slots for ?1049 save/restore (never clobbered by CSI s/u)
	altSavedX, altSavedY int
	altSavedPen          Cell

	pending []byte // unparsed tail: incomplete escape / utf-8 rune
	rev     uint64
}

// NewGrid returns a blank cols×rows screen (mins clamped like Session).
func NewGrid(cols, rows int) *Grid {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	g := &Grid{cols: cols, rows: rows}
	g.main = newScreen(cols, rows)
	g.alt = newScreen(cols, rows)
	return g
}

func newScreen(cols, rows int) screen {
	s := screen{
		lines:     make([][]Cell, rows),
		pen:       Cell{Fg: ColorDefault, Bg: ColorDefault},
		scrollBot: rows - 1,
	}
	for y := range s.lines {
		s.lines[y] = defaultLine(cols)
	}
	return s
}

func defaultLine(cols int) []Cell {
	l := make([]Cell, cols)
	for x := range l {
		l[x] = Cell{Ch: ' ', Fg: ColorDefault, Bg: ColorDefault}
	}
	return l
}

// scr points at the active screen (alt when ?1049h, else main).
func (g *Grid) scr() *screen {
	if g.altActive {
		return &g.alt
	}
	return &g.main
}

// blank mirrors the pen's background (xterm erases carry the current bg).
func (s *screen) blank() Cell {
	return Cell{Ch: ' ', Bg: s.pen.Bg, Fg: ColorDefault}
}

func (s *screen) blankLine(cols int) []Cell {
	l := make([]Cell, cols)
	b := s.blank()
	for x := range l {
		l[x] = b
	}
	return l
}

// Write implements io.Writer (the Session reader loop multi-writes here).
// O(len(p)); partial escape tails are held for the next chunk. Never fails.
func (g *Grid) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	g.mu.Lock()
	g.pending = append(g.pending, p...)
	g.parseLocked()
	g.rev++
	g.mu.Unlock()
	publish() // notify.go: nudge the app to repaint (non-blocking)
	return len(p), nil
}

// parseLocked consumes as much of g.pending as forms complete elements.
func (g *Grid) parseLocked() {
	p := g.pending
	i := 0
	for i < len(p) {
		c := p[i]
		switch {
		case c == 0x1b:
			n, complete := g.parseEscape(p[i:])
			if !complete {
				g.pending = append([]byte(nil), p[i:]...)
				return
			}
			i += n
		case c == '\r':
			g.scr().cx = 0
			i++
		case c == '\n' || c == '\v' || c == '\f':
			g.linefeed()
			i++
		case c == '\b':
			if g.scr().cx > 0 {
				g.scr().cx--
			}
			i++
		case c == '\t':
			s := g.scr()
			s.cx = (s.cx/8 + 1) * 8
			if s.cx > g.cols-1 {
				s.cx = g.cols - 1
			}
			i++
		case c < 0x20: // BELL + remaining C0: ignore
			i++
		default:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				if !utf8.FullRune(p[i:]) { // incomplete utf-8 tail: hold
					g.pending = append([]byte(nil), p[i:]...)
					return
				}
				i++ // invalid byte: skip
				continue
			}
			g.putRune(r)
			i += size
		}
	}
	g.pending = g.pending[:0]
}

// parseEscape handles one ESC-introduced sequence at buf[0]. Returns bytes
// consumed, or complete=false when the sequence is split across reads.
func (g *Grid) parseEscape(buf []byte) (int, bool) {
	if len(buf) < 2 {
		return 0, false
	}
	switch buf[1] {
	case '[': // CSI
		j := 2
		for j < len(buf) && buf[j] < 0x40 { // params + intermediates
			j++
		}
		if j >= len(buf) {
			return 0, false
		}
		g.csi(buf[j], buf[2:j])
		return j + 1, true
	case ']', 'P', 'X', '^', '_': // OSC / DCS / SOS / PM / APC — to BEL or ST
		j := 2
		for j < len(buf) {
			if buf[j] == 0x07 {
				return j + 1, true
			}
			if buf[j] == 0x1b && j+1 < len(buf) && buf[j+1] == '\\' {
				return j + 2, true
			}
			j++
		}
		return 0, false
	case '(', ')': // charset designates: ESC ( X
		if len(buf) < 3 {
			return 0, false
		}
		return 3, true
	}
	// two-byte sequences
	switch buf[1] {
	case '7': // DECSC
		g.saveCursor()
	case '8': // DECRC
		g.restoreCursor()
	case 'c': // RIS — full reset of the active screen
		s := g.scr()
		*s = newScreen(g.cols, g.rows)
	case 'D': // IND
		g.linefeed()
	case 'M': // RI
		s := g.scr()
		if s.cy == s.scrollTop {
			g.scrollDown(s.scrollTop, s.scrollBot, 1)
		} else if s.cy > 0 {
			s.cy--
		}
	case 'E': // NEL
		g.scr().cx = 0
		g.linefeed()
	}
	return 2, true
}

// parseCSIParams splits "1;2;3" / "" / "-runs" into ints; empty slots are -1.
func parseCSIParams(raw []byte) (params []int, private bool) {
	if len(raw) > 0 && raw[0] == '?' {
		private = true
		raw = raw[1:]
	}
	start := 0
	emit := func(tok []byte) {
		if len(tok) == 0 {
			params = append(params, -1)
			return
		}
		n := 0
		for _, c := range tok {
			if c < '0' || c > '9' { // intermediate byte: stop scanning
				break
			}
			n = n*10 + int(c-'0')
		}
		params = append(params, n)
	}
	for i, c := range raw {
		if c == ';' {
			emit(raw[start:i])
			start = i + 1
		}
	}
	emit(raw[start:])
	return params, private
}

// num reads params[i] with default def (missing/zero/negative → def).
func num(params []int, i, def int) int {
	if i < len(params) && params[i] > 0 {
		return params[i]
	}
	return def
}

func (g *Grid) clampCursor() {
	s := g.scr()
	if s.cx < 0 {
		s.cx = 0
	}
	if s.cx > g.cols-1 {
		s.cx = g.cols - 1
	}
	if s.cy < 0 {
		s.cy = 0
	}
	if s.cy > g.rows-1 {
		s.cy = g.rows - 1
	}
}

func (g *Grid) csi(final byte, raw []byte) {
	params, private := parseCSIParams(raw)
	s := g.scr()

	// cursor-up clamp: inside the scroll region stops at its top.
	upClamp := func(n int) {
		min := 0
		if s.cy >= s.scrollTop && s.cy <= s.scrollBot {
			min = s.scrollTop
		}
		s.cy -= n
		if s.cy < min {
			s.cy = min
		}
	}
	downClamp := func(n int) {
		max := g.rows - 1
		if s.cy >= s.scrollTop && s.cy <= s.scrollBot {
			max = s.scrollBot
		}
		s.cy += n
		if s.cy > max {
			s.cy = max
		}
	}

	switch final {
	case 'A':
		upClamp(num(params, 0, 1))
	case 'B':
		downClamp(num(params, 0, 1))
	case 'C':
		s.cx += num(params, 0, 1)
		if s.cx > g.cols-1 {
			s.cx = g.cols - 1
		}
	case 'D':
		s.cx -= num(params, 0, 1)
		if s.cx < 0 {
			s.cx = 0
		}
	case 'E':
		downClamp(num(params, 0, 1))
		s.cx = 0
	case 'F':
		upClamp(num(params, 0, 1))
		s.cx = 0
	case 'G', '`':
		s.cx = num(params, 0, 1) - 1
		g.clampCursor()
	case 'H', 'f':
		s.cy = num(params, 0, 1) - 1
		s.cx = num(params, 1, 1) - 1
		g.clampCursor()
	case 'd':
		s.cy = num(params, 0, 1) - 1
		g.clampCursor()
	case 'J':
		g.eraseDisplay(num(params, 0, 0))
	case 'K':
		g.eraseLine(num(params, 0, 0))
	case 'L':
		g.insertLines(num(params, 0, 1))
	case 'M':
		g.deleteLines(num(params, 0, 1))
	case 'P':
		g.deleteChars(num(params, 0, 1))
	case '@':
		g.insertChars(num(params, 0, 1))
	case 'X':
		g.eraseChars(num(params, 0, 1))
	case 'S':
		g.scrollUp(s.scrollTop, s.scrollBot, num(params, 0, 1))
	case 'T':
		g.scrollDown(s.scrollTop, s.scrollBot, num(params, 0, 1))
	case 'r':
		top := num(params, 0, 1) - 1
		bot := num(params, 1, g.rows) - 1
		if bot > g.rows-1 {
			bot = g.rows - 1
		}
		if top >= 0 && top < bot {
			s.scrollTop, s.scrollBot = top, bot
		} else {
			s.scrollTop, s.scrollBot = 0, g.rows-1
		}
		s.cx, s.cy = 0, 0
	case 's':
		g.saveCursor()
	case 'u':
		g.restoreCursor()
	case 'm':
		g.sgr(params)
	case 'h', 'l':
		if private {
			g.privateMode(params, final == 'h')
		}
	}
	// unknown final bytes: safely ignored
}

// privateMode handles CSI ? Ps h/l. Only ?1049 (alt screen) has an effect;
// ?25 (cursor vis), ?2004 (bracketed paste), ?1000/?1006 (mouse reporting)
// and friends are parsed and ignored.
func (g *Grid) privateMode(params []int, set bool) {
	for _, p := range params {
		if p != 1049 {
			continue
		}
		if set && !g.altActive {
			m := &g.main
			g.altSavedX, g.altSavedY, g.altSavedPen = m.cx, m.cy, m.pen
			g.alt = newScreen(g.cols, g.rows)
			g.altActive = true
		} else if !set && g.altActive {
			g.altActive = false
			m := &g.main
			m.cx, m.cy, m.pen = g.altSavedX, g.altSavedY, g.altSavedPen
			g.clampCursor()
		}
	}
}

func (g *Grid) saveCursor() {
	s := g.scr()
	s.savedX, s.savedY, s.savedPen, s.saved = s.cx, s.cy, s.pen, true
}

func (g *Grid) restoreCursor() {
	s := g.scr()
	if !s.saved {
		return
	}
	s.cx, s.cy, s.pen = s.savedX, s.savedY, s.savedPen
	g.clampCursor()
}

// sgr applies SGR attributes to the active pen.
func (g *Grid) sgr(params []int) {
	s := g.scr()
	if len(params) == 0 {
		params = []int{0}
	}
	for i := 0; i < len(params); i++ {
		p := params[i]
		if p < 0 {
			p = 0
		}
		switch {
		case p == 0:
			s.pen = Cell{Fg: ColorDefault, Bg: ColorDefault}
		case p == 1:
			s.pen.Bold = true
		case p == 2:
			s.pen.Dim = true
		case p == 4:
			s.pen.Underline = true
		case p == 7:
			s.pen.Reverse = true
		case p == 22:
			s.pen.Bold, s.pen.Dim = false, false
		case p == 24:
			s.pen.Underline = false
		case p == 27:
			s.pen.Reverse = false
		case p == 39:
			s.pen.Fg = ColorDefault
		case p == 49:
			s.pen.Bg = ColorDefault
		case p >= 30 && p <= 37, p >= 90 && p <= 97:
			s.pen.Fg = p
		case p >= 40 && p <= 47, p >= 100 && p <= 107:
			s.pen.Bg = p
		case p == 38 || p == 48:
			color, consumed := parseExtColor(params[i+1:])
			if consumed > 0 {
				if p == 38 {
					s.pen.Fg = color
				} else {
					s.pen.Bg = color
				}
				i += consumed
			}
		}
	}
}

// parseExtColor reads the operands of 38/48: "5;n" palette or "2;r;g;b".
func parseExtColor(rest []int) (color, consumed int) {
	if len(rest) < 1 {
		return ColorDefault, 0
	}
	switch rest[0] {
	case 5:
		if len(rest) < 2 {
			return ColorDefault, 0
		}
		n := rest[1]
		if n < 0 {
			n = 0
		}
		if n > 255 {
			n = 255
		}
		return color256Off + n, 2
	case 2:
		if len(rest) < 4 {
			return ColorDefault, 0
		}
		clamp := func(v int) int {
			if v < 0 {
				return 0
			}
			if v > 255 {
				return 255
			}
			return v
		}
		return colorRGBBase | clamp(rest[1])<<16 | clamp(rest[2])<<8 | clamp(rest[3]), 4
	}
	return ColorDefault, 0
}

// putRune prints one rune at the cursor with the current pen (with the
// simple autowrap: hitting the right margin wraps to the next line).
func (g *Grid) putRune(r rune) {
	s := g.scr()
	if s.cx >= g.cols {
		s.cx = 0
		g.linefeed()
	}
	c := s.pen
	c.Ch = r
	s.lines[s.cy][s.cx] = c
	s.cx++
}

// linefeed moves down one row, scrolling the region at the bottom margin.
func (g *Grid) linefeed() {
	s := g.scr()
	if s.cy == s.scrollBot {
		g.scrollUp(s.scrollTop, s.scrollBot, 1)
	} else if s.cy < g.rows-1 {
		s.cy++
	}
}

func (g *Grid) scrollUp(top, bot, n int) {
	s := g.scr()
	if top > bot {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	copy(s.lines[top:bot+1], s.lines[top+n:bot+1])
	for y := bot - n + 1; y <= bot; y++ {
		s.lines[y] = s.blankLine(g.cols)
	}
}

func (g *Grid) scrollDown(top, bot, n int) {
	s := g.scr()
	if top > bot {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	copy(s.lines[top+n:bot+1], s.lines[top:bot+1-n])
	for y := top; y < top+n; y++ {
		s.lines[y] = s.blankLine(g.cols)
	}
}

// eraseDisplay: 0 cursor→end, 1 start→cursor, 2/3 whole screen.
func (g *Grid) eraseDisplay(mode int) {
	s := g.scr()
	b := s.blank()
	switch mode {
	case 0:
		for x := s.cx; x < g.cols; x++ {
			s.lines[s.cy][x] = b
		}
		for y := s.cy + 1; y < g.rows; y++ {
			s.lines[y] = s.blankLine(g.cols)
		}
	case 1:
		for y := 0; y < s.cy; y++ {
			s.lines[y] = s.blankLine(g.cols)
		}
		for x := 0; x <= s.cx && x < g.cols; x++ {
			s.lines[s.cy][x] = b
		}
	default: // 2/3
		for y := 0; y < g.rows; y++ {
			s.lines[y] = s.blankLine(g.cols)
		}
	}
}

// eraseLine: 0 cursor→eol, 1 bol→cursor, 2 whole line.
func (g *Grid) eraseLine(mode int) {
	s := g.scr()
	b := s.blank()
	switch mode {
	case 0:
		for x := s.cx; x < g.cols; x++ {
			s.lines[s.cy][x] = b
		}
	case 1:
		for x := 0; x <= s.cx && x < g.cols; x++ {
			s.lines[s.cy][x] = b
		}
	default:
		s.lines[s.cy] = s.blankLine(g.cols)
	}
}

func (g *Grid) eraseChars(n int) {
	s := g.scr()
	b := s.blank()
	for x := s.cx; x < s.cx+n && x < g.cols; x++ {
		s.lines[s.cy][x] = b
	}
}

func (g *Grid) insertChars(n int) {
	s := g.scr()
	if n > g.cols-s.cx {
		n = g.cols - s.cx
	}
	row := s.lines[s.cy]
	copy(row[s.cx+n:], row[s.cx:g.cols-n])
	blank := s.blank()
	for x := s.cx; x < s.cx+n; x++ {
		row[x] = blank
	}
}

func (g *Grid) deleteChars(n int) {
	s := g.scr()
	if n > g.cols-s.cx {
		n = g.cols - s.cx
	}
	row := s.lines[s.cy]
	copy(row[s.cx:], row[s.cx+n:])
	blank := s.blank()
	for x := g.cols - n; x < g.cols; x++ {
		row[x] = blank
	}
}

func (g *Grid) insertLines(n int) {
	s := g.scr()
	if s.cy < s.scrollTop || s.cy > s.scrollBot {
		return
	}
	if n > s.scrollBot-s.cy+1 {
		n = s.scrollBot - s.cy + 1
	}
	copy(s.lines[s.cy+n:s.scrollBot+1], s.lines[s.cy:s.scrollBot+1-n])
	for y := s.cy; y < s.cy+n; y++ {
		s.lines[y] = s.blankLine(g.cols)
	}
}

func (g *Grid) deleteLines(n int) {
	s := g.scr()
	if s.cy < s.scrollTop || s.cy > s.scrollBot {
		return
	}
	if n > s.scrollBot-s.cy+1 {
		n = s.scrollBot - s.cy + 1
	}
	copy(s.lines[s.cy:s.scrollBot+1], s.lines[s.cy+n:s.scrollBot+1])
	for y := s.scrollBot - n + 1; y <= s.scrollBot; y++ {
		s.lines[y] = s.blankLine(g.cols)
	}
}

// SetSize reshapes both buffers: top-left content is kept, lines extend or
// truncate to the new width, and the scroll region resets to full screen.
func (g *Grid) SetSize(cols, rows int) {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if cols == g.cols && rows == g.rows {
		return
	}
	g.cols, g.rows = cols, rows
	g.main = resizeScreen(g.main, cols, rows)
	g.alt = resizeScreen(g.alt, cols, rows)
}

func resizeScreen(s screen, cols, rows int) screen {
	lines := make([][]Cell, rows)
	for y := 0; y < rows; y++ {
		if y < len(s.lines) {
			l := s.lines[y]
			switch {
			case len(l) > cols:
				l = append([]Cell(nil), l[:cols]...)
			case len(l) < cols:
				nl := make([]Cell, cols)
				copy(nl, l)
				for x := len(l); x < cols; x++ {
					nl[x] = Cell{Ch: ' ', Fg: ColorDefault, Bg: ColorDefault}
				}
				l = nl
			}
			lines[y] = l
		} else {
			lines[y] = defaultLine(cols)
		}
	}
	s.lines = lines
	s.scrollTop, s.scrollBot = 0, rows-1
	if s.cx > cols-1 {
		s.cx = cols - 1
	}
	if s.cy > rows-1 {
		s.cy = rows - 1
	}
	return s
}

// Render returns a copy of the ACTIVE screen (alt content while ?1049 is
// set) — exactly what's shown, exactly rows lines of cols cells.
func (g *Grid) Render() []Row {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.scr()
	out := make([]Row, g.rows)
	for y := range out {
		r := make(Row, g.cols)
		copy(r, s.lines[y])
		out[y] = r
	}
	return out
}

// Cursor reports the caret position on the active screen (debug/panel).
func (g *Grid) Cursor() (x, y int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.scr()
	return s.cx, s.cy
}

// Cols / Rows report the grid geometry.
func (g *Grid) Cols() int { g.mu.Lock(); defer g.mu.Unlock(); return g.cols }
func (g *Grid) Rows() int { g.mu.Lock(); defer g.mu.Unlock(); return g.rows }

// AltActive reports whether the alternate screen (?1049) is showing.
func (g *Grid) AltActive() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.altActive }

// Rev bumps on every applied Write — cheap change detection for panels.
func (g *Grid) Rev() uint64 { g.mu.Lock(); defer g.mu.Unlock(); return g.rev }

// CellAt returns the cell at (x=col, y=row) of the active screen
// (test/debug helper).
func (g *Grid) CellAt(x, y int) Cell {
	g.mu.Lock()
	defer g.mu.Unlock()
	if y < 0 || y >= g.rows || x < 0 || x >= g.cols {
		return Cell{}
	}
	return g.scr().lines[y][x]
}

// LineText returns the active screen's row y as a right-trimmed string
// (test/debug helper).
func (g *Grid) LineText(y int) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if y < 0 || y >= g.rows {
		return ""
	}
	return rowText(g.scr().lines[y])
}

func rowText(l []Cell) string {
	rs := make([]rune, len(l))
	for i, c := range l {
		ch := c.Ch
		if ch == 0 {
			ch = ' '
		}
		rs[i] = ch
	}
	s := string(rs)
	end := len(rs)
	for end > 0 && rs[end-1] == ' ' {
		end--
	}
	return s[:end]
}

// ScreenText joins all active-screen rows (test/debug helper: marker scans).
func (g *Grid) ScreenText() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.scr()
	out := make([]byte, 0, g.cols*g.rows)
	for y := range s.lines {
		out = append(out, rowText(s.lines[y])...)
		out = append(out, '\n')
	}
	return string(out)
}
