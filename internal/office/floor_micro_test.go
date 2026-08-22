// floor_micro_test.go — the micro degrade plan (the mobile stack's floor
// band): sizes that USED to be the "small terminal" badge band (W<60 ||
// H<16) now render a FLAT zoneless strip — boss corner + one pod row + a
// right-end break strip — with every anchor and walk target above row 7,
// so a band as short as 8 rows keeps the whole office. Truly unusable
// shells (<40 cols or <6 rows) keep the old badge-only degrade, and the
// full walled plan is untouched at its own sizes.
package office

import (
	"strings"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// TestMicroPlan80x10RendersBossAndPodsNoBadge — the mobile band at a
// working size: boss desk + nameplate + pod row render, NO badge, no
// zones, never a panic; every row exactly 80 cells.
func TestMicroPlan80x10RendersBossAndPodsNoBadge(t *testing.T) {
	st := state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			emp("boss", "boss", state.RoleManager, "manager", state.SpriteAtDesk),
			emp("hr-1", "hr", state.RoleHR, "hr", state.SpriteAtDesk),
			emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteWorking),
		},
	}
	plan := ComputePlan(80, 10)
	if plan.Tiny {
		t.Fatalf("80x10 is small-but-nonabsurd: micro must replace the badge, got Tiny")
	}
	if len(plan.Zones) != 0 {
		t.Fatalf("the micro plan is zoneless (walls need height), got %d zones", len(plan.Zones))
	}
	rows := BuildRows(st, 80, 10)
	if len(rows) != 10 {
		t.Fatalf("want 10 rows, got %d", len(rows))
	}
	for y, row := range rows {
		if len(row) != 80 {
			t.Fatalf("row %d: width %d, want 80", y, len(row))
		}
	}
	plain := Styleless(rows)
	if strings.Contains(plain, "small terminal") {
		t.Fatalf("80x10 must render the micro floor, not the badge:\n%s", plain)
	}
	for _, want := range []string{"[=BOSS=]", "[awaiting]", "[______]", "(_)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("micro floor missing %q:\n%s", want, plain)
		}
	}
	// boss desk sits ABOVE the pod row (corner office over the dev strip)
	deskRow := strings.Index(plain, "[=BOSS=]")
	podRow := strings.Index(plain, "[______]")
	if deskRow > podRow {
		t.Fatalf("boss desk must sit above the pod row: desk@%d pod@%d", deskRow, podRow)
	}
	// the break strip renders at 80 cols (>=60: counter, machine, tray)
	if !strings.Contains(plain, "[cof]") || !strings.Contains(plain, "[mail]") {
		t.Fatalf("80-col micro must carry the break strip (machine + tray):\n%s", plain)
	}
	t.Logf("micro floor at 80x10 (plain):\n%s", plain)
}

// TestMicroPlanSeatsStayAboveRow8 — the plan clamps to >=12 rows but the
// mobile grid can be 8 tall, and the grid CLIPS whatever sinks past it:
// every anchor and every walk target must live in grid rows 1..6.
func TestMicroPlanSeatsStayAboveRow8(t *testing.T) {
	for _, sz := range [][2]int{{80, 8}, {80, 10}, {96, 14}, {60, 8}, {40, 8}, {50, 14}} {
		plan := ComputePlan(sz[0], sz[1])
		if plan.Tiny {
			continue // tiny sizes are covered by the badge test
		}
		for seat, a := range plan.Anchor {
			if a.Y < 1 || a.Y > 6 {
				t.Fatalf("%dx%d: anchor %q at row %d — clipped off an 8-row band", sz[0], sz[1], seat, a.Y)
			}
		}
		for name, hot := range map[string]Point{
			"meet": plan.Hot.Meet, "mail": plan.Hot.Mail, "tea": plan.Hot.Tea,
			"clock": plan.Hot.Clock, "overflow": plan.Hot.Overflow,
		} {
			if hot.Y < 1 || hot.Y > 6 {
				t.Fatalf("%dx%d: hotspot %q at row %d — walkers would vanish off an 8-row band", sz[0], sz[1], name, hot.Y)
			}
		}
	}
}

