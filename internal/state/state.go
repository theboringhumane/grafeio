// Package state — the ONE contract backend and UI both speak.
// Port of node-legacy/src/state.ts. UI never calls SDK/HTTP directly;
// backend never renders.
package state

import "strings"

// Attachment — one chat-input attachment riding the outgoing boss prompt
// as an opencode prompt_async file part ({type:"file", mime, filename,
// url} — url carries the base64 data URL). The chat panel builds them
// (a clipboard-image paste lands in a temp PNG; the @ picker points at a
// repo file); the backend reads Path's bytes at SEND time. The backend
// seam is OPTIONAL: state.Backend.Send stays plain text, and backends
// that take files implement the AttachmentSender seam the app
// type-asserts (the same pattern model.go uses for teamBackend).
type Attachment struct {
	Name string `json:"name"`           // display name AND the wire "filename"
	Mime string `json:"mime,omitempty"` // resolved at attach time; senders re-sniff when empty
	Path string `json:"path"`           // the file the sender base64s into the data URL
	// Temp — non-empty when Path lives in a panel-created temp dir
	// (os.MkdirTemp "grafeio-paste-*"): the app removes the dir once the
	// send resolves (best effort — a queued send must find the file at
	// flush time, so removal never happens at enqueue).
	Temp string `json:"-"`
}

// AttachMetaSep splits the user-bubble Meta attachment carrier:
// "att" ␟ name ␟ name… (unit separator — names may contain spaces).
// Written by backends on the EvChatUser echo, read by the chat panel
// renderer for the dim " · 📎 N" suffix. Same separator trick as the
// diff Meta carrier (diffMetaSep in panels/chat.go).
const AttachMetaSep = "\x1f"

// AttachMetaPrefix tags a user bubble's Meta as the attachment carrier
// (every other Meta user — think/tool/question/diff/office-error — owns
// its own Kind or From, so the prefix cannot collide).
const AttachMetaPrefix = "att"

// AttachMeta builds the ChatMsg.Meta carrier from attachment names;
// "" when there are none (a plain user turn keeps an empty Meta).
func AttachMeta(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return AttachMetaPrefix + AttachMetaSep + strings.Join(names, AttachMetaSep)
}

// ParseAttachMeta decodes the carrier back into names; ok is false when
// Meta isn't an attachment list at all.
func ParseAttachMeta(meta string) (names []string, ok bool) {
	if !strings.HasPrefix(meta, AttachMetaPrefix+AttachMetaSep) {
		return nil, false
	}
	rest := strings.TrimPrefix(meta, AttachMetaPrefix+AttachMetaSep)
	if rest == "" {
		return nil, false
	}
	return strings.Split(rest, AttachMetaSep), true
}

// SpriteState — where an employee is / what they're doing (drives glyphs+walkers).
type SpriteState string

const (
	SpriteAtDesk    SpriteState = "at-desk"
	SpriteWorking   SpriteState = "working"
	SpriteToManager SpriteState = "to-manager"
	SpriteMeeting   SpriteState = "meeting"
	SpriteToDesk    SpriteState = "to-desk"
	SpriteToCoffee  SpriteState = "to-coffee"
	SpriteCoffee    SpriteState = "coffee"
	SpriteAtMailbox SpriteState = "at-mailbox"
)

// EmployeeRole — the office seat.
type EmployeeRole string

const (
	RoleManager   EmployeeRole = "manager"
	RoleHR        EmployeeRole = "hr"
	RoleDeveloper EmployeeRole = "developer"
	RoleScout     EmployeeRole = "scout"
	RoleReviewer  EmployeeRole = "reviewer"
	RoleRunner    EmployeeRole = "runner"
)

type Employee struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Role   EmployeeRole `json:"role"`
	Seat   string       `json:"seat"`
	Sprite SpriteState  `json:"sprite"`
	Task   string       `json:"task,omitempty"`
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in-progress"
	TaskDone       TaskStatus = "done"
)

type BoardTask struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status TaskStatus `json:"status"`
	Owner  string     `json:"owner,omitempty"`
	At     int64      `json:"at"`
}

type MailKind string

