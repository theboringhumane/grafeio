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
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	demoTickMs    = 180 * time.Millisecond
	demoPulseMs   = 700 * time.Millisecond
	demoAmbientMs = 8 * time.Second
)

type demoBackend struct {
	fl *flow

	mu          sync.Mutex // guards the demo board state below
	roster      []state.Employee
	taskByID    map[string]state.BoardTask
	active      []string        // employees on a brief (receives pulses)
	blockedIDs  map[string]bool // waving at the mailbox, not typing
	pulseIdx    int
	ambientBeat int
	adHocSeq    int
	chatSeq     int
}

func newDemoBackend() *demoBackend {
	return &demoBackend{
		fl:         newFlow(),
		taskByID:   make(map[string]state.BoardTask),
		blockedIDs: make(map[string]bool),
	}
}

func (b *demoBackend) Mode() state.Mode { return state.ModeDemo }

func demoEmployee(id string, role state.EmployeeRole, seat string) state.Employee {
	return state.Employee{ID: id, Name: id, Role: role, Seat: seat, Sprite: state.SpriteAtDesk}
}

// ---------------------------------------------------------------- start

// Start replays the office day: floor opens at t0, first briefs at 400ms,
// a third hire at 1s, a boss thought at 1.6s (done 2.2s), a boss grep at
// 2.4s, returns at 2.5s/4s/6.5s, a permission block at 5.5s, tekton-1's
// read at 5.8s (done 6.2s), ambient chatter bubbles at 3s/5s, coffee drift
// at 7s, then the ambient loop forever. Working pulses fire round-robin
// every 700ms.
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

	// t+1.6s -> t+2.2s: the boss thinks out loud before tool work starts.
	b.fl.at(1600*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
			Text: "planning the dispatch...", CallID: "demo-thought-1", Done: false})
	})
	b.fl.at(2200*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
			Text: "planning the dispatch...", CallID: "demo-thought-1", Done: true})
	})

	// t+2.4s: the boss's own grep lands, done in the same beat.
	b.fl.at(2400*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvTool, EmployeeID: "boss", EmployeeName: "boss",
			ToolName: "grep", ToolSummary: "GRAFEIO_*, 12 hits", ToolState: "done", CallID: "demo-call-1"})
	})

	// t+5.8s -> t+6.2s: tekton-1 reads the file they were blocked on earlier.
	b.fl.at(5800*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvTool, EmployeeID: "tekton-1", EmployeeName: "tekton-1",
			ToolName: "read", ToolSummary: "src/index.ts", ToolState: "running", CallID: "demo-call-2"})
	})
	b.fl.at(6200*time.Millisecond, func() {
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

	// t+5.5s: tekton-1 hits a permission gate and waves at the mailbox...
	b.fl.at(5500*time.Millisecond, func() {
		b.mu.Lock()
		b.blockedIDs["tekton-1"] = true
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvBlocked, EmployeeID: "tekton-1", Text: "permission: write src/app.tsx"})
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

// Send: the demo boss always answers cheerily (600ms ack), and one ad-hoc
// dispatch cycle (900ms) proves the request landed.
func (b *demoBackend) Send(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || b.fl.isStopped() {
		return nil
	}
	b.mu.Lock()
	b.chatSeq++
	seq := b.chatSeq
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
		ID: "user-" + itoa(seq), From: "user", Text: trimmed, At: nowMs(),
	}})

	// The demo boss always answers cheerily, naming the request.
	b.fl.at(600*time.Millisecond, func() {
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID:      "boss-" + itoa(seq),
			From:    "boss",
			Text:    `On it: "` + shortTitle(trimmed, 40) + `" is on the board - watch the floor.`,
			At:      nowMs(),
			Pending: false,
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
	b.mu.Unlock()

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
