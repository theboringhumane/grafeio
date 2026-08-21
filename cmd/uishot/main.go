// uishot — deterministic UI shot harness for Grafeio v2.
//
// Runs the REAL app model against a scripted stub backend (fixed event
// script: hires/dispatches/working/returned+mail/blocked/bubbles, boss
// EvThought + EvTool chains, two chat rounds — one boss reply contains
// markdown — plus the deep-work stream: EvQuestion, EvFileDiff, EvPermission
// for both boss and a child employee, and a rapid triple-send typed into the
// textarea while the boss reply is pending). Fixed 130x32, ~4s, then prints
// the final frame between ===== UI SHOT ===== markers.
//
//	go run ./cmd/uishot [--tab chat|agents|board|mail|activity]
//	                    [--theme noir|paper|mono|dracula|solarized]
//	                    [--slash]   (also simulates typing /theme + /themes)
//	                    [--perm]    (auto-answers the boss permission "once" at 3s)
//	                    [--diffs]   (expands all diff entries via ctrl+d)
//	                    [--debug]   (queue flush proof: resolves the pending boss,
//	                                prints [queue] trace lines, longer window)
//	                    [--think]   (think-stream proof: one CallID streamed in
//	                                accumulated updates, then collapsed after
//	                                Done — prints BOTH frames: mid-stream at
//	                                t=2.0s and collapsed at t=3.2s)
//	                    [--think-stop mid|done] (with --think: print just ONE
//	                                frame — mid = streaming, done = collapsed —
//	                                for the gallery freeze shot)
//	                    [--stream]  (chat-stream proof: one "bossmsg-m1" bubble
//	                                streamed as 5 ACCUMULATED pending updates
//	                                300ms apart, then the pinned final; prints
//	                                frame mid-stream (partial bubble + caret,
//	                                spinner row gone) and after done (one single
//	                                settled bubble — replace-in-place, no dup).
//	                                A message is typed mid-stream to prove the
//	                                queue holds until the final bubble; the
//	                                ordering trace prints enqueue/done/flush.)
//	                    [--ask-answer] (question-hold proof: boss EvQuestion q-1
//	                                opens the answer modal at 1.5s (parked turn —
//	                                typing placeholder removed); typing "the
//	                                toggle one" + enter at 2.5s must hit
//	                                AnswerQuestion, the entry gains a dim
//	                                "✓ answered", the resumed boss reply closes
//	                                the turn. Prints BOTH frames + the capture
//	                                log; an employee question stays activity-only.)
//	                    [--ask-esc]   (esc defers the hold with a notice,
//	                                /question re-opens it, answer still via
//	                                AnswerQuestion)
//	                    [--ask-queue] (queue-hold proof: a line typed while the
//	                                hold is outstanding ENQUEUES; flush fires
//	                                only after resolved + completed boss reply —
//	                                ordering trace prints it)
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/grafeio/internal/app"
	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	shotCols    = 130
	shotRows    = 32
	shotDur     = 4000 * time.Millisecond
	shotDurLong = 6500 * time.Millisecond // --debug: drain the whole queue chain
	defaultTab  = "agents"
	bossReplyMD = "**Done** — created `hello.html`:\n" +
		"- dark navy bg\n" +
		"- white text\n" +
		"```sh\n" +
		"echo done\n" +
		"```"
)

// bossDiffBody — a markdown-heavy README.md diff close to the reference
// panel: **bold** and `code` spans inside tinted rows (proves chroma paints
// md on top of the tints), realistic @@ old/new numbers, and >30 rows total
// so the expanded "+N more" clip still fires.
const bossDiffBody = "--- a/README.md\n" +
	"+++ b/README.md\n" +
	"@@ -40,6 +40,7 @@ renders **bold**, *italic*, `code` and lists.\n" +
	" \n" +
	" ## What you get\n" +
	"   format and wrap inside the panel instead of bleeding through the UI.\n" +
	" \n" +
	"- **Bug:** Scrolling everywhere (viewport), mouse wheel, multi-line input,\n" +
	"   typing spinner while the `boss` works.\n" +
	"- Native single binary. The **Ink/Node** v0.1 app is preserved under\n" +
	"   [`node-legacy/`](node-legacy/) (tagged `node-v0.1.0`).\n" +
	"+ Native single binary. Themes: `--theme noir|paper|mono|dracula`\n" +
	"+ (also `/theme` in-app, persisted to `~/.config/grafeio/theme`).\n" +
	" \n" +
	"   ## Behind the glass\n" +
	"@@ -49,6 +50,10 @@ renders **bold**, *italic*, `code` and lists.\n" +
	"   Boss turns stream as markdown; user turns wrap plain.\n" +
	"- ~90 MB of RAM idle, **instant** startup.\n" +
	"+ ~12 MB of RAM idle, **instant** startup.\n" +
	"- See [docs/](docs/) for the tour.\n" +
	"+ See [docs/](docs/) for the full tour, [AGENTS.md](AGENTS.md) for rules.\n" +
	" \n" +
	"   ### Run it\n" +
	"-```sh\n" +
	"-cd grafeio && go build ./...\n" +
	"-```\n" +
	"+```sh\n" +
	"+go run ./cmd/grafeio --theme dracula\n" +
	"+grafeio --theme paper\n" +
	"+```\n" +
	" \n" +
	" | Key | Action |\n" +
	" |-----|--------|\n" +
	"-| `ctrl+t` | toggle thinking |\n" +
	"+| `ctrl+t` | toggle thinking blocks |\n" +
	"+| `ctrl+d` | toggle expanded diffs |\n" +
	"+| `/diffs off` | collapse all diffs |\n" +
	" \n" +
	" ## Layout\n" +
	" \n" +
	"-- side: chat with the boss\n" +
	"- floor: the office\n" +
	"-```\n" +
	"-┌──────┐\n" +
	"-│ desk │\n" +
	"-└──────┘\n" +
	"-```\n" +
	"+ sidebar: chat with the boss (**all** history)\n" +
	"+ floor: the office grid, mail, board, activity tabs\n" +
	"+```text\n" +
	"+┌────────┬────────┐\n" +
	"+│  chat  │ floor  │\n" +
	"+└────────┴────────┘\n" +
	"+```"

