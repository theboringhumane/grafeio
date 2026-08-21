package office

import (
	"regexp"
	"strings"
	"testing"

	"github.com/theboringhumane/grafeio/internal/state"
)

func emp(id, name string, role state.EmployeeRole, seat string, sprite state.SpriteState) state.Employee {
	return state.Employee{ID: id, Name: name, Role: role, Seat: seat, Sprite: sprite}
}

// Invariant: every grid size renders exactly h rows of exactly w cells, no panic.
func TestBuildRowsSizes(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 25}, {84, 24}, {96, 24}, {72, 24},
		{58, 14}, {40, 10}, {8, 2}, {3, 1},
	}
	for _, s := range sizes {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%dx%d panicked: %v", s.w, s.h, r)
				}
			}()
			rows := BuildRows(state.OfficeState{}, s.w, s.h)
			if len(rows) != s.h {
				t.Fatalf("%dx%d: got %d rows", s.w, s.h, len(rows))
			}
			for y, row := range rows {
				if len(row) != s.w {
					t.Fatalf("%dx%d row %d: width %d, want %d", s.w, s.h, y, len(row), s.w)
				}
			}
		}()
	}
}

// 84x24: the three compact glass cabins stand; HR + dikastes staffed, cabin-3 empty.
func TestCabins84x24(t *testing.T) {
	st := state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			emp("hr-1", "hr", state.RoleHR, "hr", state.SpriteAtDesk),
			emp("dik-1", "dikastes", state.RoleReviewer, "cabin-2", state.SpriteAtDesk),
		},
	}
	plan := ComputePlan(84, 24)
	rows := BuildRows(st, 84, 24)
	plain := Styleless(rows)

	// at least 2 of the 3 cabin wall types show a solid 4+ run
	wallRuns := 0
	for _, re := range []string{`:{4,}`, `;{4,}`, `\.{4,}`} {
		if regexp.MustCompile(re).MatchString(plain) {
			wallRuns++
		}
	}
	if wallRuns < 2 {
		t.Fatalf("want >=2 cabin wall runs, got %d\n%s", wallRuns, plain)
	}

	// EMPTY cabin chair: gray + dim over all 3 anchor cells
	empty := plan.Anchor["cabin-3"]
	for dx := 0; dx < 3; dx++ {
		c := rows[empty.Y][empty.X+dx]
		if c.FG != "gray" || !c.Dim {
			t.Fatalf("empty cabin-3 chair cell (%d,%d) = {Ch:%q FG:%q Dim:%v}, want gray+dim",
				empty.X+dx, empty.Y, c.Ch, c.FG, c.Dim)
		}
	}

	// STAFFED cabin chair: sprite covers the anchor, never dim
	staffed := plan.Anchor["hr"]
	for dx := 0; dx < 3; dx++ {
		c := rows[staffed.Y][staffed.X+dx]
		if c.Dim {
			t.Fatalf("staffed hr chair cell (%d,%d) is dim, want lit", staffed.X+dx, staffed.Y)
		}
	}
	if rows[staffed.Y][staffed.X+1].Ch != 'H' {
		t.Fatalf("staffed hr chair center = %q, want 'H'", rows[staffed.Y][staffed.X+1].Ch)
	}
}

func nameplateRow(t *testing.T, st state.OfficeState) string {
	t.Helper()
	plan := ComputePlan(120, 25)
	rows := BuildRows(st, 120, 25)
	var b strings.Builder
	for _, c := range rows[plan.Nameplate.Y] {
		b.WriteRune(c.Ch)
	}
	line := b.String()
	return line[plan.Nameplate.X:plan.Nameplate.X+10]
}

func TestNameplate(t *testing.T) {
	t.Run("awaiting", func(t *testing.T) {
		plate := nameplateRow(t, state.OfficeState{Tick: 0})
		if plate != "[awaiting]" {
			t.Fatalf("got %q", plate)
		}
	})
	t.Run("typing on pending boss chat", func(t *testing.T) {
		plate := nameplateRow(t, state.OfficeState{
			Tick: 0,
			Chat: []state.ChatMsg{{ID: "m1", From: "boss", Text: "on it", Pending: true}},
		})
		if plate != "[typing]  " {
			t.Fatalf("got %q", plate)
		}
	})
	t.Run("meetin while someone is at the boss desk", func(t *testing.T) {
		plate := nameplateRow(t, state.OfficeState{
			Tick:       0,
			Employees:  []state.Employee{emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteMeeting)},
			Chat:       []state.ChatMsg{{ID: "m1", From: "boss", Pending: true}}, // meetin wins over typing
		})
		if plate != "[meetin]  " {
			t.Fatalf("got %q", plate)
		}
	})
}

