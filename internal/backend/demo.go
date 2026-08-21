// demo.go — the scripted touring backend. Port of
// node-legacy/src/backend/demo.ts. No network: one believable day at the
// office played back on a timer chain. Every sprite move, board row and
// mail item is expressed ONLY through state.Events, so the UI animates
// exactly the way it does in live mode.
//
// Tick ownership: this backend emits EvTick every 180ms so the demo works
// even before the app wires an animation timer. The LIVE backend
// (opencode.go) deliberately does NOT emit ticks — the app drives those.
package backend

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	demoTickMs    = 180 * time.Millisecond
	demoPulseMs   = 700 * time.Millisecond
	demoAmbientMs = 8 * time.Second
)

type demoBackend struct {
	fl *flow

	// cfg is the brain.json the factory received (never nil — NewDemo
	// substitutes config.Default()). The scripted day is fixed, so the demo
	// currently reads nothing from it; it is kept so future demo knobs
	// (names, cadence) surface identically with the live backend.
	cfg *config.Config

	mu              sync.Mutex // guards the demo board state below
	roster          []state.Employee
	taskByID        map[string]state.BoardTask
	active          []string            // employees on a brief (receives pulses)
	blockedIDs      map[string]bool     // waving at the mailbox, not typing
	pendingPerm     map[string]permHold // permission request id -> hold
	pendingQuestion map[string]permHold // question request id -> hold
	pulseIdx        int
	ambientBeat     int
	adHocSeq        int
	chatSeq         int
}

func newDemoBackend(cfg *config.Config) *demoBackend {
	return &demoBackend{
		cfg:             cfg,
		fl:              newFlow(),
		taskByID:        make(map[string]state.BoardTask),
		blockedIDs:      make(map[string]bool),
		pendingPerm:     make(map[string]permHold),
		pendingQuestion: make(map[string]permHold),
	}
}

func (b *demoBackend) Mode() state.Mode { return state.ModeDemo }

func demoEmployee(id string, role state.EmployeeRole, seat string) state.Employee {
	return state.Employee{ID: id, Name: id, Role: role, Seat: seat, Sprite: state.SpriteAtDesk}
}

// ---------------------------------------------------------------- start

