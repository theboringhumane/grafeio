// floor.go — the office floor as a STYLED char grid, DYNAMICALLY sized
// (Go port of node-legacy/src/office/floor.tsx + the walker state machine
// from sprites.ts).
//
// BuildRows() is the primary renderer — PURE: (state, width, height) ->
// exactly `height` Row of exactly `width` Cell. Styleless() joins the same
// cells, so the plain layout and the colored layout can never drift apart.
// lipgloss only paints.
//
// Walkers are tracked per employee id in a package-local map (OfficeState's
// Employee shape is fixed and cannot carry x/y). The map is pruned on fire,
// seeded on first sight, and is the only mutable thing here. Walkers live
// against the CURRENT plan (CurrentPlan): when the plan generation changes
// (terminal resized), walkers are clamped back onto the new floor and
// re-walk to their (recomputed) targets.
package office

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/state"
)

// LEGEND — role legend line (kept for parity with the TS shell).
const LEGEND = "M=boss H=hr T=dev S=scout D=rev R=run [tea]=break"

// Cell — one styled grid cell. FG is a chalk-style color name ("" = default).
type Cell struct {
	Ch   rune
	FG   string
	Bold bool
	Dim  bool
}

// Row — one grid row of exactly `width` cells.
type Row []Cell

// style — partial paint applied by put/restyle.
type style struct {
	fg   string
	bold bool
	dim  bool
}

var blankCell = Cell{Ch: ' '}

// ---------------------------------------------------------------------------
// walker state machine (port of sprites.ts walkers + advanceSprites)
// ---------------------------------------------------------------------------

type walker struct {
	x, y  int
	since int // tick when the current parked state started
	gen   int // plan generation this position was validated against
}

var walkers = map[string]*walker{}

// SpritePosition — current pixel position of an employee (ok=false until first advance).
func SpritePosition(id string) (Point, bool) {
	w, ok := walkers[id]
	if !ok {
		return Point{}, false
	}
	return Point{X: w.x, Y: w.y}, true
}

func targetFor(sprite state.SpriteState, seat string, plan Plan) Point {
	switch sprite {
	case state.SpriteToManager, state.SpriteMeeting:
		return plan.Hot.Meet
	case state.SpriteToCoffee, state.SpriteCoffee:
		return plan.Hot.Tea
	case state.SpriteAtMailbox:
		return plan.Hot.Mail
	default: // at-desk / working / to-desk head home
		return SeatAnchor(seat)
	}
}

