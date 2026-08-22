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

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/state"
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
	// Cost/Tokens — the AssistantMessage usage counters. REQUIRED fields on
	// the wire (opencode serve 1.18.19 GET /doc, AssistantMessage schema:
	// required [..., "cost", "tokens"]): cost is the message's USD total as
	// computed by opencode ITSELF — theboringoffice never prices anything. Numbers
	// decode as float64 (the wire type is "number"); token counters are
	// integral in practice and convert at emit time.
	Cost   float64  `json:"cost"`
	Tokens ocTokens `json:"tokens"`
}

// ocTokens is the AssistantMessage .tokens blob (serve 1.18.19 /doc:
// {total?, input, output, reasoning, cache:{read, write}} — total is
// optional, the rest required).
type ocTokens struct {
	Total     float64 `json:"total"`
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
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
	// Multiple marks a checkbox page (several picks allowed); absent
	// means radio (exactly one). Tolerant parse: older serves simply
	// omit it.
	Multiple bool `json:"multiple"`
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
	// cfg is the backend's brain.json (never nil — the live backend
	// substitutes config.Default()). It feeds roster naming only:
	// cfg.Roles[<rolekey>].NamePrefix replaces the stock Greek base when
	// set. RoleConfig.Model is intentionally NOT applied here:
	// per-sub-agent model dispatch is decided by opencode (the agent that
	// spawned the child session), theboringoffice cannot override it — it is
	// documented as best-effort in internal/config.
	cfg              *config.Config
	employees        map[string]state.Employee // child session id -> employee
	tasks            map[string]state.BoardTask
	nameCounts       map[state.EmployeeRole]int // role -> last issued number
	seatSeq          int
	lastWorkingAt    map[string]int64    // child id -> last "working" emit time (ms)
	returned         map[string]bool     // child sessions that already returned
	fired            map[string]bool     // dedupe delete-event vs delete-call
	pendingPerms     map[string]permHold // permission request id -> hold
	pendingQuestions map[string]permHold // question request id -> hold (mirror of pendingPerms; question replies free the PARKED turn)
	diffSeen         map[string]bool     // sessionID|path -> already surfaced
	reasoningParts   map[string]bool     // part id -> a message.part.updated said "reasoning"
	reasoningAccum   map[string]string   // part id -> delta-accumulated transcript so far
	deltaBuffer      map[string]string   // part id -> deltas seen BEFORE the part was classified
	textParts        map[string]bool     // part id -> message.part.updated classified a STREAMING text part (primary/concierge)
	textPartMsg      map[string]string   // text part id -> its messageID (deltas key the boss/office bubble)
	textAccum        map[string]string   // messageID -> delta-accumulated answer text so far
	textStart        map[string]int64    // messageID -> stream start (ms; Msg.At for every update of the bubble)
	textSess         map[string]string   // messageID -> owning sessionID (primary vs concierge routing)
	// usageSeen — per-assistant-message usage counters already emitted
	// (messageID -> last totals). Lets mapUsage ship DELTAS: repeated
	// message.updated frames for the same id re-report absolute counters,
	// and only the growth becomes an EvUsage.
	usageSeen map[string]ocUsageLast
	// conciergeID — the office concierge's session id, set by the live
	// backend's registerConcierge ("" until the first SendConcierge creates
	// the session lazily). The concierge is a PSEUDO-DESK: it sits in
	// employees (named "concierge") so actorFor attributes its tool parts
	// and its CHILDREN hire via the normal parentID chain, but it is never
	// EvHire'd, never gets a board task, and its chat lane is EvChatOffice
	// (Msg From/Kind "office") — never EvChatBoss (see mapTextDelta and
	// the backend's maybeOfficeCompleted).
	conciergeID string
	// pseudoCTO — the boot pseudo-CTO latch: true while the idle
	// "theboringcto" stand-in (pseudoCTOEmployee, cto.go) is the only CTO
	// on the floor. liveBackend.Start seats him (demo parity); an
	// architecture child session.created drops him (EvFire ahead of the
	// real hire, so the reducer frees seat "cto" first); the fire paths
	// re-seat him once the LAST real CTO is gone. The INVERSE of the
	// concierge pseudo-desk: he is EvHire'd but NEVER keyed into
	// ctx.employees, so every session-id mapper stays blind to him.
	pseudoCTO bool
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

func newNormCtx(cfg *config.Config) *normCtx {
	if cfg == nil {
		cfg = config.Default()
	}
	return &normCtx{
		cfg:              cfg,
		employees:        make(map[string]state.Employee),
		tasks:            make(map[string]state.BoardTask),
		nameCounts:       make(map[state.EmployeeRole]int),
		lastWorkingAt:    make(map[string]int64),
		returned:         make(map[string]bool),
		fired:            make(map[string]bool),
		pendingPerms:     make(map[string]permHold),
		pendingQuestions: make(map[string]permHold),
		diffSeen:         make(map[string]bool),
		reasoningParts:   make(map[string]bool),
		reasoningAccum:   make(map[string]string),
		deltaBuffer:      make(map[string]string),
		textParts:        make(map[string]bool),
		textPartMsg:      make(map[string]string),
		textAccum:        make(map[string]string),
		textStart:        make(map[string]int64),
		textSess:         make(map[string]string),
		usageSeen:        make(map[string]ocUsageLast),
	}
}

// registerConcierge pins the office concierge session as a pseudo-desk. No
// hire/dispatch event is ever emitted for it (the floor keeps ONE boss), but
// the employees row makes actorFor attribute its tool parts as "concierge"
// inline office lines, and its session becomes a second dispatch root:
// children (parentID == conciergeID) hire exactly like primary's children.
func (ctx *normCtx) registerConcierge(sessionID string) {
	ctx.conciergeID = sessionID
	ctx.employees[sessionID] = state.Employee{
		ID: sessionID, Name: "concierge", Role: state.RoleDeveloper,
		Seat: "concierge", Sprite: state.SpriteAtDesk,
	}
}

// dismissConcierge un-seats the pseudo-desk (ResetPrimary/NewOffice respawn):
// the old session simply stops being the concierge server-side — it is never
// deleted (mirrors the primary's respawn semantics); the next SendConcierge
// lazily registers a fresh one.
func (ctx *normCtx) dismissConcierge() {
	if ctx.conciergeID != "" {
		delete(ctx.employees, ctx.conciergeID)
	}
	ctx.conciergeID = ""
}

// seatPseudoCTO latches the boot pseudo-CTO and returns his hire event
// (nil when already seated — the exactly-once guard every path funnels
// through). In-order emission does the rest: whoever re-seats him fires
// the departing real CTO FIRST so the reducer frees seat "cto" before
// this EvHire's AssignSeat puts the pseudo back in it.
func (ctx *normCtx) seatPseudoCTO() []state.Event {
	if ctx.pseudoCTO {
		return nil
	}
	ctx.pseudoCTO = true
	return []state.Event{{Kind: state.EvHire, Employee: pseudoCTOEmployee()}}
}

// dropPseudoCTO unlatches the pseudo: the architecture-child hire path
// emits his EvFire ahead of the real theboringcto-N hire (one floor, one
// CTO at a time — no double, no floor-0 overflow).
func (ctx *normCtx) dropPseudoCTO() { ctx.pseudoCTO = false }

// liveCTOs counts the REAL (session-keyed) CTO rows still on the floor —
// a fired row counts as gone even while it lingers in ctx.employees
// (session.deleted marks, never deletes). The pseudo never lands in
// ctx.employees, so he neither swells nor blocks this count: the guard
// that re-seats him asks "did we just lose the LAST real one?", which is
// what keeps two overlapping architecture children from double-seating.
func (ctx *normCtx) liveCTOs() int {
	n := 0
	for id, emp := range ctx.employees {
		if emp.Role == state.RoleCTO && !ctx.fired[id] {
			n++
		}
	}
	return n
}

// Greek-desk naming per role (state canon). brain.json
// (cfg.Roles[rolekey].NamePrefix) overrides the seed when set — role keys
// are the role's string value ("developer", "scout", "reviewer", "runner",
// "hr", "cto"). Stocks match the historic roster (tekton/skopos/dikastes/
// hemerodromos/mnemosyne/theboringcto), so a default brain.json renames
// nothing.
func (ctx *normCtx) nameBase(role state.EmployeeRole) string {
	if rc, ok := ctx.cfg.Roles[string(role)]; ok && rc.NamePrefix != "" {
		return rc.NamePrefix
	}
	return nameBase(role)
}

// nameBase is the built-in Greek base when no config override applies.
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
	case state.RoleCTO:
		return "theboringcto"
	default:
		return "tekton"
	}
}

