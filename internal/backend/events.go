// events.go — normalize opencode SSE events into state.Events.
// Port of node-legacy/src/backend/events.ts. Pure helpers only: no I/O,
// no timers, no UI framework. The live backend (opencode.go) owns every
// network call; this module decides WHAT an SSE event means for the
// office floor, given a mutable context object.
package backend

import (
	"encoding/json"
	"sort"
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
	// Finish is the AssistantMessage stop-reason ("stop", "tool-calls", …);
	// a completion with finish=="tool-calls" is MID-turn: the message ends
	// at the tool call and its final text rides the next assistant message.
	Finish string `json:"finish"`
	Time   struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// ocPart covers the Part union fields the mapping reads (ReasoningPart /
// ToolPart / TextPart — see @opencode-ai/sdk gen/types.gen.d.ts).
type ocPart struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	// ToolPart
	CallID string `json:"callID"`
	Tool   string `json:"tool"`
	State  struct {
		Status string         `json:"status"` // pending | running | completed | error
		Title  string         `json:"title"`
		Input  map[string]any `json:"input"`
		Error  string         `json:"error"`
	} `json:"state"`
	// ReasoningPart typing: start is always present; end set on completion.
	Time struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`
}

// ocPermissionReq covers permission.asked / permission.updated (legacy) and
// permission.replied properties. The modern server sends permission.asked
// with id/permission/patterns/always; the legacy updated variant sent title.
type ocPermissionReq struct {
	ID         string         `json:"id"`
	RequestID  string         `json:"requestID"`
	SessionID  string         `json:"sessionID"`
	Permission string         `json:"permission"`
	Title      string         `json:"title"`
	Patterns   []string       `json:"patterns"`
	Always     []string       `json:"always"`
	Metadata   map[string]any `json:"metadata"`
	Reply      string         `json:"reply"`
}

// ocQuestionReq covers question.asked / question.replied / question.rejected
// properties (see /doc QuestionRequest schema).
type ocQuestionReq struct {
	ID        string           `json:"id"`
	RequestID string           `json:"requestID"`
	SessionID string           `json:"sessionID"`
	Questions []ocQuestionInfo `json:"questions"`
	Answers   [][]string       `json:"answers"`
}

type ocQuestionInfo struct {
	Question string `json:"question"`
	Header   string `json:"header"`
	Options  []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

// ocSnapshotFileDiff is the SnapshotFileDiff schema (events + GET diff).
type ocSnapshotFileDiff struct {
	File      string `json:"file"`
	Path      string `json:"path"`
	Patch     string `json:"patch"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Status    string `json:"status"`
}

type ocSessionDiffProps struct {
	SessionID string               `json:"sessionID"`
	Diff      []ocSnapshotFileDiff `json:"diff"`
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

// ocPartDelta is the properties blob of a message.part.delta frame: the
// incremental TEXT GROWTH channel. Verified against opencode serve 1.18.19
// (see cmd/headless probes): a reasoning part's message.part.updated only
// ever arrives as start (empty text, time.end=0) and completion (full text,
// time.end!=0) — all intermediate growth rides these deltas, appended in
// order to the same partID. Text parts delta the same way: a delta is
// surfaced (as thought growth or boss-bubble growth) only after a
// message.part.updated has classified its part, else it buffers.
type ocPartDelta struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	PartID    string `json:"partID"`
	Field     string `json:"field"` // "text" is the only field that matters here
	Delta     string `json:"delta"`
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
	employees      map[string]state.Employee // child session id -> employee
	tasks          map[string]state.BoardTask
	nameCounts     map[state.EmployeeRole]int // role -> last issued number
	seatSeq        int
	lastWorkingAt  map[string]int64    // child id -> last "working" emit time (ms)
	returned       map[string]bool     // child sessions that already returned
	fired          map[string]bool     // dedupe delete-event vs delete-call
	pendingPerms   map[string]permHold // permission/question request id -> hold
	diffSeen       map[string]bool     // sessionID|path -> already surfaced
	reasoningParts map[string]bool     // part id -> a message.part.updated said "reasoning"
	reasoningAccum map[string]string   // part id -> delta-accumulated transcript so far
	deltaBuffer    map[string]string   // part id -> deltas seen BEFORE the part was classified
	textParts      map[string]bool     // part id -> message.part.updated classified a STREAMING text part (primary only)
	textPartMsg    map[string]string   // text part id -> its messageID (deltas key the boss bubble)
	textAccum      map[string]string   // messageID -> delta-accumulated answer text so far
	textStart      map[string]int64    // messageID -> stream start (ms; Msg.At for every update of the bubble)
}

