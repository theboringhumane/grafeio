// floor_ambient_test.go — pins for the tick-driven fixture motion
// (floor_ambient.go): steam cycling above the tea machine, rack LEDs
// blinking, the uplink ripple scrolling, and the two hard invariants:
// churn stays confined to the ambient cells and HitAgent never moves.
package office

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// ambientCellSet — every cell the ambient pass may ever churn on this plan:
// the steam band above the machine, the rack inner cells, and the server
// zone's top-wall inner run (the ripple's whole scroll range).
func ambientCellSet(plan Plan) map[string]bool {
	cells := map[string]bool{}
	if m, ok := machineProp(plan); ok {
		for col := 0; col < steamCols; col++ {
			cells[cellKey(m.X+1+col, m.Y-1)] = true
			cells[cellKey(m.X+1+col, m.Y-2)] = true
		}
	}
	for _, p := range plan.Props {
		if p.Glyph != "[||]" {
			continue
		}
		cells[cellKey(p.X+1, p.Y)] = true
		cells[cellKey(p.X+2, p.Y)] = true
	}
	for _, z := range plan.Zones {
		if z.ID != "server" {
			continue
		}
		for x := z.X + 1; x <= z.X+z.W-2; x++ {
			cells[cellKey(x, z.Y)] = true
		}
	}
	return cells
}

func firstRack(t *testing.T, plan Plan) PlanProp {
	t.Helper()
	for _, p := range plan.Props {
		if p.Glyph == "[||]" {
			return p
		}
	}
	t.Fatalf("plan has no [||] rack")
	return PlanProp{}
}

// (a) the steam phase moves between t=0 and t=2 in exactly the expected
// cells, and NOTHING outside the ambient cell set differs — every other
// fixture, wall, prop and label renders untouched.
func TestSteamPhasesAndChurnConfinement(t *testing.T) {
	plan := ComputePlan(120, 26)
	m, ok := machineProp(plan)
	if !ok {
		t.Fatalf("plan has no [cof] machine")
	}

	rows0 := BuildRows(state.OfficeState{Tick: 0}, 120, 26)
	rows2 := BuildRows(state.OfficeState{Tick: 2}, 120, 26)

	// expected steam dynamics: at t=0 the column-0 wisp sits one row above
	// the machine (bright, '~'); at t=2 it rose to the wall row (masked by
	// the break-room north wall, still '-' there) and column 1 took over.
	if c := rows0[m.Y-1][m.X+1]; c.Ch != '~' || c.FG != "gray" || c.Dim {
		t.Fatalf("steam cell (%d,%d) = {Ch:%q FG:%q Dim:%v}, want bright gray '~'",
			m.X+1, m.Y-1, c.Ch, c.FG, c.Dim)
	}
	if c := rows0[m.Y-2][m.X+1]; c.Ch != '-' {
		t.Fatalf("wall cell above the steam column (%d,%d) = %q, want untouched '-'",
			m.X+1, m.Y-2, c.Ch)
	}
	if c := rows2[m.Y-2][m.X+1]; c.Ch != '-' {
		t.Fatalf("wall cell (%d,%d) overwritten at t=2: %q", m.X+1, m.Y-2, c.Ch)
	}
	if c := rows2[m.Y-1][m.X+2]; c.Ch != '~' || c.Dim {
		t.Fatalf("steam cell (%d,%d) at t=2 = {Ch:%q Dim:%v}, want bright '~'",
			m.X+2, m.Y-1, c.Ch, c.Dim)
	}
	churn := ambientCellSet(plan)
	diffs := 0
	for y := 0; y < 26; y++ {
		for x := 0; x < 120; x++ {
			if rows0[y][x] == rows2[y][x] {
				continue
			}
			diffs++
			if !churn[cellKey(x, y)] {
				t.Fatalf("non-ambient cell (%d,%d) changed between t=0 and t=2: %q -> %q",
					x, y, rows0[y][x].Ch, rows2[y][x].Ch)
			}
		}
	}
	if diffs == 0 {
		t.Fatalf("frames at t=0 and t=2 are identical — ambient motion is dead")
	}
}

// (a2) the machine's own row and the neighboring fixtures never churn: the
// "[cof]" glyph itself, the kitchen counter and the brackets of the bin
// above are identical across a full cycle of phases.
func TestSteamNeverTouchesMachineRow(t *testing.T) {
	plan := ComputePlan(120, 26)
	m, ok := machineProp(plan)
	if !ok {
		t.Fatalf("plan has no [cof] machine")
	}
	rowGlyph := func(rows []Row, y, x, n int) string {
		b := make([]rune, n)
		for i := 0; i < n; i++ {
			b[i] = rows[y][x+i].Ch
		}
		return string(b)
	}
	for _, tick := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		rows := BuildRows(state.OfficeState{Tick: tick}, 120, 26)
		if got := rowGlyph(rows, m.Y, m.X, 5); got != "[cof]" {
			t.Fatalf("tick %d: machine row = %q, want %q", tick, got, "[cof]")
		}
		if got := rowGlyph(rows, m.Y-1, m.X, 1); got != "[" {
			t.Fatalf("tick %d: bin left bracket churned: %q", tick, got)
		}
		if got := rowGlyph(rows, m.Y-1, m.X+4, 1); got != "]" {
			t.Fatalf("tick %d: bin right bracket churned: %q", tick, got)
		}
	}
}