// employeeDiffBody — a brand-new Go file (--- /dev/null) so the expanded
// header reads "← New file …" and all rows are additions with Go syntax.
const employeeDiffBody = "--- /dev/null\n" +
	"+++ b/src/main.go\n" +
	"@@ -0,0 +1,5 @@\n" +
	"+package main\n" +
	"+\n" +
	"+func main() {\n" +
	"+\tprintln(\"hello, grafeio\")\n" +
	"+run()\n" +
	"+}"

// stubBackend is the deterministic scripted backend for the shot.
type stubBackend struct {
	emit       func(state.Event)
	done       chan struct{}
	start      time.Time
	flushQueue bool              // --debug: script resolves the round-2 pending boss
	thinkMode  bool              // --think: script streams one think CallID instead
	streamMode bool              // --stream: script streams one "bossmsg-" reply instead
	askMode    string            // --ask-*: "" | "answer" | "esc" | "queue" (question-hold proof)
	permAnswer string            // recorded by AnswerPermission for the final print
	sendSeq    int               // unique reply IDs per Send (replace-by-ID safety)
	answerLog  []string          // recorded by AnswerQuestion/RejectQuestion (the capture proof)
	trace      func(line string) // --stream/--ask-*: ordering-trace sink
}

func mail(id, from, to, subject, body string, kind state.MailKind) state.MailItem {
	return state.MailItem{ID: id, From: from, To: to, At: time.Now().UnixMilli(),
		Subject: subject, Body: body, Kind: kind}
}

func chatMsg(id, from, text string, pending bool) state.ChatMsg {
	return state.ChatMsg{ID: id, From: from, Kind: from, Text: text,
		At: time.Now().UnixMilli(), Pending: pending}
}