// thoughtCapRunes bounds a thought transcript. Raised from the old 400 (a
// summary) to 3000: EvThought carries the GROWING transcript now, so the UI
// can render a live expanding block.
const thoughtCapRunes = 3000

// bossTextCapRunes bounds a streaming boss chat bubble's accumulated text.
// The pinned completion text (messageText fetch) is NOT capped here — UI
// trims for display; the cap only guards the delta accumulator.
const bossTextCapRunes = 6000

// permHold remembers a pending permission/question request so its reply
// event can be turned into a "resolved" follow-up on the same id.
type permHold struct {
	SessionID    string
	EmployeeID   string
	EmployeeName string
	Title        string
	Summary      string
}

func newNormCtx() *normCtx {
	return &normCtx{
		employees:      make(map[string]state.Employee),
		tasks:          make(map[string]state.BoardTask),
		nameCounts:     make(map[state.EmployeeRole]int),
		lastWorkingAt:  make(map[string]int64),
		returned:       make(map[string]bool),
		fired:          make(map[string]bool),
		pendingPerms:   make(map[string]permHold),
		diffSeen:       make(map[string]bool),
		reasoningParts: make(map[string]bool),
		reasoningAccum: make(map[string]string),
		deltaBuffer:    make(map[string]string),
		textParts:      make(map[string]bool),
		textPartMsg:    make(map[string]string),
		textAccum:      make(map[string]string),
		textStart:      make(map[string]int64),
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

// capThought trims and rune-caps a thought transcript at thoughtCapRunes.
func capThought(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > thoughtCapRunes {
		return sliceMax(text, thoughtCapRunes-3) + "..."
	}
	return text
}

// mapReasoningPart: a ReasoningPart from the PRIMARY session is the boss
// thinking out loud; from a child session it is that employee thinking.
// Done is set when the part's time.end lands (the SDK stamps it on the
// completed update). Empty text with no completion stamp is noise — skip.
// Text is the ACCUMULATED transcript capped at 3000 runes (thoughtCapRunes).
// CallID carries the part id so the UI can replace streaming updates of the
// same thought.
//
// Registration side effects: EVERY reasoning part is remembered in
// ctx.reasoningParts so message.part.delta frames for it can stream (the
// serve only sends updated at start+completion — deltas carry the growth).
// Any deltas that arrived before this classification (deltaBuffer) seed the
// accumulator. On completion the accumulator is freed.
func mapReasoningPart(part ocPart, ctx *normCtx, primaryID string) []state.Event {
	text := capThought(part.Text)
	done := part.Time.End != 0
	if !ctx.reasoningParts[part.ID] {
		ctx.reasoningParts[part.ID] = true
		if buffered := ctx.deltaBuffer[part.ID]; buffered != "" {
			ctx.reasoningAccum[part.ID] = buffered
			delete(ctx.deltaBuffer, part.ID)
		}
	}
	if done {
		// The completed part's own text is authoritative; the accumulator
		// is only a fallback for a completion that somehow carries none.
		if text == "" {
			text = capThought(ctx.reasoningAccum[part.ID])
		}
		delete(ctx.reasoningAccum, part.ID)
		delete(ctx.reasoningParts, part.ID)
	}
	if text == "" && !done {
		return nil
	}
	var empID, empName string
	if part.SessionID == primaryID {
		empID, empName = "boss", "boss"
	} else if emp, ok := ctx.employees[part.SessionID]; ok {
		empID, empName = emp.ID, emp.Name
	} else {
		return nil
	}
	return []state.Event{{
		Kind:         state.EvThought,
		EmployeeID:   empID,
		EmployeeName: empName,
		Text:         text,
		CallID:       part.ID,
		Done:         done,
	}}
}

// mapReasoningDelta turns one message.part.delta frame into a GROWING
// EvThought: the accumulated transcript so far for that reasoning part,
// Done=false. Deltas for parts never classified as reasoning (the final
// text answer deltas too) are never surfaced as thought — a text delta
// stream belongs to the chat reply, not the boss's mind.
//
// Classification race: a delta can theoretically precede its part's first
// message.part.updated; those deltas pile into deltaBuffer until the part
// is classified (mapReasoningPart flushes on "reasoning", drops otherwise).
// Buffers are rune-capped so an unclassified text-part flood can't grow
// without bound.
func mapReasoningDelta(d ocPartDelta, ctx *normCtx, primaryID string) []state.Event {
	if d.PartID == "" || d.Field != "text" || d.Delta == "" {
		return nil
	}
	if !ctx.reasoningParts[d.PartID] {
		buffered := ctx.deltaBuffer[d.PartID] + d.Delta
		if len([]rune(buffered)) <= thoughtCapRunes {
			ctx.deltaBuffer[d.PartID] = buffered
		}
		return nil
	}
	accumulated := ctx.reasoningAccum[d.PartID] + d.Delta
	ctx.reasoningAccum[d.PartID] = accumulated
	empID, empName, ok := actorFor(d.SessionID, ctx, primaryID)
	if !ok {
		return nil
	}
	return []state.Event{{
		Kind:         state.EvThought,
		EmployeeID:   empID,
		EmployeeName: empName,
		Text:         capThought(accumulated),
		CallID:       d.PartID,
		Done:         false,
	}}
}

// ---------------- boss text streaming (final-answer deltas) ----------------

// mapTextPart registers a STREAMING text part of the PRIMARY session
// (message.part.updated, part.type=="text", time.end==0): the boss's
// final-answer channel opens. Only the primary registers — children keep
// their part.updated text frames on the throttled-working path. Any deltas
// that arrived before classification (deltaBuffer) seed the accumulator.
// Emits nothing itself; the EvChatBoss stream rides the delta frames.
// A completed part frame (time.end!=0) just unregisters — the pinned text
// is emitted by the message.updated completion pin, never from here.
func mapTextPart(part ocPart, ctx *normCtx, now int64) []state.Event {
	if part.Time.End != 0 {
		delete(ctx.textParts, part.ID)
		delete(ctx.textPartMsg, part.ID)
		return nil
	}
	if part.MessageID == "" {
		return nil
	}
	if !ctx.textParts[part.ID] {
		ctx.textParts[part.ID] = true
		ctx.textPartMsg[part.ID] = part.MessageID
		if ctx.textStart[part.MessageID] == 0 {
			start := part.Time.Start
			if start == 0 {
				start = now
			}
			ctx.textStart[part.MessageID] = start
		}
		if buffered := ctx.deltaBuffer[part.ID]; buffered != "" {
			ctx.textAccum[part.MessageID] = capBossText(ctx.textAccum[part.MessageID] + buffered)
			delete(ctx.deltaBuffer, part.ID)
		}
	}
	return nil
}

// mapTextDelta turns one message.part.delta frame on a registered text part
// into a GROWING EvChatBoss: same Msg.ID ("bossmsg-"+messageID) as the
// eventual completion bubble, Pending:true, accumulated-so-far text. One
// bubble identity spans stream + completion; the UI replaces in place.
// Emission rate is coalesced by the backend (chatSlots gate, 150ms).
func mapTextDelta(d ocPartDelta, ctx *normCtx) []state.Event {
	if d.PartID == "" || d.Field != "text" || d.Delta == "" {
		return nil
	}
	msgID := ctx.textPartMsg[d.PartID]
	if msgID == "" {
		// Unregistered part on a message that is ALREADY streaming (a second
		// text part whose updated raced its deltas): late-register inline.
		if d.MessageID == "" || ctx.textStart[d.MessageID] == 0 {
			return nil
		}
		msgID = d.MessageID
		ctx.textParts[d.PartID] = true
		ctx.textPartMsg[d.PartID] = msgID
	}
	accumulated := capBossText(ctx.textAccum[msgID] + d.Delta)
	ctx.textAccum[msgID] = accumulated
	at := ctx.textStart[msgID]
	return []state.Event{{
		Kind: state.EvChatBoss,
		Msg: state.ChatMsg{
			ID:      "bossmsg-" + msgID,
			From:    "boss",
			Kind:    "boss",
			Text:    accumulated,
			At:      at,
			Pending: true,
		},
	}}
}

// capBossText rune-caps an accumulated boss answer at bossTextCapRunes. No
// trimming and no ellipsis — mid-stream text keeps leading/trailing space
// so later deltas append cleanly; the prefix cap simply freezes growth.
func capBossText(text string) string {
	return sliceMax(text, bossTextCapRunes)
}

// interruptedStreamEvents flushes every open boss text stream as a final
// Pending=false bubble carrying the accumulated text plus an interruption
// note (abort/error/stop), then frees ALL stream state. Only the primary
// session ever registers text streams, so every open stream belongs to the
// interrupted run. Deltas that stream cleanly into a completion are
// unaffected: their state was already freed by unregisterTextStream.
func interruptedStreamEvents(ctx *normCtx, note string) []state.Event {
	var ids []string
	for msgID, accum := range ctx.textAccum {
		if strings.TrimSpace(accum) != "" {
			ids = append(ids, msgID)
		}
	}
	sort.Strings(ids) // deterministic emit order
	var evs []state.Event
	for _, msgID := range ids {
		evs = append(evs, state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID:      "bossmsg-" + msgID,
			From:    "boss",
			Kind:    "boss",
			Text:    ctx.textAccum[msgID] + "\n" + note,
			At:      ctx.textStart[msgID],
			Pending: false,
		}})
	}
	for partID := range ctx.textParts {
		delete(ctx.textParts, partID)
	}
	for partID := range ctx.textPartMsg {
		delete(ctx.textPartMsg, partID)
	}
	for msgID := range ctx.textAccum {
		delete(ctx.textAccum, msgID)
	}
	for msgID := range ctx.textStart {
		delete(ctx.textStart, msgID)
	}
	return evs
}

