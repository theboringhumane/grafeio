// boot.go — the BOOT SPLASH: a fullscreen animated ASCII wordmark +
// staggered fake-boot status cascade shown while the office backend warms
// up. Self-contained by design (own tick chain, own messages, exact w×h
// frames) so model.go wires it in a handful of lines: route every msg into
// Boot.Update until bootDoneMsg/bootSkipMsg lands, gate View on a bootDone
// flag, and call SetReady on the first backend event (server online).
//
// Lifecycle:
//
//	NewBoot(w,h)          → frame 0 (deterministic: wordmark + first line)
//	bootTickMsg (80ms)    → advance the cascade one frame, re-arm the tick
//	SetReady()            → backend online: cascade runs at DOUBLE pace
//	any key               → bootSkipMsg (the member skips the show)
//	Done()                → bootDoneMsg fires from the crossing Update
//
// Done() is true when (every line is OK AND SetReady fired) OR the 4s hard
// cap (bootMaxTicks) trips — the splash NEVER hangs past ~4s, ready or not.
package app

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
)

// Boot pacing. The whole show is tick-count PURE (no wall-clock reads), so
// tests drive it with synthetic bootTickMsg and pin frames exactly.
const (
	bootTickEvery = 80 * time.Millisecond // frame cadence
	bootMaxTicks  = 50                    // 50×80ms = 4.0s — the never-hang cap

	bootStagger      = 6 // ticks between line starts while the backend warms
	bootReadyStagger = 3 // hot backend: compressed cascade (the JUMP forward)
	bootSpeed        = 2 // chars typed per tick, cold
	bootReadySpeed   = 4 // SetReady's double pace
)

// bootWordmark — the hand-drawn "THEBORING / OFFICE" splash: 2 stacked words
// (14 chars don't fit one block row), 2×5 rows × 44 cells of full blocks
// (U+2588, exactly one terminal cell per rune, safe for lipgloss.Width).
// EVERY row is padded to 44 so centering math is uniform.
var bootWordmark = []string{
	" ████ █  █ ████ ███  ████ ███  ███ █  █ ████",
	"  ██  █  █ █    █  █ █  █ █  █  █  ██ █ █   ",
	"  ██  ████ ███  ███  █  █ ███   █  █ ██ █ ██",
	"  ██  █  █ █    █  █ █  █ █ █   █  █  █ █  █",
	"  ██  █  █ ████ ███  ████ █  █ ███ █  █ ████",
	" ████ ████ ████ ███ ████ ████               ",
	" █  █ █    █     █  █    █                  ",
	" █  █ ███  ███   █  █    ███                ",
	" █  █ █    █     █  █    █                  ",
	" ████ █    █    ███ ████ ████               ",
}

// bootSubtitle — the brand line under the wordmark.
const bootSubtitle = "γραφείο · a startup office in your terminal"

// bootLines — the fake boot cascade, typed out in order ("▸ " prefix is
// render-owned). While a line types, a braille spinner rides the reveal
// head; fully typed flips to a green OK.
var bootLines = []string{
	"uplink: opencode serve",
	"agents: waking the floor",
	"board: agentmemory sync",
	"mail: signal router",
	"mcp: probing servers",
}

// bootHint — the skip affordance at the bottom of the content block.
const bootHint = "press any key to skip"

// Internal wire messages (package-private: model.go wires same-package).
// bootTickMsg drives the animation chain (re-armed per handled tick);
// bootDoneMsg fires once when Done() first reports true; bootSkipMsg fires
// on any key press — both are terminal for the boot gate.
type bootTickMsg struct{}
type bootDoneMsg struct{}
type bootSkipMsg struct{}

// bootTick arms the next animation frame.
func bootTick() tea.Cmd {
	return tea.Tick(bootTickEvery, func(time.Time) tea.Msg { return bootTickMsg{} })
}

// Boot is the animated ASCII boot splash shown until the office backend
// is ready (or the user skips with any key). Value semantics like every
// tea component — Update returns the advanced copy.
type Boot struct {
	w, h  int  // last known frame size (Resize keeps centering fresh)
	tick  int  // frames since start — the ONLY clock, drives everything
	ready bool // backend online: double pace, and the Done() co-condition
	sp    spinner.Model
}