// script — fixed ABSOLUTE times (ms from start), fixed payloads;
// deterministic given the same clock. Ends ~3.3s into the ~4s window
// (~3.6s with flushQueue, inside the ~6.5s --debug window).
func (b *stubBackend) script() {
	start := time.Now()
	at := func(ms int, ev state.Event) {
		if d := time.Until(start.Add(time.Duration(ms) * time.Millisecond)); d > 0 {
			time.Sleep(d)
		}
		b.emit(ev)
	}
	if b.thinkMode {
		b.scriptThink(at)
		return
	}
	if b.streamMode {
		b.scriptStream(at)
		return
	}
	if b.askMode != "" {
		b.scriptAsk(at)
		return
	}

	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — stub backend online"})
	at(100, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Sprite: state.SpriteAtDesk}})
	at(250, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "sco-1", Name: "skopos-1", Role: state.RoleScout, Sprite: state.SpriteAtDesk}})
	at(400, state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "rev-1", Name: "dikastes", Role: state.RoleReviewer, Sprite: state.SpriteAtDesk}})

	// round 1: user asks, boss THINKS (visible, collapsed by default), boss
	// answers with markdown — with a boss tool chain merging running → done.
	at(550, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"make hello.html — dark navy, white text", false)})
	at(600, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	at(650, state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
		Text: "single file, no build step.\ndark navy bg, white text — keep it simple.", Done: false})
	at(720, state.Event{Kind: state.EvThought, EmployeeID: "dev-1", EmployeeName: "tekton-1",
		Text: "activity-line-only thought (employee — no chat entry)", Done: false})
	at(780, state.Event{Kind: state.EvTool, EmployeeID: "boss", EmployeeName: "boss",
		ToolName: "write", ToolSummary: "hello.html", ToolState: "running", CallID: "call-1"})
	at(900, state.Event{Kind: state.EvTool, EmployeeID: "boss", EmployeeName: "boss",
		ToolName: "write", ToolSummary: "hello.html", ToolState: "done", CallID: "call-1"})
	at(1000, state.Event{Kind: state.EvThought, EmployeeID: "boss", EmployeeName: "boss",
		Text: "deck the reply with a list and a code fence so markdown shows.", Done: true})
	at(1080, state.Event{Kind: state.EvDispatch, EmployeeID: "dev-1",
		Task: state.BoardTask{ID: "t1", Title: "build hello.html", At: time.Now().UnixMilli()}})
	at(1150, state.Event{Kind: state.EvWorking, EmployeeID: "dev-1", TaskID: "t1"})
	at(1200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b1", "boss", bossReplyMD, false)})

	at(1400, state.Event{Kind: state.EvDispatch, EmployeeID: "sco-1",
		Task: state.BoardTask{ID: "t2", Title: "scan the repo", At: time.Now().UnixMilli()}})
	// employee tool chain: running → done for a non-boss employee
	at(1500, state.Event{Kind: state.EvTool, EmployeeID: "dev-1", EmployeeName: "tekton-1",
		ToolName: "read", ToolSummary: "src/main.go", ToolState: "running", CallID: "call-2"})
	at(1600, state.Event{Kind: state.EvBubble, EmployeeID: "dev-1",
		Text: "this diff is a crime scene.", TTL: 40})
	at(1800, state.Event{Kind: state.EvWorking, EmployeeID: "sco-1", TaskID: "t2"})
	at(1900, state.Event{Kind: state.EvTool, EmployeeID: "dev-1", EmployeeName: "tekton-1",
		ToolName: "read", ToolSummary: "src/main.go", ToolState: "done", CallID: "call-2"})

	// deep-work stream: boss question (chat entry, yellow) + an employee
	// question (activity line ONLY — no chat entry), then diffs for both.
	// The boss question opens the answer modal — resolve it quickly after
	// the entry has landed so LATER scripted workloads (--slash typing at
	// ~1950ms, --perm at 3s, the queue typing at ~3060ms) are not
	// swallowed by the modal (the entry keeps the dim "✓ answered").
	at(1920, state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-1",
		Text:        "Which DB should the leaderboard use?",
		ToolSummary: "postgres | sqlite | keep it in memory"})
	at(1935, state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-1",
		ToolSummary: "answered", ToolState: "resolved"})
	// the resumed server finishes the parked turn with a completed reply
	// (the contract that unblocks the parked queue path again)
	at(1975, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b2", "boss",
		"sqlite it is — local, zero setup, fits the leaderboard.", false)})
	at(1960, state.Event{Kind: state.EvQuestion, EmployeeName: "tekton-1", QuestionID: "q-2",
		Text: "employee question — activity line only, no chat entry"})
	at(2000, state.Event{Kind: state.EvFileDiff, EmployeeName: "boss",
		DiffPath: "README.md", DiffAdd: 18, DiffDel: 15, DiffBody: bossDiffBody})
	at(2050, state.Event{Kind: state.EvFileDiff, EmployeeName: "tekton-1",
		DiffPath: "src/main.go", DiffAdd: 5, DiffDel: 0, DiffBody: employeeDiffBody})

	// a return: desk walk + done task + return mail
	at(2100, state.Event{Kind: state.EvReturned, EmployeeID: "dev-1", TaskID: "t1",
		Mail: mail("m1", "tekton-1", "boss", "return: build hello.html", "hello.html is up.", state.MailReturn)})
	at(2300, state.Event{Kind: state.EvMail, Mail: mail("m2", "boss", "tekton-1",
		"brief: footer next", "add a footer", state.MailBrief)})

	// permission prompts: boss (modal replaces the textarea) + child
	// (activity line only, no modal). Both stay pending unless --perm
	// answers the boss one.
	at(2400, state.Event{Kind: state.EvPermission, EmployeeName: "boss", PermissionID: "perm-1",
		ToolName: "write", ToolSummary: "main.go", ToolState: ""})
	at(2450, state.Event{Kind: state.EvPermission, EmployeeName: "tekton-1", SessionID: "child-dev-1",
		PermissionID: "perm-2", ToolName: "bash", ToolSummary: "rm -rf /tmp/scratch", ToolState: ""})

	at(2500, state.Event{Kind: state.EvIdleDrift, EmployeeID: "sco-1"})
	at(2700, state.Event{Kind: state.EvBlocked, EmployeeID: "rev-1", Text: "needs the staging key"})
	at(2750, state.Event{Kind: state.EvBubble, EmployeeID: "rev-1", Text: "anyone seen the staging key?", TTL: 40})

	// round 2: boss still typing when the frame freezes (spinner visible) —
	// unless flushQueue, which resolves it so the queue drains.
	at(3000, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u2", "user",
		"and add a footer please", false)})
	at(3050, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-2", "boss", "", true)})
	at(3300, state.Event{Kind: state.EvMail, Mail: mail("m3", "hr", "all",
		"roster synced", "3 agents seated.", state.MailNotice)})
	if b.flushQueue {
		at(3900, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b3", "boss",
			"Ship it — and keep typing, I'll keep up.", false)})
	}
}