// Blink frame: z floats one row above the right shoulder, never glued ("zMz").
func TestBlinkZsFloat(t *testing.T) {
	st := state.OfficeState{
		Tick: 16, // blink phase 0 -> "z"
		Employees: []state.Employee{
			emp("boss", "boss", state.RoleManager, "manager", state.SpriteAtDesk),
		},
	}
	plan := ComputePlan(84, 24)
	rows := BuildRows(st, 84, 24)
	a := plan.Anchor["manager"]

	row := Styleless(rows)
	lines := strings.Split(row, "\n")
	managerRow := lines[a.Y]
	if !strings.Contains(managerRow, " M ") {
		t.Fatalf("manager row %q has no plain \" M \"", managerRow)
	}
	if strings.Contains(managerRow, "zMz") {
		t.Fatalf("manager row %q glues the z (\"zMz\")", managerRow)
	}

	// positive control: the z cell is one row above the right shoulder (x+2, y-1)
	zc := rows[a.Y-1][a.X+2]
	if zc.Ch != 'z' || zc.FG != "gray" {
		t.Fatalf("cell (%d,%d) = {Ch:%q FG:%q}, want gray 'z'", a.X+2, a.Y-1, zc.Ch, zc.FG)
	}
}

// Lit screens: a working dev pod monitor glows cyan bold; an idle one stays gray.
func TestWorkingPodMonitor(t *testing.T) {
	st := state.OfficeState{
		Tick: 5,
		Employees: []state.Employee{
			emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteWorking),
			emp("t2", "tekton-2", state.RoleDeveloper, "dev-2", state.SpriteAtDesk),
		},
	}
	plan := ComputePlan(120, 25)
	rows := BuildRows(st, 120, 25)

	// working pod: monitor 1 right, 2 up from the chair anchor
	a := plan.Anchor["dev-1"]
	for dx := 0; dx < 3; dx++ {
		c := rows[a.Y-2][a.X+1+dx]
		if c.FG != "cyan" || !c.Bold {
			t.Fatalf("working monitor cell (%d,%d) = {FG:%q Bold:%v}, want cyan bold",
				a.X+1+dx, a.Y-2, c.FG, c.Bold)
		}
	}

	// idle pod: monitor gray, neither bold nor dim
	b := plan.Anchor["dev-2"]
	for dx := 0; dx < 3; dx++ {
		c := rows[b.Y-2][b.X+1+dx]
		if c.FG != "gray" || c.Bold || c.Dim {
			t.Fatalf("idle monitor cell (%d,%d) = {FG:%q Bold:%v Dim:%v}, want plain gray",
				b.X+1+dx, b.Y-2, c.FG, c.Bold, c.Dim)
		}
	}
}

// Determinism: same state -> same rows, twice.
func TestDeterminism(t *testing.T) {
	st := state.OfficeState{
		Tick: 42,
		Employees: []state.Employee{
			emp("boss", "boss", state.RoleManager, "manager", state.SpriteAtDesk),
			emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteWorking),
			emp("t2", "tekton-2", state.RoleDeveloper, "dev-2", state.SpriteToCoffee),
			emp("d1", "dikastes", state.RoleReviewer, "cabin-2", state.SpriteAtMailbox),
		},
		Chat:    []state.ChatMsg{{ID: "m1", From: "boss", Pending: true}},
		Bubbles: []state.SpeechBubble{{ID: "b1", EmployeeID: "boss", Text: "big day. lots", UntilTick: 100}},
	}
	first := Styleless(BuildRows(st, 120, 25))
	second := Styleless(BuildRows(st, 120, 25))
	if first != second {
		t.Fatalf("non-deterministic render:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

// Walker machine: to-manager walks the dogleg and arrives as meeting.
func TestAdvanceSprites(t *testing.T) {
	ComputePlan(120, 25)
	delete(walkers, "w1")
	st := state.OfficeState{
		Tick: 0,
		Employees: []state.Employee{
			emp("w1", "tekton-9", state.RoleDeveloper, "dev-1", state.SpriteToManager),
		},
	}
	steps := 0
	for steps = 1; steps < 400; steps++ {
		st.Tick = steps
		st = AdvanceSprites(st)
		if st.Employees[0].Sprite == state.SpriteMeeting {
			break
		}
	}
	if st.Employees[0].Sprite != state.SpriteMeeting {
		t.Fatalf("walker never arrived after %d ticks", steps)
	}
	p, ok := SpritePosition("w1")
	plan := CurrentPlan()
	if !ok || p != plan.Hot.Meet {
		t.Fatalf("walker parked at %v (ok=%v), want meet hotspot %v", p, ok, plan.Hot.Meet)
	}
}