// roleFromSession guesses a role from the child session's title (plus an
// optional agent hint). Machine-generated titles, not member language —
// plain substring rules are the right tool here. Architecture briefs land
// FIRST: the CTO owns them (state.IsArchitectureBrief is the ONE matcher;
// it already covers "review", so a review-titled child seats at his exec
// suite rather than the dikastes cabin).
func roleFromSession(title, agentHint string) state.EmployeeRole {
	hay := strings.ToLower(agentHint + " " + title)
	if state.IsArchitectureBrief(hay) {
		return state.RoleCTO
	}
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
		Name:   ctx.nameBase(role) + "-" + itoa(n),
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
	// The concierge's reasoning is suppressed (noise): the office lane shows
	// its answers and tool runs, not its chain of thought. NOT registering
	// the part also keeps it out of reasoningParts, so its deltas fall into
	// the (capped) buffer without ever surfacing a thought.
	if part.SessionID == ctx.conciergeID {
		return nil
	}
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
	// Concierge reasoning deltas never surface (thought suppression): no
	// thought, and no buffering either (nothing on the concierge session
	// will ever classify its reasoning parts).
	if d.SessionID == ctx.conciergeID {
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

// mapTextPart registers a STREAMING text part of the PRIMARY or CONCIERGE
// session (message.part.updated, part.type=="text", time.end==0): the
// final-answer channel opens (boss bubble / office bubble, keyed by the
// part's session — see mapTextDelta). Only those two register — children
// keep their part.updated text frames on the throttled-working path. Any
// deltas that arrived before classification (deltaBuffer) seed the
// accumulator. Emits nothing itself; the EvChatBoss/EvChatOffice stream
// rides the delta frames. A completed part frame (time.end!=0) just
// unregisters — the pinned text is emitted by the message.updated
// completion pin, never from here.
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
		ctx.textSess[part.MessageID] = part.SessionID
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
// into a GROWING boss/office bubble: the SAME identity the eventual
// completion pin reuses, Pending:true, accumulated-so-far text. One bubble
// identity spans stream + completion; the UI replaces in place. A concierge
// session's stream takes the office lane — EvChatOffice, Msg.ID
// "office-"+messageID, From/Kind "office" — so exactly one of
// EvChatOffice/EvChatBoss ever fires per message (the identity prefix is
// disjoint). Emission rate is coalesced by the backend (chatSlots gate,
// 150ms).
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
		ctx.textSess[d.MessageID] = d.SessionID
	}
	accumulated := capBossText(ctx.textAccum[msgID] + d.Delta)
	ctx.textAccum[msgID] = accumulated
	at := ctx.textStart[msgID]
	if ctx.conciergeID != "" && d.SessionID == ctx.conciergeID {
		return []state.Event{{
			Kind: state.EvChatOffice,
			Msg: state.ChatMsg{
				ID:      "office-" + msgID,
				From:    "office",
				Kind:    "office",
				Text:    accumulated,
				At:      at,
				Pending: true,
			},
		}}
	}
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

// interruptedStreamEvents flushes every open text stream as a final
// Pending=false bubble carrying the accumulated text plus an interruption
// note (abort/error/stop), then frees ALL stream state. Only the primary and
// concierge sessions ever register text streams; each message's owning
// session (textSess) decides the lane — a concierge stream flushes as
// EvChatOffice ("office-"+messageID), a boss stream as EvChatBoss
// ("bossmsg-"+messageID). Deltas that stream cleanly into a completion are
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
		if ctx.conciergeID != "" && ctx.textSess[msgID] == ctx.conciergeID {
			evs = append(evs, state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
				ID:      "office-" + msgID,
				From:    "office",
				Kind:    "office",
				Text:    ctx.textAccum[msgID] + "\n" + note,
				At:      ctx.textStart[msgID],
				Pending: false,
			}})
			continue
		}
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
	for msgID := range ctx.textSess {
		delete(ctx.textSess, msgID)
	}
	return evs
}