// scriptThink (--think) — the live think-transcript proof, everything in
// the window's earlier half: one old block ALREADY Done (renders collapsed
// in both frames), then one CallID streamed in 4 ACCUMULATED updates 600ms
// apart, then the final Done update. Frame 1 (t=2.0s) catches update 3 of
// 4 mid-stream (12 lines → "… 2 more above"); frame 2 (t=3.2s) shows both
// blocks collapsed. Lines are kept ≤34 cells so chat-width wrapping stays
// 1:1 with the source lines and the counts are deterministic.
func (b *stubBackend) scriptThink(at func(ms int, ev state.Event)) {
	thought := func(callID, text string, done bool) state.Event {
		return state.Event{Kind: state.EvThought, EmployeeID: "boss",
			EmployeeName: "boss", CallID: callID, Text: text, Done: done}
	}
	part1 := "goals first: weekly leaderboard.\n" +
		"top 20, tie-break on streak."
	part2 := part1 + "\n" +
		"store stays local — sqlite only.\n" +
		"boss peeks but never edits rows.\n" +
		"window rolls monday at midnight.\n" +
		"empty week shows last week's ghost."
	part3 := part2 + "\n" +
		"render: dim row tint, gold crown.\n" +
		"rank one gets the mug sprite.\n" +
		"long names clip at 18 cells.\n" +
		"stale rows fade after 3 weeks.\n" +
		"footer keeps the total count.\n" +
		"no pagination — one screen."
	part4 := part3 + "\n" +
		"panic path: retry once, then hold.\n" +
		"the boss asks before writes.\n" +
		"tests pin the window rollover.\n" +
		"ship friday with the mug.\n" +
		"backlog: per-team leaderboards.\n" +
		"keep the ghost rule — nice touch."

	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — think-stream stub online"})
	at(150, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"sketch the leaderboard flow", false)})
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	// an older, already-complete thought — collapsed in BOTH frames
	at(350, thought("th-old",
		"weekly beats daily — less noise.\nboss only sees the rollout row.", true))
	// the live stream: 4 accumulated updates, ~600ms apart, one CallID
	at(500, thought("th-1", part1, false))
	at(1100, thought("th-1", part2, false))
	at(1700, thought("th-1", part3, false))
	at(2300, thought("th-1", part4, false))
	at(2900, thought("th-1", part4, true)) // Done: final accumulated text
}

// scriptAsk (--ask-*) — the question-hold deadlock proof: the boss parks
// the turn at the question reply API and a plain typed chat message must
// NOT be Send()n — it must go through AnswerQuestion. Timeline: user
// message + typing placeholder at 150/200ms, a REGRESSION employee
// question at 1200ms (activity line only, never a modal), then the boss
// EvQuestion q-1 pending at 1500ms — the modal opens, the "boss-1"
// placeholder is REMOVED (parked, not typing), and the hold waits for the
// harness (see ask*Workload). Answering → the stub emits "resolved" +
// a completed boss reply (scriptAsk's server-resume leg lives in
// AnswerQuestion, like the real opencode round trip).
func (b *stubBackend) scriptAsk(at func(ms int, ev state.Event)) {
	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — question-hold stub online"})
	at(150, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"summarize the flagged rows", false)})
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	// regression: an employee question stays activity-line only, no chat
	// entry, no modal — even while the boss hold opens a beat later
	at(1200, state.Event{Kind: state.EvQuestion, EmployeeName: "tekton-1", QuestionID: "q-2",
		Text: "employee question — activity line only, no chat entry"})
	at(1500, state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-1",
		Text:        "Which toggle do you want me to flip — the feature flag or the dark-mode switch?",
		ToolSummary: "the toggle one | dark mode | both"})
}

func (b *stubBackend) Mode() state.Mode { return state.ModeDemo }

func (b *stubBackend) Start(emit func(state.Event)) error {
	b.emit = emit
	b.start = time.Now()
	go b.script()
	return nil
}

// streamReply — the --stream bubble's full pinned text; streamParts are its
// accumulated prefixes (what the backend's deltas accumulate to). Kept ≤ one
// sidebar wrap per prefix so the mid-stream frame is deterministic.
const streamReply = "Honey never spoils — jars buried 3,000 years ago were " +
	"still **good to eat**. It crystallizes over time, but a warm water " +
	"bath brings it right back."

var streamParts = []string{
	"Honey never",
	"Honey never spoils — jars buried",
	"Honey never spoils — jars buried 3,000 years ago were still **good to",
	"Honey never spoils — jars buried 3,000 years ago were still **good to eat**. It crystallizes over time,",
	streamReply,
}

// scriptStream (--stream) — the live-typing proof, matching the backend's
// streaming contract: Send stages ONE "boss-N" placeholder; the reply
// arrives as 5 ACCUMULATED pending updates on the STABLE ID "bossmsg-m1"
// (300ms apart), then the pinned final (same ID, Pending=false). The mid
// frame (t=1.25s) shows the grown bubble + caret with the spinner row gone;
// the done frame (t=2.8s) shows exactly ONE settled bubble — deltas merged
// in place, never appended. Done also flushes the message typed mid-stream.
func (b *stubBackend) scriptStream(at func(ms int, ev state.Event)) {
	trace := func(line string) {
		if b.trace != nil {
			b.trace(line)
		}
	}
	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — chat-stream stub online"})
	at(150, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"tell me about honey", false)})
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	for i, part := range streamParts {
		at(500+i*300, state.Event{Kind: state.EvChatBoss,
			Msg: chatMsg("bossmsg-m1", "boss", part, true)})
	}
	// the final pinned update — same stable ID, Pending=false
	at(2000, state.Event{Kind: state.EvChatBoss,
		Msg: chatMsg("bossmsg-m1", "boss", streamReply, false)})
	trace("[stream] done: bossmsg-m1 → pending=false")
}