// unregisterTextStream stops the delta stream for one message (completion
// pin): its parts go, the accumulator and start stamp are freed. The pinned
// completion bubble text supersedes whatever the deltas accumulated.
func unregisterTextStream(ctx *normCtx, messageID string) {
	for partID, msgID := range ctx.textPartMsg {
		if msgID == messageID {
			delete(ctx.textParts, partID)
			delete(ctx.textPartMsg, partID)
		}
	}
	delete(ctx.textAccum, messageID)
	delete(ctx.textStart, messageID)
}

// mapToolPart surfaces a ToolPart (read/grep/glob/bash/write/edit/task/...)
// as floor-visible work. ToolState maps the SDK union: pending/running ->
// "running", completed -> "done", error -> "error". The part's callID is
// the dedupe key — running and done updates share it.
func mapToolPart(part ocPart, ctx *normCtx, primaryID string) (state.Event, bool) {
	// The "question" tool call surfaces via the dedicated question.asked
	// SSE event (EvQuestion) — a bare tool glyph would only duplicate it.
	if part.Tool == "question" {
		return state.Event{}, false
	}
	toolState := "running"
	switch part.State.Status {
	case "completed":
		toolState = "done"
	case "error":
		toolState = "error"
	}
	var empID, empName string
	if part.SessionID == primaryID {
		empID, empName = "boss", "boss"
	} else if emp, ok := ctx.employees[part.SessionID]; ok {
		empID, empName = emp.ID, emp.Name
	} else {
		return state.Event{}, false
	}
	callID := part.CallID
	if callID == "" {
		callID = part.ID
	}
	return state.Event{
		Kind:         state.EvTool,
		EmployeeID:   empID,
		EmployeeName: empName,
		ToolName:     part.Tool,
		ToolSummary:  toolSummary(part),
		ToolState:    toolState,
		CallID:       callID,
	}, true
}

