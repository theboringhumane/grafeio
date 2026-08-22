// roster.go — seat assignment, resolved against the CURRENT floor plan
// (Go port of node-legacy/src/office/roster.ts; geometry lives in
// floorplan.go — seats are no longer fixed pixels).
//
// Seat ids: manager | hr (cabin-1) | cabin-2 | cabin-3 | cto (exec suite,
// inside the boss office — widescreen plans only) | dev-1..N |
// scout-1..2 | treadmill-1 | floor-<n> (overflow standing near the break area).
package office

import (
	"regexp"
	"sort"
	"strconv"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// devSeatRE — machine-format seat ids, not natural language.
var devSeatRE = regexp.MustCompile(`^dev-(\d+)$`)

var floorSeatRE = regexp.MustCompile(`^floor-(\d+)$`)

// devSeats — dev seat ids present in the plan, sorted numerically: dev-1, dev-2, ...
func devSeats(plan Plan) []string {
	var seats []string
	for k := range plan.Anchor {
		if devSeatRE.MatchString(k) {
			seats = append(seats, k)
		}
	}
	sort.Slice(seats, func(i, j int) bool {
		a, _ := strconv.Atoi(seats[i][4:])
		b, _ := strconv.Atoi(seats[j][4:])
		return a < b
	})
	return seats
}

// RoleSeats — seat candidates per role, in fill order, against a plan.
func RoleSeats(role state.EmployeeRole, plan Plan) []string {
	switch role {
	case state.RoleManager:
		return []string{"manager"}
	case state.RoleHR:
		return []string{"hr"} // cabin-1, the HR cabin
	case state.RoleReviewer:
		return []string{"cabin-2"} // dikastes
	case state.RoleCTO:
		return []string{"cto"} // theboringcto, exec suite in the boss office
	case state.RoleRunner:
		return []string{"treadmill-1"} // hemerodromos, in the server room
	case state.RoleScout:
		return []string{"scout-1", "scout-2"} // right-side pods of the dev field
	default:
		return devSeats(plan) // tekton devs, in pod order
	}
}

// AssignSeat — first free seat for the role; overflow -> "floor-<n>" standing spots.
func AssignSeat(taken map[string]bool, role state.EmployeeRole) string {
	plan := CurrentPlan()
	for _, s := range RoleSeats(role, plan) {
		if !taken[s] {
			return s
		}
	}
	n := 0
	for taken["floor-"+strconv.Itoa(n)] {
		n++
	}
	return "floor-" + strconv.Itoa(n)
}

// SeatAnchor — anchor point of a seat id. Unknown / overflow seats stand near
// the break area.
func SeatAnchor(seat string) Point {
	plan := CurrentPlan()
	if a, ok := plan.Anchor[seat]; ok {
		return a
	}
	n := 0
	if m := floorSeatRE.FindStringSubmatch(seat); m != nil { // machine format, not NL
		n, _ = strconv.Atoi(m[1])
	}
	o := plan.Hot.Overflow
	x := min(max(1, o.X-(n%4)*3), plan.Width-3)
	y := min(max(1, o.Y-(n/4)*2), plan.Height-2)
	return Point{X: x, Y: y}
}

// NameColor — panel color for an employee/agent name (boss yellow, dev cyan, ...).
func NameColor(name string) string {
	n := []rune(name)
	for i, r := range n {
		if r >= 'A' && r <= 'Z' {
			n[i] = r + ('a' - 'A')
		}
	}
	lower := string(n)
	has := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if len(lower) >= len(p) && lower[:len(p)] == p {
				return true
			}
		}
		return false
	}
	switch {
	case has("boss", "manager"):
		return "yellow"
	case has("hr"):
		return "red"
	case has("theboringcto", "cto"):
		return "white"
	case has("tekton", "dev"):
		return "cyan"
	case has("skopos", "scout"):
		return "green"
	case has("dikastes", "review"):
		return "magenta"
	case has("hemero", "run"):
		return "blue"
	}
	return "white"
}