// Send answers any interactive prompt deterministically (600ms ack). Reply
// IDs are UNIQUE per call ("bx-N") — with replace-by-ID in the reducer, a
// recycled ID would collapse consecutive flushed replies into one bubble.
func (b *stubBackend) Send(text string) error {
	if b.emit != nil {
		b.sendSeq++
		id := fmt.Sprintf("bx-%d", b.sendSeq)
		reply := "Roger that."
		if b.streamMode {
			reply = "flushed follow-up handled: " + text
		}
		time.AfterFunc(600*time.Millisecond, func() {
			b.emit(state.Event{Kind: state.EvChatBoss,
				Msg: chatMsg(id, "boss", reply, false)})
		})
	}
	return nil
}

// AnswerPermission records the reply (proof for --perm) and emits the
// matching "resolved" event a beat later, like the real backends.
func (b *stubBackend) AnswerPermission(permissionID, response string) error {
	b.permAnswer = permissionID + ":" + response
	if b.emit != nil {
		time.AfterFunc(200*time.Millisecond, func() {
			b.emit(state.Event{Kind: state.EvPermission, PermissionID: permissionID,
				EmployeeName: "boss", ToolState: "resolved"})
		})
	}
	return nil
}

// AnswerQuestion records the reply (capture proof for --ask-*) and plays
// the resumed server: the matching "resolved" event a beat later, then a
// COMPLETED boss reply — the contract leg that unblocks the parked queue.
func (b *stubBackend) AnswerQuestion(requestID string, answers []string) error {
	line := fmt.Sprintf("AnswerQuestion(%s, [%s])", requestID, strings.Join(answers, ", "))
	b.answerLog = append(b.answerLog, line)
	if b.trace != nil {
		b.trace("[ask] " + line)
	}
	if b.emit != nil {
		emit := b.emit
		time.AfterFunc(200*time.Millisecond, func() {
			if b.trace != nil {
				b.trace("[ask] resolved: " + requestID)
			}
			emit(state.Event{Kind: state.EvQuestion, QuestionID: requestID,
				EmployeeName: "boss", ToolSummary: "answered", ToolState: "resolved"})
		})
		time.AfterFunc(450*time.Millisecond, func() {
			if b.trace != nil {
				b.trace("[ask] server resumed: completed boss reply")
			}
			b.sendSeq++
			emit(state.Event{Kind: state.EvChatBoss, Msg: chatMsg(
				fmt.Sprintf("bq-%d", b.sendSeq),
				"boss", "flipped — thanks, that clears it.", false)})
		})
	}
	return nil
}

// RejectQuestion records the reply; the hold resolves like an answer.
func (b *stubBackend) RejectQuestion(requestID string) error {
	line := fmt.Sprintf("RejectQuestion(%s)", requestID)
	b.answerLog = append(b.answerLog, line)
	if b.trace != nil {
		b.trace("[ask] " + line)
	}
	if b.emit != nil {
		emit := b.emit
		time.AfterFunc(200*time.Millisecond, func() {
			emit(state.Event{Kind: state.EvQuestion, QuestionID: requestID,
				EmployeeName: "boss", ToolSummary: "rejected", ToolState: "resolved"})
		})
	}
	return nil
}

func (b *stubBackend) Stop() error { return nil }

// slashWorkload simulates the user typing a slash command into the chat
// textarea and hitting Enter — proving slash dispatch never hits the backend
// and the office notice renders. It types /theme dracula (switch + persist),
// then /themes (listing notice) — they land before the 3050ms pending lock.
func slashWorkload(p *tea.Program) {
	typeLine := func(s string) {
		for _, r := range s {
			p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
			time.Sleep(10 * time.Millisecond)
		}
		p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		time.Sleep(80 * time.Millisecond)
	}
	time.Sleep(1950 * time.Millisecond)
	typeLine("/theme dracula")
	typeLine("/themes")
}

// queueWorkload types three short messages and hits Enter while the round-2
// boss reply is pending (3050ms) — each lands in the model-level queue; the
// placeholder + statusbar badge show the depth. TEXTS AVOID y/a/n: while the
// permission prompt is open those keys answer it (by design).
func queueWorkload(p *tea.Program) {
	typeLine := func(s string) {
		for _, r := range s {
			p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
			time.Sleep(8 * time.Millisecond)
		}
		p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	}
	time.Sleep(3060 * time.Millisecond)
	typeLine("first queued")
	time.Sleep(100 * time.Millisecond)
	typeLine("look it up")
	time.Sleep(100 * time.Millisecond)
	typeLine("ship this")
}

// permWorkload (--perm) answers the boss permission prompt with "once" 3s
// in — after the prompt opened at 2400ms, before the frame at 4s.
func permWorkload(p *tea.Program) {
	time.Sleep(3000 * time.Millisecond)
	p.Send(tea.KeyPressMsg(tea.Key{Code: 'y', Text: "y"}))
}

// diffsWorkload (--diffs) presses ctrl+d once the diff entries exist
// (2000/2050ms), expanding all of them for the final frame.
func diffsWorkload(p *tea.Program) {
	time.Sleep(2200 * time.Millisecond)
	p.Send(tea.KeyPressMsg(tea.Key{Code: 'd', Mod: tea.ModCtrl}))
}