// toolSummary is the one-liner under a tool glyph: the opencode title when
// the state carries one (completed grep reads "N matches" etc.), else the
// most specific input field — filePath, pattern, command, path, then any
// short string value. Capped at 60 chars; never the raw JSON.
func toolSummary(part ocPart) string {
	s := strings.TrimSpace(part.State.Title)
	if s == "" {
		for _, key := range []string{"filePath", "pattern", "command", "path", "query", "url"} {
			if v, ok := part.State.Input[key].(string); ok && strings.TrimSpace(v) != "" {
				s = strings.TrimSpace(v)
				break
			}
		}
	}
	if s == "" {
		for _, v := range part.State.Input {
			if str, ok := v.(string); ok && strings.TrimSpace(str) != "" {
				s = strings.TrimSpace(str)
				break
			}
		}
	}
	if err := strings.TrimSpace(part.State.Error); err != "" && part.State.Status == "error" {
		s = strings.Join(strings.Fields(err), " ")
	}
	if s == "" {
		return "working"
	}
	return shortTitle(s, 60)
}

// actorFor resolves the floor actor for a session id: the primary session
// is "boss", a known child is its employee row; anything else is skipped.
func actorFor(sessionID string, ctx *normCtx, primaryID string) (empID, empName string, ok bool) {
	if sessionID == primaryID {
		return "boss", "boss", true
	}
	if emp, found := ctx.employees[sessionID]; found {
		return emp.ID, emp.Name, true
	}
	return "", "", false
}