// NewBoot builds the splash at the current size (Resize fixes it later;
// 0×0 renders "" until the first WindowSizeMsg lands).
func NewBoot(w, h int) Boot {
	return Boot{
		w: w, h: h,
		sp: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

// Update advances frames on tea.TickMsg / this package's bootTickMsg;
// any key press returns bootSkipMsg; when Done() the returned cmd fires
// bootDoneMsg. SetReady() flips the reveal percentage faster (backend
// hot).
func (b Boot) Update(msg tea.Msg) (Boot, tea.Cmd) {
	switch msg := msg.(type) {
	case bootTickMsg:
		b.tick++
		// advance the braille spinner off OUR tick: a synthetic zero
		// spinner.TickMsg steps one frame with no id/tag to reject, and
		// the re-arming cmd it hands back is dropped — the boot owns the
		// whole cadence, which keeps tick→frame mapping deterministic.
		b.sp, _ = b.sp.Update(spinner.TickMsg{})
		if b.Done() {
			return b, func() tea.Msg { return bootDoneMsg{} }
		}
		return b, bootTick()
	case tea.KeyPressMsg:
		// any key skips the show (the office hides nothing behind it)
		return b, func() tea.Msg { return bootSkipMsg{} }
	case tea.WindowSizeMsg:
		b.Resize(msg.Width, msg.Height)
	}
	return b, nil
}

// Done — splash finished: the whole cascade is OK AND the backend is hot,
// OR the 4s cap tripped (guaranteed exit, ready or not).
func (b Boot) Done() bool {
	return b.tick >= bootMaxTicks || (b.ready && b.cascadeDone())
}

// SetReady — backend online: the cascade finishes at double pace (and a
// completed cascade exits on the spot).
func (b *Boot) SetReady() { b.ready = true }

// Resize recomputes centering for the next View (WindowSizeMsg-routed).
func (b *Boot) Resize(w, h int) { b.w, b.h = w, h }

// speed/stagger — pace knobs; SetReady flips both, and because progress is
// recomputed FROM THE TICK, flipping them mid-show fast-forwards the whole
// cascade at once (the "jump forward" on backend-ready).
func (b Boot) speed() int {
	if b.ready {
		return bootReadySpeed
	}
	return bootSpeed
}

func (b Boot) stagger() int {
	if b.ready {
		return bootReadyStagger
	}
	return bootStagger
}

// progress — revealed chars of line i right now (0 while its stagger slot
// is still ahead; clamps at the full text). Pure in (tick, ready).
func (b Boot) progress(i int) int {
	start := i * b.stagger()
	if b.tick < start {
		return 0
	}
	p := (b.tick - start + 1) * b.speed()
	if p > len(bootLines[i]) {
		p = len(bootLines[i])
	}
	return p
}

// cascadeDone — every status line fully typed (the reveal finished).
func (b Boot) cascadeDone() bool {
	return b.progress(len(bootLines)-1) >= len(bootLines[len(bootLines)-1])
}

// statusLine renders cascade row i: "▸ " + typed text, then EITHER the
// braille spinner (still typing) OR the green OK (done). ok=false while the
// line's stagger slot hasn't opened (the row renders NOTHING — the cascade
// stays a reveal, not a pre-printed checklist).
func (b Boot) statusLine(i int) (string, bool) {
	if b.tick < i*b.stagger() {
		return "", false
	}
	txt := bootLines[i]
	p := b.progress(i)
	line := chrome.DimText.Render("▸ ") + chrome.Fg(chrome.White, txt[:p])
	if p >= len(txt) {
		line += chrome.DimText.Render(" … ") + chrome.OKText.Render("OK")
	} else {
		// spinner styled at render time (chrome vars re-point on /theme —
		// a construction-time copy would freeze the ink).
		line += chrome.AccentText.Render(b.sp.View())
	}
	return line, true
}

// View — the full frame as one ANSI string, ALWAYS exactly w×h cells:
// content (≤44 cols, 20 rows) centered with space fill, truncated
// ANSI-aware when the terminal is smaller than the content.
func (b Boot) View() string {
	if b.w <= 0 || b.h <= 0 {
		return ""
	}
	// the content block, top to bottom: wordmark / subtitle / cascade / hint
	var rows []string
	mark := lipgloss.NewStyle().Foreground(chrome.Accent).Bold(true)
	for _, r := range bootWordmark {
		rows = append(rows, mark.Render(r))
	}
	rows = append(rows, "", chrome.DimText.Render(bootSubtitle), "")
	for i := range bootLines {
		// unrevealed lines RESERVE their row anyway: the block keeps a
		// constant 20 rows, so centering never drifts as the cascade grows.
		if line, ok := b.statusLine(i); ok {
			rows = append(rows, line)
		} else {
			rows = append(rows, "")
		}
	}
	rows = append(rows, "", chrome.DimText.Render(bootHint))

	top := (b.h - len(rows)) / 2
	if top < 0 {
		top = 0
	}
	frame := make([]string, b.h)
	for r := 0; r < b.h; r++ {
		s := ""
		if i := r - top; i >= 0 && i < len(rows) {
			s = rows[i]
		}
		frame[r] = bootFitRow(s, b.w)
	}
	return strings.Join(frame, "\n")
}

// bootFitRow pads/clips ONE row to exactly w cells: centered with space
// fill while it fits, left-aligned ANSI-aware truncation when it doesn't
// (escapes are consumed atomically — a narrow window never shreds an SGR).
func bootFitRow(s string, w int) string {
	switch cw := lipgloss.Width(s); {
	case cw < w:
		pad := (w - cw) / 2
		return strings.Repeat(" ", pad) + s + strings.Repeat(" ", w-cw-pad)
	case cw > w:
		t := ansi.Truncate(s, w, "")
		if tw := lipgloss.Width(t); tw < w { // defensive: never leave a short row
			t += strings.Repeat(" ", w-tw)
		}
		return t
	default:
		return s
	}
}