// Start replays the office day: floor opens at t0, first briefs at 400ms,
// a third hire at 1s, a streaming boss thought at 1.6s-2.4s (four growing
// updates on CallID th-1, then done), a boss question
// at 2s, a boss grep at 2.4s, tekton-2's file diff at 4.5s, returns at
// 2.5s/4s/6.5s, tekton-1's read at 5.2s (done 5.6s) ahead of the permission
// gate at 6s (Write /tmp/x — stays until AnswerPermission or the scripted
// return), ambient chatter bubbles at 3s/5s, coffee drift at 7s, then the
// ambient loop forever. Working pulses fire round-robin every 700ms.
func (b *demoBackend) Start(emit func(state.Event)) error {
	b.fl.setEmit(emit)

	// t0: floor opens. Manager + hr are permanent seats.
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "DEMO - simulated events (no real agents)"})
	b.hire(demoEmployee("manager", state.RoleManager, "manager"))
	b.hire(demoEmployee("hr", state.RoleHR, "hr"))

	// t+400ms: the boss hands out the first two briefs.
	b.fl.at(400*time.Millisecond, func() {
		b.hire(demoEmployee("tekton-1", state.RoleDeveloper, "desk-1"))
		b.dispatch("t1", "Wire the SSE stream into the office reducer", "tekton-1")
		b.hire(demoEmployee("skopos-1", state.RoleScout, "desk-2"))
		b.dispatch("t2", "Map the repo's event flow end to end", "skopos-1")
	})

	// t+1s: a third hire joins the first brief wave.
	b.fl.at(1*time.Second, func() {
		b.hire(demoEmployee("tekton-2", state.RoleDeveloper, "desk-3"))
		b.dispatch("t3", "Draft the demo smoke script", "tekton-2")
	})

	// t+1.6s -> t+2.4s: the boss thinks out loud before tool work starts —
	// a STREAMING thought: one CallID ("th-1") emits four growing transcript
	// updates (the way message.part.delta drives live mode), then collapses
	// Done=true. The UI renders an expanding thinking block that folds shut.
	thoughtStream := []struct {
		at   time.Duration
		text string
	}{
		{1600 * time.Millisecond, "planning"},
		{1867 * time.Millisecond, "planning the"},
		{2133 * time.Millisecond, "planning the dispatch"},
		{2400 * time.Millisecond, "planning the dispatch... ok, wiring it to tekton-1"},
	}
	for _, beat := range thoughtStream {
		beat := beat
		b.fl.at(beat.at, func() {
			b.fl.emit(state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
				Text: beat.text, CallID: "th-1", Done: false})
		})
	}
	b.fl.at(2400*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
			Text: "planning the dispatch... ok, wiring it to tekton-1", CallID: "th-1", Done: true})
	})

	// t+2s: the boss wants a steer — a real question request (the UI would
	// answer it in a question modal). The hold is registered so
	// AnswerQuestion/RejectQuestion can resolve the same id.
	b.fl.at(2*time.Second, func() {
		b.mu.Lock()
		b.pendingQuestion["que-demo-1"] = permHold{
			SessionID: "boss", EmployeeID: "boss", EmployeeName: "boss",
			Title: "question", Summary: "internal/app/model.go | internal/state/state.go",
		}
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvQuestion, QuestionID: "que-demo-1", SessionID: "boss",
			EmployeeID: "boss", EmployeeName: "boss",
			Text:        "Which file should I touch first?",
			ToolSummary: "internal/app/model.go | internal/state/state.go",
			ToolState:   "pending"})
	})

	// t+2.4s: the boss's own grep lands, done in the same beat.
	b.fl.at(2400*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvTool, EmployeeID: "boss", EmployeeName: "boss",
			ToolName: "grep", ToolSummary: "GRAFEIO_*, 12 hits", ToolState: "done", CallID: "demo-call-1"})
	})

	// t+4.5s: tekton-2's edits land inline — one file diff beside the brief.
	b.fl.at(4500*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvFileDiff, SessionID: "tekton-2",
			EmployeeID: "tekton-2", EmployeeName: "tekton-2",
			DiffPath: "internal/app/model.go", DiffAdd: 40, DiffDel: 12,
			DiffBody: "--- a/internal/app/model.go\n" +
				"+++ b/internal/app/model.go\n" +
				"@@ -10,7 +10,9 @@\n" +
				" func newOfficeModel() officeModel {\n" +
				"-\treturn officeModel{floor: newFloor()}\n" +
				"+\tm := officeModel{floor: newFloor()}\n" +
				"+\tm.floor.loadSeats(defaultRoster())\n" +
				"+\tm.connectBackend(newLiveBackend())\n" +
				"@@ -44,5 +46,9 @@\n" +
				"-\treturn m\n" +
				"+\tm.diffView = newDiffView()\n" +
				"+\treturn m\n"})
	})

	// t+5.2s -> t+5.6s: tekton-1 reads the file before hitting the write gate.
	b.fl.at(5200*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvTool, EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			ToolName: "read", ToolSummary: "src/index.ts", ToolState: "running", CallID: "demo-call-2"})
	})
	b.fl.at(5600*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvTool, EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			ToolName: "read", ToolSummary: "src/index.ts", ToolState: "done", CallID: "demo-call-2"})
	})

	// Working pulses: typing frames for whoever is on a brief, round-robin.
	// Blocked folks are at the mailbox waving, not typing — skip them.
	b.fl.every(demoPulseMs, func() {
		b.mu.Lock()
		var free []string
		for _, id := range b.active {
			if !b.blockedIDs[id] {
				free = append(free, id)
			}
		}
		if len(free) == 0 {
			b.mu.Unlock()
			return
		}
		id := free[b.pulseIdx%len(free)]
		b.pulseIdx++
		taskID := ""
		for _, t := range b.taskByID {
			if t.Owner == id && t.Status == state.TaskInProgress {
				taskID = t.ID
				break
			}
		}
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvWorking, EmployeeID: id, TaskID: taskID})
	})

	// t+3s: ambient chatter starts early — the floor shouldn't feel mute
	// before the first return lands.
	b.fl.at(3*time.Second, func() {
		b.fl.emit(state.Event{Kind: state.EvBubble, EmployeeID: "tekton-1", Text: "standup moved to 4."})
	})

	// t+2.5s: the scout returns with findings.
	b.fl.at(2500*time.Millisecond, func() {
		b.doReturn("skopos-1", "t2", "return: scout report",
			"Scout report: events.ts maps 8 SSE types cleanly. Only child-idle and boss-complete need fetches; the rest are pure. No blockers to wiring the reducer.")
	})

	// t+4s: tekton-2 ships the smoke script.
	b.fl.at(4*time.Second, func() {
		b.doReturn("tekton-2", "t3", "return: demo smoke script",
			"DONE - smoke script records 6.5s of demo events and asserts the floor contract.\n"+
				"FILES - scripts/smoke-demo.ts.\n"+
				"VERIFY - npx tsx scripts/smoke-demo.ts prints SMOKE OK.")
	})

	// t+5s: skopos chimes in after the smoke-script ship.
	b.fl.at(5*time.Second, func() {
		b.fl.emit(state.Event{Kind: state.EvBubble, EmployeeID: "skopos-1", Text: "nice catch in review."})
	})

	// t+6s: tekton-1 hits a real permission gate (Write /tmp/x). EvPermission
	// opens the answer modal AND the floor still shows them blocked at the
	// mailbox — and there they stay until the UI calls AnswerPermission (or
	// the scripted return at 6.5s auto-resolves it as an implicit approval).
	b.fl.at(6*time.Second, func() {
		hold := permHold{
			SessionID: "tekton-1", EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			Title: "Write", Summary: "/tmp/x",
		}
		b.mu.Lock()
		b.pendingPerm["perm-demo-1"] = hold
		b.blockedIDs["tekton-1"] = true
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvPermission, PermissionID: "perm-demo-1",
			SessionID: "tekton-1", EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			ToolName: hold.Title, ToolSummary: hold.Summary, ToolState: "pending"})
		b.fl.emit(state.Event{Kind: state.EvBlocked, EmployeeID: "tekton-1", Text: "permission: Write /tmp/x"})
	})

	// ...approved; t+6.5s the brief lands in the tray.
	b.fl.at(6500*time.Millisecond, func() {
		b.doReturn("tekton-1", "t1", "return: SSE wiring",
			"DONE - reducer consumes hire/dispatch/working/returned/blocked and the floor animates. VERIFY: demo timeline replays the whole flow without SDK calls.")
	})

	// t+7s: someone drifts to the tea machine.
	b.fl.at(7*time.Second, func() {
		b.fl.emit(state.Event{Kind: state.EvIdleDrift, EmployeeID: "skopos-1"})
	})

	// Ambient life: gentle working pulses, occasional coffee, forever.
	b.fl.every(demoAmbientMs, func() {
		b.mu.Lock()
		b.ambientBeat++
		beat := b.ambientBeat
		b.mu.Unlock()
		folks := []string{"tekton-1", "skopos-1", "tekton-2"}
		who := folks[beat%len(folks)]
		b.fl.emit(state.Event{Kind: state.EvWorking, EmployeeID: who})
		if beat%3 == 0 {
			b.fl.emit(state.Event{Kind: state.EvIdleDrift, EmployeeID: folks[(beat+1)%len(folks)]})
		}
	})

	// Animation frames. DEMO emits these itself (see module docblock).
	b.fl.every(demoTickMs, func() {
		b.fl.emit(state.Event{Kind: state.EvTick})
	})
	return nil
}