// permissionSummary picks a one-liner for a permission request: the first
// pattern it gates, else the most descriptive metadata string, else "".
func permissionSummary(p ocPermissionReq) string {
	for _, pat := range p.Patterns {
		if s := shortTitle(pat, 60); s != "untitled brief" {
			return s
		}
	}
	for _, key := range []string{"command", "filepath", "filePath", "path", "pattern"} {
		if v, ok := p.Metadata[key].(string); ok && strings.TrimSpace(v) != "" {
			return shortTitle(strings.TrimSpace(v), 60)
		}
	}
	return "permission needed"
}

// mapPermissionAsked: permission.asked (modern) / permission.updated (legacy),
// for ANY session. The boss gets ONLY the EvPermission (the UI renders a
// modal; the manager glyph stays at its desk); a child additionally emits
// the EvBlocked it always did so the floor stays correct.
func mapPermissionAsked(p ocPermissionReq, ctx *normCtx, primaryID string, now int64) []state.Event {
	empID, empName, ok := actorFor(p.SessionID, ctx, primaryID)
	if !ok {
		return nil
	}
	id := p.ID
	if id == "" {
		id = p.RequestID
	}
	title := p.Permission
	if title == "" {
		title = p.Title
	}
	if title == "" {
		title = "permission"
	}
	summary := permissionSummary(p)
	ctx.pendingPerms[id] = permHold{
		SessionID: p.SessionID, EmployeeID: empID, EmployeeName: empName,
		Title: title, Summary: summary,
	}
	evs := []state.Event{{
		Kind:         state.EvPermission,
		PermissionID: id,
		SessionID:    p.SessionID,
		EmployeeID:   empID,
		EmployeeName: empName,
		ToolName:     shortTitle(title, 60),
		ToolSummary:  summary,
		ToolState:    "pending",
	}}
	if empID != "boss" {
		evs = append(evs, state.Event{
			Kind: state.EvBlocked, EmployeeID: empID,
			Text: shortTitle("permission: "+title+" "+summary, 60),
		})
	}
	return evs
}

