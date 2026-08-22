package office

import (
	"regexp"
	"strings"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
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
	return line[plan.Nameplate.X : plan.Nameplate.X+10]
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
			Tick:      0,
			Employees: []state.Employee{emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteMeeting)},
			Chat:      []state.ChatMsg{{ID: "m1", From: "boss", Pending: true}}, // meetin wins over typing
		})
		if plate != "[meetin]  " {
			t.Fatalf("got %q", plate)
		}
	})
	t.Run("delegat while the boss delegated to busy workers", func(t *testing.T) {
		plate := nameplateRow(t, state.OfficeState{
			Tick:           0,
			BossDelegating: true,
			Chat:           []state.ChatMsg{{ID: "m1", From: "boss", Pending: true}}, // delegat supersedes typing
		})
		if plate != "[delegat] " {
			t.Fatalf("got %q", plate)
		}
	})
	t.Run("offline takes TOP precedence, in red", func(t *testing.T) {
		st := state.OfficeState{
			Tick:           0,
			Offline:        true,
			BossDelegating: true, // stacked badges…
			Employees:      []state.Employee{emp("t1", "tekton", state.RoleDeveloper, "dev-1", state.SpriteMeeting)},
			Chat:           []state.ChatMsg{{ID: "m1", From: "boss", Pending: true}}, // …offline still wins
		}
		if plate := nameplateRow(t, st); plate != "[offline] " {
			t.Fatalf("got %q", plate)
		}
		// red bold — a connectivity alarm, not a status hint.
		plan := ComputePlan(120, 25)
		rows := BuildRows(st, 120, 25)
		for dx := 0; dx < len("[offline]"); dx++ {
			c := rows[plan.Nameplate.Y][plan.Nameplate.X+dx]
			if c.FG != "red" || !c.Bold {
				t.Fatalf("offline plate cell (%d,%d) = {Ch:%q FG:%q Bold:%v}, want red bold",
					plan.Nameplate.X+dx, plan.Nameplate.Y, c.Ch, c.FG, c.Bold)
			}
		}
	})
	t.Run("healthy plate stays yellow (positive control)", func(t *testing.T) {
		plan := ComputePlan(120, 25)
		rows := BuildRows(state.OfficeState{Tick: 0}, 120, 25)
		for dx := 0; dx < len("[awaiting]"); dx++ {
			c := rows[plan.Nameplate.Y][plan.Nameplate.X+dx]
			if c.FG != "yellow" || !c.Bold {
				t.Fatalf("ordinary plate cell (%d,%d) = {FG:%q Bold:%v}, want yellow bold",
					plan.Nameplate.X+dx, plan.Nameplate.Y, c.FG, c.Bold)
			}
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

// cropRows — plain text of the cell window rows[y0:y1+1] x cols[x0:x1+1]
// (floorshot-style proof crops; machine layout, not NL).
func cropRows(rows []Row, x0, y0, x1, y1 int) string {
	var b strings.Builder
	for y := y0; y <= y1; y++ {
		if y > y0 {
			b.WriteByte('\n')
		}
		for x := x0; x <= x1; x++ {
			b.WriteRune(rows[y][x].Ch)
		}
	}
	return b.String()
}

// The CTO exec suite renders at 120x30: his name plate follows the 10-col
// boss-plate convention ("theboringcto" -> "theboringc"), his desk and
// chair sit inside the boss office right of the mug, and the seated
// sprite stamps a white 'C' over the chair anchor.
func TestCTOSuiteRenders120x30(t *testing.T) {
	plan := ComputePlan(120, 30)
	anchor, ok := plan.Anchor["cto"]
	if !ok {
		t.Fatalf("120x30 plan has no cto anchor (bw>=28 && topH>=7 must carry the suite)")
	}
	st := state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			emp("theboringcto", "theboringcto", state.RoleCTO, "cto", state.SpriteAtDesk),
		},
	}
	rows := BuildRows(st, 120, 30)
	plain := Styleless(rows)

	// name plate: exactly the 10-col clip, yellow bold like the boss plate
	for dx := 0; dx < len("theboringc"); dx++ {
		c := rows[3][17+dx]
		if c.Ch != rune("theboringc"[dx]) {
			t.Fatalf("plate cell (%d,3) = %q, want %q", 17+dx, c.Ch, "theboringc"[dx:dx+1])
		}
		if c.FG != "yellow" || !c.Bold {
			t.Fatalf("plate cell (%d,3) = {FG:%q Bold:%v}, want yellow bold", 17+dx, c.FG, c.Bold)
		}
	}
	if !strings.Contains(plain, "theboringc") || strings.Contains(plain, "theboringcto") {
		t.Fatalf("plate must render the 10-col clip \"theboringc\" and never spill the full name")
	}
	// desk + chair props (machine layout)
	if got := cropRows(rows, 17, 4, 24, 4); got != "[=CTO==]" {
		t.Fatalf("CTO desk row = %q, want %q", got, "[=CTO==]")
	}
	// seated sprite: white 'C' centered over the chair anchor, never dim
	c := rows[anchor.Y][anchor.X+1]
	if c.Ch != 'C' {
		t.Fatalf("chair center (%d,%d) = %q, want 'C'", anchor.X+1, anchor.Y, c.Ch)
	}
	if c.FG != "white" || c.Dim {
		t.Fatalf("seated CTO cell = {FG:%q Dim:%v}, want lit white", c.FG, c.Dim)
	}
	for dx := 0; dx < 3; dx++ {
		if rows[anchor.Y][anchor.X+dx].Dim {
			t.Fatalf("staffed cto chair cell (%d,%d) is dim, want lit", anchor.X+dx, anchor.Y)
		}
	}
	// seat routing: the role lands on his exec-suite desk, overflow stands
	if got := AssignSeat(map[string]bool{}, state.RoleCTO); got != "cto" {
		t.Fatalf("AssignSeat(RoleCTO) = %q, want \"cto\"", got)
	}
	if got := AssignSeat(map[string]bool{"cto": true}, state.RoleCTO); got != "floor-0" {
		t.Fatalf("AssignSeat(RoleCTO, taken) = %q, want overflow \"floor-0\"", got)
	}
	// panel/paint lookups stay on the roster conventions
	if got := NameColor("theboringcto"); got != "white" {
		t.Fatalf("NameColor(theboringcto) = %q, want white", got)
	}
	if got := ROLE_GLYPH[state.RoleCTO]; got != 'C' {
		t.Fatalf("ROLE_GLYPH[RoleCTO] = %q, want 'C'", got)
	}
	// floorshot-style proof crop: the exec corner with the CTO seated.
	t.Logf("CTO exec suite at 120x30 (boss office, rows 0-10 x cols 0-31):\n%s",
		cropRows(rows, 0, 0, 31, 10))

	// Same renderer, wider frame (the floorshot 120x34 size): the seated
	// exec corner, plain text, for shot-review crops.
	rows34 := BuildRows(st, 120, 34)
	if got := cropRows(rows34, 17, 3, 26, 3); got != "theboringc" {
		t.Fatalf("120x34 plate row = %q, want %q", got, "theboringc")
	}
	t.Logf("CTO exec suite at 120x34, seated (rows 0-10 x cols 0-31):\n%s",
		cropRows(rows34, 0, 0, 31, 10))

	// 84x24 degrades like the cabins do: no anchor, he stands overflow.
	// (ComputePlan re-pins CurrentPlan; rebuild the big plan first so the
	// assertions above stay anchored.)
	plan84 := ComputePlan(84, 24)
	if _, ok := plan84.Anchor["cto"]; ok {
		t.Fatalf("84x24 must not carry the CTO suite (bw<28) — it degrades to overflow")
	}
}

// The suite gate is EXACTLY bw>=28 && topH>=7 (floorplan.go): bw first hits
// 28 at W=108 (W*26/100), topH first hits 7 at H=23 ((H-2)*34/100). One
// cell short on either axis and the CTO degrades to standing overflow.
func TestCTOSuiteGateBoundaries(t *testing.T) {
	cases := []struct {
		w, h int
		want bool
	}{
		{107, 23, false}, // bw=27: one col short of the width gate
		{108, 23, true},  // the exact corner: 108*26/100=28 && 21*34/100=7
		{108, 22, false}, // topH=6: one row short of the height gate
		{107, 30, false}, // height alone can't rescue the width gate
		{120, 22, false}, // width alone can't rescue the height gate
		{140, 30, true},  // well past both (bw clamps to 30, topH to 9)
	}
	for _, c := range cases {
		_, got := ComputePlan(c.w, c.h).Anchor["cto"]
		if got != c.want {
			t.Fatalf("%dx%d: cto anchor present=%v, want %v", c.w, c.h, got, c.want)
		}
	}
}

// 84x24 has no cto anchor (bw=21<28): SeatAnchor falls through to the
// shared overflow spot beside the break area and he STANDS there — white,
// lit — instead of sitting in a suite.
func TestCTOOverflowStandsBreakArea84x24(t *testing.T) {
	// ComputePlan re-pins CurrentPlan, so SeatAnchor below resolves
	// against the 84x24 plan, not a cached wide one.
	plan := ComputePlan(84, 24)
	if anchor, ok := plan.Anchor["cto"]; ok {
		t.Fatalf("84x24 must not carry the CTO suite, anchor at %v", anchor)
	}
	p := SeatAnchor("cto")
	if p != plan.Hot.Overflow {
		t.Fatalf("SeatAnchor(cto) = %v, want the shared overflow spot %v", p, plan.Hot.Overflow)
	}
	st := state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			// unique id: never a walker, so the fallback below is the only path
			emp("cto-overflow", "theboringcto", state.RoleCTO, "cto", state.SpriteAtDesk),
		},
	}
	rows := BuildRows(st, 84, 24)
	c := rows[p.Y][p.X+1]
	if c.Ch != 'C' {
		t.Fatalf("overflow center (%d,%d) = %q, want 'C'", p.X+1, p.Y, c.Ch)
	}
	if c.FG != ROLE_COLOR[state.RoleCTO] || c.Dim {
		t.Fatalf("overflow CTO cell = {FG:%q Dim:%v}, want lit %q", c.FG, c.Dim, ROLE_COLOR[state.RoleCTO])
	}
	// standing means bare floor: no chair under him to stay lit
	t.Logf("CTO standing at the overflow spot, 84x24 (rows %d-%d x cols %d-%d):\n%s",
		p.Y-1, p.Y+1, p.X-4, p.X+5, cropRows(rows, p.X-4, p.Y-1, p.X+5, p.Y+1))
}

// Frame zero, before ANY walker advance: SpritePosition misses so the
// renderer must fall back to the seat anchor — the CTO is visible from the
// very first frame, not only after EvTick has parked him.
func TestCTOVisibleFirstFrame(t *testing.T) {
	plan := ComputePlan(120, 30)
	anchor, ok := plan.Anchor["cto"]
	if !ok {
		t.Fatalf("120x30 plan has no cto anchor")
	}
	st := state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			// unique id: guarantees no walker map entry from another test
			emp("cto-first-frame", "theboringcto", state.RoleCTO, "cto", state.SpriteAtDesk),
		},
	}
	if _, found := SpritePosition("cto-first-frame"); found {
		t.Fatalf("walker for cto-first-frame must not exist before the first advance")
	}
	rows := BuildRows(st, 120, 30)
	c := rows[anchor.Y][anchor.X+1]
	if c.Ch != 'C' {
		t.Fatalf("first-frame chair center (%d,%d) = %q, want 'C' via the SeatAnchor fallback", anchor.X+1, anchor.Y, c.Ch)
	}
	if c.FG != "white" || c.Dim {
		t.Fatalf("first-frame CTO cell = {FG:%q Dim:%v}, want lit white", c.FG, c.Dim)
	}
}
