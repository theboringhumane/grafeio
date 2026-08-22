// floor_ambient.go — tick-driven AMBIENT motion for the inanimate fixtures:
// steam off the tea/coffee machine, blinking server-rack LEDs, and a small
// "uplink" ripple scrolling along the server room's north wall.
//
// All three are pure functions of (plan, tick): BuildRows rebuilds the whole
// grid every frame, so the fixtures re-appear untouched on any tick a blinker
// or wisp is off — the plan's props/zones are never mutated, and there are no
// goroutines or timers here, only integer phase math off st.Tick (the same
// negative-safe mod the sprite beats use).
//
// Churn budget (glyph churn stays inside the fixtures + the rows directly
// above the machine):
//
//	steam  — the 3 interior columns of the machine glyph (the "cof" letters),
//	         rows Y-1 and Y-2; wall cells and sprite-occupied cells are
//	         skipped, so a masked wisp simply doesn't render that tick;
//	leds   — the 2 inner cells of each "[||]" rack glyph; the brackets are
//	         never touched;
//	uplink — a 4-cell "~^~^" ripple on the server zone's top wall row,
//	         stamped only on '-' cells (never corners, door gaps, or props).
package office

// phaseMod — negative-safe modulo, the same shape sprites.go uses.
func phaseMod(v, n int) int { return ((v % n) + n) % n }

// ---------------------------------------------------------------------------
// steam — wisps rising off the tea/coffee machine
// ---------------------------------------------------------------------------

const (
	steamStepTicks = 2 // a wisp advances one row every N ticks
	steamCols      = 3 // wisps rise over the machine's interior columns ("cof")
	steamCycle     = 8 // full wisp lifecycle, in ticks
)

// steamWisp — the wisp for interior column `col` (0..steamCols-1) at `tick`.
// layer 0 is the row just above the machine (bright '~', like the gray of a
// sleep-z); layer 1 is the next row up (dim '.', the same gray+dim the floor
// uses for inactive cells — the wisp fades as it rises). Columns are
// staggered by steamStepTicks so the wisps travel left-to-right; ok=false
// while the column's wisp is dead (the fixture shows through).
func steamWisp(col, tick int) (layer int, ch rune, dim bool, ok bool) {
	age := phaseMod(tick-col*steamStepTicks, steamCycle)
	switch {
	case age < steamStepTicks:
		return 0, '~', false, true
	case age < 2*steamStepTicks:
		return 1, '.', true, true
	}
	return 0, 0, false, false
}

// machineProp — the tea/coffee machine ("[cof]" on the kitchen counter), the
// steam anchor. Absent on a machine-less plan: no steam, never an error.
func machineProp(plan Plan) (PlanProp, bool) {
	for _, p := range plan.Props {
		if p.Glyph == "[cof]" {
			return p, true
		}
	}
	return PlanProp{}, false
}

// stampSteam — wisps over the machine's interior columns, 1-2 rows up. A cell
// is skipped when it is structural (outer border / zone wall — never
// overwrite geometry) or covered by an employee sprite; furniture cells in
// the steam band (the wall-mounted bin above the machine at standard sizes)
// are transiently overlaid, exactly like the floating sleep-z's.
func stampSteam(g []Row, plan Plan, W, H, tick int, occupied map[string]bool) {
	m, ok := machineProp(plan)
	if !ok {
		return
	}
	for col := 0; col < steamCols; col++ {
		layer, ch, dim, on := steamWisp(col, tick)
		if !on {
			continue
		}
		x, y := m.X+1+col, m.Y-1-layer
		if isStructural(plan, W, H, x, y) || occupied[cellKey(x, y)] {
			continue
		}
		g[y][x] = Cell{Ch: ch, FG: "gray", Dim: dim}
	}
}

// ---------------------------------------------------------------------------
// server-rack LEDs — one small blinker per "[||]" rack, churning only the
// rack's two inner cells ('|' <-> '•'); brackets never move.
// ---------------------------------------------------------------------------

const ledCycle = 6 // each rack is lit for 2 ticks in 6

