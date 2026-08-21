// topbar.go — one-line app bar, full width (port of node-legacy topbar.tsx):
//
//	left:  grafeio v0.1.0 | MODE | agents <n>
//	right: <office clock> | <cwd basename>
//
// app name bold white, DEMO yellow / LIVE green, agents count cyan,
// clock + cwd dim, all on the inverted (blackBright) bar.
package chrome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/state"
)

// AppVersion is shown in the topbar (grafeio v0.1.0).
const AppVersion = "v0.1.0"

// OfficeClock — port of topbar.tsx officeClock: starts 09:00,
// +1 minute per ~30 ticks. Kept here on purpose: chrome does NOT import
// internal/office (dup noted: office owns the same tick clock for its staff).
func OfficeClock(tick int) string {
	if tick < 0 {
		tick = 0
	}
	minutes := (tick / 30) % (12 * 60)
	return fmt.Sprintf("%02d:%02d", 9+minutes/60, minutes%60)
}

var (
	cwdOnce  sync.Once
	cwdValue string
)

func cwdBase() string {
	cwdOnce.Do(func() {
		if d, err := os.Getwd(); err == nil {
			cwdValue = filepath.Base(d)
			return
		}
		cwdValue = "grafeio"
	})
	return cwdValue
}

// TopBar renders the full-width top bar for one frame.
func TopBar(st state.OfficeState, width int) string {
	mode := strings.ToUpper(string(st.Mode))
	agents := fmt.Sprintf("%d", len(st.Employees))
	clock := OfficeClock(st.Tick)
	cwd := cwdBase()

	left := OnBarBold(White, " grafeio "+AppVersion) +
		OnBar(White, " | ") +
		OnBar(ModeColor(st.Mode), mode) +
		OnBar(White, " | agents ") +
		OnBar(Info, agents)
	right := OnBar(Dim, clock) +
		OnBar(White, " | ") +
		OnBar(Dim, cwd+" ")

	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	return Bar.Width(width).Render(line)
}

// TopBarCompact — the /compact layout's compressed top bar: the segment
// budget drops mode and cwd, keeping the app name, agents count and clock.
func TopBarCompact(st state.OfficeState, width int) string {
	agents := fmt.Sprintf("%d", len(st.Employees))
	clock := OfficeClock(st.Tick)

	line := OnBarBold(White, " grafeio "+AppVersion) +
		OnBar(White, " | agents ") +
		OnBar(Info, agents) +
		OnBar(White, " | ") +
		OnBar(Dim, clock+" ")

	if lipgloss.Width(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	return Bar.Width(width).Render(line)
}