// ---------------------------------------------------------------- send

// Send: plain-text state.Backend contract — delegates to the attachment
// seam SendWith, mirroring the live backend.
func (b *demoBackend) Send(text string) error {
	return b.SendWith(text, nil)
}

// SendWith — the demo twin of the live attachment send. Files never leave
// the box (there is no server to receive parts), but the twin stays
// honest: the user-bubble echo carries the names in Meta exactly like
// live (the chat renders the dim " · 📎 N" suffix), and the scripted boss
// ack NAMES the attachments so a paste/@ attach visibly round-trips
// through the office. The ack STREAMS (four accumulated updates on one
// bubble id over 500ms, final pin at 600ms, mirroring the live text-delta
// stream), and one ad-hoc dispatch cycle (900ms) proves the request
// landed.
func (b *demoBackend) SendWith(text string, atts []state.Attachment) error {
	trimmed := strings.TrimSpace(text)
	if (trimmed == "" && len(atts) == 0) || b.fl.isStopped() {
		return nil
	}
	b.mu.Lock()
	b.chatSeq++
	seq := b.chatSeq
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
		ID: "user-" + itoa(seq), From: "user", Text: trimmed, At: nowMs(),
		Meta: state.AttachMeta(attachmentNames(atts)),
	}})

	// The demo boss always answers cheerily, naming the request — and it
	// STREAMS the ack exactly like a live text-part delta stream: four
	// growing EvChatBoss updates on ONE bubble id (Pending:true) inside a
	// 500ms window, then the final Pending:false bubble at 600ms. The UI
	// watches one bubble grow, then pin.
	named := `"` + shortTitle(trimmed, 40) + `"`
	if trimmed == "" {
		named = "the attachments"
	}
	ack := "On it: " + named + " is on the board - watch the floor."
	if names := attachmentNames(atts); len(names) > 0 {
		ack += " I see " + itoa(len(names)) + " attachment(s): " + strings.Join(names, ", ") + "."
	}
	ackRunes := []rune(ack)
	bossID := "boss-" + itoa(seq)
	streamBeats := []time.Duration{100, 250, 400, 500}
	for i, at := range streamBeats {
		i, at := i, at
		b.fl.at(at*time.Millisecond, func() {
			n := len(ackRunes) * (i + 1) / len(streamBeats)
			b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
				ID: bossID, From: "boss", Kind: "boss",
				Text: string(ackRunes[:n]), At: nowMs(), Pending: true,
			}})
		})
	}
	b.fl.at(600*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID: bossID, From: "boss", Kind: "boss",
			Text: ack, At: nowMs(), Pending: false,
		}})
	})

	// ...and one ad-hoc dispatch cycle proves the request landed.
	b.fl.at(900*time.Millisecond, func() {
		b.mu.Lock()
		assignee := "tekton-1"
		if b.adHocSeq%2 != 0 {
			assignee = "tekton-2"
		}
		b.adHocSeq++
		taskID := "adhoc-" + itoa(b.adHocSeq)
		b.mu.Unlock()
		b.dispatch(taskID, "Ad-hoc: "+shortTitle(trimmed, 36), assignee)
	})
	return nil
}