// rackLED — the lamp of rack `idx` (ordinal among the plan's "[||]" props —
// plan.Props is a slice, so the order is stable) at `tick`: dx is the offset
// inside the glyph (1 or 2, the inner cells) when on. Off-phase stagger of 2
// per rack makes the column twinkle down the server room instead of the whole
// rack row blinking in unison; a lit lamp also flips side every tick.
func rackLED(idx, tick int) (dx int, on bool) {
	if phaseMod(tick+2*idx, ledCycle) >= 2 {
		return 0, false
	}
	return 1 + phaseMod(tick+idx, 2), true
}

// stampRackLEDs — light each rack's LED on its on-phase: '•' painted
// magentaBright+bold (a brighter step of the rack's own magenta). Off ticks
// leave the stamped prop untouched — the plain '|' shows through.
func stampRackLEDs(g []Row, plan Plan, W, H, tick int, occupied map[string]bool) {
	idx := 0
	for _, p := range plan.Props {
		if p.Glyph != "[||]" {
			continue
		}
		dx, on := rackLED(idx, tick)
		idx++
		if !on {
			continue
		}
		x, y := p.X+dx, p.Y
		if x < 0 || x >= W || y < 0 || y >= H || occupied[cellKey(x, y)] {
			continue
		}
		g[y][x] = Cell{Ch: '•', FG: "magentaBright", Bold: true}
	}
}

// ---------------------------------------------------------------------------
// uplink wave — a 4-cell ripple scrolling along the server room's north wall
// ---------------------------------------------------------------------------

const (
	uplinkRun    = 4 // glyph run length ("~^~^")
	uplinkPhases = 8 // scroll period in ticks
)

// uplinkStart — left edge of the ripple's run on the server zone's top wall
// at `tick`, plus the wall row and run length. The run scrolls one cell right
// per tick over uplinkPhases phases (clamped when the wall run is shorter),
// always inset one cell from the '+' corners. ok=false when the room is
// absent or its wall run can't hold the ripple.
func uplinkStart(plan Plan, tick int) (x, y, n int, ok bool) {
	for _, z := range plan.Zones {
		if z.ID != "server" {
			continue
		}
		lo := z.X + 1
		hi := z.X + z.W - 1 - uplinkRun
		if hi < lo {
			return 0, 0, 0, false
		}
		steps := uplinkPhases
		if span := hi - lo + 1; span < steps {
			steps = span
		}
		return lo + phaseMod(tick, steps), z.Y, uplinkRun, true
	}
	return 0, 0, 0, false
}

// uplinkGlyph — the run's j-th cell at `tick`: the pattern phase-shifts with
// the scroll, so the ripple travels like a wave instead of sliding as a
// solid block.
func uplinkGlyph(j, tick int) rune {
	if phaseMod(tick+j, 2) == 0 {
		return '^'
	}
	return '~'
}

// stampUplink — the ripple on the wall row. Stamped cell-by-cell only where
// the current glyph is a plain '-' wall cell: corners ('+'), door gaps,
// windows and anything else keep their glyph, and a sprite standing in the
// way mutes that cell for the tick. '^' reads as the wave crest (bright
// cyan), '~' as the trough (dim cyan).
func stampUplink(g []Row, plan Plan, W, H, tick int, occupied map[string]bool) {
	x, y, n, ok := uplinkStart(plan, tick)
	if !ok {
		return
	}
	for j := 0; j < n; j++ {
		xx := x + j
		if xx < 0 || xx >= W || y < 0 || y >= H {
			continue
		}
		if g[y][xx].Ch != '-' || occupied[cellKey(xx, y)] {
			continue
		}
		g[y][xx] = Cell{Ch: uplinkGlyph(j, tick), FG: "cyan", Dim: uplinkGlyph(j, tick) == '~'}
	}
}

// ---------------------------------------------------------------------------
// stampAmbient — all fixture motion for one frame. Called from BuildRows
// after sprites (so `occupied` can mute anything under a walker) and before
// speech bubbles (which keep their overwrite-everything semantics).
// ---------------------------------------------------------------------------

func stampAmbient(g []Row, plan Plan, W, H, tick int, occupied map[string]bool) {
	stampSteam(g, plan, W, H, tick, occupied)
	stampRackLEDs(g, plan, W, H, tick, occupied)
	stampUplink(g, plan, W, H, tick, occupied)
}
