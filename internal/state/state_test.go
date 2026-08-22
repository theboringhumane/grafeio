package state

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// --- Role / sprite / status / kind / mode constants -------------------------

func TestRoleConstants(t *testing.T) {
	want := map[EmployeeRole]string{
		RoleManager:   "manager",
		RoleHR:        "hr",
		RoleDeveloper: "developer",
		RoleScout:     "scout",
		RoleReviewer:  "reviewer",
		RoleRunner:    "runner",
		RoleCTO:       "cto",
	}
	if len(want) != 7 {
		t.Fatalf("test covers %d roles; update when seats change", len(want))
	}
	for role, s := range want {
		if string(role) != s {
			t.Errorf("role = %q, want %q", string(role), s)
		}
	}
}

func TestSpriteStateConstants(t *testing.T) {
	want := map[SpriteState]string{
		SpriteAtDesk:    "at-desk",
		SpriteWorking:   "working",
		SpriteToManager: "to-manager",
		SpriteMeeting:   "meeting",
		SpriteToDesk:    "to-desk",
		SpriteToCoffee:  "to-coffee",
		SpriteCoffee:    "coffee",
		SpriteAtMailbox: "at-mailbox",
	}
	if len(want) != 8 {
		t.Fatalf("test covers %d sprites; update when states change", len(want))
	}
	for sprite, s := range want {
		if string(sprite) != s {
			t.Errorf("sprite = %q, want %q", string(sprite), s)
		}
	}
}

func TestTaskStatusConstants(t *testing.T) {
	want := map[TaskStatus]string{
		TaskPending:    "pending",
		TaskInProgress: "in-progress",
		TaskDone:       "done",
	}
	for status, s := range want {
		if string(status) != s {
			t.Errorf("task status = %q, want %q", string(status), s)
		}
	}
}

// The ONE architecture-brief matcher: "architect" | "design" | "review",
// case-insensitive; negatives stay developer work.
func TestIsArchitectureBrief(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		// positives — each match word, in the wild's mixed case
		{"Design the agentmemory board sync protocol", true},
		{"ARCHITECTURE RFC: event flow", true},
		{"architect the next office floor", true},
		{"Review the diff before merge", true},
		{"design review for the task board", true},
		{"designer chair shopping", true}, // substring contract: "design" trips
		{"ReView", true},
		// negatives — plain build/scout briefs never match
		{"Wire the SSE stream into the office reducer", false},
		{"Map the repo's event flow end to end", false},
		{"Draft the demo smoke script", false},
		{"ship floorshot", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsArchitectureBrief(c.title); got != c.want {
			t.Errorf("IsArchitectureBrief(%q) = %v, want %v", c.title, got, c.want)
		}
	}
}

func TestMailKindConstants(t *testing.T) {
	want := map[MailKind]string{
		MailBrief:  "brief",
		MailReturn: "return",
		MailNotice: "notice",
		MailUser:   "user",
	}
	for kind, s := range want {
		if string(kind) != s {
			t.Errorf("mail kind = %q, want %q", string(kind), s)
		}
	}
}

func TestModeConstants(t *testing.T) {
	if ModeLive != "live" {
		t.Errorf("ModeLive = %q, want %q", ModeLive, "live")
	}
	if ModeDemo != "demo" {
		t.Errorf("ModeDemo = %q, want %q", ModeDemo, "demo")
	}
}

