package panels

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSlashPopoverStopRow — the popover is a menu of REAL outcomes: /stop
// (a real applySlash command since wave 13) must be discoverable. Typing
// "/st" prefix-filters the box to /status + /stop; the /stop row carries
// its one-line abort description.
func TestSlashPopoverStopRow(t *testing.T) {
	c := NewChat(nil)
	c.slashOpen = true
	c.slashMode = slashModeCmd
	c.slashFrag = "st"
	c.SetSize(60, 24)
	c.refilterSlash()
	plain := ansi.Strip(c.renderSlashPopover())
	for _, want := range []string{"/stop", "abort current work (boss + workers)", "/status"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("slash popover \"/st\" filter missing %q:\n%s", want, plain)
		}
	}
	t.Logf("slash popover \"/st\" box:\n%s", plain)
}

// TestSlashCommandsAllReal — every curated popover command must name a
// command applySlash actually implements. The name list here mirrors the
// model's slash switch (greps in the wave proof pin each entry to its
// case); a popover row for a nonexistent command is a UX lie.
func TestSlashCommandsAllReal(t *testing.T) {
	seen := map[string]bool{}
	for _, sc := range slashCommands {
		if seen[sc.name] {
			t.Fatalf("duplicate popover row %q", sc.name)
		}
		seen[sc.name] = true
		if !strings.HasPrefix(sc.name, "/") {
			t.Fatalf("popover row %q is not a slash command", sc.name)
		}
	}
	for _, required := range []string{
		"/stop", "/new", "/route", "/zen", "/focus", "/power", "/model", "/question", "/perm",
	} {
		if !seen[required] {
			t.Fatalf("real command %q missing from the popover list", required)
		}
	}
}