// mapPermissionReplied: permission.replied clears the pending hold and emits
// a "resolved" EvPermission on the same id so the UI can drop the modal.
// Children also get their forced working pulse (the old behavior).
func mapPermissionReplied(p ocPermissionReq, ctx *normCtx, primaryID string, now int64) []state.Event {
	id := p.RequestID
	if id == "" {
		id = p.ID
	}
	empID, empName, _ := actorFor(p.SessionID, ctx, primaryID)
	if hold, ok := ctx.pendingPerms[id]; ok {
		if empID == "" {
			empID, empName = hold.EmployeeID, hold.EmployeeName
		}
		delete(ctx.pendingPerms, id)
		evs := []state.Event{{
			Kind: state.EvPermission, PermissionID: id, SessionID: hold.SessionID,
			EmployeeID: empID, EmployeeName: empName,
			ToolName: hold.Title, ToolSummary: p.Reply, ToolState: "resolved",
		}}
		emp, isEmp := ctx.employees[p.SessionID]
		if isEmp && !ctx.returned[p.SessionID] {
			evs = append(evs, ctx.throttledWorking(emp.ID, ctx.tasks[p.SessionID].ID, now, true)...)
		}
		return evs
	}
	// Unknown id (backend restarted etc.): keep the old child pulse alive.
	emp, ok := ctx.employees[p.SessionID]
	if !ok || ctx.returned[p.SessionID] {
		return nil
	}
	return ctx.throttledWorking(emp.ID, ctx.tasks[p.SessionID].ID, now, true)
}

// mapQuestionAsked: question.asked, for ANY session. The full question text
// rides in Text; options collapse into ToolSummary ("a | b | c").
func mapQuestionAsked(p ocQuestionReq, ctx *normCtx, primaryID string) []state.Event {
	empID, empName, ok := actorFor(p.SessionID, ctx, primaryID)
	if !ok {
		return nil
	}
	id := p.ID
	if id == "" {
		id = p.RequestID
	}
	var texts, options []string
	for _, q := range p.Questions {
		if s := strings.TrimSpace(q.Question); s != "" {
			texts = append(texts, s)
		}
		for _, opt := range q.Options {
			if s := strings.TrimSpace(opt.Label); s != "" {
				options = append(options, s)
			}
		}
	}
	text := strings.Join(texts, " ")
	if text == "" {
		text = "question from the floor"
	}
	summary := strings.Join(options, " | ")
	if summary == "" {
		summary = "free-form answer"
	}
	ctx.pendingPerms[id] = permHold{
		SessionID: p.SessionID, EmployeeID: empID, EmployeeName: empName,
		Title: "question", Summary: summary,
	}
	return []state.Event{{
		Kind:         state.EvQuestion,
		QuestionID:   id,
		SessionID:    p.SessionID,
		EmployeeID:   empID,
		EmployeeName: empName,
		Text:         shortTitle(text, 240),
		ToolSummary:  shortTitle(summary, 120),
		ToolState:    "pending",
	}}
}