const (
	MailBrief  MailKind = "brief"
	MailReturn MailKind = "return"
	MailNotice MailKind = "notice"
	MailUser   MailKind = "user"
)

type MailItem struct {
	ID      string   `json:"id"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	At      int64    `json:"at"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	Kind    MailKind `json:"kind"`
}

type ChatMsg struct {
	ID      string `json:"id"`
	From    string `json:"from"` // "user" | "boss" | "office"
	Text    string `json:"text"`
	At      int64  `json:"at"`
	Pending bool   `json:"pending,omitempty"`
	// Kind — "user" | "boss" | "think" | "tool" | "office". Empty "" keeps
	// existing literals valid (classic user/boss chat).
	//
	// "office" (the concierge) follows the EXACT same streaming contract as
	// "boss": EvChatOffice carries Msg{ID:"office-"+<messageID>, From:
	// "office", Kind:"office", Pending:true, Text:accumulated-so-far} —
	// repeated updates of the SAME Msg.ID grow the one bubble; the
	// completion pin re-emits that ID with Pending:false and the pinned
	// full text. UI: update-in-place by Msg.ID, never append a streaming
	// update as a new bubble.
	//
	// "boss" STREAMING contract (live text-part deltas): while the boss's
	// final answer streams in, EvChatBoss carries Msg{ID:"bossmsg-"+<messageID>,
	// Kind:"boss", Pending:true, Text:accumulated-so-far} — repeated updates
	// of the SAME Msg.ID grow the one bubble. The completion pin re-emits the
	// same ID with Pending:false and the pinned full text; the UI replaces
	// the streaming bubble with it. A stream that dies before completion
	// (abort/error/stop) ends Pending:false with a "[grafeio] stream
	// interrupted" note appended. UI: update-in-place by Msg.ID, never
	// append a streaming update as a new bubble.
	Kind string `json:"kind,omitempty"`
	// Meta — short decoration, e.g. "read · src/main.go". Empty for plain chat.
	Meta string `json:"meta,omitempty"`
}

// SpeechBubble — ambient office chatter balloon, expires after ttl ticks.
type SpeechBubble struct {
	ID         string `json:"id"`
	EmployeeID string `json:"employeeId"`
	Text       string `json:"text"`
	UntilTick  int    `json:"untilTick"`
}

type Mode string

const (
	ModeLive Mode = "live"
	ModeDemo Mode = "demo"
)

type OfficeState struct {
	Employees  []Employee     `json:"employees"`
	Tasks      []BoardTask    `json:"tasks"`
	Mails      []MailItem     `json:"mails"`
	Chat       []ChatMsg      `json:"chat"`
	Bubbles    []SpeechBubble `json:"bubbles"`
	Mode       Mode           `json:"mode"`
	StatusLine string         `json:"statusLine"`
	Tick       int            `json:"tick"`
	// BossThinking — the primary session is between a prompt and its reply,
	// with a live EvThought open. UI dims the desk glyph / shows a spinner.
	BossThinking bool `json:"bossThinking,omitempty"`
	// BossDelegating — a boss typing placeholder is outstanding but the
	// primary session has been quiet (no stream/thought/primary-tool) for
	// >6 ticks while hired employees are visibly busy (working/to-manager/
	// meeting): the boss is MANAGING, not generating. The UI swaps the
	// noisy typing spinner for a settled "delegating" row and stamps the
	// floor nameplate "[delegat]". Clears the moment any boss-side
	// activity lands.
	BossDelegating bool `json:"bossDelegating,omitempty"`
	// TokensIn/TokensOut/CostUSD — the conversation's REAL usage totals,
	// accumulated reducer-side from EvUsage deltas the backend lifts
	// verbatim out of opencode assistant messages (AssistantMessage
	// .tokens/.cost — required on the wire since serve 1.18.19, verified
	// against GET /doc). Zero while nothing real has been reported; the
	// status bar hides its usage segment in that case rather than
	// estimate. TokensIn counts wire `input`; TokensOut counts wire
	// `output`+`reasoning` (cache read/write stay out of the headline —
	// opencode's own CostUSD is the authoritative bill). Additive state:
	// a new office starts at 0 and the session snapshot never threads
	// them (Snapshot keeps chat only).
	TokensIn  int64   `json:"tokensIn,omitempty"`
	TokensOut int64   `json:"tokensOut,omitempty"`
	CostUSD   float64 `json:"costUsd,omitempty"`
}

