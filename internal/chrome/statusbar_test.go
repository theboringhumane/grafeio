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
