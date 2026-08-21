// scroll.go — the Scrollback: a mutex-guarded ring of RAW terminal output
// (ANSI color escapes preserved exactly as emitted, so colors survive into
// the panel), plus the surgical sanitizer panels use before painting into
// a bubbletea surface.
//
// Storage model: the full byte stream is kept in a rolling window of
// maxBytes; DrainNew serves the delta since the last Read(); Lines()
// splits the raw stream into \n-bounded rows (no rewrap on resize — raw
// lines are the honest history).
package term

import (
	"bytes"
	"strings"
	"sync"
)

// defaultScrollback — minimum 2000 lines: ~160 cols × 2000 lines with
// margin for SGR sequences.
const defaultScrollback = 384 * 1024

// Scrollback buffers raw PTY bytes with a hard byte cap (oldest bytes drop
// first). Concurrent-safe: the reader goroutine writes while the TUI reads.
type Scrollback struct {
	mu sync.Mutex

	buf      []byte // rolling window of raw output
	maxBytes int

	// pending — bytes appended since the last DrainNew (the delta the
	// TUI polls). Bounded by maxBytes too.
	pending []byte

	// newc counts appends so callers can cheaply skip re-renders.
	newc uint64
}

// NewScrollback returns an empty buffer holding at most maxBytes.
func NewScrollback(maxBytes int) *Scrollback {
	if maxBytes < 4096 {
		maxBytes = defaultScrollback
	}
	return &Scrollback{maxBytes: maxBytes}
}

// Write implements io.Writer — the PTY reader loop's destination.
func (s *Scrollback) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	s.buf = append(s.buf, cp...)
	s.pending = append(s.pending, cp...)
	if over := len(s.buf) - s.maxBytes; over > 0 {
		s.buf = append([]byte(nil), s.buf[over:]...) // drop oldest
	}
	if over := len(s.pending) - s.maxBytes; over > 0 {
		s.pending = append([]byte(nil), s.pending[over:]...)
	}
	s.newc++
	return len(p), nil
}

// DrainNew returns the output appended since the last DrainNew call.
func (s *Scrollback) DrainNew() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := s.pending
	s.pending = nil
	return out
}

// Rev bumps on every append — cheap change detection for renderers.
func (s *Scrollback) Rev() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newc
}

// Raw returns a copy of the full retained byte window.
func (s *Scrollback) Raw() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, len(s.buf))
	copy(out, s.buf)
	return out
}

// len returns the retained byte count.
func (s *Scrollback) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

// Lines splits the retained stream into rows. The stream uses \r\n; lone
// \r (carriage-return rewrites, e.g. progress bars) keep their LAST
// rewrite — a pragmatic screen-ish flatten that never mangles newlines.
func (s *Scrollback) Lines() []string {
	raw := s.Raw()
	return splitLines(raw)
}

// splitLines flattens a raw stream: \r\n → \n, then \r inside a row
// keeps the longest suffix (rewritten progress bars land on one row).
func splitLines(raw []byte) []string {
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	rows := bytes.Split(raw, []byte("\n"))
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		text := string(r)
		if i := strings.LastIndexByte(text, '\r'); i >= 0 {
			text = text[i+1:] // classic \r rewrite: later text wins
		}
		out = append(out, text)
	}
	return out
}

// ---------------------------------------------------------------------------
// Sanitizer — strip cursor-protocol sequences that would fight the outer
// bubbletea app, while KEEPING SGR colors/styles (they render fine in a
// panel). Surgical, not a full vt emulator:
//
//	CSI ?...h/l            (private mode set/reset: \x1b[?25h, alt-screen…)
//	CSI ...H/f             (cursor position), CSI A/B/C/D/E/F/G (moves)
//	CSI J / CSI K          (erase display/line)
//	CSI s / u              (save/restore cursor)
//	ESC >  /  ESC =        (keypad modes), ESC c (RIS — full reset)
//	ESC 7 / ESC 8          (save/restore), ESC D/M/E (index/scroll/newline)
//	OSC \x1b] ... \x07 or ST (window title, hyperlinks stay stripped)
//	\x1b[?2026 sync, bracketed paste markers \x1b[?2004h etc (same CSI ? h/l)
// ---------------------------------------------------------------------------

// Sanitize returns text safe to embed inside an outer TUI frame.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		switch {
		case i+1 < len(s) && s[i+1] == ']':
			// OSC: ESC ] ... terminated by BEL or ESC \
			j := i + 2
			for j < len(s) {
				if s[j] == 0x07 {
					j++
					break
				}
				if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
					j += 2
					break
				}
				j++
			}
			i = j
			continue
		case i+1 < len(s) && s[i+1] == '[':
			// CSI: ESC [ params... final-byte  (0x40–0x7e)
			j := i + 2
			for j < len(s) && s[j] < 0x40 {
				j++
			}
			if j >= len(s) {
				i = j
				continue
			}
			final := s[j]
			params := s[i+2 : j]
			if keepsCSI(final, params) {
				b.WriteString(s[i : j+1])
			}
			i = j + 1
			continue
		default:
			// Two-byte ESC sequences: keep ONLY SGR-adjacent coloring —
			// none exist as 2-byte; all the dangerous ones drop.
			i += 2
			continue
		}
	}
	return b.String()
}

// keepsCSI reports whether a CSI sequence is display-safe (SGR color/style
// and a couple of harmless text attribute ops). Everything cursor- or
// mode-shaped drops so the inner terminal can't shove the outer frame.
func keepsCSI(final byte, params string) bool {
	switch final {
	case 'm': // SGR — colors, bold, etc. ALWAYS kept (idle prompt lives here)
		return true
	}
	return false
}

// sanitizeLine sanitizes one row and trims it to width display cells.
// Trims on ANSI boundaries: truncates PLAIN bytes only — a cheap guard:
// because keepsCSI only ever lets SGR through, trimming drops at most a
// trailing color sequence, and we append a reset so styles never leak.
func sanitizeLine(s string, width int) string {
	s = Sanitize(s)
	if width < 1 {
		return s + "\x1b[0m"
	}
	cells := 0
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// copy the whole escape run; escapes cost 0 cells
			// (Sanitize only ever lets CSI SGR through, so '[' is the only
			// introducer possible — still walk defensively)
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] < 0x40 {
					j++
				}
				if j < len(s) {
					j++ // final byte
				}
			} else if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		if cells >= width {
			break
		}
		// single byte-copy is fine for the wide-char bulk of shell output
		// (latin/UTF-8 lead byte counting as one cell stays within panel
		// slack; viewport padding absorbs the rest).
		b.WriteByte(s[i])
		i++
		if s[i-1] < 0x80 || s[i-1] >= 0xc0 {
			cells++
		}
	}
	return b.String() + "\x1b[0m"
}

// Render returns the Sanitized last-N rows, each trimmed to width cells.
// Empty scrollback renders an empty slice (panel paints its own prompt).
func (s *Scrollback) Render(n, width int) []string {
	rows := s.Lines()
	if len(rows) == 0 {
		return nil
	}
	// drop the trailing empty row (screen always ends with a \n tail)
	for len(rows) > 0 && strings.TrimSpace(stripANSI(rows[len(rows)-1])) == "" {
		rows = rows[:len(rows)-1]
	}
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = sanitizeLine(r, width)
	}
	return out
}

// stripASCII — minimal SGR strip for emptiness checks.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && s[j] < 0x40 {
					j++
				}
				if j < len(s) {
					j++ // final byte
				}
			} else if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