// mapQuestionResolved: question.replied / question.rejected clear the hold
// and emit a "resolved" EvQuestion on the same id.
func mapQuestionResolved(p ocQuestionReq, ctx *normCtx, primaryID string) []state.Event {
	id := p.RequestID
	if id == "" {
		id = p.ID
	}
	hold, ok := ctx.pendingPerms[id]
	if !ok {
		return nil
	}
	delete(ctx.pendingPerms, id)
	return []state.Event{{
		Kind: state.EvQuestion, QuestionID: id, SessionID: hold.SessionID,
		EmployeeID: hold.EmployeeID, EmployeeName: hold.EmployeeName,
		ToolSummary: "answered", ToolState: "resolved",
	}}
}

// diffBody compacts a unified patch for the panel: strips the hunk-noise
// headers, keeps +/- context, capped at 2000 runes.
func diffBody(patch string) string {
	var keep []string
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "index ") {
			continue
		}
		keep = append(keep, line)
	}
	return sliceMax(strings.Join(keep, "\n"), 2000)
}

// mapSessionDiff: session.diff carries the full SnapshotFileDiff list inline.
// One EvFileDiff per file (deduped against paths already surfaced, e.g. by a
// completion-time GET in the backend).
func mapSessionDiff(p ocSessionDiffProps, ctx *normCtx, primaryID string) []state.Event {
	empID, empName, _ := actorFor(p.SessionID, ctx, primaryID)
	var evs []state.Event
	for _, d := range p.Diff {
		if ev, ok := diffEvent(p.SessionID, empID, empName, d, ctx); ok {
			evs = append(evs, ev)
		}
	}
	return evs
}

// diffEvent builds the per-file EvFileDiff, skipping empties and repeats.
func diffEvent(sessionID, empID, empName string, d ocSnapshotFileDiff, ctx *normCtx) (state.Event, bool) {
	path := d.File
	if path == "" {
		path = d.Path
	}
	if path == "" || (d.Additions == 0 && d.Deletions == 0 && strings.TrimSpace(d.Patch) == "") {
		return state.Event{}, false
	}
	key := sessionID + "|" + path
	if ctx.diffSeen[key] {
		return state.Event{}, false
	}
	ctx.diffSeen[key] = true
	return state.Event{
		Kind:         state.EvFileDiff,
		SessionID:    sessionID,
		EmployeeID:   empID,
		EmployeeName: empName,
		DiffPath:     path,
		DiffBody:     diffBody(d.Patch),
		DiffAdd:      d.Additions,
		DiffDel:      d.Deletions,
	}, true
}

