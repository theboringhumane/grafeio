// statusbar_test.go — the cost/token tag contract: REAL opencode-reported
// counters render immediately before the mode segment (dim, humanized);
// while nothing real has been reported the segment hides and the bar stays
// byte-identical to the pre-tag layout. No number is ever estimated.
package chrome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

func TestHumanTokens(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0k"},
		{1234, "1.2k"},
		{12_345, "12.3k"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, c := range cases {
		if got := humanTokens(c.in); got != c.want {
			t.Errorf("humanTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCostUSD(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0.0042, "$0.0042"}, // sub-dollar: 4 decimals
		{0.5, "$0.5000"},
		{1, "$1.00"}, // $1 and up: 2 decimals
		{2.5, "$2.50"},
		{12.34, "$12.34"},
	}
	for _, c := range cases {
		if got := costUSD(c.in); got != c.want {
			t.Errorf("costUSD(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestUsageTag(t *testing.T) {
	if got := usageTag(state.OfficeState{}); got != "" {
		t.Fatalf("zero usage must hide the tag, got %q", got)
	}
	// Tokens known but real cost data absent: $ drops on its own.
	if got := usageTag(state.OfficeState{TokensIn: 10_000, TokensOut: 2_400}); got != "12.4k tok" {
		t.Fatalf("tokens-only tag = %q, want %q", got, "12.4k tok")
	}
	// Cost only (defensive — the wire always pairs them): $ leads, no tok.
	if got := usageTag(state.OfficeState{CostUSD: 0.0042}); got != "$0.0042" {
		t.Fatalf("cost-only tag = %q, want %q", got, "$0.0042")
	}
	// Both: cost first, then the humanized token total (in+out).
	if got := usageTag(state.OfficeState{TokensIn: 10_000, TokensOut: 2_400, CostUSD: 0.0042}); got != "$0.0042 · 12.4k tok" {
		t.Fatalf("full tag = %q, want %q", got, "$0.0042 · 12.4k tok")
	}
	// Prompt-cache: zero read hides the segment entirely (sessions without
	// caching stay byte-identical); a read hit appends read+write volume.
	if got := usageTag(state.OfficeState{TokensIn: 10_000, TokensOut: 2_400, CostUSD: 0.0042, TokensCacheWrite: 900}); got != "$0.0042 · 12.4k tok" {
		t.Fatalf("cache-write-only tag = %q, want %q (read 0 hides the segment)", got, "$0.0042 · 12.4k tok")
	}
	if got := usageTag(state.OfficeState{TokensIn: 180_000, TokensOut: 7_900, CostUSD: 0.5921, TokensCacheRead: 45_000, TokensCacheWrite: 200}); got != "$0.5921 · 187.9k tok · cache 45.2k" {
		t.Fatalf("cache tag = %q, want %q", got, "$0.5921 · 187.9k tok · cache 45.2k")
	}
}

func TestStatusBarHidesUsageTagWhenZero(t *testing.T) {
	st := state.OfficeState{Mode: state.ModeLive, StatusLine: "scroll"}
	out := ansi.Strip(StatusBar(st, "enter:send", 0, 120))
	if strings.Contains(out, "tok") || strings.Contains(out, "$") {
		t.Fatalf("zero usage must leave the bar untouched, got:\n%s", out)
	}
	if !strings.Contains(out, "board 0/0/0") || !strings.Contains(out, "live") {
		t.Fatalf("baseline segments must render, got:\n%s", out)
	}
}

func TestStatusBarRendersUsageTagBeforeMode(t *testing.T) {
	st := state.OfficeState{
		Mode:       state.ModeLive,
		StatusLine: "scroll",
		TokensIn:   10_000,
		TokensOut:  2_400,
		CostUSD:    0.0042,
	}
	out := ansi.Strip(StatusBar(st, "enter:send", 0, 120))
	tag := "$0.0042 · 12.4k tok"
	if !strings.Contains(out, tag) {
		t.Fatalf("usage tag missing from the bar, got:\n%s", out)
	}
	ti, li := strings.Index(out, tag), strings.Index(out, "live")
	if li < 0 || ti < 0 || ti > li {
		t.Fatalf("tag must sit immediately before the mode segment (tag@%d, live@%d):\n%s", ti, li, out)
	}
	if !strings.Contains(out, "board 0/0/0") {
		t.Fatalf("board segment must survive the insertion, got:\n%s", out)
	}
}

func TestStatusBarHidesCacheSegmentWhenNoCacheRead(t *testing.T) {
	st := state.OfficeState{
		Mode:       state.ModeLive,
		StatusLine: "scroll",
		TokensIn:   180_000,
		TokensOut:  7_900,
		CostUSD:    0.5921,
		// Write-only so far (cache.written but never read back): read 0
		// keeps the segment hidden.
		TokensCacheWrite: 45_200,
	}
	out := ansi.Strip(StatusBar(st, "enter:send", 0, 120))
	if strings.Contains(out, "cache") {
		t.Fatalf("zero cache READ must hide the cache segment, got:\n%s", out)
	}
	if !strings.Contains(out, "$0.5921 · 187.9k tok") {
		t.Fatalf("headline tag must render untouched, got:\n%s", out)
	}
}

func TestStatusBarRendersCacheSegmentBeforeMode(t *testing.T) {
	st := state.OfficeState{
		Mode:             state.ModeLive,
		StatusLine:       "scroll",
		TokensIn:         180_000,
		TokensOut:        7_900,
		CostUSD:          0.5921,
		TokensCacheRead:  45_000,
		TokensCacheWrite: 200,
	}
	out := ansi.Strip(StatusBar(st, "enter:send", 0, 120))
	tag := "$0.5921 · 187.9k tok · cache 45.2k"
	if !strings.Contains(out, tag) {
		t.Fatalf("cache segment missing from the bar, got:\n%s", out)
	}
	ci, li := strings.Index(out, "cache 45.2k"), strings.Index(out, "live")
	if li < 0 || ci < 0 || ci > li {
		t.Fatalf("cache segment must sit before the mode segment (cache@%d, live@%d):\n%s", ci, li, out)
	}
	// The composed line the member sees, verbatim:
	t.Logf("statusbar composed: %s", out)
	if idx := strings.Index(out, "| $"); idx >= 0 {
		t.Logf("usage segment: %s", out[idx:])
	}
}

func TestStatusBarUsageTagNarrowWidthSafe(t *testing.T) {
	st := state.OfficeState{
		Mode:       state.ModeLive,
		StatusLine: "a very long status line that fights for room on small terminals",
		TokensIn:   10_000,
		TokensOut:  2_400,
		CostUSD:    0.0042,
	}
	out := StatusBar(st, "tab:panels · enter:send", 2, 48)
	if w := lipgloss.Width(ansi.Strip(out)); w > 48 {
		t.Fatalf("bar must truncate gracefully on narrow widths, got %d cells:\n%s", w, ansi.Strip(out))
	}
}
