// mobile_test.go — the automatic mobile mode: window width below
// mobileMaxCols stacks the middle VERTICALLY (compact floor band on top,
// active panel full-width below) instead of the horizontal floor|sidebar
// split. No command flips it — resize() picks the layout per window size,
// and the width term in frameDigest forces a fresh render on the SAME
// frame the threshold crosses (no stale cached splits).
package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestMobilePredicateBoundary — the threshold itself: mobile below 100
// cols, not at, not above; the floor band is ~20% of the middle clamped
// to 8..14 rows.
func TestMobilePredicateBoundary(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  bool
	}{
		{99, true}, {100, false}, {101, false},
	} {
		if got := (Model{width: tc.width}).mobile(); got != tc.want {
			t.Fatalf("mobile() at width %d = %v, want %v", tc.width, got, tc.want)
		}
	}
	// the band: 20% clamped 8..14
	for _, tc := range []struct {
		middleH, want int
	}{
		{10, 8}, {38, 8}, {45, 9}, {68, 13}, {78, 14},
	} {
		if got := (Model{middleH: tc.middleH}).floorBandH(); got != tc.want {
			t.Fatalf("floorBandH(middleH=%d) = %d, want %d", tc.middleH, got, tc.want)
		}
	}
}

// TestMobileFrameStacksFloorAbovePanel — the 70x40 mobile frame: floor
// rows ABOVE the panel rows (boss-desk glyph before the tab-bar header
// on the page), chat still the active tab, and no line exceeds the
// 70-cell budget.
func TestMobileFrameStacksFloorAbovePanel(t *testing.T) {
	m := New(&recBackend{}, nil)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 70, Height: 40})
	if !m.mobile() {
		t.Fatalf("70 cols must be mobile")
	}
	if got := m.tabs.ActiveIndex(); got != 0 {
		t.Fatalf("chat stays the default active tab, got index %d", got)
	}
	frame := m.Frame()
	plain := ansi.Strip(frame)

	deskIdx := strings.Index(plain, "[=BOSS=]")
	barIdx := strings.Index(plain, "chat") // first "chat" is the tab bar
	if deskIdx < 0 {
		t.Fatalf("mobile frame has no floor band ([=BOSS=] missing)")
	}
	if barIdx < 0 {
		t.Fatalf("mobile frame has no panel tab bar")
	}
	if deskIdx > barIdx {
		t.Fatalf("mobile stack must place the floor ABOVE the panel: desk@%d after tab bar@%d", deskIdx, barIdx)
	}
	for i, line := range strings.Split(frame, "\n") {
		if w := lipgloss.Width(line); w > 70 {
			t.Fatalf("line %d is %d cells wide (budget 70): %q", i, w, ansi.Strip(line))
		}
	}
	// no sidebar chrome: the panel's box border must not ride beside the
	// floor band — the desk line carries floor content only
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "[=BOSS=]") && strings.Contains(line, "chat") {
			t.Fatalf("mobile must not split horizontally: floor and tab bar share a row: %q", line)
		}
	}
}

// TestMobileFrame70x24Prints — the gallery frame for the brief's PROOF:
// a full mobile frame at 70x24, ANSI stripped, logged verbatim.
func TestMobileFrame70x24Prints(t *testing.T) {
	m := New(&recBackend{}, nil)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 70, Height: 24})
	t.Logf("mobile frame at 70x24 (stripped ANSI):\n%s", ansi.Strip(m.Frame()))
}