// EventKind — Go has no tagged unions; one Event struct with a Kind + optional fields.
type EventKind string

const (
	EvHire      EventKind = "hire"
	EvFire      EventKind = "fire"
	EvDispatch  EventKind = "dispatch"
	EvWorking   EventKind = "working"
	EvReturned  EventKind = "returned"
	EvIdleDrift EventKind = "idle-drift"
	EvBlocked   EventKind = "blocked"
	EvTask      EventKind = "task"
	EvMail      EventKind = "mail"
	EvChatUser  EventKind = "chat-user"
	EvChatBoss  EventKind = "chat-boss"
	// EvChatOffice — the office concierge's chat lane (ADDITIVE). When the
	// boss's turn is occupied the app routes chat through the concierge,
	// whose replies ride this kind instead of EvChatBoss: exactly ONE of
	// the two fires per assistant message. Msg carries From "office",
	// Kind "office", ID "office-"+messageID; the streaming contract
	// (Pending growth + completion pin, 150ms-coalesced deltas) mirrors
	// the boss stream (see ChatMsg.Kind).
	EvChatOffice EventKind = "chat-office"
	EvThought    EventKind = "thought"
	EvTool       EventKind = "tool"
	EvBubble     EventKind = "bubble"
	EvStatus     EventKind = "status"
	EvTick       EventKind = "tick"
	EvPermission EventKind = "permission"
	EvQuestion   EventKind = "question"
	EvFileDiff   EventKind = "diff"
	// EvUsage — a usage/cost delta lifted from one opencode assistant
	// message.updated (AssistantMessage.tokens/.cost). CallID carries the
	// messageID; TokensIn/TokensOut/CostUSD are the GROWTH since the last
	// frame for that same message (never an absolute re-report), so the
	// reducer simply accumulates. Emitted only for sessions the office
	// owns (primary, concierge pseudo-desk, hired children) and only when
	// the delta is non-zero.
	EvUsage EventKind = "usage"
)