func sign(v int) int {
	if v > 0 {
		return 1
	}
	if v < 0 {
		return -1
	}
	return 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// AdvanceSprites — advance every walker by up to 2 cells (dogleg: x first,
// then y); drive state transitions.
func AdvanceSprites(st state.OfficeState) state.OfficeState {
	plan := CurrentPlan()
	live := map[string]bool{}
	for _, e := range st.Employees {
		live[e.ID] = true
	}
	for id := range walkers {
		if !live[id] {
			delete(walkers, id)
		}
	}

	changed := false
	employees := make([]state.Employee, len(st.Employees))
	for i, e := range st.Employees {
		w, ok := walkers[e.ID]
		if !ok {
			a := SeatAnchor(e.Seat)
			w = &walker{x: a.X, y: a.Y, since: st.Tick, gen: plan.Gen}
			walkers[e.ID] = w
		}
		if w.gen != plan.Gen {
			// plan resized: clamp back onto the new floor, then re-walk to target
			w.x = min(max(1, w.x), plan.Width-2)
			w.y = min(max(1, w.y), plan.Height-2)
			w.gen = plan.Gen
		}
		t := targetFor(e.Sprite, e.Seat, plan)
		if w.x != t.X {
			d := min(2, abs(t.X-w.x))
			w.x += sign(t.X-w.x) * d
		} else if w.y != t.Y {
			d := min(2, abs(t.Y-w.y))
			w.y += sign(t.Y-w.y) * d
		}

		arrived := w.x == t.X && w.y == t.Y
		sprite := e.Sprite
		switch {
		case sprite == state.SpriteToManager && arrived:
			sprite = state.SpriteMeeting
		case sprite == state.SpriteToCoffee && arrived:
			sprite = state.SpriteCoffee
			w.since = st.Tick
		case sprite == state.SpriteToDesk && arrived:
			sprite = state.SpriteAtDesk
		case sprite == state.SpriteCoffee && st.Tick-w.since >= coffeeTicks:
			sprite = state.SpriteToDesk
		}
		if sprite != e.Sprite {
			changed = true
			e.Sprite = sprite
		}
		employees[i] = e
	}
	if !changed {
		return st
	}
	out := st
	out.Employees = employees
	return out
}

// ---------------------------------------------------------------------------
// styled cell grid internals
// ---------------------------------------------------------------------------

func put(g []Row, W, H, x, y int, s string, st *style) {
	if y < 0 || y >= H {
		return
	}
	for i := 0; i < len(s); i++ {
		xx := x + i
		if xx < 0 || xx >= W {
			continue
		}
		c := Cell{Ch: rune(s[i])}
		if st != nil {
			c.FG, c.Bold, c.Dim = st.fg, st.bold, st.dim
		}
		g[y][xx] = c
	}
}

// restyle — repaint existing cells in place (keeps their chars).
func restyle(g []Row, W, H, x, y, n int, st style) {
	if y < 0 || y >= H {
		return
	}
	for i := 0; i < n; i++ {
		xx := x + i
		if xx < 0 || xx >= W {
			continue
		}
		c := g[y][xx]
		c.FG, c.Bold, c.Dim = st.fg, st.bold, st.dim
		g[y][xx] = c
	}
}

// drawZone — walled room: "+" corners, wall char for the sides, skipped cells
// at door gaps.
func drawZone(g []Row, W, H int, z Zone) {
	zoneStyle := style{fg: z.Color}
	if zoneStyle.fg == "" {
		zoneStyle.fg = "gray"
	}
	wall := z.Wall
	inDoor := func(side string, i int) bool {
		for _, d := range z.Doors {
			if d.Side == side && i >= d.At && i < d.At+d.Size {
				return true
			}
		}
		return false
	}
	for dx := 0; dx < z.W; dx++ {
		corner := dx == 0 || dx == z.W-1
		ch := wall
		if corner {
			ch = "+"
		}
		if !inDoor("n", dx) {
			put(g, W, H, z.X+dx, z.Y, ch, &zoneStyle)
		}
		if !inDoor("s", dx) {
			put(g, W, H, z.X+dx, z.Y+z.H-1, ch, &zoneStyle)
		}
	}
	wallSide := wall
	if z.Wall == "-" {
		wallSide = "|"
	}
	for dy := 1; dy < z.H-1; dy++ {
		if !inDoor("w", dy) {
			put(g, W, H, z.X, z.Y+dy, wallSide, &zoneStyle)
		}
		if !inDoor("e", dy) {
			put(g, W, H, z.X+z.W-1, z.Y+dy, wallSide, &zoneStyle)
		}
	}
}

const bubbleW = 16 // 14 text cols + "|" borders

var (
	bubbleBorder = style{fg: "gray"}
	bubbleText   = style{fg: "white"}
)

func centerPad(t string, n int) string {
	room := max(0, n-len(t))
	l := room / 2
	return strings.Repeat(" ", l) + t + strings.Repeat(" ", room-l)
}

// drawBubble — ".--------------." / "|   big day.   |" / "+--*-----------+"
// three rows directly above the sprite. Clipped at the grid top (colliding
// rows drop). Borders + trailing pointer gray, inner text white.
func drawBubble(g []Row, W, H int, text string, cx, cy int) {
	if len(text) > 14 {
		text = text[:14]
	}
	t := centerPad(text, 14)
	x := max(0, min(max(0, W-bubbleW), cx-bubbleW/2))
	ry := func(i int) int { return cy - 3 + i }
	if ry(0) >= 1 {
		put(g, W, H, x, ry(0), "."+strings.Repeat("-", 14)+".", &bubbleBorder)
	}
	if ry(1) >= 1 {
		put(g, W, H, x, ry(1), "|", &bubbleBorder)
		put(g, W, H, x+1, ry(1), t, &bubbleText)
		put(g, W, H, x+bubbleW-1, ry(1), "|", &bubbleBorder)
	}
	if ry(2) >= 1 {
		put(g, W, H, x, ry(2), "+"+"--*"+strings.Repeat("-", 11)+"+", &bubbleBorder)
	}
}

// spriteStyle — sprite paint: role color (legend), red bold when blocked,
// yellow on coffee.
func spriteStyle(role state.EmployeeRole, sprite state.SpriteState) style {
	switch sprite {
	case state.SpriteAtMailbox:
		return style{fg: "red", bold: true} // blocked: waving for attention
	case state.SpriteCoffee:
		return style{fg: "yellow"} // sipping
	}
	return style{fg: ROLE_COLOR[role]} // walking/working/idle keep role color, never dim
}

// isStructural — is (x,y) a structural cell: out of grid, the outer border,
// or a zone wall (door gaps excluded)? Used to keep floating sleep-z's off
// buildings — walls and bubbles are the named blockers; furniture glyphs may
// be transiently overlaid by the blink animation (it is gray and 2/16 ticks).
func isStructural(plan Plan, W, H, x, y int) bool {
	if x < 0 || x >= W || y < 0 || y >= H {
		return true
	}
	if W >= 2 && H >= 2 && (y == 0 || y == H-1 || x == 0 || x == W-1) {
		return true
	}
	for _, z := range plan.Zones {
		if x < z.X || x >= z.X+z.W || y < z.Y || y >= z.Y+z.H {
			continue
		}
		onN := y == z.Y
		onS := y == z.Y+z.H-1
		onW := x == z.X
		onE := x == z.X+z.W-1
		inDoor := func(side string, i int) bool {
			for _, d := range z.Doors {
				if d.Side == side && i >= d.At && i < d.At+d.Size {
					return true
				}
			}
			return false
		}
		if onN || onS {
			side := "n"
			if onS {
				side = "s"
			}
			if !inDoor(side, x-z.X) {
				return true
			}
		}
		if onW || onE {
			side := "w"
			if onE {
				side = "e"
			}
			if y > z.Y && y < z.Y+z.H-1 && !inDoor(side, y-z.Y) {
				return true
			}
		}
	}
	return false
}

func padEnd(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

func cellKey(x, y int) string { return strconv.Itoa(x) + "," + strconv.Itoa(y) }

// machine-format seat ids, not natural language:
// POD_SEAT  = /^(?:dev|scout)-\d+$/               (monitors can light up)
// CHAIR_SEAT = /^(?:hr|cabin-\d+|dev-\d+|scout-\d+)$/ (anchors are "(_)" chairs)
func podSeatMatch(seat string) bool {
	if devSeatRE.MatchString(seat) {
		return true
	}
	return matchNumSeat(seat, "scout-")
}

func chairSeatMatch(seat string) bool {
	if seat == "hr" || devSeatRE.MatchString(seat) || matchCabin(seat) {
		return true
	}
	return matchNumSeat(seat, "scout-")
}

func matchCabin(s string) bool { return matchNumSeat(s, "cabin-") }

// matchNumSeat — "<prefix><digits>" (equivalent of /^(?:prefix)-\d+$/).
func matchNumSeat(s, prefix string) bool {
	if !strings.HasPrefix(s, prefix) || len(s) == len(prefix) {
		return false
	}
	for _, r := range s[len(prefix):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// PRIMARY renderer
// ---------------------------------------------------------------------------

// BuildRows — the floor as styled rows. PURE.
// Exactly `height` entries; each row is exactly `width` cells.
func BuildRows(st state.OfficeState, width, height int) []Row {
	W := max(1, width)
	H := max(1, height)
	plan := ComputePlan(width, height)
	g := make([]Row, H)
	for y := range g {
		g[y] = make(Row, W)
		for x := range g[y] {
			g[y][x] = blankCell
		}
	}

	// outer border
	if W >= 2 && H >= 2 {
		border := style{fg: "gray"}
		put(g, W, H, 0, 0, "+"+strings.Repeat("-", W-2)+"+", &border)
		put(g, W, H, 0, H-1, "+"+strings.Repeat("-", W-2)+"+", &border)
		for y := 1; y < H-1; y++ {
			put(g, W, H, 0, y, "|", &border)
			put(g, W, H, W-1, y, "|", &border)
		}
	} else {
		for y := 0; y < H; y++ {
			put(g, W, H, 0, y, strings.Repeat("-", W), &style{fg: "gray"})
		}
	}

	// furniture first, walls over any spill, clock on the boss-office wall;
	// `over` props (window) go last so the zone walls don't erase them
	for _, p := range plan.Props {
		if !p.Over {
			put(g, W, H, p.X, p.Y, p.Glyph, &style{fg: p.Color, bold: p.Bold})
		}
	}
	for _, z := range plan.Zones {
		drawZone(g, W, H, z)
	}
	for _, p := range plan.Props {
		if p.Over {
			put(g, W, H, p.X, p.Y, p.Glyph, &style{fg: p.Color, bold: p.Bold})
		}
	}
	put(g, W, H, plan.Hot.Clock.X, plan.Hot.Clock.Y, TickClock(st.Tick), &style{fg: "white"})

	// boss nameplate is a STATUS line: typing when a boss chat answer is
	// pending, meetin while anyone is at the boss desk, awaiting otherwise
	plate := "[awaiting]"
	pendingBoss := false
	for _, m := range st.Chat {
		if m.From == "boss" && m.Pending {
			pendingBoss = true
			break
		}
	}
	meeting := false
	for _, e := range st.Employees {
		if e.Sprite == state.SpriteMeeting {
			meeting = true
			break
		}
	}
	switch {
	case meeting:
		plate = "[meetin]"
	case pendingBoss:
		plate = "[typing]"
	}
	put(g, W, H, plan.Nameplate.X, plan.Nameplate.Y, padEnd(plate, 10), &style{fg: "yellow", bold: true})

	// degrade badge (drawn once, centered)
	if plan.Tiny {
		badge := "small terminal"
		put(g, W, H, max(0, (W-len(badge))/2), H/2, badge, nil)
	}

	// animated sprites, stamped over the floor
	occupied := map[string]bool{} // sprite cells: "x,y" -> someone sits/stands here
	for _, e := range st.Employees {
		p, ok := SpritePosition(e.ID)
		if !ok {
			p = SeatAnchor(e.Seat)
		}
		for dx := 0; dx < 3; dx++ {
			occupied[cellKey(p.X+dx, p.Y)] = true
		}
		sp := spriteStyle(e.Role, e.Sprite)
		put(g, W, H, p.X, p.Y, SpriteFrame(e.Role, e.Sprite, st.Tick), &sp)
	}

	// floating sleep-z's: one row above an idling sprite's right shoulder at
	// (x+2, y-1) — NEVER glued into the sprite's own row (zMz reads as a typo,
	// not as a sleeping worker). Skipped off-grid, onto a wall, or onto another
	// sprite; bubbles are drawn later and simply overwrite any z under them.
	for _, e := range st.Employees {
		zs := IdleBlinkZs(e.Sprite, st.Tick)
		if zs == "" {
			continue
		}
		p, ok := SpritePosition(e.ID)
		if !ok {
			p = SeatAnchor(e.Seat)
		}
		zx, zy := p.X+2, p.Y-1
		blocked := false
		for i := 0; i < len(zs); i++ {
			if isStructural(plan, W, H, zx+i, zy) || occupied[cellKey(zx+i, zy)] {
				blocked = true
				break
			}
		}
		if !blocked {
			put(g, W, H, zx, zy, zs, &style{fg: "gray"})
		}
	}

	// empty seats read EMPTY: a seat-anchored chair stays green only while
	// some employee's sprite actually covers its anchor cell; otherwise gray+dim
	for seat, a := range plan.Anchor {
		if !chairSeatMatch(seat) {
			continue
		}
		free := true
		for dx := 0; dx < 3 && free; dx++ {
			if occupied[cellKey(a.X+dx, a.Y)] {
				free = false
			}
		}
		if free {
			restyle(g, W, H, a.X, a.Y, 3, style{fg: "gray", dim: true})
		}
	}

	// lit screens: a dev pod's monitor glows cyan bold while someone works there
	for _, e := range st.Employees {
		if e.Sprite != state.SpriteWorking || !podSeatMatch(e.Seat) {
			continue
		}
		a := SeatAnchor(e.Seat) // pod chair; the "[=]" monitor sits 1 right, 2 up
		restyle(g, W, H, a.X+1, a.Y-2, 3, style{fg: "cyan", bold: true})
	}

	// speech bubbles above sprites (live ones only, one per employee)
	byID := map[string]state.Employee{}
	for _, e := range st.Employees {
		byID[e.ID] = e
	}
	shown := map[string]bool{}
	for _, b := range st.Bubbles {
		if b.UntilTick < st.Tick {
			continue
		}
		e, ok := byID[b.EmployeeID]
		if !ok || shown[e.ID] {
			continue
		}
		shown[e.ID] = true
		p, ok := SpritePosition(e.ID)
		if !ok {
			p = SeatAnchor(e.Seat)
		}
		drawBubble(g, W, H, b.Text, p.X+1, p.Y)
	}

	return g
}

// ---------------------------------------------------------------------------
// render helpers — Styleless (plain) and Styled (lipgloss)
// ---------------------------------------------------------------------------

// lipStyle — cell paint as a lipgloss style. FG names map to ANSI256 indices
// (chalk parity: gray=8, magentaBright=13, ...). Dim -> Faint.
func lipStyle(c Cell) lipgloss.Style {
	s := lipgloss.NewStyle()
	if code, ok := ansiColors[c.FG]; ok {
		s = s.Foreground(lipgloss.Color(code))
	}
	if c.Bold {
		s = s.Bold(true)
	}
	if c.Dim {
		s = s.Faint(true)
	}
	return s
}

// Styleless — plain-join of the rows (layout-only view of the SAME cells;
// tests assert against this: single source of truth).
func Styleless(rows []Row) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, c := range row {
			b.WriteRune(c.Ch)
		}
	}
	return b.String()
}

// Styled — the same cells painted with lipgloss (runs of identically-styled
// cells merged, like mergeRow in the TS oracle).
func Styled(rows []Row) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		var seg strings.Builder
		var cur Cell
		flush := func() {
			if seg.Len() > 0 {
				b.WriteString(lipStyle(cur).Render(seg.String()))
				seg.Reset()
			}
		}
		for j, c := range row {
			if j == 0 || (c.FG == cur.FG && c.Bold == cur.Bold && c.Dim == cur.Dim) {
				cur = c
				seg.WriteRune(c.Ch)
			} else {
				flush()
				cur = c
				seg.WriteRune(c.Ch)
			}
		}
		flush()
	}
	return b.String()
}