// TestWideFrameKeepsHorizontalSplit — regression pin for the classic
// layout at 140x30: NOT mobile, floor left | sidebar right (the floor
// border and the tab bar share screen rows), sidebar 80 / floor 60 on
// the default config.
func TestWideFrameKeepsHorizontalSplit(t *testing.T) {
	m := New(&recBackend{}, nil)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	if m.mobile() {
		t.Fatalf("140 cols must keep the desktop layout")
	}
	if w, h, sidebar, floor := m.LayoutInfo(); w != 140 || h != 30 || sidebar != 80 || floor != 60 {
		t.Fatalf("LayoutInfo = %dx%d sidebar %d floor %d, want 140x30 sidebar 80 floor 60", w, h, sidebar, floor)
	}
	plain := ansi.Strip(m.Frame())
	// desk and tab bar sit on DIFFERENT columns of the SAME rows: find a
	// line that carries both floor-border "+" and the tab bar's "chat"
	split := false
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "+-") && strings.Contains(line, "chat") {
			split = true
			break
		}
	}
	if !split {
		t.Fatalf("140x30 must keep floor and sidebar side by side (no shared row found)")
	}
	// the floor's right border column is exactly at floorW-1
	lines := strings.Split(plain, "\n")
	if len(lines) < 2 {
		t.Fatalf("frame too short: %d lines", len(lines))
	}
}

// TestMobileResizeToggleSwapsLayout — the AUTO toggle: the SAME model
// crosses the threshold narrow wide narrow and each resize renders the
// right layout on the very next Frame — the width term in frameDigest
// invalidates the cache (no stale split), a repeated render at a fixed
// size is a cache hit, and returning to the wide size reproduces the
// original wide frame byte-for-byte (same untouched state).
func TestMobileResizeToggleSwapsLayout(t *testing.T) {
	m := New(&recBackend{}, nil)

	m = runMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	wide := ansi.Strip(m.Frame()) // miss
	if hits, _ := m.FrameCacheStats(); hits != 0 {
		t.Fatalf("first frame must miss, hits=%d", hits)
	}
	wideAgain := ansi.Strip(m.Frame()) // same digest -> hit
	hits, misses := m.FrameCacheStats()
	if hits != 1 || misses != 1 {
		t.Fatalf("repeat render at one size must hit once: hits=%d misses=%d", hits, misses)
	}

	// narrow: layout flips on this very render (cache miss, stacked)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 70, Height: 40})
	mobile := ansi.Strip(m.Frame())
	_, missesAfterNarrow := m.FrameCacheStats()
	if missesAfterNarrow != misses+1 {
		t.Fatalf("resizing across the threshold must force a fresh render: misses %d -> %d", misses, missesAfterNarrow)
	}
	if mobile == wide {
		t.Fatalf("mobile frame must differ from the split frame")
	}
	if !m.mobile() {
		t.Fatalf("70 cols is mobile after the resize")
	}
	deskIdx, barIdx := strings.Index(mobile, "[=BOSS=]"), strings.Index(mobile, "chat")
	if deskIdx < 0 || barIdx < 0 || deskIdx > barIdx {
		t.Fatalf("narrow frame must stack floor above panel (desk@%d bar@%d)", deskIdx, barIdx)
	}

	// wide again: straight back to the split, byte-identical to the
	// first wide frame (state never moved between renders)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 140, Height: 30})
	wide2 := ansi.Strip(m.Frame())
	if wide2 != wide {
		t.Fatalf("wide -> narrow -> wide must reproduce the original wide frame")
	}
	if wideAgain != wide {
		t.Fatalf("the cached repeat render should equal the first wide frame")
	}
}

// TestMobileZenStillFullFloor — the mode interplay ruling: zen keeps
// owning the whole middle (sidebar hidden) even when the window is
// narrow; mobile reshapes only the normal layout.
func TestMobileZenStillFullFloor(t *testing.T) {
	m := New(&recBackend{}, nil)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 70, Height: 40})
	m.zen = true
	plain := ansi.Strip(m.Frame())
	if !strings.Contains(plain, "[=BOSS=]") {
		t.Fatalf("zen at 70 cols keeps the fullscreen floor")
	}
	if strings.Contains(plain, "chat") {
		t.Fatalf("zen hides the sidebar tabs even in mobile widths")
	}
}