// ---------------------------------------------------------------- permission replies

// AnswerPermission resolves a pending demo permission: logs the reply on
// the status line, clears the blocked employee (pulses resume), and emits a
// "resolved" EvPermission on the same id so the UI drops the modal.
func (b *demoBackend) AnswerPermission(permissionID, response string) error {
	switch response {
	case "once", "always", "reject":
	default:
		return fmt.Errorf("invalid permission response %q (want once|always|reject)", response)
	}
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	b.mu.Lock()
	hold, ok := b.pendingPerm[permissionID]
	delete(b.pendingPerm, permissionID)
	if !ok {
		hold = permHold{
			SessionID: "tekton-1", EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			Title: "permission", Summary: "demo request",
		}
	}
	empID := hold.EmployeeID
	if empID != "" && empID != "boss" {
		delete(b.blockedIDs, empID)
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[demo] permission %s answered %q (%s: %s %s)", permissionID, response, hold.EmployeeName, hold.Title, hold.Summary)})
	b.fl.emit(state.Event{Kind: state.EvPermission, PermissionID: permissionID,
		SessionID: hold.SessionID, EmployeeID: hold.EmployeeID, EmployeeName: hold.EmployeeName,
		ToolName: hold.Title, ToolSummary: response, ToolState: "resolved"})
	return nil
}

// ---------------------------------------------------------------- question replies

// AnswerQuestion resolves a pending demo question: logs the answers on the
// status line and emits a "resolved" EvQuestion on the same id so the UI
// drops the question modal.
func (b *demoBackend) AnswerQuestion(requestID string, answers []string) error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	b.mu.Lock()
	hold, ok := b.pendingQuestion[requestID]
	delete(b.pendingQuestion, requestID)
	if !ok {
		hold = permHold{
			SessionID: "boss", EmployeeID: "boss", EmployeeName: "boss",
			Title: "question", Summary: "demo question",
		}
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[demo] answered question %s: %s", requestID, strings.Join(answers, ", "))})
	b.fl.emit(state.Event{Kind: state.EvQuestion, QuestionID: requestID,
		SessionID: hold.SessionID, EmployeeID: hold.EmployeeID, EmployeeName: hold.EmployeeName,
		ToolSummary: "answered", ToolState: "resolved"})
	return nil
}

// RejectQuestion rejects a pending demo question: analogous status line
// plus a "resolved" EvQuestion on the same id.
func (b *demoBackend) RejectQuestion(requestID string) error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	b.mu.Lock()
	hold, ok := b.pendingQuestion[requestID]
	delete(b.pendingQuestion, requestID)
	if !ok {
		hold = permHold{
			SessionID: "boss", EmployeeID: "boss", EmployeeName: "boss",
			Title: "question", Summary: "demo question",
		}
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[demo] rejected question %s", requestID)})
	b.fl.emit(state.Event{Kind: state.EvQuestion, QuestionID: requestID,
		SessionID: hold.SessionID, EmployeeID: hold.EmployeeID, EmployeeName: hold.EmployeeName,
		ToolSummary: "rejected", ToolState: "resolved"})
	return nil
}

// ---------------------------------------------------------------- queue board + respawn

// QueueItemStart is the demo twin of the live board seam: no agentmemory,
// so it returns a deterministic "demo-<index>" id proving interface-compat.
func (b *demoBackend) QueueItemStart(index int, title string) string {
	return "demo-" + itoa(index)
}

// QueueItemDone is a demo no-op (no board to mark).
func (b *demoBackend) QueueItemDone(boardID string) {}

// ResetPrimary is a demo no-op (a scripted boss has no session to drop).
func (b *demoBackend) ResetPrimary(forceNew bool) error { return nil }

// ---------------------------------------------------------------- stop

func (b *demoBackend) Stop() error {
	b.fl.stop()
	return nil
}

// ---------------------------------------------------------------- script helpers

func (b *demoBackend) hire(e state.Employee) {
	b.mu.Lock()
	b.roster = append(b.roster, e)
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: e})
}

