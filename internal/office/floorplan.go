// Package office — ASCII office floor (Go port of node-legacy/src/office).
//
// floorplan.go — props-driven office floor geometry.
//
// computePlan(width, height) -> Plan:
//
//	zones:    walled rooms/partitions with door gaps
//	          (boss office, conference, server room, glass cabins, break area)
//	props:    furniture glyphs ("[=BOSS=]" desk, "=[=======" table,
//	          "[||]" racks, plants, bins, cooler, mail tray...)
//	anchors:  map[seatId]Point — where sprites park at home
//	hotspots: meet/mail/tea/clock/overflow targets for walkers + wall
//
// Degrades gracefully, NEVER panics:
//   - cabins drop first (need W>=96 && H>=24 upstream; here W>=72 && H>=24,
//     going compact below W=88 so all three still fit at 84 cols)
//   - conference drops next (need W>=72 && H>=18)
//   - pods row minimum: 2 rows, collapsing to 2x3 pods when cabins are gone
//   - W<60 || H<16: "small terminal" badge (drawn once, centered, by floor.go)
//
// Props and zones may carry a foreground color (and bold on props);
// floor.go stamps them onto its styled cell grid. Default paint:
// drywall zone walls gray; unstyled props render uncolored.
package office

import (
	"fmt"
	"strings"
)

// ColorName — chalk-style ANSI color names ("yellow" | "cyan" | "gray" | ...).
// Stored as FG strings on cells/props/zones; mapped to lipgloss at render.
type ColorName = string

// Color codes — chalk ANSI names to ANSI256 indices.
var ansiColors = map[string]string{
	"black": "0", "red": "1", "green": "2", "yellow": "3",
	"blue": "4", "magenta": "5", "cyan": "6", "white": "7",
	"gray": "8", "grey": "8",
	"redBright": "9", "greenBright": "10", "yellowBright": "11",
	"blueBright": "12", "magentaBright": "13", "cyanBright": "14",
	"whiteBright": "15",
}

// floorThemes remap the canonical floor colors per UI theme. The floor paints
// with chalk names; themes translate them. Values may be ANSI codes or hex —
// lipgloss.Color accepts both.
var floorThemes = map[string]map[string]string{
	"noir": nil, // identity — the hand-tuned default
	"paper": {
		"gray": "240", "grey": "240", "white": "238", "whiteBright": "255",
	},
	"mono": {
		"red": "7", "green": "7", "yellow": "15", "blue": "7", "magenta": "7",
		"cyan": "7", "white": "15", "gray": "8", "grey": "8", "redBright": "15",
		"greenBright": "7", "yellowBright": "15", "blueBright": "7",
		"magentaBright": "7", "cyanBright": "7", "whiteBright": "15", "black": "8",
	},
	"dracula": {
		"yellow": "#f1fa8c", "red": "#ff5555", "cyan": "#8be9fd",
		"green": "#50fa7b", "blue": "#8be9fd", "magenta": "#bd93f9",
		"magentaBright": "#bd93f9", "blueBright": "#8be9fd", "cyanBright": "#8be9fd",
		"gray": "#6272a4", "grey": "#6272a4", "white": "#f8f8f2",
		"whiteBright": "#f8f8f2",
	},
	"solarized": {
		"yellow": "#b58900", "red": "#dc322f", "cyan": "#2aa198",
		"green": "#859900", "blue": "#268bd2", "magenta": "#d33682",
		"magentaBright": "#d33682", "blueBright": "#268bd2", "cyanBright": "#2aa198",
		"gray": "#586e75", "grey": "#586e75", "white": "#93a1a1",
		"whiteBright": "#eee8d5",
	},
}

var ansiColorsBase = ansiColors // pristine default for theme resets

// SetTheme re-points the floor's color map at the given theme. Unknown names
// restore the default noir palette (never errors).
func SetTheme(name string) {
	m := ansiColorsBase
	if alt, ok := floorThemes[name]; ok && alt != nil {
		m = map[string]string{}
		for k, v := range ansiColorsBase {
			m[k] = v
		}
		for k, v := range alt {
			m[k] = v
		}
	}
	ansiColors = m
}

type Point struct {
	X int
	Y int
}

type Door struct {
	Side string // "n" | "s" | "e" | "w"
	At   int    // offset along that side from the zone origin corner
	Size int    // width of the gap in cells
}

