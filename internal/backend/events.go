// events.go — normalize opencode SSE events into state.Events.
// Port of node-legacy/src/backend/events.ts. Pure helpers only: no I/O,
// no timers, no UI framework. The live backend (opencode.go) owns every
// network call; this module decides WHAT an SSE event means for the
// office floor, given a mutable context object.
package backend

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/theboringhumane/grafeio/internal/state"
)

// ---------------- opencode wire shapes ----------------
// Subsets of @opencode-ai/sdk types (gen/types.gen.d.ts), only the fields
// the mapping reads. Unknown fields are ignored by encoding/json.

type ocSession struct {
	ID       string `json:"id"`
	ParentID string `json:"parentID"`
	Title    string `json:"title"`
	Time     struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type ocMessage struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	Time      struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

type ocPart struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
}

type ocPermission struct {
	SessionID string `json:"sessionID"`
	Title     string `json:"title"`
}

type ocSessionStatusProps struct {
	SessionID string `json:"sessionID"`
	Status    struct {
		Type string `json:"type"`
	} `json:"status"`
}

type ocSessionErrorProps struct {
	SessionID string `json:"sessionID"`
	Error     *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// ocSSEEvent is one frame off GET /event (the Event union's `type` plus a
// raw `properties` blob each case unmarshals for itself).
type ocSSEEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// ---------------- norm context (state, not I/O) ----------------

// normCtx is the mutable reducer-side context (TS: NormCtx). Not
// goroutine-safe on its own — the owning backend holds a mutex.
type normCtx struct {
	employees     map[string]state.Employee // child session id -> employee
	tasks         map[string]state.BoardTask
	nameCounts    map[state.EmployeeRole]int // role -> last issued number
	seatSeq       int
	lastWorkingAt map[string]int64 // child id -> last "working" emit time (ms)
	returned      map[string]bool  // child sessions that already returned
	fired         map[string]bool  // dedupe delete-event vs delete-call
}

func newNormCtx() *normCtx {
	return &normCtx{
		employees:     make(map[string]state.Employee),
		tasks:         make(map[string]state.BoardTask),
		nameCounts:    make(map[state.EmployeeRole]int),
		lastWorkingAt: make(map[string]int64),
		returned:      make(map[string]bool),
		fired:         make(map[string]bool),
	}
}

// Greek-desk naming per role (state canon).
func nameBase(role state.EmployeeRole) string {
	switch role {
	case state.RoleScout:
		return "skopos"
	case state.RoleReviewer:
		return "dikastes"
	case state.RoleRunner:
		return "hemerodromos"
	case state.RoleHR:
		return "hr"
	case state.RoleManager:
		return "manager"
	default:
		return "tekton"
	}
}

// roleFromSession guesses a role from the child session's title (plus an
// optional agent hint). Machine-generated titles, not member language —
// plain substring rules are the right tool here.
func roleFromSession(title, agentHint string) state.EmployeeRole {
	hay := strings.ToLower(agentHint + " " + title)
	if strings.Contains(hay, "explore") || strings.Contains(hay, "scout") || strings.Contains(hay, "skopos") {
		return state.RoleScout
	}
	if strings.Contains(hay, "review") || strings.Contains(hay, "dikastes") {
		return state.RoleReviewer
	}
	if strings.Contains(hay, "runner") || strings.Contains(hay, "hemerodromos") {
		return state.RoleRunner
	}
	return state.RoleDeveloper
}

// shortTitle collapses whitespace, bounds length, keeps it ASCII-ish for
// the floor. max 0 means the TS default of 48.
func shortTitle(s string, max int) string {
	if max <= 0 {
		max = 48
	}
	flat := strings.Join(strings.Fields(s), " ")
	if flat == "" {
		return "untitled brief"
	}
	r := []rune(flat)
	if len(r) > max {
		return strings.TrimRightFunc(string(r[:max-3]), unicode.IsSpace) + "..."
	}
	return flat
}

func (ctx *normCtx) issueEmployee(s ocSession) state.Employee {
	role := roleFromSession(s.Title, "")
	n := ctx.nameCounts[role] + 1
	ctx.nameCounts[role] = n
	emp := state.Employee{
		ID:     s.ID, // subagent session id IS the employee id
		Name:   nameBase(role) + "-" + itoa(n),
		Role:   role,
		Seat:   "desk-" + itoa(ctx.seatSeq+1),
		Sprite: state.SpriteToManager, // dispatch walk starts immediately
		Task:   shortTitle(orTitle(s.Title), 0),
	}
	ctx.seatSeq++
	ctx.employees[s.ID] = emp
	return emp
}

func orTitle(t string) string {
	if t == "" {
		return "untitled brief"
	}
	return t
}

func (ctx *normCtx) issueTask(s ocSession, owner string, at int64) state.BoardTask {
	task := state.BoardTask{
		ID:     "task-" + s.ID,
		Title:  shortTitle(orTitle(s.Title), 0),
		Status: state.TaskInProgress,
		Owner:  owner,
		At:     at,
	}
	ctx.tasks[s.ID] = task
	return task
}

// throttledWorking emits at most one "working" pulse per 500ms per employee.
func (ctx *normCtx) throttledWorking(employeeID, taskID string, now int64, force bool) []state.Event {
	last := ctx.lastWorkingAt[employeeID]
	if !force && now-last < 500 {
		return nil
	}
	ctx.lastWorkingAt[employeeID] = now
	return []state.Event{{Kind: state.EvWorking, EmployeeID: employeeID, TaskID: taskID}}
}

// mapOCEvent is the ONE pure mapping entry point. primaryID identifies the
// boss session; everything with parentID == primaryID is an employee.
//
// SSE -> OfficeEvent mapping table (ported verbatim from events.ts):
//
//	session.created (parentID = primary)   -> hire + dispatch
//	session.updated (known child, title)   -> task upsert (retitle)
//	message.part.updated (child)           -> working (throttled 500ms/employee)
//	message.updated (child, assistant)     -> working
//	session.status idle (child)            -> [] here (backend fetches -> returned+mail)
//	permission.updated (child)             -> blocked {note}
//	permission.replied (child)             -> working (forced)
//	session.deleted (child)                -> fire
//	message.updated (primary, completed)   -> [] here (backend fetches -> chat-boss)
//	session.error (primary)                -> chat-boss error line
//	anything else                          -> []
func mapOCEvent(raw ocSSEEvent, ctx *normCtx, primaryID string, now int64) []state.Event {
	switch raw.Type {
	case "session.created":
		var p struct {
			Info ocSession `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		info := p.Info
		if info.ParentID != primaryID {
			return nil
		}
		if _, ok := ctx.employees[info.ID]; ok {
			return nil
		}
		emp := ctx.issueEmployee(info)
		task := ctx.issueTask(info, emp.Name, now)
		return []state.Event{
			{Kind: state.EvHire, Employee: emp},
			{Kind: state.EvDispatch, Task: task, EmployeeID: emp.ID},
		}

	case "session.updated":
		// Title often lands after creation; keep the board row honest.
		var p struct {
			Info ocSession `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		task, ok := ctx.tasks[p.Info.ID]
		if !ok {
			return nil
		}
		title := shortTitle(p.Info.Title, 0)
		if title == "untitled brief" || title == task.Title {
			return nil
		}
		task.Title = title
		ctx.tasks[p.Info.ID] = task
		return []state.Event{{Kind: state.EvTask, Task: task}}

	case "message.part.updated":
		var p struct {
			Part ocPart `json:"part"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		emp, ok := ctx.employees[p.Part.SessionID]
		if !ok || ctx.returned[p.Part.SessionID] {
			return nil
		}
		return ctx.throttledWorking(emp.ID, ctx.tasks[p.Part.SessionID].ID, now, false)

	case "message.updated":
		var p struct {
			Info ocMessage `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		info := p.Info
		if info.SessionID == primaryID {
			return nil // boss completion needs a fetch — backend handles it
		}
		emp, ok := ctx.employees[info.SessionID]
		if !ok || info.Role != "assistant" || ctx.returned[info.SessionID] {
			return nil
		}
		return ctx.throttledWorking(emp.ID, ctx.tasks[info.SessionID].ID, now, false)

	case "permission.updated":
		var p ocPermission
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		emp, ok := ctx.employees[p.SessionID]
		if !ok {
			return nil
		}
		note := p.Title
		if note == "" {
			note = "permission needed"
		}
		return []state.Event{{Kind: state.EvBlocked, EmployeeID: emp.ID, Text: shortTitle(note, 60)}}

	case "permission.replied":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		emp, ok := ctx.employees[p.SessionID]
		if !ok || ctx.returned[p.SessionID] {
			return nil
		}
		return ctx.throttledWorking(emp.ID, ctx.tasks[p.SessionID].ID, now, true)

	case "session.deleted":
		var p struct {
			Info ocSession `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		if _, ok := ctx.employees[p.Info.ID]; !ok || ctx.fired[p.Info.ID] {
			return nil
		}
		ctx.fired[p.Info.ID] = true
		return []state.Event{{Kind: state.EvFire, EmployeeID: p.Info.ID}}

	case "session.error":
		var p ocSessionErrorProps
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		if p.SessionID != primaryID {
			return nil
		}
		message := "unknown error"
		if p.Error != nil && p.Error.Data.Message != "" {
			message = p.Error.Data.Message
		}
		return []state.Event{{
			Kind: state.EvChatBoss,
			Msg: state.ChatMsg{
				ID:      "boss-error-" + itoa64(now),
				From:    "boss",
				Text:    "[grafeio] boss error: " + shortTitle(message, 120),
				At:      now,
				Pending: false,
			},
		}}

	default:
		return nil
	}
}

// itoa/itoa64 — tiny int formatters to avoid strconv noise in mappers.
func itoa(n int) string {
	return itoa64(int64(n))
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
