// topbar_test.go — the project segment contract: with a projinfo.Info the
// right side reads "<clock> | <project> (<branch>)" (branch in the theme
// accent); zero infos renders exactly the legacy cwd- basename layout; and
// every rendered line stays within its width budget. ANSI-stripped
// assertions, matching statusbar_test.go idiom.
package chrome

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/projinfo"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

var testInfo = projinfo.Info{Project: "myproj", Branch: "main"}

// TestTopBarProjectBranch is also the PROOF fixture: it logs the stripped
// width-80 render for the feature review.
func TestTopBarProjectBranch(t *testing.T) {
	st := state.OfficeState{Mode: state.ModeLive}
	out := ansi.Strip(TopBar(st, 80, testInfo))
	for _, want := range []string{"theboringoffice " + AppVersion, "LIVE", "agents 0", "09:00", "myproj", "(main)"} {
		if !strings.Contains(out, want) {
			t.Errorf("top bar missing %q:\n%s", want, out)
		}
	}
	t.Logf("width-80 render: %q", out)
}

func TestTopBarZeroInfoMatchesLegacyLayout(t *testing.T) {
	st := state.OfficeState{Mode: state.ModeLive}
	out := ansi.Strip(TopBar(st, 80))
	for _, want := range []string{"theboringoffice " + AppVersion, "LIVE", "agents 0", "09:00", cwdBase()} {
		if !strings.Contains(out, want) {
			t.Errorf("zero-arg top bar missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(main)") || strings.Contains(out, "myproj") {
		t.Errorf("zero-arg top bar must not grow a project segment:\n%s", out)
	}
}

func TestTopBarWidthBudget(t *testing.T) {
	st := state.OfficeState{Mode: state.ModeLive, Tick: 450}
	for _, width := range []int{80, 40} {
		if w := lipgloss.Width(ansi.Strip(TopBar(st, width, testInfo))); w > width {
			t.Errorf("TopBar(width %d): rendered %d cells", width, w)
		}
		if w := lipgloss.Width(ansi.Strip(TopBar(st, width))); w > width {
			t.Errorf("zero-arg TopBar(width %d): rendered %d cells", width, w)
		}
	}
}

func TestTopBarCompactDropsBranchBelowSixty(t *testing.T) {
	st := state.OfficeState{Mode: state.ModeLive}

	// 57 = the old 50-col budget + the 7 cells the app name grew in the
	// grafeio→theboringoffice rename — below 60 the rule is unchanged:
	// project stays, branch drops.
	narrow := ansi.Strip(TopBarCompact(st, 57, testInfo))
	if !strings.Contains(narrow, "myproj") {
		t.Errorf("compact(57) must keep the project:\n%s", narrow)
	}
	if strings.Contains(narrow, "(main)") {
		t.Errorf("compact(57) must drop the branch below 60 cells:\n%s", narrow)
	}

	wide := ansi.Strip(TopBarCompact(st, 80, testInfo))
	if !strings.Contains(wide, "myproj") || !strings.Contains(wide, "(main)") {
		t.Errorf("compact(80) must show project and branch:\n%s", wide)
	}

	zero := ansi.Strip(TopBarCompact(st, 80))
	if strings.Contains(zero, "myproj") || strings.Contains(zero, "(main)") {
		t.Errorf("zero-arg compact must not grow a project segment:\n%s", zero)
	}
	if !strings.Contains(zero, "agents 0") || !strings.Contains(zero, "09:00") {
		t.Errorf("zero-arg compact baseline segments must survive:\n%s", zero)
	}
}
