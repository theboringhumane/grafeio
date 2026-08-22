// mcp_status_test.go — the /mcp chat-block contract: the four-status
// fixture renders one glyph+name+detail row per server (sorted input
// order honored), every row fits its panel width at 60 and at 30 (ANSI-
// safe fold, never overflow), glyphs carry their chrome colors (ANSI
// present, stripped text intact), and the footer hint appears EXACTLY
// when a failed or needs_auth server is on the list — preferring the
// failed target.
package panels

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

// mcpFixture is the demo backend's cast, mirrored locally (panels never
// import backend): one server per glyph lane.
func mcpFixture() []state.MCPServer {
	return []state.MCPServer{
		{Name: "github", Status: "needs_auth", Detail: "run: opencode mcp auth github"},
		{Name: "local-memory", Status: "connected", Detail: "12 tools"},
		{Name: "postgres", Status: "failed", Detail: "connection refused"},
		{Name: "web-search", Status: "connected", Detail: "3 tools"},
	}
}

// stripAll ansi-strips every line for content assertions.
func stripAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

func TestRenderMCPStatusFixture(t *testing.T) {
	lines := RenderMCPStatus(mcpFixture(), 60)
	want := []string{
		"mcp servers",
		"◐ github  run: opencode mcp auth github",
		"● local-memory  12 tools",
		"✗ postgres  connection refused",
		"● web-search  3 tools",
		"reconnect: /mcp reconnect postgres",
	}
	got := stripAll(lines)
	if len(got) != len(want) {
		t.Fatalf("w=60 must render 6 unwrapped lines, got %d:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d:\n want %q\n got  %q", i, want[i], got[i])
		}
	}
	// the glyphs carry real chrome color (ANSI survives the fold) while
	// the stripped text reads clean
	for _, l := range lines[1:5] {
		if !strings.Contains(l, "\x1b[") {
			t.Fatalf("server rows must be styled, got %q", l)
		}
	}
}

func TestRenderMCPStatusFitsWidths(t *testing.T) {
	for _, w := range []int{60, 30} {
		lines := RenderMCPStatus(mcpFixture(), w)
		for _, l := range lines {
			if cw := ansi.StringWidth(l); cw > w {
				t.Fatalf("w=%d: every row must fit, got width %d on %q", w, cw, l)
			}
		}
	}
	if wide, narrow := len(RenderMCPStatus(mcpFixture(), 60)), len(RenderMCPStatus(mcpFixture(), 30)); narrow <= wide {
		t.Fatalf("w=30 must fold more rows than w=60, got %d vs %d", narrow, wide)
	}
	// w=30 must FOLD (more lines than w=60) yet keep every name, glyph and
	// the hint's own words (the hint's target name may legitimately land
	// on the continuation row — that IS the wrap contract).
	narrow := strings.Join(stripAll(RenderMCPStatus(mcpFixture(), 30)), "\n")
	for _, want := range []string{"mcp servers", "github", "local-memory", "postgres", "web-search",
		"◐", "●", "✗", "reconnect: /mcp reconnect"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("w=30 folded render must retain %q, got:\n%s", want, narrow)
		}
	}
}

func TestRenderMCPStatusHintLogic(t *testing.T) {
	// all healthy: no footer hint at all
	healthy := []state.MCPServer{
		{Name: "a", Status: "connected"},
		{Name: "b", Status: "disabled"},
	}
	for _, l := range stripAll(RenderMCPStatus(healthy, 60)) {
		if strings.Contains(l, "reconnect:") {
			t.Fatalf("all-healthy lists render no hint, got %q", l)
		}
	}
	// needs_auth only: the hint targets that server
	authOnly := []state.MCPServer{{Name: "github", Status: "needs_auth", Detail: "run: opencode mcp auth github"}}
	got := stripAll(RenderMCPStatus(authOnly, 60))
	if got[len(got)-1] != "reconnect: /mcp reconnect github" {
		t.Fatalf("needs_auth is actionable, got last line %q", got[len(got)-1])
	}
	// failed + needs_auth: the failed name wins (reconnect fixes it)
	mixed := []state.MCPServer{
		{Name: "agithub", Status: "needs_auth"},
		{Name: "zpostgres", Status: "failed", Detail: "connection refused"},
	}
	got = stripAll(RenderMCPStatus(mixed, 60))
	if got[len(got)-1] != "reconnect: /mcp reconnect zpostgres" {
		t.Fatalf("failed must win the hint over needs_auth, got last line %q", got[len(got)-1])
	}
}

func TestRenderMCPStatusEmpty(t *testing.T) {
	got := stripAll(RenderMCPStatus(nil, 60))
	if len(got) != 2 || got[0] != "mcp servers" || got[1] != "no mcp servers configured" {
		t.Fatalf("an empty list renders header + empty note, got %q", got)
	}
}