// unregisterTextStream stops the delta stream for one message (completion
// pin): its parts go, the accumulator, start stamp and session tag are
// freed. The pinned completion bubble text supersedes whatever the deltas
// accumulated. Works identically for boss and concierge messages (the pin
// paths re-request each lane by ID prefix).
func unregisterTextStream(ctx *normCtx, messageID string) {
	for partID, msgID := range ctx.textPartMsg {
		if msgID == messageID {
			delete(ctx.textParts, partID)
			delete(ctx.textPartMsg, partID)
		}
	}
	delete(ctx.textAccum, messageID)
	delete(ctx.textStart, messageID)
	delete(ctx.textSess, messageID)
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
// rides in Text; options collapse into ToolSummary ("a | b | c") — that
// flattening is KEPT verbatim (history/list views and tests lean on it) and
// the STRUCTURED pages additionally ride in Questions (one QuestionItem per
// asked question with its header/options/multiple), so the popover can
// render real radio/checkbox/textarea kinds.
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
	var items []state.QuestionItem
	for _, q := range p.Questions {
		if s := strings.TrimSpace(q.Question); s != "" {
			texts = append(texts, s)
			// Structured page: whitespace-only questions drop here too
			// (never a blank popover page); a request with NO real
			// question leaves Questions nil.
			item := state.QuestionItem{
				Question: s,
				Header:   strings.TrimSpace(q.Header),
				Multiple: q.Multiple,
			}
			for _, opt := range q.Options {
				if s := strings.TrimSpace(opt.Label); s != "" {
					item.Options = append(item.Options, state.QuestionOption{
						Label:       s,
						Description: strings.TrimSpace(opt.Description),
					})
				}
			}
			items = append(items, item)
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
	ctx.pendingQuestions[id] = permHold{
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
		Questions:    items,
	}}
}

// mapQuestionResolved: question.replied / question.rejected clear the hold
// and emit a "resolved" EvQuestion on the same id.
func mapQuestionResolved(p ocQuestionReq, ctx *normCtx, primaryID string) []state.Event {
	id := p.RequestID
	if id == "" {
		id = p.ID
	}
	hold, ok := ctx.pendingQuestions[id]
	if !ok {
		return nil
	}
	delete(ctx.pendingQuestions, id)
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

// ocUsageLast remembers the counters already emitted for one assistant
// message so repeated message.updated frames ship only the growth.
type ocUsageLast struct {
	in, out        int64
	cacheR, cacheW int64
	cost           float64
}

// mapUsage lifts the REAL usage counters off one assistant message.updated
// (AssistantMessage.tokens/.cost — required on the wire since opencode
// serve 1.18.19, verified against GET /doc) and emits their GROWTH since
// the same message's last frame as state.EvUsage (CallID = messageID; the
// reducer accumulates with +=). Rules:
//
//   - role "assistant" only (user messages carry no counters);
//   - office-owned sessions only: the primary ("boss"), the concierge
//     pseudo-desk and hired children (both in ctx.employees) — a shared
//     serve's foreign sessions never leak into the member's tally;
//   - TokensIn = wire input; TokensOut = wire output + reasoning. Cache
//     read/write stay OUT of the headline counts (provider-billing
//     overlap) and ride ALONGSIDE as TokensCacheRead/TokensCacheWrite —
//     informational only (the $ figure already prices cache; the member
//     just gets to verify prompt caching is actually happening);
//   - an all-zero delta (the typical first frame, counters still 0)
//     emits nothing.
func mapUsage(info ocMessage, ctx *normCtx, primaryID string) []state.Event {
	if info.Role != "assistant" || info.ID == "" {
		return nil
	}
	if info.SessionID != primaryID {
		if _, ok := ctx.employees[info.SessionID]; !ok {
			return nil
		}
	}
	cur := ocUsageLast{
		in:     int64(info.Tokens.Input),
		out:    int64(info.Tokens.Output) + int64(info.Tokens.Reasoning),
		cacheR: int64(info.Tokens.Cache.Read),
		cacheW: int64(info.Tokens.Cache.Write),
		cost:   info.Cost,
	}
	prev := ctx.usageSeen[info.ID]
	d := ocUsageLast{
		in:     cur.in - prev.in,
		out:    cur.out - prev.out,
		cacheR: cur.cacheR - prev.cacheR,
		cacheW: cur.cacheW - prev.cacheW,
		cost:   cur.cost - prev.cost,
	}
	if d.in == 0 && d.out == 0 && d.cacheR == 0 && d.cacheW == 0 && d.cost == 0 {
		return nil
	}
	ctx.usageSeen[info.ID] = cur
	return []state.Event{{
		Kind:             state.EvUsage,
		CallID:           info.ID,
		TokensIn:         d.in,
		TokensOut:        d.out,
		CostUSD:          d.cost,
		TokensCacheRead:  d.cacheR,
		TokensCacheWrite: d.cacheW,
	}}
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
//	message.updated (assistant, office-owned) -> usage (cost/token DELTA
//		per message id, real counters off the wire — see mapUsage)
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
//
// Office concierge additions (EvChatOffice lane; "office-"+messageID ids).
// The concierge is a second dispatch root, NEVER a second boss:
//
//	session.created (parentID = concierge)   -> hire + dispatch (same shape
//		as the primary's children)
//	message.part.updated (text, concierge, streaming) -> register office stream
//	message.part.delta (text part, concierge)  -> chat-office, growing bubble
//	message.part.updated (reasoning, concierge) -> [] (thought suppressed as noise)
//	message.updated (concierge, completed)   -> [] here (backend fetches -> chat-office)
//	session.error (concierge)                -> chat-office error line
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
		// Two dispatch roots: the primary ("boss") and the office concierge
		// (ctx.conciergeID, "" until first use). Either root's children
		// hire via the identical shape; the concierge session itself is a
		// root session, so its own creation never lands here (and even the
		// registration carries no hire event — see registerConcierge).
		if info.ParentID != primaryID && (ctx.conciergeID == "" || info.ParentID != ctx.conciergeID) {
			return nil
		}
		if _, ok := ctx.employees[info.ID]; ok {
			return nil
		}
		emp := ctx.issueEmployee(info)
		task := ctx.issueTask(info, emp.Name, now)
		var evs []state.Event
		// CTO swap: an architecture brief's child takes over the exec
		// suite. Fire the boot pseudo-CTO FIRST — events emit in order, so
		// the reducer removes him (freeing seat "cto") before the real
		// theboringcto-N's EvHire re-seats it via AssignSeat: no double
		// CTO, no floor-0 overflow. Exactly once per boot-pseudo:
		// dropPseudoCTO clears the latch, so the NEXT architecture child
		// hires plain while a real CTO already holds the chair.
		if emp.Role == state.RoleCTO && ctx.pseudoCTO {
			ctx.dropPseudoCTO()
			evs = append(evs, state.Event{Kind: state.EvFire, EmployeeID: ctoName})
		}
		return append(evs,
			state.Event{Kind: state.EvHire, Employee: emp},
			state.Event{Kind: state.EvDispatch, Task: task, EmployeeID: emp.ID},
		)

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
			// its deltas grow the "bossmsg-"+messageID chat bubble;
			// the CONCIERGE's final answer registers identically, its
			// deltas growing the EvChatOffice "office-"+messageID bubble
			// instead (mapTextDelta keys the lane by session). Children
			// stay on the old working-pulse path below.
			if part.SessionID == primaryID || part.SessionID == ctx.conciergeID {
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
		// Every assistant message.updated also re-reports its usage
		// counters — the delta rides along whatever this frame already
		// emitted (children's working pulse included).
		usage := mapUsage(info, ctx, primaryID)
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
			return usage
		}
		emp, ok := ctx.employees[info.SessionID]
		if !ok || info.Role != "assistant" || ctx.returned[info.SessionID] {
			return usage
		}
		return append(usage, ctx.throttledWorking(emp.ID, ctx.tasks[info.SessionID].ID, now, false)...)

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
		emp, ok := ctx.employees[p.Info.ID]
		if !ok || ctx.fired[p.Info.ID] {
			return nil
		}
		ctx.fired[p.Info.ID] = true
		evs := []state.Event{{Kind: state.EvFire, EmployeeID: p.Info.ID}}
		// CTO re-seat (mirror of deleteChild in opencode.go): the last
		// real CTO just left, so the idle pseudo takes his exec suite
		// back — AFTER the EvFire, so the reducer frees the chair before
		// the hire's AssignSeat. Guards: latch still clear + no OTHER
		// un-fired CTO row (overlapping architecture children re-seat
		// exactly once, on the final departure).
		if emp.Role == state.RoleCTO && !ctx.pseudoCTO && ctx.liveCTOs() == 0 {
			evs = append(evs, ctx.seatPseudoCTO()...)
		}
		return evs

	case "session.error":
		var p ocSessionErrorProps
		if json.Unmarshal(raw.Properties, &p) != nil {
			return nil
		}
		if p.SessionID != primaryID {
			// The concierge lane: a concierge session error flushes its open
			// office streams and surfaces an office error line (never
			// EvChatBoss — one lane per session).
			if ctx.conciergeID == "" || p.SessionID != ctx.conciergeID {
				return nil
			}
			message := "unknown error"
			if p.Error != nil && p.Error.Data.Message != "" {
				message = p.Error.Data.Message
			}
			evs := interruptedStreamEvents(ctx, "[theboringoffice] stream interrupted")
			return append(evs, state.Event{
				Kind: state.EvChatOffice,
				Msg: state.ChatMsg{
					ID:      "office-error-" + itoa64(now),
					From:    "office",
					Kind:    "office",
					Text:    "[theboringoffice] office error: " + shortTitle(message, 120),
					At:      now,
					Pending: false,
				},
			})
		}
		message := "unknown error"
		if p.Error != nil && p.Error.Data.Message != "" {
			message = p.Error.Data.Message
		}
		// Any boss text still streaming dies with the run: flush whatever
		// accumulated as a final Pending=false bubble (update-in-place on
		// the same ID), then the error line.
		evs := interruptedStreamEvents(ctx, "[theboringoffice] stream interrupted")
		return append(evs, state.Event{
			Kind: state.EvChatBoss,
			Msg: state.ChatMsg{
				ID:      "boss-error-" + itoa64(now),
				From:    "boss",
				Text:    "[theboringoffice] boss error: " + shortTitle(message, 120),
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