func TestEventKindConstants(t *testing.T) {
	want := map[EventKind]string{
		EvHire:       "hire",
		EvFire:       "fire",
		EvDispatch:   "dispatch",
		EvWorking:    "working",
		EvReturned:   "returned",
		EvIdleDrift:  "idle-drift",
		EvBlocked:    "blocked",
		EvTask:       "task",
		EvMail:       "mail",
		EvChatUser:   "chat-user",
		EvChatBoss:   "chat-boss",
		EvChatOffice: "chat-office",
		EvThought:    "thought",
		EvTool:       "tool",
		EvBubble:     "bubble",
		EvStatus:     "status",
		EvTick:       "tick",
		EvPermission: "permission",
		EvQuestion:   "question",
		EvFileDiff:   "diff",
		EvUsage:      "usage",
	}
	if len(want) != 21 {
		t.Fatalf("test covers %d event kinds; update when kinds change", len(want))
	}
	for kind, s := range want {
		if string(kind) != s {
			t.Errorf("event kind = %q, want %q", string(kind), s)
		}
	}
}

// --- AttachMeta / ParseAttachMeta -------------------------------------------

func TestAttachMetaEmpty(t *testing.T) {
	if got := AttachMeta(nil); got != "" {
		t.Errorf("AttachMeta(nil) = %q, want empty", got)
	}
	if got := AttachMeta([]string{}); got != "" {
		t.Errorf("AttachMeta([]) = %q, want empty", got)
	}
}

func TestAttachMetaFormat(t *testing.T) {
	got := AttachMeta([]string{"a.png", "b file.txt"})
	want := AttachMetaPrefix + AttachMetaSep + "a.png" + AttachMetaSep + "b file.txt"
	if got != want {
		t.Errorf("AttachMeta = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, AttachMetaPrefix) {
		t.Errorf("AttachMeta missing prefix %q: %q", AttachMetaPrefix, got)
	}
}

func TestParseAttachMetaRoundTrip(t *testing.T) {
	// NB: names containing the separator don't round-trip by design (names
	// may contain spaces, not ␟) — keep them separator-free here.
	names := []string{"shot 1.png", "src/main.go", "report final.md"}
	got, ok := ParseAttachMeta(AttachMeta(names))
	if !ok {
		t.Fatalf("ParseAttachMeta(AttachMeta(names)) ok = false")
	}
	if len(got) != len(names) {
		t.Fatalf("round trip got %d names, want %d (%v)", len(got), len(names), got)
	}
	for i, n := range names {
		if got[i] != n {
			t.Errorf("round trip name[%d] = %q, want %q", i, got[i], n)
		}
	}
}

func TestParseAttachMetaRejects(t *testing.T) {
	cases := []struct {
		name string
		meta string
	}{
		{"empty", ""},
		{"plain text", "read · src/main.go"},
		{"prefix only, no separator", AttachMetaPrefix},
		{"prefix+sep but no names", AttachMetaPrefix + AttachMetaSep},
		{"wrong prefix", "diff" + AttachMetaSep + "a.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			names, ok := ParseAttachMeta(tc.meta)
			if ok {
				t.Errorf("ParseAttachMeta(%q) ok = true, names = %v; want ok = false", tc.meta, names)
			}
			if names != nil {
				t.Errorf("ParseAttachMeta(%q) names = %v, want nil", tc.meta, names)
			}
		})
	}
}

// --- Struct JSON contracts ---------------------------------------------------

// roundTrip marshals v and unmarshals back into a fresh T, comparing.
func roundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %T: %v (json: %s)", v, err, data)
	}
	return out
}

func TestEmployeeJSON(t *testing.T) {
	in := Employee{
		ID:     "tekton-1",
		Name:   "Tekton",
		Role:   RoleDeveloper,
		Seat:   "dev-1",
		Sprite: SpriteWorking,
		Task:   "fix the thing",
	}
	out := roundTrip(t, in)
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}

	// Task is omitempty: an idle employee carries no "task" key.
	data, _ := json.Marshal(Employee{ID: "x", Name: "X", Role: RoleScout, Seat: "scout-1", Sprite: SpriteAtDesk})
	if strings.Contains(string(data), `"task"`) {
		t.Errorf("idle Employee JSON carries task key: %s", data)
	}
	for _, key := range []string{`"id"`, `"name"`, `"role"`, `"seat"`, `"sprite"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("Employee JSON missing key %s: %s", key, data)
		}
	}
}

func TestChatMsgJSON(t *testing.T) {
	in := ChatMsg{
		ID:      "bossmsg-1",
		From:    "boss",
		Text:    "hello member",
		At:      1720000000,
		Pending: true,
		Kind:    "boss",
		Meta:    "read · src/main.go",
	}
	out := roundTrip(t, in)
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}

	// Pending/Kind/Meta are omitempty: a classic plain user turn stays minimal.
	data, _ := json.Marshal(ChatMsg{ID: "u-1", From: "user", Text: "hi", At: 1})
	for _, key := range []string{`"pending"`, `"kind"`, `"meta"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("plain ChatMsg JSON carries %s: %s", key, data)
		}
	}
}