func (b *demoBackend) dispatch(taskID, title, owner string) {
	t := state.BoardTask{
		ID:     taskID,
		Title:  title,
		Status: state.TaskInProgress,
		Owner:  owner,
		At:     nowMs(),
	}
	b.mu.Lock()
	b.taskByID[taskID] = t
	found := false
	for _, id := range b.active {
		if id == owner {
			found = true
			break
		}
	}
	if !found {
		b.active = append(b.active, owner)
	}
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvDispatch, Task: t, EmployeeID: owner})
}

func (b *demoBackend) doReturn(employeeID, taskID, subject, body string) {
	b.mu.Lock()
	prev, ok := b.taskByID[taskID]
	if !ok {
		prev = state.BoardTask{
			ID:     taskID,
			Title:  "untitled brief",
			Status: state.TaskInProgress,
			Owner:  employeeID,
			At:     nowMs(),
		}
	}
	done := prev
	done.Status = state.TaskDone
	b.taskByID[taskID] = done
	next := b.active[:0]
	for _, id := range b.active {
		if id != employeeID {
			next = append(next, id)
		}
	}
	b.active = next
	delete(b.blockedIDs, employeeID)
	// A staged return implies the boss approved: auto-resolve any permission
	// the employee was still waiting on so no modal dangles past the brief.
	var resolved []state.Event
	for id, hold := range b.pendingPerm {
		if hold.EmployeeID == employeeID {
			delete(b.pendingPerm, id)
			resolved = append(resolved, state.Event{Kind: state.EvPermission, PermissionID: id,
				SessionID: hold.SessionID, EmployeeID: hold.EmployeeID, EmployeeName: hold.EmployeeName,
				ToolName: hold.Title, ToolSummary: "once", ToolState: "resolved"})
		}
	}
	b.mu.Unlock()

	for _, ev := range resolved {
		b.fl.emit(ev)
	}

	mail := state.MailItem{
		ID:      "mail-" + taskID,
		From:    employeeID,
		To:      "manager",
		At:      nowMs(),
		Subject: subject,
		Body:    body,
		Kind:    state.MailReturn,
	}
	b.fl.emit(state.Event{Kind: state.EvTask, Task: done})
	b.fl.emit(state.Event{Kind: state.EvReturned, EmployeeID: employeeID, TaskID: done.ID, Mail: mail})
}
