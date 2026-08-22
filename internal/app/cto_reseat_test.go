// cto_reseat_test.go — the app-side half of the LIVE pseudo-CTO contract
// (backend half: internal/backend/cto_test.go (e)-(h)):
//
//	(reducer) the pseudo's boot EvHire seats him at "cto" via the normal
//	    AssignSeat authority (model.go EvHire recomputes every seat), and
//	    the swap sequence [EvFire pseudo -> EvHire real] leaves exactly ONE
//	    RoleCTO on the floor — the real theboringcto-N, seated at "cto";
//	(hydrate) a restored office session NEVER resurrects a saved CTO row:
//	    its session was deleted server-side after the return, and live
//	    Start re-seats the pseudo deterministically every boot — a
//	    restored row would pin seat "cto" to a dead desk (sessions.go).
package app

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// bootFloor — the floor as live Start opens it: the two fixed seats
// (manager, hr) exactly as initialState + Start's own hires leave them.
func bootFloor() state.OfficeState {
	return state.OfficeState{
		Mode: state.ModeLive,
		Employees: []state.Employee{
			{ID: "ses-primary", Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk},
			{ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr", Sprite: state.SpriteAtDesk},
		},
	}
}

// ctosOn — roster facts for assertions: how many RoleCTO rows and where
// they sit.
func ctosOn(st state.OfficeState) (emps []state.Employee) {
	for _, e := range st.Employees {
		if e.Role == state.RoleCTO {
			emps = append(emps, e)
		}
	}
	return emps
}

// (reducer) the pseudo seats at "cto"; the fire-then-hire swap leaves the
// real CTO alone in the exec suite — the exact event order the backend's
// session.created mapper emits (fire first: AssignSeat then finds "cto"
// free for theboringcto-1).
func TestReducerSeatsPseudoCTOAndSwapsReal(t *testing.T) {
	st := bootFloor()

	// Boot hire #3: the idle pseudo-CTO (mirrors backend pseudoCTOEmployee
	// — the reducer is the seat AUTHORITY: it recomputes, so we hand him
	// over with the same fields the backend emits).
	st = reducer(st, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "theboringcto", Name: "theboringcto", Role: state.RoleCTO,
		Seat: "cto", Sprite: state.SpriteAtDesk,
	}})
	ctos := ctosOn(st)
	if len(ctos) != 1 || ctos[0].ID != "theboringcto" || ctos[0].Seat != "cto" {
		t.Fatalf("boot: pseudo must be the only CTO, seated at cto, got %+v", ctos)
	}
	if len(st.Employees) != 3 {
		t.Fatalf("boot floor must hold manager+hr+pseudo, got %d employees", len(st.Employees))
	}

	// An architecture child lands: the mapper fires the pseudo FIRST,
	// then hires the real session-backed CTO (wire order is the
	// correctness — reversed, AssignSeat would overflow to floor-0).
	st = reducer(st, state.Event{Kind: state.EvFire, EmployeeID: "theboringcto"})
	if len(ctosOn(st)) != 0 {
		t.Fatalf("the fire must clear the pseudo before the real hire, got %+v", ctosOn(st))
	}
	st = reducer(st, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "ses-arch", Name: "theboringcto-1", Role: state.RoleCTO,
		Seat: "desk-1", Sprite: state.SpriteToManager, // pre-reducer seat; authority overwrites it
	}})
	ctos = ctosOn(st)
	if len(ctos) != 1 {
		t.Fatalf("after the swap exactly ONE CTO must stand, got %+v", ctos)
	}
	if ctos[0].ID != "ses-arch" || ctos[0].Seat != "cto" {
		t.Fatalf("the real CTO must take seat cto, got %+v", ctos[0])
	}
	t.Logf("after swap: %+v", ctos[0])

	// And back: the real one leaves (removed after his return), the
	// pseudo's re-seat hire lands — the room is his again.
	st = reducer(st, state.Event{Kind: state.EvFire, EmployeeID: "ses-arch"})
	st = reducer(st, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "theboringcto", Name: "theboringcto", Role: state.RoleCTO,
		Seat: "cto", Sprite: state.SpriteAtDesk,
	}})
	ctos = ctosOn(st)
	if len(ctos) != 1 || ctos[0].ID != "theboringcto" || ctos[0].Seat != "cto" {
		t.Fatalf("re-seat must restore the pseudo at cto, got %+v", ctos)
	}
}

// (hydrate) restore skips manager/hr AND the CTO's chair: start's boot
// hire owns the exec suite deterministically; a saved CTO row points at a
// child session the server deleted long ago and must never resurrect as
// a silent desk.
func TestHydrateSessionSkipsCTORow(t *testing.T) {
	m := New(&recBackend{}, nil)
	sf := &SessionFile{
		Dir: "/x",
		Agents: []state.Employee{
			{ID: "ses-old-boss", Name: "boss", Role: state.RoleManager, Seat: "manager"},
			{ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr"},
			{ID: "ses-dead-cto", Name: "theboringcto-1", Role: state.RoleCTO, Seat: "cto", Sprite: state.SpriteAtDesk},
			{ID: "ses-dev", Name: "tekton-1", Role: state.RoleDeveloper, Seat: "dev-1", Task: "wire the SSE stream"},
		},
		SavedAt: 1700000000000,
	}
	m.hydrateSession(sf)

	if e := findEmployee(m.st, "ses-dead-cto"); e != nil {
		t.Fatalf("a restored CTO row must never resurrect (dead session), got %+v", *e)
	}
	if got := ctosOn(m.st); len(got) != 0 {
		t.Fatalf("hydrate must add NO CTO row — the pseudo owns the chair, got %+v", got)
	}
	if e := findEmployee(m.st, "ses-old-boss"); e != nil {
		t.Fatalf("manager rows stay skipped, got %+v", *e)
	}
	dev := findEmployee(m.st, "ses-dev")
	if dev == nil {
		t.Fatal("other agents still return as silent hires — the developer is missing")
	}
	if dev.Task != "" || dev.Sprite != state.SpriteAtDesk {
		t.Fatalf("restored dev must be idle at his desk (task cleared, at-desk), got %+v", *dev)
	}
	t.Logf("hydrated roster: %d employees, no CTO row (pseudo owns the chair)", len(m.st.Employees))
}
