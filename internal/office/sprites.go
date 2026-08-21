// sprites.go — animated glyphs (Go port of node-legacy/src/office/sprites.ts).
// Pure unit logic, deterministic: same tick + same event stream -> same frame.
//
// The walker state machine (positions, dogleg pathing, plan-gen clamping)
// lives in floor.go next to the grid renderer, matching the brief's layout.
package office

import (
	"github.com/theboringhumane/grafeio/internal/state"
)

// ROLE_GLYPH — one rune per role.
var ROLE_GLYPH = map[state.EmployeeRole]rune{
	state.RoleManager:   'M',
	state.RoleHR:        'H',
	state.RoleDeveloper: 'T',
	state.RoleScout:     'S',
	state.RoleReviewer:  'D',
	state.RoleRunner:    'R',
}

// ROLE_COLOR — sprite paint per role, matches the app legend (M yellow, H red, ...).
var ROLE_COLOR = map[state.EmployeeRole]ColorName{
	state.RoleManager:   "yellow",
	state.RoleHR:        "red",
	state.RoleDeveloper: "cyan",
	state.RoleScout:     "green",
	state.RoleReviewer:  "magenta",
	state.RoleRunner:    "blue",
}

const coffeeTicks = 60 // how long a break lasts

// SpriteFrame — animated 3-char glyph for a role+state at a given tick.
func SpriteFrame(role state.EmployeeRole, sprite state.SpriteState, tick int) string {
	L := string(ROLE_GLYPH[role])
	if L == "" {
		L = "?"
	}
	beat := tick % 2 != 0
	switch sprite {
	case state.SpriteWorking: // typing arms
		if beat {
			return "_" + L + "_"
		}
		return "~" + L + "~"
	case state.SpriteToManager, state.SpriteToDesk, state.SpriteToCoffee: // walking bob
		if beat {
			return "(" + L + ")"
		}
		return " " + L + " "
	case state.SpriteMeeting: // talking
		if beat {
			return " " + L + "."
		}
		return " " + L + " "
	case state.SpriteAtMailbox: // waving for attention (blocked)
		if beat {
			return "\\" + L + " "
		}
		return " " + L + "/"
	case state.SpriteCoffee: // sipping, steam wisp
		if beat {
			return " " + L + "~"
		}
		return " " + L + " "
	default: // at-desk: idle; sleep-z's float above (IdleBlinkZs), NEVER glued into this row
		return " " + L + " "
	}
}

// IdleBlinkZs — floating sleep-z's for an idling (at-desk) sprite on its blink
// frames: "z" on the first blink frame (tick % 16 == 0), "zZ" on the deeper
// one (tick % 16 == 1), "" otherwise. floor.go stamps these one row above the
// sprite's right shoulder at (x+2, y-1) — never inside the sprite's own row,
// where they glue into the role letter and read as typos ("zMz").
func IdleBlinkZs(sprite state.SpriteState, tick int) string {
	if sprite != state.SpriteAtDesk {
		return ""
	}
	phase := ((tick % 16) + 16) % 16
	switch phase {
	case 0:
		return "z"
	case 1:
		return "zZ"
	}
	return ""
}