// askTypeLine types a line into the open boss question modal rune by rune
// and hits Enter — the text must land in the modal's OWN input (the
// textarea is disabled while the hold is open).
func askTypeLine(p *tea.Program, s string) {
	for _, r := range s {
		p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		time.Sleep(8 * time.Millisecond)
	}
	p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	time.Sleep(60 * time.Millisecond)
}

// askEsc presses esc — with an open question modal this DEFERS the hold
// (notice "(question deferred — /question to reopen)").
func askEsc(p *tea.Program) {
	p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	time.Sleep(60 * time.Millisecond)
}

// askAnswerWorkload (--ask-answer): at 2.5s the user types "the toggle
// one" + enter into the open modal → must hit AnswerQuestion, never Send.
func askAnswerWorkload(p *tea.Program) {
	time.Sleep(2500 * time.Millisecond)
	askTypeLine(p, "the toggle one")
}

// askEscWorkload (--ask-esc): esc defers the hold, /question re-opens it,
// the answer still routes through AnswerQuestion.
func askEscWorkload(p *tea.Program) {
	time.Sleep(2300 * time.Millisecond)
	askEsc(p)
	time.Sleep(500 * time.Millisecond)
	askTypeLine(p, "/question")
	time.Sleep(300 * time.Millisecond)
	askTypeLine(p, "the toggle one")
}

// askQueueWorkload (--ask-queue): the queue-hold proof — a message typed
// while the hold is DEFERRED-but-outstanding must ENQUEUE (the turn is
// parked, flushing it would re-create the deadlock); answering q-1 then
// resolving + the completed boss reply must flush it, in that order.
func askQueueWorkload(p *tea.Program) {
	time.Sleep(2300 * time.Millisecond)
	askEsc(p)
	time.Sleep(300 * time.Millisecond)
	askTypeLine(p, "fix the badge too") // enqueued — turn is parked
	time.Sleep(600 * time.Millisecond)
	askTypeLine(p, "/question") // re-open the deferred hold
	time.Sleep(300 * time.Millisecond)
	askTypeLine(p, "the toggle one") // AnswerQuestion → resume → flush
}