// (b) rack LEDs blink: the first rack's lamp is lit on t=0 and t=1 on
// alternating sides of the glyph, and the off cell shows the plain prop.
func TestRackLEDBlinkToggles(t *testing.T) {
	dx0, on0 := rackLED(0, 0)
	dx1, on1 := rackLED(0, 1)
	if !on0 || !on1 || dx0 == dx1 {
		t.Fatalf("rackLED model: t=0 -> (dx=%d,on=%v), t=1 -> (dx=%d,on=%v), want lit + side toggled",
			dx0, on0, dx1, on1)
	}

	plan := ComputePlan(120, 26)
	r := firstRack(t, plan)
	rows0 := BuildRows(state.OfficeState{Tick: 0}, 120, 26)
	rows1 := BuildRows(state.OfficeState{Tick: 1}, 120, 26)

	if c := rows0[r.Y][r.X+dx0]; c.Ch != '•' || c.FG != "magentaBright" || !c.Bold {
		t.Fatalf("LED cell (%d,%d) = {Ch:%q FG:%q Bold:%v}, want bold magentaBright '•'",
			r.X+dx0, r.Y, c.Ch, c.FG, c.Bold)
	}
	if c := rows1[r.Y][r.X+dx1]; c.Ch != '•' {
		t.Fatalf("LED cell (%d,%d) at t=1 = %q, want '•'", r.X+dx1, r.Y, c.Ch)
	}
	if c := rows1[r.Y][r.X+dx0]; c.Ch != '|' || c.FG != "magenta" || c.Bold {
		t.Fatalf("off LED cell (%d,%d) = {Ch:%q FG:%q Bold:%v}, want plain magenta '|'",
			r.X+dx0, r.Y, c.Ch, c.FG, c.Bold)
	}
	// brackets are geometry: never touched
	if rows0[r.Y][r.X].Ch != '[' || rows0[r.Y][r.X+3].Ch != ']' {
		t.Fatalf("rack brackets churned: %q ... %q",
			rows0[r.Y][r.X].Ch, rows0[r.Y][r.X+3].Ch)
	}
}

// (c) the uplink ripple scrolls one cell per tick (8 phases), phase-shifts
// its glyph pattern, and leaves the wall corners alone.
func TestUplinkWaveScrolls(t *testing.T) {
	plan := ComputePlan(120, 26)
	x0, y0, n0, ok0 := uplinkStart(plan, 0)
	x1, y1, n1, ok1 := uplinkStart(plan, 1)
	x8, _, _, ok8 := uplinkStart(plan, 8)
	if !ok0 || !ok1 || !ok8 {
		t.Fatalf("uplink segment missing: ok0=%v ok1=%v ok8=%v", ok0, ok1, ok8)
	}
	if y0 != y1 || n0 != n1 || n0 != uplinkRun {
		t.Fatalf("wave moved rows or changed length: y %d->%d, run %d->%d", y0, y1, n0, n1)
	}
	if x1 != x0+1 {
		t.Fatalf("wave start scrolled %d -> %d, want +1", x0, x1)
	}
	if x8 != x0 {
		t.Fatalf("wave did not wrap after %d phases: %d, want %d", uplinkPhases, x8, x0)
	}

	rows := BuildRows(state.OfficeState{Tick: 0}, 120, 26)
	for j := 0; j < n0; j++ {
		c := rows[y0][x0+j]
		if want := uplinkGlyph(j, 0); c.Ch != want || c.FG != "cyan" {
			t.Fatalf("wave cell (%d,%d) = {Ch:%q FG:%q}, want cyan %q", x0+j, y0, c.Ch, c.FG, want)
		}
	}
	// corners and the cell just past the scroll range stay wall
	var server Zone
	for _, z := range plan.Zones {
		if z.ID == "server" {
			server = z
		}
	}
	if c := rows[y0][server.X]; c.Ch != '+' {
		t.Fatalf("server west corner = %q, want '+'", c.Ch)
	}
	if c := rows[y0][server.X+server.W-1]; c.Ch != '+' {
		t.Fatalf("server east corner = %q, want '+'", c.Ch)
	}
}

// (d) HitAgent regression: sprite hit regions are byte-identical with the
// ambient motion on any phase — the churn only repaints fixture cells and
// can never grow, shrink, or move a sprite's 3-cell hit box.
func TestHitAgentUnaffectedByAmbient(t *testing.T) {
	_ = ComputePlan(120, 26) // pins CurrentPlan so SeatAnchor resolves on this floor
	base := state.OfficeState{
		Employees: []state.Employee{
			emp("boss", "boss", state.RoleManager, "manager", state.SpriteAtDesk),
			emp("t1", "tekton-1", state.RoleDeveloper, "dev-1", state.SpriteWorking),
			emp("d1", "dikastes", state.RoleReviewer, "cabin-2", state.SpriteAtDesk),
		},
	}
	sweep := func(tick int) map[string]string {
		st := base
		st.Tick = tick
		hits := map[string]string{}
		for y := 0; y < 26; y++ {
			for x := 0; x < 120; x++ {
				if id, ok := HitAgent(st, x, y); ok {
					hits[cellKey(x, y)] = id
				}
			}
		}
		return hits
	}
	t0, t7 := sweep(0), sweep(7)
	if len(t0) == 0 {
		t.Fatalf("no hits at t=0 — the seam itself is broken")
	}
	if len(t0) != len(t7) {
		t.Fatalf("hit count changed with the ambient phase: %d -> %d", len(t0), len(t7))
	}
	for k, id := range t0 {
		if t7[k] != id {
			t.Fatalf("hit at %s moved between ambient phases: %q -> %q", k, id, t7[k])
		}
	}
	// a seat anchor still resolves to its employee
	a := SeatAnchor("dev-1")
	if id, ok := HitAgent(state.OfficeState{Tick: 3, Employees: base.Employees}, a.X+1, a.Y); !ok || id != "t1" {
		t.Fatalf("dev-1 chair center hit = (%q, %v), want (t1, true)", id, ok)
	}
}