// Event — the wire between backend and the tea.Model. Only fields relevant
// to Kind are populated.
type Event struct {
	Kind       EventKind `json:"kind"`
	Employee   Employee  `json:"employee,omitempty"`   // hire
	EmployeeID string    `json:"employeeId,omitempty"` // fire/dispatch/working/returned/idle/blocked/bubble
	Task       BoardTask `json:"task,omitempty"`       // dispatch/task + returned.TaskID via Task.ID
	TaskID     string    `json:"taskId,omitempty"`     // working/returned
	Mail       MailItem  `json:"mail,omitempty"`       // returned/mail
	Msg        ChatMsg   `json:"msg,omitempty"`        // chat-user/chat-boss/chat-office
	Text       string    `json:"text,omitempty"`       // status note / bubble text / thought text
	TTL        int       `json:"ttl,omitempty"`        // bubble
	// EmployeeName — human label for the actor. The backend fills it from
	// the employee registry ("boss" for the primary session, "tekton-1"
	// etc. for children) so the UI never has to resolve an ID back to a name.
	EmployeeName string `json:"employeeName,omitempty"` // thought/tool
	// Tool fields (EvTool).
	ToolName    string `json:"toolName,omitempty"`
	ToolSummary string `json:"toolSummary,omitempty"` // e.g. "src/main.go" or "GRAFEIO_*, 12 hits"
	ToolState   string `json:"toolState,omitempty"`   // "running" | "done" | "error"
	CallID      string `json:"callId,omitempty"`      // part/call id for dedupe
	Done        bool   `json:"done,omitempty"`        // thought completion
	// Permission/question/diff fields (EvPermission/EvQuestion/EvFileDiff).
	// SessionID is the opencode session the event belongs to ("boss"-side
	// events carry the primary id); PermissionID/QuestionID are the wire
	// request ids (per…/que…) the UI hands back to AnswerPermission.
	PermissionID string `json:"permissionId,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	QuestionID   string `json:"questionId,omitempty"`
	DiffPath     string `json:"diffPath,omitempty"` // file path relative to the working dir
	DiffBody     string `json:"diffBody,omitempty"` // compact unified diff, capped by the backend
	DiffAdd      int    `json:"diffAdd,omitempty"`
	DiffDel      int    `json:"diffDel,omitempty"`
	// Usage fields (EvUsage): per-message DELTAS, not absolutes — the
	// backend remembers what each messageID already reported and sends
	// only the growth, so the app accumulates with += and repeated
	// message.updated frames can never double-count.
	TokensIn  int64   `json:"tokensIn,omitempty"`  // wire tokens.input growth
	TokensOut int64   `json:"tokensOut,omitempty"` // wire tokens.output+reasoning growth
	CostUSD   float64 `json:"costUsd,omitempty"`   // wire cost growth (USD, opencode-computed)
}

// Backend — one per run. Demo scripted, live via opencode serve + agentmemory.
type Backend interface {
	Mode() Mode
	// Start wires events; f MUST be safe to call from backend goroutines
	// (the app hands it tea.Program.Send).
	Start(emit func(Event)) error
	// Send pushes user chat to the boss. Plain text on purpose: chat-input
	// attachments ride an OPTIONAL second seam — SendWith(text, atts
	// []Attachment) — that the app type-asserts (see attachmentBackend in
	// internal/app/model.go, the same pattern as the team board seam).
	// Keeping them out of this interface means harness stubs (uishot,
	// headless) unchanged: they simply never attach files.
	Send(text string) error
	// AnswerPermission replies to a pending permission prompt. response is
	// "once" | "always" | "reject" (opencode serve permission.reply enum).
	AnswerPermission(permissionID, response string) error
	// AnswerQuestion replies to a pending question request (the boss used
	// the question tool; the agent loop is PARKED at question.asked until
	// the question API gets this reply — a normal chat prompt does NOT
	// answer it, which is the question-loop deadlock). answers is one
	// string per asked question, in order (ui picks one label or free-form
	// text per question); the backend wraps each in its own array for the
	// wire shape (QuestionAnswer = string[], payload answers = string[][]).
	AnswerQuestion(requestID string, answers []string) error
	// RejectQuestion declines a pending question request outright, freeing
	// the parked turn without an answer (opencode serve exposes a true
	// reject route: POST /question/{requestID}/reject, /doc 1.18.19).
	RejectQuestion(requestID string) error
	Stop() error
}

// ConciergeCapable — the office-concierge seam (ADDITIVE; deliberately NOT
// folded into Backend, same convention as the attachment/team/office-session/
// abort seams — harness stubs stay untouched, the app type-asserts it).
//
// The concierge keeps the member answered while the boss's turn is occupied:
// SendConcierge delivers chat to a lightweight side session ("grafeio
// concierge", lazily created on FIRST use on the live backend) that answers
// instantly — directly when the message is trivial, by dispatching its own
// developer sub-agents (tracked like the boss's children) when it is real
// work. Replies arrive as EvChatOffice bubbles (From "office"), never
// EvChatBoss. When the feature is off (config boss.concierge=false) a backend
// degrades open by treating the message as a normal boss Send. The live
// backend additionally emits the chat-user echo (same ownership as Send, so
// the app never echoes twice).
type ConciergeCapable interface {
	SendConcierge(text string) error
}

// SessionAborter — the /stop seam (ADDITIVE; deliberately NOT folded into
// Backend, same convention as the attachment/team/office-session seams —
// see internal/app sessions.go "backend seams (additive; type-asserted,
// never added to state.Backend)": harness stubs stay untouched). Backends
// that can cancel in-flight LLM turns implement it; the app type-asserts
// it when wiring /stop.
//
// AbortSessions cancels the primary ("boss") turn AND every live child
// session's turn — an opencode abort only ends its own session's run, so
// sub-agent work the boss fanned out must be called out session by
// session. Per-session failures are non-fatal: collected into the returned
// error (errors.Join), the sessions that DID abort still stop. The boss's
// outstanding typing placeholder closes with a stopped marker so the UI
// never shows a frozen "typing…" bubble for a dead turn (live backend),
// and the demo backend emits "[demo] abort ok".
type SessionAborter interface {
	AbortSessions() error
}
