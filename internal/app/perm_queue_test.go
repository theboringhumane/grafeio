// perm_queue_test.go — permission asks from ANY session ride ONE queue:
// a child EvPermission opens the chat panel's permission popover (Agent
// names the requester), a boss ask stacks behind it ("1 of N" via
// Index/Total), answering pops the front and advances, esc defers into the
// esc'd pile and /perm re-opens the newest, and a server-side "resolved"
// drops its entry from BOTH piles.
package app

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// permRecBackend — the app-test recBackend PLUS a recording
// AnswerPermission seam (the queue must answer exactly the displayed
// front, by wire id).
type permRecBackend struct {
	recBackend
	answered [][2]string
}

func (p *permRecBackend) AnswerPermission(id, response string) error {
	p.answered = append(p.answered, [2]string{id, response})
	return nil
}

func TestPermissionQueueChildStacks(t *testing.T) {
	b := &permRecBackend{}
	m := New(b, nil)

	// a CHILD permission arrives → opens the popover (Agent = child name)
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-child-1",
		EmployeeID: "tekton-1", EmployeeName: "tekton", ToolName: "Write",
		ToolSummary: "/tmp/x", ToolState: "pending"})
	if len(m.permQ.pending) != 1 {
		t.Fatalf("child perm must enqueue, got %d pending", len(m.permQ.pending))
	}
	v := m.permQ.view()
	if v == nil || v.Agent != "tekton" || v.ToolName != "Write" || v.Index != 1 || v.Total != 1 {
		t.Fatalf("front must be the child ask 1 of 1: %+v", v)
	}

	// a BOSS permission stacks behind it — front unchanged, Total grows
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-boss-1",
		EmployeeName: "boss", ToolName: "Bash",
		ToolSummary: "rm -rf /tmp/y", ToolState: "pending"})
	if len(m.permQ.pending) != 2 {
		t.Fatalf("boss perm must stack, got %d pending", len(m.permQ.pending))
	}
	v = m.permQ.view()
	if v == nil || v.Agent != "tekton" || v.Index != 1 || v.Total != 2 {
		t.Fatalf("front stays the child ask at 1 of 2: %+v", v)
	}

	// answering pops ONLY the front — boss ask advances into the popover
	m = runMsg(t, m, permAnswerMsg{response: "once"})
	if len(b.answered) != 1 || b.answered[0] != [2]string{"perm-child-1", "once"} {
		t.Fatalf("answer must hit the displayed front id: %+v", b.answered)
	}
	v = m.permQ.view()
	if v == nil || v.Agent != "boss" || v.ID != "perm-boss-1" || v.Index != 1 || v.Total != 1 {
		t.Fatalf("boss ask must advance after the answer: %+v", v)
	}

	// esc defers the displayed front → popover closes, pile holds it
	m = runMsg(t, m, permLaterMsg{})
	if len(m.permQ.pending) != 0 || len(m.permQ.escd) != 1 {
		t.Fatalf("esc must move the front to the esc'd pile: %d pending, %d esc'd",
			len(m.permQ.pending), len(m.permQ.escd))
	}
	if m.permQ.view() != nil {
		t.Fatalf("popover must close while only esc'd entries remain: %+v", m.permQ.view())
	}

	// /perm re-opens the most recent esc'd ask at the queue front
	m = runMsg(t, m, slashMsg{text: "/perm"})
	if len(m.permQ.pending) != 1 || len(m.permQ.escd) != 0 {
		t.Fatalf("/perm must move the esc'd ask back to the front: %d pending, %d esc'd",
			len(m.permQ.pending), len(m.permQ.escd))
	}
	v = m.permQ.view()
	if v == nil || v.ID != "perm-boss-1" || v.Agent != "boss" {
		t.Fatalf("/perm re-open must display the esc'd boss ask: %+v", v)
	}

	// a server-side "resolved" drops the DISPLAYED entry → popover closes
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-boss-1",
		EmployeeName: "boss", ToolState: "resolved"})
	if len(m.permQ.pending) != 0 || m.permQ.view() != nil {
		t.Fatalf("resolved must drop the displayed entry: %d pending, view %+v",
			len(m.permQ.pending), m.permQ.view())
	}

	// ...and the same resolved sweep clears ESC'D entries (fresh child ask,
	// esc'd, then resolved server-side).
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-child-2",
		EmployeeID: "skopos-1", EmployeeName: "skopos", ToolName: "external_directory",
		ToolSummary: "~/docs", ToolState: "pending"})
	m = runMsg(t, m, permLaterMsg{})
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-child-2",
		EmployeeName: "skopos", ToolState: "resolved"})
	if len(m.permQ.pending) != 0 || len(m.permQ.escd) != 0 {
		t.Fatalf("resolved must clear esc'd entries too: %d pending, %d esc'd",
			len(m.permQ.pending), len(m.permQ.escd))
	}

	// legacy blank EmployeeName still means "boss".
	m = runMsg(t, m, state.Event{Kind: state.EvPermission, PermissionID: "perm-legacy",
		ToolName: "Read", ToolSummary: "x", ToolState: "pending"})
	if p := m.permQ.front(); p == nil || p.Agent != "boss" {
		t.Fatalf("blank EmployeeName must display as boss: %+v", p)
	}
}