// mapOCEvent is the ONE pure mapping entry point. primaryID identifies the
// boss session; everything with parentID == primaryID is an employee.
//
// SSE -> OfficeEvent mapping table (ported verbatim from events.ts):
//
//	session.created (parentID = primary)   -> hire + dispatch
//	session.updated (known child, title)   -> task upsert (retitle)
//	message.part.updated (reasoning, primary) -> thought (boss mind)
//	message.part.updated (reasoning, child)   -> thought (employee mind)
//	message.part.delta (reasoning part, any)  -> thought, growing transcript
//	(serve streams growth ONLY via deltas: updated lands at start
//	empty and completion full — verified 1.18.19)
//	message.part.updated (text, primary, streaming) -> register boss stream
//	message.part.delta (text part, primary)  -> chat-boss, growing bubble
//	("bossmsg-"+messageID, Pending:true; the completion pin reuses the
//	same ID with Pending:false so one bubble spans stream + final)
//	message.part.updated (tool, any)       -> tool run/done/error (+ child working pulse)
//	message.part.updated (child, other)    -> working (throttled 500ms/employee)
//	message.updated (primary, ANY role)    -> [] — the primary's own user
//		message must NEVER echo as chat-user (Send() owns the only
//		chat-user echo; kids' briefs are not chat)
//	message.updated (child, assistant)     -> working
//	session.status idle (child)            -> [] here (backend fetches -> returned+mail)
//	permission.asked/.updated (any)        -> permission (+ blocked for children)
//	permission.replied (any)               -> permission resolved (+ forced working)
//	question.asked (any)                   -> question (text + compact options)
//	question.replied/rejected (any)        -> question resolved
//	session.diff (any)                     -> diff events, one per file
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
		part := p.Part
		// The boss mind, live: reasoning + tool parts stream into the office.
		// Children get the same treatment, labelled with their desk name.
		switch part.Type {
		case "reasoning":
			return mapReasoningPart(part, ctx, primaryID)
		case "text":
			// The boss's final answer streams too — register the part so
			// its deltas grow the "bossmsg-"+messageID chat bubble.
			// Children stay on the old working-pulse path below.
			if part.SessionID == primaryID {
				return mapTextPart(part, ctx, now)
			}
		case "tool":
			ev, ok := mapToolPart(part, ctx, primaryID)
			if !ok {
				return nil
			}
			// A child running a tool also drives the typing pulse it always did.
			if emp, isEmp := ctx.employees[part.SessionID]; isEmp && !ctx.returned[part.SessionID] {
				return append([]state.Event{ev}, ctx.throttledWorking(emp.ID, ctx.tasks[part.SessionID].ID, now, false)...)
			}
			return []state.Event{ev}
		}
		emp, ok := ctx.employees[part.SessionID]
		if !ok || ctx.returned[part.SessionID] {
			return nil
		}
		return ctx.throttledWorking(emp.ID, ctx.tasks[part.SessionID].ID, now, false)

	case "message.part.delta":
		var p ocPartDelta
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		// Registered text parts stream the boss's chat bubble; reasoning
		// parts stream the thought block; unclassified parts buffer until
		// their message.part.updated classifies them.
		if ctx.textParts[p.PartID] || (p.MessageID != "" && !ctx.reasoningParts[p.PartID] && ctx.textStart[p.MessageID] != 0) {
			return mapTextDelta(p, ctx)
		}
		return mapReasoningDelta(p, ctx, primaryID)

	case "message.updated":
		var p struct {
			Info ocMessage `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		info := p.Info
		if info.SessionID == primaryID {
			// Boss completion needs a fetch — the backend handles it.
			// User-role messages on the primary are the member's own chat,
			// already echoed exactly once by Send() — NEVER echoed here.
			if info.Role == "user" {
				// A user message can carry text parts too (the prompt echo);
				// it never streams. Purge any stream registration so its
				// parts never open a boss bubble.
				unregisterTextStream(ctx, info.ID)
			}
			return nil
		}
		emp, ok := ctx.employees[info.SessionID]
		if !ok || info.Role != "assistant" || ctx.returned[info.SessionID] {
			return nil
		}
		return ctx.throttledWorking(emp.ID, ctx.tasks[info.SessionID].ID, now, false)

	case "permission.asked", "permission.updated":
		var p ocPermissionReq
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		return mapPermissionAsked(p, ctx, primaryID, now)

	case "permission.replied":
		var p ocPermissionReq
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		return mapPermissionReplied(p, ctx, primaryID, now)

	case "question.asked":
		var p ocQuestionReq
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		return mapQuestionAsked(p, ctx, primaryID)

	case "question.replied", "question.rejected":
		var p ocQuestionReq
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		return mapQuestionResolved(p, ctx, primaryID)

	case "session.diff":
		var p ocSessionDiffProps
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		return mapSessionDiff(p, ctx, primaryID)

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
		// Any boss text still streaming dies with the run: flush whatever
		// accumulated as a final Pending=false bubble (update-in-place on
		// the same ID), then the error line.
		evs := interruptedStreamEvents(ctx, "[grafeio] stream interrupted")
		return append(evs, state.Event{
			Kind: state.EvChatBoss,
			Msg: state.ChatMsg{
				ID:      "boss-error-" + itoa64(now),
				From:    "boss",
				Text:    "[grafeio] boss error: " + shortTitle(message, 120),
				At:      now,
				Pending: false,
			},
		})

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
