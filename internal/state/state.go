// Package state — the ONE contract backend and UI both speak.
// Port of node-legacy/src/state.ts. UI never calls SDK/HTTP directly;
// backend never renders.
package state

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
	From    string `json:"from"` // "user" | "boss"
	Text    string `json:"text"`
	At      int64  `json:"at"`
	Pending bool   `json:"pending,omitempty"`
	// Kind — "user" | "boss" | "think" | "tool". Empty "" keeps existing
	// literals valid (classic user/boss chat).
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
}

// EventKind — Go has no tagged unions; one Event struct with a Kind + optional fields.
type EventKind string

const (
	EvHire       EventKind = "hire"
	EvFire       EventKind = "fire"
	EvDispatch   EventKind = "dispatch"
	EvWorking    EventKind = "working"
	EvReturned   EventKind = "returned"
	EvIdleDrift  EventKind = "idle-drift"
	EvBlocked    EventKind = "blocked"
	EvTask       EventKind = "task"
	EvMail       EventKind = "mail"
	EvChatUser   EventKind = "chat-user"
	EvChatBoss   EventKind = "chat-boss"
	EvThought    EventKind = "thought"
	EvTool       EventKind = "tool"
	EvBubble     EventKind = "bubble"
	EvStatus     EventKind = "status"
	EvTick       EventKind = "tick"
	EvPermission EventKind = "permission"
	EvQuestion   EventKind = "question"
	EvFileDiff   EventKind = "diff"
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
	Msg        ChatMsg   `json:"msg,omitempty"`        // chat-user/chat-boss
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
}

// Backend — one per run. Demo scripted, live via opencode serve + agentmemory.
type Backend interface {
	Mode() Mode
	// Start wires events; f MUST be safe to call from backend goroutines
	// (the app hands it tea.Program.Send).
	Start(emit func(Event)) error
	// Send pushes user chat to the boss.
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