// RenderPlain — join of BuildRows (same source of truth as the styled view).
func RenderPlain(st state.OfficeState, width, height int) string {
	return Styleless(BuildRows(st, width, height))
}

// ---------------------------------------------------------------------------
// FLOOR_FRAME_CACHE — power-governor seam (the one sanctioned extension):
// Styled(BuildRows(...)) memoized so repeated identical states skip the
// grid rebuild entirely. The memo key is (size, planGen, tick, renderRev):
// the plan generation pins geometry + walker validity, the tick pins every
// animated surface (sprite beats, blink-z's, wall clock, bubble expiry),
// and renderRev pins the tick-independent inputs (employee sprites/seats/
// tasks, bubble spawns, the pending-boss nameplate, the theme paint epoch).
// Walkers only move inside AdvanceSprites (per EvTick), so the same key is
// always the same grid — a hit is provably identical, never stale.
// ---------------------------------------------------------------------------

var (
	floorCacheKey    string
	floorCacheFrame  string
	floorCacheHits   uint64
	floorCacheMisses uint64
)

// floorRenderRev — the tick-independent render inputs of BuildRows.
func floorRenderRev(st state.OfficeState) string {
	var b strings.Builder
	for _, e := range st.Employees {
		b.WriteString(e.ID)
		b.WriteByte('=')
		b.WriteString(string(e.Sprite))
		b.WriteByte('@')
		b.WriteString(e.Seat)
		b.WriteByte(':')
		b.WriteString(e.Task)
		b.WriteByte(';')
	}
	for _, bl := range st.Bubbles {
		b.WriteString(bl.ID)
		b.WriteByte(';')
	}
	for _, c := range st.Chat { // boss typing → nameplate "[typing]"
		if c.From == "boss" && c.Pending {
			b.WriteString("P")
			break
		}
	}
	b.WriteString(ansiColors["gray"]) // theme epoch: re-paint on /theme
	b.WriteString(ansiColors["yellow"])
	return b.String()
}

// CachedStyled — Styled(BuildRows(st, width, height)) behind the memo.
func CachedStyled(st state.OfficeState, width, height int) string {
	plan := ComputePlan(width, height)
	key := strconv.Itoa(width) + "x" + strconv.Itoa(height) +
		"|g" + strconv.Itoa(plan.Gen) +
		"|t" + strconv.Itoa(st.Tick) +
		"|" + floorRenderRev(st)
	if key == floorCacheKey {
		floorCacheHits++
		return floorCacheFrame
	}
	floorCacheMisses++
	floorCacheKey = key
	floorCacheFrame = Styled(BuildRows(st, width, height))
	return floorCacheFrame
}

// CacheStats — the memo's counters (hits skip the whole grid rebuild).
func CacheStats() (hits, misses uint64) { return floorCacheHits, floorCacheMisses }

// CacheReset — zero the counters and drop the memo (harness proof runs).
func CacheReset() {
	floorCacheKey, floorCacheFrame = "", ""
	floorCacheHits, floorCacheMisses = 0, 0
}