// runThinkShot runs one fresh app+program against a think-mode stub for
// `dur`, then returns the final frame. Two calls with different durations
// = the deterministic before/after pair (--think's frames).
func runThinkShot(tab string, dur time.Duration) (string, error) {
	backend := &stubBackend{done: make(chan struct{}), thinkMode: true}
	m := app.New(backend)
	if !m.SelectTab(tab) {
		return "", fmt.Errorf("unknown tab %q", tab)
	}
	p := tea.NewProgram(m,
		tea.WithWindowSize(shotCols, shotRows),
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	emit := func(ev state.Event) { p.Send(ev) }
	if err := backend.Start(emit); err != nil {
		return "", err
	}
	go func() {
		time.Sleep(dur)
		p.Quit()
	}()
	final, err := p.Run()
	if err != nil {
		return "", err
	}
	fm, ok := final.(app.Model)
	if !ok {
		return "", fmt.Errorf("unexpected final model type %T", final)
	}
	return fm.Frame(), nil
}

// streamWorkload (--stream) types ONE message and hits Enter mid-stream —
// the deltas run 500–1700ms and the placeholder went pending at 200ms, so
// Enter lands while the boss reply is outstanding: it must ENQUEUE, and the
// flush must fire only after the done bubble (2000ms). The trace lines
// (enqueued / done / flush, all timestamped) prove the order.
func streamWorkload(p *tea.Program) {
	typeLine := func(s string) {
		for _, r := range s {
			p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
			time.Sleep(8 * time.Millisecond)
		}
		p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	}
	time.Sleep(900 * time.Millisecond)
	typeLine("how do bees make it")
}

// traceLog collects timestamped ordering lines for the --stream proof
// (enqueue / done / flush), written from the script goroutine, the tea
// update loop (via app.QueueDebugf) and the shot runner.
type traceLog struct {
	mu    sync.Mutex
	start time.Time
	lines []string
}

func (t *traceLog) add(line string) {
	t.mu.Lock()
	t.lines = append(t.lines, fmt.Sprintf("%s @+%dms", line, time.Since(t.start).Milliseconds()))
	t.mu.Unlock()
}

// runStreamShot runs one fresh app+program against a stream-mode stub for
// `dur`, then returns the final frame plus the ordering trace. Two calls
// with different durations = the deterministic mid-stream/after-done pair.
func runStreamShot(dur time.Duration) (string, []string, error) {
	tl := &traceLog{start: time.Now()}
	backend := &stubBackend{done: make(chan struct{}), streamMode: true, trace: tl.add}
	m := app.New(backend)
	if !m.SelectTab("chat") {
		return "", nil, fmt.Errorf("unknown tab %q", "chat")
	}
	p := tea.NewProgram(m,
		tea.WithWindowSize(shotCols, shotRows),
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	app.QueueDebugf = func(format string, args ...any) {
		tl.add("[queue] " + fmt.Sprintf(format, args...))
	}
	emit := func(ev state.Event) { p.Send(ev) }
	if err := backend.Start(emit); err != nil {
		return "", nil, err
	}
	go streamWorkload(p)
	go func() {
		time.Sleep(dur)
		p.Quit()
	}()
	final, err := p.Run()
	if err != nil {
		return "", nil, err
	}
	fm, ok := final.(app.Model)
	if !ok {
		return "", nil, fmt.Errorf("unexpected final model type %T", final)
	}
	app.QueueDebugf = nil
	tl.mu.Lock()
	lines := append([]string(nil), tl.lines...)
	tl.mu.Unlock()
	return fm.Frame(), lines, nil
}

// runAskShot runs one fresh app+program against a question-hold stub for
// `dur`, driving the ask workload for `mode`, then returns the final
// frame, the ordering trace, and the stub's captured answer calls.
func runAskShot(mode string, dur time.Duration) (string, []string, []string, error) {
	tl := &traceLog{start: time.Now()}
	backend := &stubBackend{done: make(chan struct{}), askMode: mode, trace: tl.add}
	m := app.New(backend)
	if !m.SelectTab("chat") {
		return "", nil, nil, fmt.Errorf("unknown tab %q", "chat")
	}
	p := tea.NewProgram(m,
		tea.WithWindowSize(shotCols, shotRows),
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	app.QueueDebugf = func(format string, args ...any) {
		tl.add("[queue] " + fmt.Sprintf(format, args...))
	}
	emit := func(ev state.Event) { p.Send(ev) }
	if err := backend.Start(emit); err != nil {
		return "", nil, nil, err
	}
	switch mode {
	case "answer":
		go askAnswerWorkload(p)
	case "esc":
		go askEscWorkload(p)
	case "queue":
		go askQueueWorkload(p)
	}
	go func() {
		time.Sleep(dur)
		p.Quit()
	}()
	final, err := p.Run()
	if err != nil {
		return "", nil, nil, err
	}
	fm, ok := final.(app.Model)
	if !ok {
		return "", nil, nil, fmt.Errorf("unexpected final model type %T", final)
	}
	app.QueueDebugf = nil
	tl.mu.Lock()
	lines := append([]string(nil), tl.lines...)
	tl.mu.Unlock()
	return fm.Frame(), lines, backend.answerLog, nil
}

// printAskCapture prints the stub's captured AnswerQuestion/RejectQuestion
// calls and FAILS the run when the expected capture is missing (the
// deadlock regression: the answer must never fall through to Send).
func printAskCapture(capture []string, want string) {
	fmt.Println("--- stub capture log ---")
	found := false
	for _, line := range capture {
		fmt.Println(line)
		if strings.Contains(line, want) {
			found = true
		}
	}
	if len(capture) == 0 {
		fmt.Println("<empty>")
	}
	if !found {
		fmt.Fprintf(os.Stderr, "uishot: expected capture %q missing\n", want)
		os.Exit(1)
	}
}

func main() {
	tab := flag.String("tab", defaultTab, "active tab: chat|agents|board|mail|activity")
	theme := flag.String("theme", "", "force a ui theme: "+strings.Join(chrome.ThemeNames(), "|"))
	slash := flag.Bool("slash", false, "simulate typing /theme dracula + /themes (exercises slash dispatch + theme persist)")
	perm := flag.Bool("perm", false, "auto-answer the boss permission prompt with 'once' at 3s (open → answered)")
	diffs := flag.Bool("diffs", false, "press ctrl+d to expand all diff entries")
	debug := flag.Bool("debug", false, "queue flush proof: resolves the pending boss so the queue drains; prints [queue] trace lines")
	think := flag.Bool("think", false, "think-stream proof: one CallID streamed in accumulated updates, prints BOTH frames (t=2.0s mid-stream expanded, t=3.2s collapsed after Done)")
	thinkStop := flag.String("think-stop", "", "with --think: print ONE frame only (mid = t=2.0s streaming, done = t=3.2s collapsed) for the gallery shot")
	stream := flag.Bool("stream", false, "chat-stream proof: one \"bossmsg-m1\" reply streamed as 5 accumulated pending updates then pinned; prints frame mid-stream (bubble + caret, spinner gone) and after done (single settled bubble) plus the enqueue/done/flush ordering trace")
	askAnswer := flag.Bool("ask-answer", false, "question-hold proof: boss EvQuestion opens the answer modal (typing placeholder removed, park status line); typing + enter routes through AnswerQuestion — prints BOTH frames (modal open / after answered) + the stub capture log")
	askEsc := flag.Bool("ask-esc", false, "question-hold proof: esc defers the modal (notice), /question re-opens it, the answer still routes through AnswerQuestion")
	askQueue := flag.Bool("ask-queue", false, "queue-hold proof: a message typed while the question hold is outstanding must ENQUEUE; AnswerQuestion → resolved → completed boss reply → flush, ordering trace printed")
	flag.Parse()

	// keystroke workloads only reach the textarea / modal on the chat tab
	if (*slash || *perm || *diffs || *debug || *think || *askAnswer || *askEsc || *askQueue) && *tab == defaultTab {
		*tab = "chat"
	}

	if *theme != "" {
		if !chrome.SetTheme(*theme) {
			fmt.Fprintf(os.Stderr, "uishot: unknown theme %q (%s)\n", *theme,
				strings.Join(chrome.ThemeNames(), ", "))
			os.Exit(2)
		}
	}

	if *askAnswer || *askEsc || *askQueue {
		mode, stop2, label2 := "answer", 3600*time.Millisecond,
			"frame 2 — t=3.6s (AFTER ANSWER: modal closed, dim ✓ answered on the entry, resumed boss reply)"
		switch {
		case *askEsc:
			mode, stop2, label2 = "esc", 4300*time.Millisecond,
				"frame 2 — t=4.3s (deferred notice → /question reopen → ✓ answered)"
		case *askQueue:
			mode, stop2, label2 = "queue", 4700*time.Millisecond,
				"frame 2 — t=4.7s (queued line held through the hold, flushed after the resumed reply)"
		}
		stops := []struct {
			at    time.Duration
			label string
		}{
			{2200 * time.Millisecond, "frame 1 — t=2.2s (MODAL OPEN: boss asks, typing placeholder REMOVED — parked, not typing)"},
			{stop2, label2},
		}
		var trace, capture []string
		for i, s := range stops {
			frame, t, c, err := runAskShot(mode, s.at)
			if err != nil {
				fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("===== UI SHOT · %s =====\n", s.label)
			fmt.Println(frame)
			fmt.Println("===== UI SHOT =====")
			if i == len(stops)-1 {
				trace, capture = t, c
			}
		}
		if len(trace) > 0 {
			fmt.Println("--- ordering trace ---")
			for _, ln := range trace {
				fmt.Println(ln)
			}
		}
		printAskCapture(capture, "AnswerQuestion(q-1, [the toggle one])")
		return
	}

	if *think {
		type stop struct {
			at    time.Duration
			label string
		}
		stops := []stop{
			{2000 * time.Millisecond, "frame 1 — t=2.0s (mid-stream, EXPANDED)"},
			{3200 * time.Millisecond, "frame 2 — t=3.2s (collapsed after Done)"},
		}
		switch *thinkStop {
		case "mid":
			stops = stops[:1]
		case "done":
			stops = stops[1:]
		case "":
		default:
			fmt.Fprintf(os.Stderr, "uishot: --think-stop must be mid|done, got %q\n", *thinkStop)
			os.Exit(2)
		}
		for _, s := range stops {
			frame, err := runThinkShot(*tab, s.at)
			if err != nil {
				fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
				os.Exit(1)
			}
			if *thinkStop == "" {
				fmt.Printf("===== UI SHOT · %s =====\n", s.label)
			} else {
				fmt.Println("===== UI SHOT =====")
			}
			fmt.Println(frame)
			fmt.Println("===== UI SHOT =====")
		}
		return
	}

	if *stream {
		stops := []struct {
			at    time.Duration
			label string
		}{
			{1250 * time.Millisecond, "frame 1 — t=1.25s (MID-STREAM: grown bubble + caret, spinner row gone, one message enqueued)"},
			{2800 * time.Millisecond, "frame 2 — t=2.8s (AFTER DONE: one settled bubble — replace-in-place, no dup — queue flushed)"},
		}
		for _, s := range stops {
			frame, trace, err := runStreamShot(s.at)
			if err != nil {
				fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("===== UI SHOT · %s =====\n", s.label)
			fmt.Println(frame)
			fmt.Println("===== UI SHOT =====")
			fmt.Println("--- ordering trace ---")
			for _, ln := range trace {
				fmt.Println(ln)
			}
		}
		return
	}

	if *debug {
		app.QueueDebugf = func(format string, args ...any) {
			fmt.Printf("[queue] "+format+"\n", args...)
		}
	}

	backend := &stubBackend{done: make(chan struct{}), flushQueue: *debug}
	m := app.New(backend)
	if !m.SelectTab(*tab) {
		fmt.Fprintf(os.Stderr, "uishot: unknown tab %q\n", *tab)
		os.Exit(2)
	}

	p := tea.NewProgram(m,
		tea.WithWindowSize(shotCols, shotRows),
		tea.WithoutRenderer(), // no redraw loop; we print the final frame ourselves
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	// backend events flow in as tea.Msgs through Program.Send (state.Event
	// satisfies the empty tea.Msg/uv.Event interface).
	emit := func(ev state.Event) { p.Send(ev) }
	_ = backend.Start(emit)
	if *slash {
		go slashWorkload(p)
	}
	if *perm {
		go permWorkload(p)
	}
	if *diffs {
		go diffsWorkload(p)
	}
	// queue typing always runs — on the agents tab the keys are absorbed by
	// the (non-text) panel; on chat they enqueue.
	go queueWorkload(p)
	go func() {
		if *debug {
			time.Sleep(shotDurLong)
		} else {
			time.Sleep(shotDur)
		}
		p.Quit()
	}()

	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
		os.Exit(1)
	}
	fm, ok := final.(app.Model)
	if !ok {
		fmt.Fprintf(os.Stderr, "uishot: unexpected final model type %T\n", final)
		os.Exit(1)
	}

	fmt.Println("===== UI SHOT =====")
	fmt.Println(fm.Frame())
	fmt.Println("===== UI SHOT =====")

	if *slash {
		// persist proof: the /theme slash run must have written the file
		path := chrome.ThemeConfigPath()
		content, rerr := os.ReadFile(path)
		fmt.Printf("theme file: %s\n", path)
		if rerr != nil {
			fmt.Printf("theme file content: <error: %v>\n", rerr)
			os.Exit(1)
		}
		fmt.Printf("theme file content: %q\n", strings.TrimSpace(string(content)))
	}
}