func TestQuestionOptionJSON(t *testing.T) {
	in := QuestionOption{Label: "Yes, ship it", Description: "pushes the release"}
	out := roundTrip(t, in)
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}

	data, _ := json.Marshal(QuestionOption{Label: "No"})
	if strings.Contains(string(data), `"description"`) {
		t.Errorf("empty Description should be omitted: %s", data)
	}
}

func TestQuestionItemJSON(t *testing.T) {
	in := QuestionItem{
		Question: "Which flavor?",
		Header:   "deploy",
		Options: []QuestionOption{
			{Label: "tar.gz"},
			{Label: "zip", Description: "windows style"},
		},
		Multiple: true,
	}
	out := roundTrip(t, in)
	if out.Question != in.Question || out.Header != in.Header || out.Multiple != in.Multiple {
		t.Errorf("round trip scalars = %+v, want %+v", out, in)
	}
	if len(out.Options) != 2 || out.Options[0] != in.Options[0] || out.Options[1] != in.Options[1] {
		t.Errorf("round trip options = %+v, want %+v", out.Options, in.Options)
	}

	// Free-text page: no options, no header, no multiple.
	data, _ := json.Marshal(QuestionItem{Question: "Why?"})
	for _, key := range []string{`"header"`, `"options"`, `"multiple"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("free-text QuestionItem JSON carries %s: %s", key, data)
		}
	}
}

func TestMCPServerJSON(t *testing.T) {
	in := MCPServer{Name: "agentmemory", Status: "connected", Detail: "12 tools"}
	out := roundTrip(t, in)
	if out != in {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}

	data, _ := json.Marshal(MCPServer{Name: "x", Status: "unknown"})
	if strings.Contains(string(data), `"detail"`) {
		t.Errorf("empty Detail should be omitted: %s", data)
	}
	if !strings.Contains(string(data), `"name"`) || !strings.Contains(string(data), `"status"`) {
		t.Errorf("MCPServer JSON missing name/status: %s", data)
	}
}

func TestAttachmentJSON(t *testing.T) {
	in := Attachment{Name: "paste.png", Mime: "image/png", Path: "/tmp/theboringoffice-paste-1/p.png", Temp: "/tmp/theboringoffice-paste-1"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "theboringoffice-paste-1\"") && strings.Contains(string(data), `"Temp"`) {
		t.Errorf("Temp must not serialize (json:\"-\"): %s", data)
	}
	out := roundTrip(t, in)
	if out.Temp != "" {
		t.Errorf("Temp leaked through JSON round trip: %q", out.Temp)
	}
	if out.Name != in.Name || out.Mime != in.Mime || out.Path != in.Path {
		t.Errorf("round trip = %+v, want Name/Mime/Path of %+v", out, in)
	}

	// Mime omitempty.
	data, _ = json.Marshal(Attachment{Name: "f", Path: "/f"})
	if strings.Contains(string(data), `"mime"`) {
		t.Errorf("empty Mime should be omitted: %s", data)
	}
}

func TestOfficeStateJSON(t *testing.T) {
	in := OfficeState{
		Employees:      []Employee{{ID: "boss", Name: "Boss", Role: RoleManager, Seat: "manager", Sprite: SpriteAtDesk}},
		Tasks:          []BoardTask{{ID: "t-1", Title: "ship", Status: TaskInProgress, Owner: "tekton-1", At: 5}},
		Mails:          []MailItem{{ID: "m-1", From: "boss", To: "tekton-1", At: 5, Subject: "go", Body: "do it", Kind: MailBrief}},
		Chat:           []ChatMsg{{ID: "c-1", From: "user", Text: "hi", At: 4}},
		Bubbles:        []SpeechBubble{{ID: "b-1", EmployeeID: "tekton-1", Text: "on it", UntilTick: 9}},
		Mode:           ModeLive,
		StatusLine:     "ok",
		Tick:           7,
		BossThinking:   true,
		BossDelegating: true,
		TokensIn:       100,
		TokensOut:      42,
		CostUSD:        0.0123,
	}
	out := roundTrip(t, in)
	if out.Mode != in.Mode || out.StatusLine != in.StatusLine || out.Tick != in.Tick {
		t.Errorf("scalars = %+v, want %+v", out, in)
	}
	if !out.BossThinking || !out.BossDelegating {
		t.Errorf("boss flags = thinking:%v delegating:%v, want both true", out.BossThinking, out.BossDelegating)
	}
	if out.TokensIn != 100 || out.TokensOut != 42 || out.CostUSD != 0.0123 {
		t.Errorf("usage = in:%d out:%d cost:%f, want 100/42/0.0123", out.TokensIn, out.TokensOut, out.CostUSD)
	}
	if len(out.Employees) != 1 || len(out.Tasks) != 1 || len(out.Mails) != 1 || len(out.Chat) != 1 || len(out.Bubbles) != 1 {
		t.Errorf("slices = %+v, want one entry each", out)
	}

	// Zero-value office: all usage/boss fields are omitempty.
	data, _ := json.Marshal(OfficeState{Mode: ModeDemo})
	for _, key := range []string{`"bossThinking"`, `"bossDelegating"`, `"tokensIn"`, `"tokensOut"`, `"costUsd"`} {
		if strings.Contains(string(data), key) {
			t.Errorf("zero OfficeState JSON carries %s: %s", key, data)
		}
	}
}

func TestEventJSONAndAdditiveQuestions(t *testing.T) {
	in := Event{
		Kind:       EvQuestion,
		QuestionID: "que-1",
		SessionID:  "ses-1",
		Text:       "pick one | other",
		Questions: []QuestionItem{
			{Question: "Q1", Options: []QuestionOption{{Label: "a"}, {Label: "b"}}},
			{Question: "Q2", Multiple: true, Options: []QuestionOption{{Label: "x"}, {Label: "y"}, {Label: "z"}}},
		},
	}
	out := roundTrip(t, in)
	if out.Kind != EvQuestion || out.QuestionID != "que-1" || out.SessionID != "ses-1" {
		t.Errorf("scalars = %+v, want kind/questionId/sessionId of %+v", out, in)
	}
	if len(out.Questions) != 2 {
		t.Fatalf("Questions len = %d, want 2", len(out.Questions))
	}
	if out.Questions[0].Question != "Q1" || len(out.Questions[0].Options) != 2 {
		t.Errorf("Questions[0] = %+v", out.Questions[0])
	}
	if !out.Questions[1].Multiple || len(out.Questions[1].Options) != 3 {
		t.Errorf("Questions[1] = %+v", out.Questions[1])
	}

	// Events without questions leave the key out entirely.
	data, _ := json.Marshal(Event{Kind: EvTick})
	if strings.Contains(string(data), `"questions"`) {
		t.Errorf("EvTick JSON carries questions: %s", data)
	}
}

func TestEventUsageFieldsAreDeltas(t *testing.T) {
	// Contract: EvUsage carries per-message GROWTH — accumulate with +=,
	// never overwrite. Simulate two deltas for the same CallID.
	var in, out int64
	var cost float64
	for _, ev := range []Event{
		{Kind: EvUsage, CallID: "msg-1", TokensIn: 10, TokensOut: 3, CostUSD: 0.001},
		{Kind: EvUsage, CallID: "msg-1", TokensIn: 50, TokensOut: 7, CostUSD: 0.004},
	} {
		in += ev.TokensIn
		out += ev.TokensOut
		cost += ev.CostUSD
	}
	if in != 60 || out != 10 || cost != 0.005 {
		t.Errorf("accumulated = in:%d out:%d cost:%f, want 60/10/0.005", in, out, cost)
	}
}

// --- Backend / seam interface satisfaction ------------------------------------

// stubBackend proves a minimal implementation satisfies Backend — the contract
// both the demo backend and harness stubs live by.
type stubBackend struct {
	emit func(Event)
}

func (s *stubBackend) Mode() Mode                   { return ModeDemo }
func (s *stubBackend) Start(emit func(Event)) error { s.emit = emit; return nil }
func (s *stubBackend) Send(text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty send")
	}
	if s.emit != nil {
		s.emit(Event{Kind: EvChatUser, Msg: ChatMsg{ID: "u-1", From: "user", Text: text}})
	}
	return nil
}
func (s *stubBackend) AnswerPermission(permissionID, response string) error { return nil }
func (s *stubBackend) AnswerQuestion(requestID string, answers [][]string) error {
	if len(answers) == 0 {
		return errors.New("no answers")
	}
	return nil
}
func (s *stubBackend) RejectQuestion(requestID string) error { return nil }
func (s *stubBackend) MCPServers() ([]MCPServer, error) {
	return []MCPServer{{Name: "stub", Status: "connected"}}, nil
}
func (s *stubBackend) ReconnectMCP(name string) error { return nil }
func (s *stubBackend) Stop() error                    { return nil }

func TestBackendInterfaceSatisfaction(t *testing.T) {
	var b Backend = &stubBackend{}
	if b.Mode() != ModeDemo {
		t.Errorf("Mode() = %q, want demo", b.Mode())
	}
	var got []Event
	if err := b.Start(func(e Event) { got = append(got, e) }); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Send("hello boss"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := b.Send("   "); err == nil {
		t.Errorf("Send(whitespace) expected error")
	}
	if len(got) != 1 || got[0].Kind != EvChatUser || got[0].Msg.Text != "hello boss" {
		t.Errorf("emitted = %+v, want one chat-user echo", got)
	}
	if err := b.AnswerPermission("per-1", "once"); err != nil {
		t.Errorf("AnswerPermission: %v", err)
	}
	if err := b.AnswerQuestion("que-1", [][]string{{"a"}}); err != nil {
		t.Errorf("AnswerQuestion: %v", err)
	}
	if err := b.AnswerQuestion("que-1", nil); err == nil {
		t.Errorf("AnswerQuestion with no answers expected error")
	}
	if err := b.RejectQuestion("que-1"); err != nil {
		t.Errorf("RejectQuestion: %v", err)
	}
	servers, err := b.MCPServers()
	if err != nil || len(servers) != 1 || servers[0].Name != "stub" {
		t.Errorf("MCPServers = %v, %v", servers, err)
	}
	if err := b.ReconnectMCP("stub"); err != nil {
		t.Errorf("ReconnectMCP: %v", err)
	}
	if err := b.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// Optional seams stay OUT of Backend: a Backend value need not implement them.
func TestOptionalSeamsAreSeparate(t *testing.T) {
	var b Backend = &stubBackend{}
	if _, ok := b.(ConciergeCapable); ok {
		t.Errorf("stubBackend unexpectedly implements ConciergeCapable")
	}
	if _, ok := b.(SessionAborter); ok {
		t.Errorf("stubBackend unexpectedly implements SessionAborter")
	}
}