// TestMicroStripNeeds60Cols — below 60 cols the break strip (counter,
// machine, tray) drops and the pods own the row; tea/mail/overflow move
// onto the corridor row so sprites never stand on furniture.
func TestMicroStripNeeds60Cols(t *testing.T) {
	rows := BuildRows(state.OfficeState{}, 50, 12)
	plain := Styleless(rows)
	if strings.Contains(plain, "[cof]") || strings.Contains(plain, "[mail]") {
		t.Fatalf("50-col micro must drop the break strip, got it:\n%s", plain)
	}
	if !strings.Contains(plain, "[=BOSS=]") || !strings.Contains(plain, "[______]") {
		t.Fatalf("50-col micro keeps the corner + pod row:\n%s", plain)
	}
	plan := ComputePlan(50, 12)
	if plan.Hot.Mail.Y > 3 {
		t.Fatalf("no strip -> mail target merges onto the corridor row, got row %d", plan.Hot.Mail.Y)
	}
	t.Logf("micro floor without strip at 50x12 (plain):\n%s", plain)
}

// TestTinyDegradeUnder40BadgeOnly — truly unusable widths keep the old
// badge path: the badge renders (cleanly, when nobody is walking) at the
// center of the frame and the plan is flagged Tiny.
func TestTinyDegradeUnder40BadgeOnly(t *testing.T) {
	plan := ComputePlan(36, 20)
	if !plan.Tiny {
		t.Fatalf("36 cols is truly unusable: want Tiny, got the micro strip")
	}
	plain := Styleless(BuildRows(state.OfficeState{}, 36, 20))
	if !strings.Contains(plain, "small terminal") {
		t.Fatalf("36x20 must draw the centered badge:\n%s", plain)
	}
	// height tier: <6 rows is badge regardless of width
	if !ComputePlan(120, 4).Tiny {
		t.Fatalf("120x4: <6 rows must stay badge-only")
	}
	// boundary: 40 cols is small-but-nonabsurd -> micro, not tiny
	if ComputePlan(40, 12).Tiny {
		t.Fatalf("40x12 must be micro (the app floor never goes below 40 cols)")
	}
}

// TestFullPlan120x30KeepsWalledRoomsAndCTO — regression pin: the micro
// band must NOT swallow mid/wide sizes; 120x30 is still the full walled
// office (cabins + conference + CTO suite), badge-free.
func TestFullPlan120x30KeepsWalledRoomsAndCTO(t *testing.T) {
	plan := ComputePlan(120, 30)
	if plan.Tiny {
		t.Fatalf("120x30 is the full office, never the badge")
	}
	if len(plan.Zones) == 0 {
		t.Fatalf("120x30 keeps its walled rooms (boss office, cabins, break)")
	}
	if _, ok := plan.Anchor["cto"]; !ok {
		t.Fatalf("120x30 keeps the CTO exec suite anchor")
	}
	plain := Styleless(BuildRows(state.OfficeState{Tick: 3}, 120, 30))
	for _, want := range []string{"[=BOSS=]", "theboringc", "[cof]", "::::"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("120x30 full plan missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "small terminal") {
		t.Fatalf("no badge at 120x30:\n%s", plain)
	}
}

// TestMicroPlanBandSizesNoPanic — the exact band sizes the mobile stack
// renders (8..14 rows across widths 40..99) plus the harness sizes: no
// panic, exact grid shape, never the badge.
func TestMicroPlanBandSizesNoPanic(t *testing.T) {
	for _, sz := range [][2]int{
		{40, 8}, {58, 14}, {70, 8}, {80, 9}, {96, 12}, {99, 14}, {120, 12}, {140, 8},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%dx%d panicked: %v", sz[0], sz[1], r)
				}
			}()
			rows := BuildRows(state.OfficeState{Tick: 3}, sz[0], sz[1])
			if len(rows) != sz[1] {
				t.Fatalf("%dx%d: got %d rows", sz[0], sz[1], len(rows))
			}
			for y, row := range rows {
				if len(row) != sz[0] {
					t.Fatalf("%dx%d row %d: width %d, want %d", sz[0], sz[1], y, len(row), sz[0])
				}
			}
			if plain := Styleless(rows); strings.Contains(plain, "small terminal") {
				t.Fatalf("%dx%d: micro band must not draw the badge:\n%s", sz[0], sz[1], plain)
			}
		}()
	}
}