type Zone struct {
	ID     string
	X      int
	Y      int
	W      int
	H      int
	Wall   string // "-" for drywall rooms; cabins use ":", ";", "."
	Color  ColorName // wall paint; default gray (applied by floor.go)
	Doors  []Door
}

type PlanProp struct {
	X     int
	Y     int
	Glyph string
	Color  ColorName // furniture paint; "" = uncolored
	Bold  bool
	Over  bool // stamp AFTER zone walls (wall-mounted items: windows)
}

type Plan struct {
	Width  int
	Height int
	Gen    int // monotonic: walkers re-seat when this changes
	Zones  []Zone
	Props  []PlanProp
	Anchor map[string]Point
	Hot    struct {
		Meet     Point // in front of the boss desk (dispatch briefings)
		Mail     Point // at the mail tray (blocked / permission asks)
		Tea      Point // at the coffee machine (idle drift)
		Clock    Point // on the boss-office wall
		Overflow Point // base spot for "floor-<n>" overflow near the break area
	}
	Nameplate Point // top-left of the boss-office nameplate (status text, 10 cols)
	Tiny      bool  // draw the "small terminal" badge
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var planGen int
var planCache = map[string]Plan{}
var planCurrent *Plan

// TickClock — office wall clock: 09:00 start, +1 minute per 30 ticks -> "(9:41)".
// Shared: the app shell duplicates this formula today; acceptable per plan
// (shell renders its own surface), but it belongs here long-term.
func TickClock(tick int) string {
	t := tick
	if t < 0 {
		t = 0
	}
	total := 9*60 + t/30
	hh := (total / 60) % 24
	mm := total % 60
	return fmt.Sprintf("(%d:%02d)", hh, mm)
}

// ComputePlan — the floor plan for a grid size, cached.
func ComputePlan(width, height int) Plan {
	key := fmt.Sprintf("%dx%d", width, height)
	if hit, ok := planCache[key]; ok {
		planCurrent = &hit
		return hit
	}
	w := width
	if w < 24 {
		w = 24
	}
	h := height
	if h < 12 {
		h = 12
	}
	p := buildPlan(w, h)
	if len(planCache) > 8 {
		planCache = map[string]Plan{}
	}
	planCache[key] = p
	stored := p
	planCurrent = &stored
	return p
}

// CurrentPlan — the plan most recently computed (walkers live against this).
func CurrentPlan() Plan {
	if planCurrent == nil {
		return ComputePlan(120, 28)
	}
	return *planCurrent
}

func buildPlan(W, H int) Plan {
	planGen++
	p := Plan{Width: W, Height: H, Gen: planGen, Anchor: map[string]Point{}}
	zones := &p.Zones
	props := &p.Props
	anchors := p.Anchor

	tiny := W < 60 || H < 16
	// glass cabins survive down to W=72; below W=88 they go compact (~11 wide)
	// so all three still fit the middle band at the real 84-col shell size
	cabinsOn := W >= 72 && H >= 24
	cabinsCompact := W < 88
	confOn := W >= 72 && H >= 18

	topH := clampInt((H-2)*34/100, 5, 9)
	bw := clampInt(W*26/100, 15, 30)
	sw := clampInt(W*20/100, 13, 24)

	// ---- BOSS CORNER OFFICE (top-left) ----
	*zones = append(*zones, Zone{
		ID: "boss", X: 1, Y: 1, W: bw, H: topH, Wall: "-",
		Doors: []Door{{Side: "s", At: bw / 2, Size: 2}},
	})
	nameplate := Point{X: 3, Y: 2}
	// top-wall window, right of the clock, never past the corner (over: stamped
	// after the zone walls so the drywall doesn't erase it)
	winX := min(1+bw/2+2, bw-8)
	*props = append(*props,
		// nameplate: layout placeholder; floor.go re-stamps it with the
		// boss's live status ("[awaiting]" / "[typing]" / "[meetin]")
		PlanProp{X: nameplate.X, Y: nameplate.Y, Glyph: "[awaiting]", Color: "yellow", Bold: true},
		PlanProp{X: 3, Y: 3, Glyph: "[=BOSS=]"},            // boss desk
		PlanProp{X: 14, Y: 3, Glyph: "(~)", Color: "white"}, // desk-side mug
		PlanProp{X: winX, Y: 1, Glyph: "[==o==]", Color: "cyan", Over: true}, // window
		PlanProp{X: 3, Y: 5, Glyph: "o", Color: "green"},   // guest chair
		PlanProp{X: 8, Y: 5, Glyph: "o", Color: "green"},   // guest chair
		PlanProp{X: 10, Y: 1, Glyph: "[###]", Color: "gray"}, // wall calendar
	)
	if bw >= 20 {
		*props = append(*props,
			PlanProp{X: 13, Y: 2, Glyph: "[#]", Color: "yellow"}, // bookshelf, beside the nameplate
			PlanProp{X: 16, Y: 2, Glyph: "[=]", Color: "yellow"}, // books, second shelf cell
		)
	}
	anchors["manager"] = Point{X: 11, Y: 3}
	meet := Point{X: 5, Y: 4}
	clock := Point{X: 3, Y: 1}

	// ---- SERVER / PRINT ROOM (top-right) ----
	sx := W - 1 - sw
	*zones = append(*zones, Zone{
		ID: "server", X: sx, Y: 1, W: sw, H: topH, Wall: "-",
		Doors: []Door{{Side: "s", At: 3, Size: 2}},
	})
	*props = append(*props,
		PlanProp{X: sx + 2, Y: 2, Glyph: "[=]", Color: "gray"}, // pod 1 monitor (screen off)
		PlanProp{X: sx + 1, Y: 3, Glyph: "[______]"},           // pod 1 desk
		PlanProp{X: sx + 1, Y: topH - 1, Glyph: "[cpr]", Color: "gray"}, // copier
		PlanProp{X: sx + 1, Y: topH - 2, Glyph: "[o==o]"},               // treadmill (runner seat desk)
	)
	if topH >= 8 {
		*props = append(*props,
			PlanProp{X: sx + 2, Y: 4, Glyph: "[=]", Color: "gray"}, // pod 2 monitor
			PlanProp{X: sx + 1, Y: 5, Glyph: "[______]"},           // pod 2 desk
		)
	}
	// racks: single-file, one-row spacing, max 2 columns (2nd column staggered)
	for r := 2; r <= topH-1; r += 2 {
		*props = append(*props, PlanProp{X: sx + sw - 5, Y: r, Glyph: "[||]", Color: "magenta"})
	}
	if sw >= 20 {
		for r := 3; r <= topH-1; r += 2 {
			*props = append(*props, PlanProp{X: sx + sw - 10, Y: r, Glyph: "[||]", Color: "magenta"})
		}
	}
	// power box between the desk pod and the rack column
	if sw >= 15 && sw < 20 {
		*props = append(*props, PlanProp{X: sx + 9, Y: 3, Glyph: "[ups]", Color: "magenta"})
	} else if sw >= 20 {
		*props = append(*props, PlanProp{X: sx + 9, Y: 4, Glyph: "[ups]", Color: "magenta"})
	}
	anchors["treadmill-1"] = Point{X: sx + 2, Y: topH - 2}

	// ---- CONFERENCE ROOM (top-center) ----
	fx := 1 + bw
	fw := sx - fx
	if confOn && fw >= 18 {
		*zones = append(*zones, Zone{
			ID: "conference", X: fx, Y: 1, W: fw, H: topH, Wall: "-",
			Doors: []Door{{Side: "s", At: fw / 2, Size: 2}},
		})
		ty := 1 + topH/2
		// table leaves the easel its right-hand 8 cols (2-col wall clearance included)
		tlen := clampInt(fw-12, 6, 14)
		*props = append(*props, PlanProp{X: fx + 3, Y: ty, Glyph: "=[" + strings.Repeat("=", max(0, tlen-3)) + "]", Color: "yellow"})
		nchairs := clampInt((fw-8)/6, 2, 8)
		up := (nchairs + 1) / 2
		down := nchairs - up
		for i := 0; i < up; i++ {
			*props = append(*props, PlanProp{X: fx + 4 + i*3, Y: ty - 1, Glyph: "o", Color: "green"})
		}
		for i := 0; i < down; i++ {
			*props = append(*props, PlanProp{X: fx + 4 + i*3, Y: ty + 1, Glyph: "o", Color: "green"})
		}
		*props = append(*props,
			PlanProp{X: fx + 1, Y: topH - 1, Glyph: "(Y)", Color: "green"},        // corner plant
			PlanProp{X: fx + fw - 8, Y: 2, Glyph: "[|> ]", Color: "cyan"},          // easel / whiteboard (2-col clearance from the right wall)
			PlanProp{X: fx + fw - 7, Y: 3, Glyph: sslash()},                        // tiny chart marks
		)
	}

	// ---- GLASS CABINS (middle band) — walls ":", ";", "." ----
	cabY := 2 + topH
	cabIDs := []string{"hr", "cabin-2", "cabin-3"}
	cw, cstep := 13, 16
	if cabinsCompact {
		cw, cstep = 11, 14 // compact glass cabins keep all 3 at W<88
	}
	if cabinsOn {
		cabWall := []string{":", ";", "."}
		cabColor := []string{"magenta", "blue", "magentaBright"} // glass tint per cabin
		for i := 0; i < 3; i++ {
			cx := 3 + i*cstep
			*zones = append(*zones, Zone{
				ID: fmt.Sprintf("cabin-%d", i+1), X: cx, Y: cabY, W: cw, H: 6,
				Wall: cabWall[i], Color: cabColor[i],
				Doors: []Door{{Side: "s", At: cw / 2, Size: 2}},
			})
			*props = append(*props,
				PlanProp{X: cx + 4, Y: cabY + 1, Glyph: "[=]", Color: "gray"},
				PlanProp{X: cx + 2, Y: cabY + 2, Glyph: "[=____=]"},
				PlanProp{X: cx + 4, Y: cabY + 3, Glyph: "(_)", Color: "green"},
				PlanProp{X: cx + cw - 3, Y: cabY + 1, Glyph: "(Y)", Color: "green"},
			)
			anchors[cabIDs[i]] = Point{X: cx + 4, Y: cabY + 3}
		}
	} else {
		// cabins collapsed: freestanding desks for HR + reviewer on the freed band
		for i, id := range []string{"hr", "cabin-2"} {
			cx := 2 + i*14
			*props = append(*props,
				PlanProp{X: cx + 2, Y: cabY, Glyph: "[=]", Color: "gray"},
				PlanProp{X: cx, Y: cabY + 1, Glyph: "[______]"},
				PlanProp{X: cx + 2, Y: cabY + 2, Glyph: "(_)", Color: "green"},
			)
			anchors[id] = Point{X: cx + 2, Y: cabY + 2}
		}
	}

	// ---- BREAK AREA (bottom-right) ----
	bwd := clampInt(W*19/100, 14, 26)
	bx0 := W - 1 - bwd
	by0 := H - 9
	*zones = append(*zones, Zone{
		ID: "break", X: bx0, Y: by0, W: bwd, H: 8, Wall: "-",
		Doors: []Door{
			{Side: "n", At: 3, Size: 3},
			{Side: "w", At: 4, Size: 2},
		},
	})
	*props = append(*props,
		PlanProp{X: bx0 + 2, Y: by0 + 1, Glyph: "[===]", Color: "yellow"},   // fridge
		PlanProp{X: bx0 + 8, Y: by0 + 1, Glyph: "[bin]", Color: "gray"},     // recycle bin near the kitchen
		PlanProp{X: bx0 - 4, Y: by0 + 4, Glyph: "brk", Color: "gray"},       // door-gap label on the left partition
		PlanProp{X: bx0 + 2, Y: by0 + 2, Glyph: "[#####]", Color: "yellow"}, // kitchen counter
		PlanProp{X: bx0 + 8, Y: by0 + 2, Glyph: "[cof]", Color: "yellow"},   // coffee machine on the counter
	)
	if bwd >= 20 {
		*props = append(*props, PlanProp{X: bx0 + bwd - 6, Y: by0 + 2, Glyph: "[vnd]"}) // vending machine (right wall)
	} else {
		*props = append(*props, PlanProp{X: bx0 + 2, Y: by0 + 5, Glyph: "[vnd]"}) // narrow room: under the fridge
	}
	*props = append(*props,
		PlanProp{X: bx0 + bwd - 6, Y: by0 + 4, Glyph: "[cpy]", Color: "magenta"}, // rack
		PlanProp{X: bx0 + 10, Y: by0 + 5, Glyph: "("},                            // stool
		PlanProp{X: bx0 + 11, Y: by0 + 5, Glyph: "(__)"},                         // small table
		PlanProp{X: bx0 + 15, Y: by0 + 5, Glyph: ")"},                            // stool
		PlanProp{X: bx0 + 2, Y: by0 + 6, Glyph: "[mail]"},                        // mail tray
	)
	tea := Point{X: bx0 + 8, Y: by0 + 3}
	mail := Point{X: bx0 + 2, Y: by0 + 5}
	overflow := Point{X: bx0 + 2, Y: by0 - 2}

	// ---- DEV POD FIELD (bottom-left/center) ----
	// pod: 8 wide x 3 tall — "[=]" monitor / "[______]" desk / "(_)" chair
	fieldRight := bx0 - 2
	nMin := 3
	if cabinsOn {
		nMin = 4
	}
	nb := max(nMin, floorDiv(fieldRight-3-8, 11)+1)
	podRows := []int{H - 9, H - 5}
	scoutIdx := []int{nb - 1, 2*nb - 1} // right-side pods (skopos)
	devN := 0
	for i := 0; i < 2*nb; i++ {
		r := 0
		if i >= nb {
			r = 1
		}
		px := 3 + (i%nb)*11
		py := podRows[r]
		*props = append(*props,
			PlanProp{X: px + 3, Y: py, Glyph: "[=]", Color: "gray"}, // monitor (screen off; lit cyan bold by floor.go when a dev works here)
			PlanProp{X: px, Y: py + 1, Glyph: "[______]"},
			PlanProp{X: px + 2, Y: py + 2, Glyph: "(_)", Color: "green"},
		)
		if i == scoutIdx[0] {
			anchors["scout-1"] = Point{X: px + 2, Y: py + 2}
		} else if i == scoutIdx[1] {
			anchors["scout-2"] = Point{X: px + 2, Y: py + 2}
		} else {
			devN++
			anchors[fmt.Sprintf("dev-%d", devN)] = Point{X: px + 2, Y: py + 2}
		}
	}

	// ---- MIDDLE-BAND PARTITION STRIP (corridor between cabins and pod rows) ----
	// low wall fragments flanking a hanging whiteboard + 2 plants; decor only,
	// no zones/no hotspots, walkers stay unblocked. Drawn only when a free
	// corridor row exists (>= 1 blank row above the first pod row).
	stripY := cabY + 6
	if stripY <= H-10 {
		cabCenter := 3 + (2*cstep+cw)/2
		wbX := cabCenter - 3 // hanging whiteboard, roughly band-centered
		*props = append(*props,
			PlanProp{X: wbX - 11, Y: stripY, Glyph: "+--- ---+", Color: "gray"},  // low wall, left
			PlanProp{X: wbX, Y: stripY, Glyph: "[plan]", Color: "yellow"},        // hanging whiteboard
			PlanProp{X: wbX + 8, Y: stripY, Glyph: "+--- ---+", Color: "gray"},   // low wall, right
			PlanProp{X: cabCenter - 17, Y: stripY, Glyph: "(Y)", Color: "green"}, // plant, left
			PlanProp{X: cabCenter + 22, Y: stripY, Glyph: "(Y)", Color: "green"}, // plant, right
		)
	}

	// ---- SCATTER: plants, bins, water cooler ----
	*props = append(*props,
		PlanProp{X: 2, Y: H - 3, Glyph: "[h2o]", Color: "blue"},           // water cooler
		PlanProp{X: 2, Y: H - 2, Glyph: "(Y)", Color: "green"},            // plant, bottom-left corner
		PlanProp{X: 1, Y: podRows[0] + 1, Glyph: "(.)", Color: "gray"},    // trash near pod row 1
		PlanProp{X: 1, Y: podRows[1] + 1, Glyph: "(.)", Color: "gray"},    // trash near pod row 2
		PlanProp{X: bx0 - 2, Y: H - 2, Glyph: "(Y)", Color: "green"},      // plant by the break door
	)

	p.Hot.Meet = meet
	p.Hot.Mail = mail
	p.Hot.Tea = tea
	p.Hot.Clock = clock
	p.Hot.Overflow = overflow
	p.Nameplate = nameplate
	p.Tiny = tiny
	return p
}

// sslash — the tiny chart marks under the whiteboard (kept as a fn so the
// quoting stays obvious).
func sslash() string {
	return "|/_"
}

// floorDiv — Math.floor division (Go's / truncates toward zero, which
// diverges from the TS oracle for the negative tiny-floor case).
func floorDiv(n, d int) int {
	q := n / d
	if n%d != 0 && n < 0 {
		q--
	}
	return q
}
