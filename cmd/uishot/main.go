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
//	                                frame mid-stream (partial bubble growing in
//	                                the viewport, typing row below the divider)
//	                                and after done (one single settled bubble —
//	                                replace-in-place, no dup).
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
//	                    [--batch]    (intelligent-backlog proof: boss busy ~3s
//	                                while three messages enqueue as backlog
//	                                #1 #2 #3; the turn-complete flush must be
//	                                ONE composed [BATCH DISPATCH] send. Prints
//	                                the frame, the composed batch text, the
//	                                stub Send/QueueItemStart/Done logs and the
//	                                ordering trace)
//	                    [--batch-respawn] (failure-respawn proof: the stub
//	                                rejects the first batch Send — the app
//	                                must ResetPrimary(true) and resend the
//	                                SAME batch once; second send succeeds)
//	                    [--power auto|saver|performance|all]
//	                                power-governor proof: the model runs in a
//	                                manual event loop (every update renders a
//	                                frame — that is what the caches count) for
//	                                a 6s scripted window per mode; prints tick
//	                                counts (performance > auto > saver), the
//	                                floor frame-cache hit %, the TickDelay
//	                                decision table (busy/idle/drift + tickMs
//	                                override), the /power + /model slash demo
//	                                (chat frame + persisted brain.json), and a
//	                                custom boss-name agents frame.
//	                    [--social]  (social-clock proof: scripted window pumping
//	                                EvTicks synchronously (no wall clock — tick-
//	                                seeded = deterministic). THREE frames:
//	                                SOCIAL A = tea request asked (bubble «<B>:
//	                                coffee?»), SOCIAL B = both sprites walking
//	                                to the machine, SOCIAL C = gossip chain
//	                                mid-fire; plus the banter chain trace, the
//	                                question-modal gate assert (nothing fires
//	                                while a modal is open), and a two-run
//	                                determinism check over the frame triplet.)
//	                    [--layout]  (layout-modes proof: THREE frames over the
//	                                same scripted window — NORMAL (sidebar 44),
//	                                compact (sidebar 30, short tab labels, 2-row
//	                                chat input, compressed topbar) and wide 56 —
//	                                each with its computed width asserts)
//	                    [--terminal] (terminal-tab proof: the stub TermPanel
//	                                (terminal_panel_stub.go — uishot ONLY)
//	                                wires through app.SpawnTerminal; selecting
//	                                the "terminal" tab lazy-spawns it, typed
//	                                keys route into the shell surface, and
//	                                CloseTerminal kills it — frame + asserts)
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
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/app"
	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/office"
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

	batchMode       bool     // --batch/--batch-respawn: backlog-batch proof script
	respawnMode     bool     // --batch-respawn: reject the first batch Send once
	batchFailedOnce bool     // the one-shot rejection sentinel
	sendLog         []string // every Send call, verbatim (the proof)
	teamLog         []string // QueueItemStart/Done + ResetPrimary calls (the proof)

	powerDemo bool // --power: minimal quiet script for the slash/name legs
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
	if b.batchMode {
		b.scriptBatch(at)
		return
	}
	if b.powerDemo {
		b.scriptPowerDemo(at)
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
// frame (t=1.25s) shows the grown bubble with the typing row still live
// below the divider (the row runs for the whole pending period now —
// there is no caret); the done frame (t=2.8s) shows exactly ONE settled
// bubble — deltas merged in place, never appended. Done also flushes the
// message typed mid-stream.
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

// scriptBatch (--batch / --batch-respawn) — the intelligent-backlog proof:
// the boss is busy from 200ms to 3000ms; the workload types three messages
// into the backlog in that window (#1 #2 #3); the turn-complete flush at
// ~3s must go out as ONE composed [BATCH DISPATCH] send.
func (b *stubBackend) scriptBatch(at func(ms int, ev state.Event)) {
	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — backlog-batch stub online"})
	at(150, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"start the standup notes", false)})
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	// busy until ~3s — everything the workload types ENQUEUES in this window
	at(3000, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b1", "boss",
		"standup notes are in drafts — checking the backlog.", false)})
}

// Send answers any interactive prompt deterministically (600ms ack). Reply
// IDs are UNIQUE per call ("bx-N") — with replace-by-ID in the reducer, a
// recycled ID would collapse consecutive flushed replies into one bubble.
// batchMode adds the backlog seam: the composed batch echoes as chat-user
// (proving the composite bubble) and stages the typing placeholder like the
// real backends — the pending→non-pending transition closes the board
// rows. respawnMode rejects the FIRST [BATCH DISPATCH] send once (a dead
// boss session), so the app must ResetPrimary + resend the same batch.
func (b *stubBackend) Send(text string) error {
	clip := text
	if r := []rune(clip); len(r) > 60 {
		clip = string(r[:59]) + "…"
	}
	b.sendLog = append(b.sendLog, text)
	if b.trace != nil {
		b.trace("[stub] Send(" + clip + ")")
	}
	if b.respawnMode && !b.batchFailedOnce && strings.HasPrefix(text, "[BATCH DISPATCH") {
		b.batchFailedOnce = true
		if b.trace != nil {
			b.trace("[stub] Send REJECTED — stubbed dead boss session (one-shot)")
		}
		return fmt.Errorf("stub: boss session dead")
	}
	if b.emit != nil {
		if b.batchMode {
			b.sendSeq++
			seq := b.sendSeq
			emit := b.emit
			emit(state.Event{Kind: state.EvChatUser, Msg: chatMsg(
				fmt.Sprintf("ue-%d", seq), "user", text, false)})
			emit(state.Event{Kind: state.EvChatBoss, Msg: chatMsg(
				fmt.Sprintf("boss-batch-%d", seq), "boss", "", true)})
			time.AfterFunc(600*time.Millisecond, func() {
				emit(state.Event{Kind: state.EvChatBoss, Msg: chatMsg(
					fmt.Sprintf("bx-%d", seq), "boss",
					"backlog dispatched: 3 items split across the floor — status table on their return.", false)})
			})
			return nil
		}
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

// --- teamBackend seam (the backlog board) ----------------------------------
// Log-only twins of the live/demo contract: the frame's proof is the
// printed call log, not an emitted board (board tabs are staged separately).

// QueueItemStart mirrors one backlog item: logs the call, returns the
// deterministic "demo-N" board row id.
func (b *stubBackend) QueueItemStart(index int, title string) string {
	id := fmt.Sprintf("demo-%d", index)
	b.teamLog = append(b.teamLog, fmt.Sprintf("QueueItemStart(%d, %q) -> %s", index, title, id))
	if b.trace != nil {
		b.trace(fmt.Sprintf("[team] QueueItemStart(%d, %q) -> %s", index, title, id))
	}
	return id
}

// QueueItemDone closes the board row when the batch's turn completes.
func (b *stubBackend) QueueItemDone(boardID string) {
	b.teamLog = append(b.teamLog, fmt.Sprintf("QueueItemDone(%s)", boardID))
	if b.trace != nil {
		b.trace("[team] QueueItemDone(" + boardID + ")")
	}
}

// ResetPrimary is the failure-respawn hook: logs the respawn of the boss
// session (the retry resends the SAME batch right after).
func (b *stubBackend) ResetPrimary(forceNew bool) error {
	b.teamLog = append(b.teamLog, fmt.Sprintf("ResetPrimary(%v)", forceNew))
	if b.trace != nil {
		b.trace(fmt.Sprintf("[team] ResetPrimary(%v)", forceNew))
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
	m := app.New(backend, config.Default())
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

// batchWorkload (--batch / --batch-respawn): three messages typed while
// the boss turn is pending (200–3000ms) — each ENQUEUES as a numbered
// backlog item; the flush at the turn-complete sends them as ONE batch.
func batchWorkload(p *tea.Program) {
	typeLine := func(s string) {
		for _, r := range s {
			p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
			time.Sleep(8 * time.Millisecond)
		}
		p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	}
	time.Sleep(700 * time.Millisecond)
	typeLine("fix the badge")
	time.Sleep(400 * time.Millisecond)
	typeLine("ship v2")
	time.Sleep(400 * time.Millisecond)
	typeLine("write the release notes")
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
	m := app.New(backend, config.Default())
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
	m := app.New(backend, config.Default())
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

// runBatchShot runs one fresh app+program against the backlog stub and
// returns the frame, ordering trace, verbatim Send calls and team-seam
// calls. respawn=true stubs the first batch Send dead (the respawn proof).
func runBatchShot(respawn bool, dur time.Duration) (string, []string, []string, []string, error) {
	tl := &traceLog{start: time.Now()}
	backend := &stubBackend{done: make(chan struct{}),
		batchMode: true, respawnMode: respawn, trace: tl.add}
	m := app.New(backend, config.Default())
	if !m.SelectTab("chat") {
		return "", nil, nil, nil, fmt.Errorf("unknown tab %q", "chat")
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
		return "", nil, nil, nil, err
	}
	go batchWorkload(p)
	go func() {
		time.Sleep(dur)
		p.Quit()
	}()
	final, err := p.Run()
	if err != nil {
		return "", nil, nil, nil, err
	}
	fm, ok := final.(app.Model)
	if !ok {
		return "", nil, nil, nil, fmt.Errorf("unexpected final model type %T", final)
	}
	app.QueueDebugf = nil
	tl.mu.Lock()
	lines := append([]string(nil), tl.lines...)
	tl.mu.Unlock()
	return fm.Frame(), lines, backend.sendLog, backend.teamLog, nil
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

// --- power-governor proof ---------------------------------------------------
// The standard shots run under tea.WithoutRenderer (no View calls until the
// final Frame). The power proof needs the caches exercised per rendered
// frame, so it drives the REAL model in a manual event loop: backend emit
// → channel, every Update feeds a Frame() render pass (that is what the
// floor/app caches count), tea.Tick re-arms land on their governor delay.

// runManualLoop drives model+cmd execution by hand for `dur`, then returns
// the final model. Every processed message renders one frame through the
// real Frame() path (cache-exercising), exactly like the bubbletea runtime.
func runManualLoop(cfg *config.Config, b *stubBackend, tab string, dur time.Duration,
	workload func(send func(tea.Msg))) (app.Model, error) {
	m := app.New(b, cfg)
	var zero app.Model
	if tab != "" && !m.SelectTab(tab) {
		return zero, fmt.Errorf("unknown tab %q", tab)
	}
	msgCh := make(chan tea.Msg, 512)
	exec := func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			if msg := c(); msg != nil {
				msgCh <- msg
			}
		}()
	}
	var tm tea.Model = m
	exec(tm.Init())
	var cmd tea.Cmd
	tm, cmd = tm.Update(tea.WindowSizeMsg{Width: shotCols, Height: shotRows})
	exec(cmd)
	if err := b.Start(func(ev state.Event) { msgCh <- ev }); err != nil {
		return zero, err
	}
	if workload != nil {
		workload(func(msg tea.Msg) { msgCh <- msg })
	}
	deadline := time.After(dur)
	for {
		select {
		case <-deadline:
			if fm, ok := tm.(app.Model); ok {
				return fm, nil
			}
			return zero, fmt.Errorf("unexpected model type %T", tm)
		case msg := <-msgCh:
			if bmsg, ok := msg.(tea.BatchMsg); ok {
				for _, sc := range bmsg {
					exec(sc)
				}
				continue
			}
			tm, cmd = tm.Update(msg)
			exec(cmd)
			if fm, ok := tm.(app.Model); ok {
				fm.Frame() // render pass — exercises digest + floor caches
			}
		}
	}
}

// scriptPowerDemo (--power slash leg) — minimal quiet script: one user
// message and a boss typing placeholder that NEVER completes inside the
// window, so the busy placeholder ("jorge is typing…") and the slash
// notices share the final frame.
func (b *stubBackend) scriptPowerDemo(at func(ms int, ev state.Event)) {
	at(50, state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — power-governor stub online"})
	at(150, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"ship the power governor", false)})
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
}

// printTickTable — the deterministic TickDelay decision table: synthetic
// busy/idle/drift states across every power mode, plus the tickMs override.
func printTickTable() {
	idleSt := state.OfficeState{Employees: []state.Employee{
		{ID: "manager", Name: "boss", Role: state.RoleManager, Sprite: state.SpriteAtDesk},
		{ID: "hr", Name: "hr", Role: state.RoleHR, Sprite: state.SpriteAtDesk},
	}}
	busySt := state.OfficeState{
		Employees: idleSt.Employees,
		Chat:      []state.ChatMsg{{ID: "boss-1", From: "boss", Pending: true}},
	}
	fmt.Println("--- TickDelay decision table (synthetic states) ---")
	rows := []struct {
		name string
		cfg  *config.Config
	}{
		{"auto", config.Default()},
		{"performance", config.Default()},
		{"saver", config.Default()},
		{"auto+tickMs=50", config.Default()},
	}
	rows[1].cfg.UI.Power = config.PowerPerformance
	rows[2].cfg.UI.Power = config.PowerSaver
	rows[3].cfg.UI.TickMs = 50
	want := []struct{ busy, idle, drift time.Duration }{
		{180 * time.Millisecond, 1 * time.Second, 3 * time.Second},
		{150 * time.Millisecond, 150 * time.Millisecond, 150 * time.Millisecond},
		{400 * time.Millisecond, 2 * time.Second, 2 * time.Second},
		{50 * time.Millisecond, 1 * time.Second, 3 * time.Second},
	}
	fail := false
	for idx, r := range rows {
		b := app.TickDelay(busySt, r.cfg, false, false, 0)
		idleDelay := app.TickDelay(idleSt, r.cfg, false, false, 0)
		d := app.TickDelay(idleSt, r.cfg, false, false, 61*time.Second)
		ok := b == want[idx].busy && idleDelay == want[idx].idle && d == want[idx].drift
		mark := "PASS"
		if !ok {
			mark = "FAIL"
			fail = true
		}
		fmt.Printf("  [%s] %-15s busy=%-6s idle=%-6s drift(61s)=%-6s\n", mark, r.name, b, idleDelay, d)
	}
	if fail {
		fmt.Fprintln(os.Stderr, "uishot: TickDelay decision table mismatch")
		os.Exit(1)
	}
	fmt.Println("asserts: OK — auto 180ms/1s/3s-drift, performance 150ms flat, saver 400ms/2s, tickMs overrides busy")
}

// powerWindow — one power mode's scripted-window tallies.
type powerWindow struct {
	ticks                int
	floorHits, floorMiss uint64
	appHits, appMiss     uint64
}

func runPowerProof(mode string) error {
	// brain.json write-through lands in a scratch GRAFEIO_HOME — the user's
	// real config is never touched by shots.
	home, err := os.MkdirTemp("", "grafeio-power")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	if err := os.Setenv("GRAFEIO_HOME", home); err != nil {
		return err
	}
	fmt.Printf("--- scratch GRAFEIO_HOME: %s ---\n", home)

	var modes []config.PowerMode
	switch config.PowerMode(mode) {
	case config.PowerAuto, config.PowerSaver, config.PowerPerformance:
		modes = []config.PowerMode{config.PowerMode(mode)}
	case "all":
		modes = []config.PowerMode{config.PowerAuto, config.PowerSaver, config.PowerPerformance}
	default:
		return fmt.Errorf("unknown power mode %q (auto|saver|performance|all)", mode)
	}

	const window = 6 * time.Second
	fmt.Printf("--- power windows (%s scripted quiet-after-burst window, same stub script per mode) ---\n", window)
	byMode := map[config.PowerMode]powerWindow{}
	for _, pm := range modes {
		cfg := config.Default()
		cfg.UI.Power = pm
		b := &stubBackend{done: make(chan struct{}), flushQueue: true}
		office.CacheReset()
		fm, err := runManualLoop(cfg, b, "agents", window, nil)
		if err != nil {
			return err
		}
		fh, fMiss := office.CacheStats()
		ah, aMiss := fm.FrameCacheStats()
		w := powerWindow{ticks: fm.Ticks(), floorHits: fh, floorMiss: fMiss, appHits: ah, appMiss: aMiss}
		byMode[pm] = w
		hitPct := 0.0
		if fh+fMiss > 0 {
			hitPct = 100 * float64(fh) / float64(fh+fMiss)
		}
		avg := window / time.Duration(w.ticks)
		fmt.Printf("  mode=%-11s ticks=%2d avg-delay=%7s  floor-cache: hits=%3d misses=%3d hit=%05.1f%%  app-frame: hits=%3d misses=%3d\n",
			pm, w.ticks, avg.Round(time.Millisecond), fh, fMiss, hitPct, ah, aMiss)
	}
	if len(modes) == 3 {
		a, s, p := byMode[config.PowerAuto], byMode[config.PowerSaver], byMode[config.PowerPerformance]
		if !(p.ticks > a.ticks && a.ticks > s.ticks) {
			return fmt.Errorf("tick ordering violated: want performance > auto > saver, got %d / %d / %d",
				p.ticks, a.ticks, s.ticks)
		}
		fmt.Printf("asserts: OK — performance(%d) > auto(%d) > saver(%d) ticks in the identical window\n",
			p.ticks, a.ticks, s.ticks)
	}
	if config.PowerMode(mode) != "all" {
		return nil
	}

	printTickTable()

	// slash /power + /model leg: busy typing placeholder carries the custom
	// boss short name; slash notices + brain.json write-through in-frame.
	cfg := config.Default()
	cfg.Boss.Name = "jorge (El Jefe)"
	sb := &stubBackend{done: make(chan struct{}), powerDemo: true}
	fm, err := runManualLoop(cfg, sb, "chat", 2400*time.Millisecond, func(send func(tea.Msg)) {
		typeLine := func(at time.Duration, s string) {
			go func() {
				time.Sleep(at)
				for _, r := range s {
					send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
					time.Sleep(8 * time.Millisecond)
				}
				send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			}()
		}
		typeLine(450*time.Millisecond, "/power")
		typeLine(800*time.Millisecond, "/power saver")
		typeLine(1200*time.Millisecond, "/model")
		typeLine(1600*time.Millisecond, "/model anthropic/claude-haiku-4-5")
	})
	if err != nil {
		return err
	}
	fmt.Println("===== UI SHOT · slash /power + /model (boss.name \"jorge (El Jefe)\", boss typing) =====")
	fmt.Println(fm.Frame())
	fmt.Println("===== UI SHOT =====")
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	if got := fm.Config(); got.UI.Power != config.PowerSaver {
		return fail("expected in-memory power=saver after /power saver, got %q", got.UI.Power)
	}
	if got := fm.Config(); got.Boss.Model != "anthropic/claude-haiku-4-5" {
		return fail("expected in-memory boss.model after /model, got %q", got.Boss.Model)
	}
	bts, rerr := os.ReadFile(config.Path())
	if rerr != nil {
		return fail("read persisted brain.json: %v", rerr)
	}
	if !strings.Contains(string(bts), `"power": "saver"`) || !strings.Contains(string(bts), `"model": "anthropic/claude-haiku-4-5"`) {
		return fail("persisted brain.json missing the /power + /model writes:\n%s", bts)
	}
	fmt.Printf("--- persisted brain.json (%s) ---\n%s", config.Path(), bts)
	fmt.Println("asserts: OK — /power saver honored + persisted, /model set + persisted, placeholder personalized")

	// custom-boss-name leg: the agents roster pins cfg.Boss.Name.
	nb := &stubBackend{done: make(chan struct{})}
	nfm, err := runManualLoop(cfg, nb, "agents", 1600*time.Millisecond, nil)
	if err != nil {
		return err
	}
	frame := nfm.Frame()
	if !strings.Contains(frame, "jorge (El Jefe)") {
		return fail("agents frame missing custom boss name")
	}
	fmt.Println("===== UI SHOT · custom boss name on the agents roster =====")
	fmt.Println(frame)
	fmt.Println("===== UI SHOT =====")
	return nil
}

// --- social-clock proof -----------------------------------------------------
// Deterministic: the model is pumped with SYNCHRONOUS EvTick updates (no
// tea.Program, no wall clock) and the SocialClock seeds its PRNG from
// tick+seq, so a repetition replays bit-for-bit — except package-global
// office walker state across reps, neutralized by prefixing employee IDs
// per rep (identical NAMES/seats/glyphs; the frame never shows the IDs).

// socialDriver — minimal synchronous model pump (one EvTick per step, the
// returned tea.Cmd is the tick re-arm timer; not needed here).
type socialDriver struct {
	m app.Model
}

func newSocialDriver(rep int) *socialDriver {
	backend := &stubBackend{done: make(chan struct{})} // Mode() only — no script
	m := app.New(backend, config.Default())
	d := &socialDriver{m: m}
	d.send(tea.WindowSizeMsg{Width: shotCols, Height: shotRows})
	pref := fmt.Sprintf("soc%d", rep)
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: pref + "-dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: pref + "-sco-1", Name: "skopos-1", Role: state.RoleScout, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: pref + "-rev-1", Name: "dikastes", Role: state.RoleReviewer, Sprite: state.SpriteAtDesk}})
	d.m.Frame() // lock the floor plan before any sprite advance
	return d
}

func (d *socialDriver) send(msg tea.Msg) {
	tm, _ := d.m.Update(msg)
	if fm, ok := tm.(app.Model); ok {
		d.m = fm
	}
}

func (d *socialDriver) pump(n int) {
	for i := 0; i < n; i++ {
		d.send(state.Event{Kind: state.EvTick})
	}
}

func (d *socialDriver) pumpUntil(desc string, maxTicks int, cond func(state.OfficeState) bool) error {
	for i := 0; i < maxTicks; i++ {
		if cond(d.m.State()) {
			return nil
		}
		d.pump(1)
	}
	return fmt.Errorf("social: %s did not happen within %d ticks (state: %s, trace missing)",
		desc, maxTicks, d.m.State().StatusLine)
}

func hasBubbleContaining(st state.OfficeState, sub string) bool {
	for _, b := range st.Bubbles {
		if strings.Contains(b.Text, sub) {
			return true
		}
	}
	return false
}

func countWalkingToCoffee(st state.OfficeState) int {
	n := 0
	for _, e := range st.Employees {
		if e.Sprite == state.SpriteToCoffee || e.Sprite == state.SpriteCoffee {
			n++
		}
	}
	return n
}

// socialFramesSim — the three-printed-frames run: hires, then the forced
// tea request (frame A: the ask bubble; frame B: both sprites walking),
// then the forced gossip chain (frame C: all three beats live). Also
// captures the forced gossip/banter chain traces (speaker › line).
func socialFramesSim(rep int) (frames [3]string, banter, gossip []string, err error) {
	var trace []string
	app.SocialTracef = func(format string, args ...any) {
		trace = append(trace, fmt.Sprintf(format, args...))
	}
	defer func() { app.SocialTracef = nil; app.SocialForceRoll = nil }()
	roll := 0
	app.SocialForceRoll = &roll

	d := newSocialDriver(rep)

	// SOCIAL A — tea request (roll 50): the ask bubble by A.
	roll = 50
	if err := d.pumpUntil("tea ask bubble", 1500, func(st state.OfficeState) bool {
		return hasBubbleContaining(st, "coffee?")
	}); err != nil {
		return frames, nil, nil, err
	}
	roll = -1 // force NOTHING while the sequence plays out (frames stay clean)
	frames[0] = d.m.Frame()

	// SOCIAL B — co-walking: A (t+2) then B (t+6) drift to the machine.
	if err := d.pumpUntil("both walkers to the tea machine", 200, func(st state.OfficeState) bool {
		return countWalkingToCoffee(st) >= 2
	}); err != nil {
		return frames, nil, nil, err
	}
	frames[1] = d.m.Frame()

	// SOCIAL C — gossip chain (roll 70): the PLAN emits its 3 trace lines at
	// once (plan time), so wait for the plan, then pump until the third
	// beat's tick (t0, +5, +10) has landed (+1) before freezing the frame.
	roll = 70
	if err := d.pumpUntil("gossip chain plan", 2500, func(st state.OfficeState) bool {
		n := 0
		for _, ln := range trace {
			if strings.HasPrefix(ln, "gossip: ") {
				n++
			}
		}
		return n >= 3
	}); err != nil {
		return frames, nil, nil, err
	}
	for _, ln := range trace {
		if strings.HasPrefix(ln, "gossip: ") {
			gossip = append(gossip, ln)
		}
	}
	roll = -1
	d.pump(12) // beats at +0/+5/+10 all armed and rendered
	frames[2] = d.m.Frame()

	// banter (roll 10): capture the chain for the PROOF section.
	roll = 10
	banterMark := len(trace)
	if err := d.pumpUntil("banter pair dialog", 2500, func(st state.OfficeState) bool {
		n := 0
		for _, ln := range trace {
			if strings.HasPrefix(ln, "banter: ") {
				n++
			}
		}
		return n >= 2
	}); err != nil {
		return frames, nil, nil, err
	}
	for _, ln := range trace[banterMark:] {
		if strings.HasPrefix(ln, "banter: ") {
			banter = append(banter, ln)
		}
	}
	return frames, banter, gossip, nil
}

// socialModalGateSim — the scripted-modal assert: a boss question opens the
// modal; across 400 pumped ticks NO social beat may fire (no trace, no
// bubbles, no walkers). After the modal resolves, the clock must resume
// (forced tea request lands).
func socialModalGateSim() error {
	var trace []string
	app.SocialTracef = func(format string, args ...any) {
		trace = append(trace, fmt.Sprintf(format, args...))
	}
	defer func() { app.SocialTracef = nil; app.SocialForceRoll = nil }()

	d := newSocialDriver(99)
	base := len(d.m.State().Employees)
	d.send(state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-soc",
		Text: "ship tonight or tomorrow?", ToolSummary: "tonight | tomorrow"})
	d.pump(400)
	st := d.m.State()
	if len(trace) != 0 {
		return fmt.Errorf("social fired while the question modal was open: %q", trace)
	}
	if len(st.Bubbles) != 0 {
		return fmt.Errorf("social bubble appeared while the question modal was open: %+v", st.Bubbles)
	}
	if n := countWalkingToCoffee(st); n != 0 {
		return fmt.Errorf("sprite walked to coffee while the question modal was open (%d walkers)", n)
	}
	if len(st.Employees) != base {
		return fmt.Errorf("roster changed unexpectedly (%d -> %d)", base, len(st.Employees))
	}
	// gate lifts when the modal closes
	d.send(state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-soc",
		ToolSummary: "answered", ToolState: "resolved"})
	roll := 50
	app.SocialForceRoll = &roll
	if err := d.pumpUntil("social resume after modal close", 1500, func(st state.OfficeState) bool {
		return hasBubbleContaining(st, "coffee?")
	}); err != nil {
		return err
	}
	fmt.Println("  modal gate: PASS — 400 ticks, no social beat while the question modal was open; resumed after resolve")
	return nil
}

func runSocialProof() error {
	if err := socialModalGateSim(); err != nil {
		return err
	}
	frames1, banter1, gossip1, err := socialFramesSim(1)
	if err != nil {
		return err
	}
	frames2, banter2, gossip2, err := socialFramesSim(2)
	if err != nil {
		return err
	}
	// determinism: tick-seeded — the two script runs must be byte-identical.
	for i := 0; i < 3; i++ {
		if frames1[i] != frames2[i] {
			return fmt.Errorf("social: frame %d differs between tick-seeded runs", i+1)
		}
	}
	if strings.Join(banter1, "\n") != strings.Join(banter2, "\n") ||
		strings.Join(gossip1, "\n") != strings.Join(gossip2, "\n") {
		return fmt.Errorf("social: banter/gossip chains differ between tick-seeded runs")
	}
	labels := [3]string{
		"SOCIAL A — tea request: A asks at their desk («<B>: coffee?»)",
		"SOCIAL B — co-walking: both sprites heading to the tea machine",
		"SOCIAL C — gossip chain: three bubbles fired over time, absent third named",
	}
	for i := 0; i < 3; i++ {
		fmt.Printf("===== UI SHOT · %s =====\n", labels[i])
		fmt.Println(frames1[i])
		fmt.Println("===== UI SHOT =====")
	}
	fmt.Println("--- gossip chain (3 beats, absent third named) ---")
	for _, ln := range gossip1 {
		fmt.Println("  " + ln)
	}
	fmt.Println("--- banter chain (one pair dialog, role-banked) ---")
	for _, ln := range banter1 {
		fmt.Println("  " + ln)
	}
	fmt.Println("asserts: OK — modal gate held, tea co-walk fired, gossip 3-beat chain fired, tick-seeded runs byte-identical")
	return nil
}

// --- layout-modes proof (--layout) ------------------------------------------
// THREE frames over the identical scripted window + identical config base,
// differing ONLY by the layout knobs: NORMAL (defaults, sidebar 44),
// compact (ui.compact → sidebar 30, short tab labels, 2-row chat input,
// compressed topbar) and wide 56 (ui.sidebarWidth). Each frame prints its
// computed geometry and passes width/label asserts.

type layoutLeg struct {
	name        string
	mutate      func(cfg *config.Config)
	wantSidebar int
	compact     bool
}

func runLayoutProof() error {
	legs := []layoutLeg{
		{"NORMAL", func(*config.Config) {}, 44, false},
		{"compact", func(c *config.Config) { c.UI.Compact = true }, 30, true},
		{"wide 56", func(c *config.Config) { c.UI.SidebarWidth = 56 }, 56, false},
	}
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	for _, leg := range legs {
		cfg := config.Default()
		leg.mutate(cfg)
		b := &stubBackend{done: make(chan struct{})}
		fm, err := runManualLoop(cfg, b, "chat", 2200*time.Millisecond, nil)
		if err != nil {
			return err
		}
		w, h, side, floor := fm.LayoutInfo()
		frame := fm.Frame()
		fmt.Printf("===== UI SHOT · layout %s =====\n", leg.name)
		fmt.Println(frame)
		fmt.Println("===== UI SHOT =====")
		fmt.Printf("computed: cols=%d rows=%d sidebar=%d floorW=%d\n", w, h, side, floor)
		if w != shotCols || h != shotRows {
			return fail("[%s] geometry drift: cols=%d rows=%d (want %dx%d)", leg.name, w, h, shotCols, shotRows)
		}
		if side != leg.wantSidebar {
			return fail("[%s] sidebar=%d, want %d", leg.name, side, leg.wantSidebar)
		}
		if floor != shotCols-leg.wantSidebar {
			return fail("[%s] floorW=%d, want %d (sidebar %d)", leg.name, floor, shotCols-leg.wantSidebar, leg.wantSidebar)
		}
		if leg.compact {
			// the 30-col sidebar drops to padded bare short labels
			// (" c  t  a  b  m  x ") — numbers never fit six tabs at 30.
			for _, short := range []string{" c ", " t ", " x "} {
				if !strings.Contains(frame, short) {
					return fail("[compact] tab bar missing short label %q", short)
				}
			}
			if strings.Contains(frame, "chat") || strings.Contains(frame, "terminal") {
				return fail("[compact] tab bar still shows a full tab name")
			}
			if strings.Contains(frame, "DEMO") {
				return fail("[compact] topbar still carries the mode segment (should compress to agents + clock)")
			}
			fmt.Println("asserts: OK — sidebar 30, compact tab labels (c t a b m x), compressed topbar, 2-row chat input")
		} else {
			if !strings.Contains(frame, "terminal") || !strings.Contains(frame, "activity") {
				return fail("[%s] tab bar missing full tab labels (want \"terminal\" + \"activity\" visible — six tabs must never clip)", leg.name)
			}
			if !strings.Contains(frame, "DEMO") {
				return fail("[%s] normal topbar lost the mode segment", leg.name)
			}
			fmt.Printf("asserts: OK — sidebar %d, all six full tab labels visible, full topbar (mode segment present)\n", leg.wantSidebar)
		}
	}
	fmt.Println("asserts: OK — 44 (default) / 30 (compact) / 56 (wide 56) sidebars; floor = 130 - sidebar in every leg")
	return nil
}

// --- terminal-tab proof (--terminal) ----------------------------------------
// The stub TermPanel (uisshot ONLY) wires through app.SpawnTerminal — the
// production wiring point where cmd/grafeio will plug panels.NewTerminal.

func runTerminalShot() (app.Model, *terminalPanelStub, int, error) {
	var zero app.Model
	calls := 0
	var stub *terminalPanelStub
	app.SpawnTerminal = func(cols, rows int) (app.TerminalTab, error) {
		calls++
		stub = newTerminalPanelStub(cols, rows)
		return stub, nil
	}
	backend := &stubBackend{done: make(chan struct{})}
	m := app.New(backend, config.Default())
	if !m.SelectTab("terminal") {
		return zero, nil, 0, fmt.Errorf("unknown tab %q", "terminal")
	}
	p := tea.NewProgram(m,
		tea.WithWindowSize(shotCols, shotRows),
		tea.WithoutRenderer(),
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	)
	emit := func(ev state.Event) { p.Send(ev) }
	if err := backend.Start(emit); err != nil {
		return zero, nil, 0, err
	}
	go queueWorkload(p) // types 3 lines at ~3s — must land in the "shell"
	go func() {
		time.Sleep(shotDur)
		p.Quit()
	}()
	final, err := p.Run()
	if err != nil {
		return zero, nil, 0, err
	}
	fm, ok := final.(app.Model)
	if !ok {
		return zero, nil, 0, fmt.Errorf("unexpected final model type %T", final)
	}
	return fm, stub, calls, nil
}

func runTerminalProof() error {
	fm, stub, calls, err := runTerminalShot()
	if err != nil {
		return err
	}
	frame := fm.Frame()
	app.SpawnTerminal = nil
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	fmt.Println("===== UI SHOT · terminal tab (uisshot stub TermPanel) =====")
	fmt.Println(frame)
	fmt.Println("===== UI SHOT =====")
	if calls != 1 {
		return fail("lazy-spawn violated: SpawnTerminal factory called %d times (want exactly 1, on first visit)", calls)
	}
	if stub == nil {
		return fail("no stub panel was spawned")
	}
	if !strings.Contains(frame, "uisshot STUB shell") {
		return fail("frame missing the stub terminal's content marker")
	}
	if !strings.Contains(frame, "terminal") {
		return fail("frame missing the \"terminal\" tab label")
	}
	fmt.Printf("lazy-spawn: OK — SpawnTerminal called exactly once, on the first visit\n")
	fmt.Printf("key routing: %d keystrokes routed into the shell surface (queue workload typing at ~3s)\n", stub.received)
	if stub.received == 0 {
		return fail("no keystrokes reached the terminal panel — routing broken")
	}
	if stub.closed {
		return fail("stub already closed before CloseTerminal — quit hook ran early")
	}
	// quit hook: cmd/grafeio calls CloseTerminal after p.Run returns (the
	// runtime intercepts tea.QuitMsg before Update, so p.Quit() never
	// reached handleKey here — the explicit close is the leak guard)
	fm.CloseTerminal()
	if !stub.closed {
		return fail("CloseTerminal did not close the shell panel")
	}
	fmt.Printf("quit hook: OK — CloseTerminal closed the spawned shell (alive→false)\n")
	return nil
}

// --- fix-wave proof (--focus) ----------------------------------------------
// Three frames over ONE synchronous driver (no tea.Program, no wall clock):
// every EvTick is pumped by hand, so the panel state is exact. Frame A
// catches the empty typing placeholder: the typing row sits BELOW the
// divider (first row above the input region), no "▌" anywhere. Frame B
// catches a streaming partial bubble: the text grows in the viewport and
// the typing row STAYS below the divider for the whole pending period —
// still no caret. Frame C catches two concurrent agents: employee tool
// calls grouped into per-agent work threads (headers + merged rows), the
// boss's own tool line still inline, and the boss quiet at its
// placeholder with workers busy → BossDelegating ("boss: delegating ·
// 2 busy" + [delegat] nameplate). Every frame also asserts the chat
// panel's rows stay inside the width the divider draws (wrap, never
// overflow, never clip).

// focusDriver — minimal synchronous model pump (same shape as socialDriver).
type focusDriver struct {
	m app.Model
}

func newFocusDriver() *focusDriver {
	backend := &stubBackend{done: make(chan struct{})} // Mode() only — no script
	m := app.New(backend, config.Default())
	d := &focusDriver{m: m}
	d.send(tea.WindowSizeMsg{Width: shotCols, Height: shotRows})
	return d
}

func (d *focusDriver) send(msg tea.Msg) {
	tm, _ := d.m.Update(msg)
	if fm, ok := tm.(app.Model); ok {
		d.m = fm
	}
}

func (d *focusDriver) pump(n int) {
	for i := 0; i < n; i++ {
		d.send(state.Event{Kind: state.EvTick})
	}
}

func focusTool(ownerID, ownerName, callID, toolName, summary, toolState string) state.Event {
	return state.Event{Kind: state.EvTool, EmployeeID: ownerID, EmployeeName: ownerName,
		ToolName: toolName, ToolSummary: summary, ToolState: toolState, CallID: callID}
}

// chatPanelSegs yields the chat panel's interior segment of every full
// frame line — the slice between the line's LAST two "│" borders (the
// chat sidebar is the rightmost panel), ansi-stripped for width math.
// Rows without panel borders (topbar/statusbar) are skipped, so indexes
// are panel-relative, not screen rows.
func chatPanelSegs(frame string) []string {
	var segs []string
	for _, ln := range strings.Split(frame, "\n") {
		parts := strings.Split(ansi.Strip(ln), "│")
		if len(parts) < 3 {
			continue
		}
		segs = append(segs, parts[len(parts)-2])
	}
	return segs
}

// chatDividerIdx — the segs index of the chat divider row (a segment of
// pure "─"), -1 when absent.
func chatDividerIdx(segs []string) int {
	for i, seg := range segs {
		t := strings.TrimSpace(seg)
		if t != "" && strings.Trim(t, "─") == "" {
			return i
		}
	}
	return -1
}

// chatPanelTail — the chat panel's segments from the divider down (the
// divider row itself first), capped at n rows.
func chatPanelTail(frame string, n int) []string {
	segs := chatPanelSegs(frame)
	if di := chatDividerIdx(segs); di >= 0 {
		tail := segs[di:]
		if len(tail) > n {
			tail = tail[:n]
		}
		return tail
	}
	return nil
}

// assertChatLayout — the panel hygiene sweep shared by every focus frame:
// no "▌" anywhere (the blinking caret is gone in EVERY state), and every
// chat row stays inside the width the divider row itself draws (wrap,
// never overflow).
func assertChatLayout(tag, frame string) error {
	if strings.Contains(frame, "▌") {
		return fmt.Errorf("%s: found \"▌\" — the stream caret must not exist in any chat state", tag)
	}
	segs := chatPanelSegs(frame)
	di := chatDividerIdx(segs)
	if di < 0 {
		return fmt.Errorf("%s: no chat divider row found", tag)
	}
	budget := ansi.StringWidth(segs[di])
	for i, seg := range segs {
		if w := ansi.StringWidth(seg); w > budget {
			return fmt.Errorf("%s: chat row %d overflows the %d-cell panel budget (%d cells): %q", tag, i, budget, w, strings.TrimSpace(seg))
		}
	}
	return nil
}

// assertTypingRowBelowDivider — the row carrying needle (typing spinner /
// delegating line) sits EXACTLY one segs-step under the divider: the
// first row of the input region, above chips/picker/textarea. While the
// boss is busy the TEXTAREA PLACEHOLDER quotes the same busy text
// ("› boss is typing…") — those prompt rows are skipped; the typing row
// is the one renderer-owned line carrying it (exactly one must exist).
func assertTypingRowBelowDivider(tag, frame, needle string) error {
	segs := chatPanelSegs(frame)
	di := chatDividerIdx(segs)
	if di < 0 {
		return fmt.Errorf("%s: no chat divider row found", tag)
	}
	row := -1
	for i, seg := range segs {
		if !strings.Contains(seg, needle) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(seg), "›") {
			continue // textarea placeholder quoting the busy text — a prompt row, not the typing row
		}
		if row >= 0 {
			return fmt.Errorf("%s: %q appears on MORE than one chat row", tag, needle)
		}
		row = i
	}
	if row < 0 {
		return fmt.Errorf("%s: typing row %q missing from the chat panel", tag, needle)
	}
	if row != di+1 {
		return fmt.Errorf("%s: typing row must be the FIRST row below the divider (divider at seg %d, row at %d)", tag, di, row)
	}
	return nil
}

func runFocusProof() error {
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	d := newFocusDriver()
	d.send(state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — focus stub online"})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "sco-1", Name: "skopos-1", Role: state.RoleScout, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"wire the sse stream", false)})
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-1", "boss", "", true)})
	d.pump(4) // tick 4 — settled placeholder
	fmt.Println("===== UI SHOT · FOCUS A — empty pending bubble: typing row BELOW the divider, NO caret =====")
	frameA := d.m.Frame()
	fmt.Println(frameA)
	fmt.Println("===== UI SHOT =====")

	// (b) the reply streams in as accumulated pending updates; the
	// placeholder boss-1 is replaced by the first real-bubble update.
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("bossmsg-m1", "boss",
		"Wiring the handler —", true)})
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("bossmsg-m1", "boss",
		"Wiring the handler — the **SSE stream** fans out to both workers now.", true)})
	d.pump(2) // tick 6 — mid-stream
	fmt.Println("===== UI SHOT · FOCUS B — streaming bubble grows in the viewport; typing row STAYS below the divider, NO caret =====")
	frameB := d.m.Frame()
	fmt.Println(frameB)
	fmt.Println("===== UI SHOT =====")
	fmt.Println("--- chat panel, divider row down to the input (frame B, ansi-stripped segments) ---")
	for _, seg := range chatPanelTail(frameB, 6) {
		fmt.Println(seg)
	}

	// (c) settle the round-1 bubble, dispatch two workers, storm tool
	// calls for BOTH (interleaved — merge per agent+CallID), one boss
	// inline tool, then a fresh user turn whose placeholder goes quiet.
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("bossmsg-m1", "boss",
		"Wiring the handler — the **SSE stream** fans out to both workers now. Watch their threads below.", false)})
	d.send(state.Event{Kind: state.EvDispatch, EmployeeID: "dev-1",
		Task: state.BoardTask{ID: "t1", Title: "Wire the SSE stream", At: time.Now().UnixMilli()}})
	d.send(state.Event{Kind: state.EvWorking, EmployeeID: "dev-1", TaskID: "t1"})
	d.send(state.Event{Kind: state.EvDispatch, EmployeeID: "sco-1",
		Task: state.BoardTask{ID: "t2", Title: "Scan the repo", At: time.Now().UnixMilli()}})
	d.send(state.Event{Kind: state.EvWorking, EmployeeID: "sco-1", TaskID: "t2"})
	d.send(focusTool("dev-1", "tekton-1", "call-t1", "read", "internal/room/manager.go", "running"))
	d.send(focusTool("sco-1", "skopos-1", "call-s1", "grep", "SSE, 12 hits", "running"))
	d.send(focusTool("dev-1", "tekton-1", "call-t1", "read", "internal/room/manager.go", "done"))
	d.send(focusTool("sco-1", "skopos-1", "call-s1", "grep", "SSE, 12 hits", "done"))
	d.send(focusTool("dev-1", "tekton-1", "call-t2", "edit", "internal/room/handler.go", "running"))
	d.send(focusTool("sco-1", "skopos-1", "call-s2", "read", "internal/api/room.go", "done"))
	d.send(focusTool("boss", "boss", "call-b1", "write", "static/sse.html", "done"))
	d.send(state.Event{Kind: state.EvChatUser, Msg: chatMsg("u2", "user",
		"and the reconnect backoff", false)})
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("boss-2", "boss", "", true)})
	d.pump(10) // tick 16 — boss quiet for 10 ticks, both workers busy
	fmt.Println("===== UI SHOT · FOCUS C — concurrent agents grouped into work threads, boss delegating =====")
	frameC := d.m.Frame()
	fmt.Println(frameC)
	fmt.Println("===== UI SHOT =====")

	// asserts — frame A: typing row below the divider, no caret anywhere,
	// no delegation while the boss itself is generating.
	for _, want := range []string{"is typing…"} {
		if !strings.Contains(frameA, want) {
			return fail("focus A: frame missing %q", want)
		}
	}
	if strings.Contains(frameA, "delegating") {
		return fail("focus A: empty pending placeholder shows delegation text")
	}
	if err := assertTypingRowBelowDivider("focus A", frameA, "is typing…"); err != nil {
		return err
	}
	if err := assertChatLayout("focus A", frameA); err != nil {
		return err
	}
	// asserts — frame B: a STREAMING bubble keeps the typing row for the
	// whole pending period — the partial text lives in the viewport, the
	// pulse stays below the divider. No caret in any state.
	for _, want := range []string{"is typing…", "SSE stream"} {
		if !strings.Contains(frameB, want) {
			return fail("focus B: frame missing %q (streaming text in the viewport AND the typing row at once)", want)
		}
	}
	if err := assertTypingRowBelowDivider("focus B", frameB, "is typing…"); err != nil {
		return err
	}
	if err := assertChatLayout("focus B", frameB); err != nil {
		return err
	}
	// asserts — frame C
	for _, want := range []string{
		"┌ tekton-1 · Wire the SSE stream",           // per-agent thread header (task from dispatch)
		"┌ skopos-1 · Scan the repo",                 // second agent, newer at the bottom
		"│ [tool] read · internal/room/manager.go ✓", // merged running→done, one row
		"│ [tool] grep · SSE, 12 hits ✓",
		"[tool] write · static/sse.html ✓", // boss's own tool stays INLINE (no thread)
		"delegating · 2 busy",              // settled placeholder text (no spinner)
		"[delegat]",                        // floor nameplate
		"reconnect backoff",                // round-2 user turn survived the storm
	} {
		if !strings.Contains(frameC, want) {
			return fail("focus C: frame missing %q", want)
		}
	}
	if strings.Contains(frameC, "│ [tool] write") {
		return fail("focus C: boss tool line was captured into a worker thread (must stay inline)")
	}
	// the delegating row rides the SAME below-divider slot as the typing
	// row (a settled swap of it)
	if err := assertTypingRowBelowDivider("focus C", frameC, "delegating · 2 busy"); err != nil {
		return err
	}
	if err := assertChatLayout("focus C", frameC); err != nil {
		return err
	}
	// collapse leg: tekton-1 returns (sprite leaves the busy set) → its
	// thread AUTO-COLLAPSES to the one-line summary; ctrl+g expands ALL
	// completed threads again.
	d.send(state.Event{Kind: state.EvReturned, EmployeeID: "dev-1", TaskID: "t1",
		Mail: mail("m1", "tekton-1", "boss", "return: sse stream", "stream is live.", state.MailReturn)})
	d.pump(1)
	frameCollapse := d.m.Frame()
	if !strings.Contains(frameCollapse, "tekton-1 · Wire the SSE stream (· 2 tool calls ✓ done)") {
		return fail("focus collapse: missing auto-collapsed thread summary for tekton-1")
	}
	if strings.Contains(frameCollapse, "┌ tekton-1") {
		return fail("focus collapse: tekton-1 thread still expanded after EvReturned")
	}
	if !strings.Contains(frameCollapse, "┌ skopos-1") {
		return fail("focus collapse: skopos-1 thread collapsed too — only the RETURNED agent should collapse")
	}
	d.send(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	frameExpanded := d.m.Frame()
	if !strings.Contains(frameExpanded, "┌ tekton-1 · Wire the SSE stream") ||
		!strings.Contains(frameExpanded, "│ [tool] read · internal/room/manager.go ✓") {
		return fail("focus expand: ctrl+g did not re-expand the completed thread")
	}
	fmt.Println("asserts: OK — no caret in any state; typing row sits below the divider (above the input) for the WHOLE pending period; delegating row swaps into the same slot; every chat row inside the divider's width budget; worker threads grouped + CallID-merged, boss tool inline, [delegat] nameplate, EvReturned auto-collapses, ctrl+g re-expands completed threads")
	return nil
}

// runPersistDemoSkipProof (--persist) — the office-session DEMO regression:
// restore + persist are LIVE-only by ruling (demo restore = confusing).
// Seeds a FRESH session.json for cwd in a scratch GRAFEIO_HOME (so the
// "skip" cannot be a missing-file false pass), runs the standard scripted
// demo shot, then asserts: (1) LoadSession DOES find the seeded file
// (the gate is the mode check, not the file lookup), (2) NO "restored
// office session" notice ever surfaces in the office state chat, (3) the
// demo boot never OVERWRITES the seeded file (SavedAt byte-identical).
func runPersistDemoSkipProof() error {
	home, err := os.MkdirTemp("", "grafeio-persist-demo-skip")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	if err := os.Setenv("GRAFEIO_HOME", home); err != nil {
		return err
	}
	fmt.Printf("--- scratch GRAFEIO_HOME: %s ---\n", home)

	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	seed := app.Snapshot(dir, "ses-demo-fake", state.OfficeState{
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "old turn from a previous run"},
			{ID: "b1", From: "boss", Kind: "boss", Text: "the previous reply"},
		},
	})
	if err := app.SaveSession(dir, seed); err != nil {
		return err
	}
	before, err := os.ReadFile(app.SessionPath(dir))
	if err != nil {
		return err
	}
	if _, ok := app.LoadSession(dir); !ok {
		return fmt.Errorf("seeded session.json not loadable — the skip assert would be vacuous")
	}

	// The standard shot: the REAL app model over the scripted stub (demo
	// mode) for the full window.
	backend := &stubBackend{done: make(chan struct{}), flushQueue: true}
	fm, err := runManualLoop(config.Default(), backend, "chat", shotDur, nil)
	if err != nil {
		return err
	}
	frame := fm.Frame()
	fmt.Println("===== UI SHOT · --persist (demo boot with a seeded session.json — restore must NOT fire) =====")
	fmt.Println(frame)
	fmt.Println("===== UI SHOT =====")

	// (2) no restore notice in the office chat state.
	for _, c := range fm.State().Chat {
		if strings.Contains(c.Text, "restored office session from") {
			return fmt.Errorf("demo boot restored the seeded office session (restore is live-only): %q", c.Text)
		}
	}
	// (2b) the demo script ran normally — its scripted turn is present.
	found := false
	for _, c := range fm.State().Chat {
		if strings.Contains(c.Text, "hello.html") {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("the demo script did not run (no hello.html turn) — cannot claim a clean demo boot")
	}
	// (3) the demo boot never persisted over the seeded file.
	after, err := os.ReadFile(app.SessionPath(dir))
	if err != nil {
		return err
	}
	if string(before) != string(after) {
		return fmt.Errorf("demo boot overwrote session.json (persist is live-only)")
	}
	fmt.Println("asserts: OK — seeded session.json found by LoadSession but demo mode skipped restore, no restore notice in chat, file untouched (live-only)")
	fmt.Println("PERSIST-DEMO-SKIP: OK")
	return nil
}

// --- slash popover proof (--slashpop) --------------------------------------
// Synchronous keys through the REAL app model: typing "/" at a word start
// opens the command popover, "/th" prefix-filters it live, Enter on /theme
// pre-fills "/theme " and flips the box into the THEME picker, arrows apply
// a LIVE preview (two states printed), esc cancels back to the original
// theme, Enter commits through the plain /theme slash path (persist +
// office notice).

// drainCmd bounded-drives a returned cmd tree (the slash commit runs
// through onSend → slashMsg): timer arms (tick/blink) are skipped after a
// short wait — the app re-arms them on its own events; the proof only
// needs the produced MESSAGES.
func drainCmd(d *focusDriver, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 8 {
		return
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(150 * time.Millisecond):
		return // a timer arm (tick/cursor blink): not a message the proof needs
	}
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drainCmd(d, c, depth+1)
		}
		return
	}
	d.send(msg)
}

func runSlashPopProof() error {
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	d := newFocusDriver()
	names := chrome.ThemeNames()
	orig := chrome.CurrentTheme().Name
	defer func() { // leave the machine as found (theme file included)
		chrome.SetTheme(orig)
		office.SetTheme(orig)
		_ = chrome.PersistTheme()
	}()
	if len(names) < 3 {
		return fail("slashpop: need ≥3 themes registered, got %d", len(names))
	}
	typeIn := func(s string) {
		for _, r := range s {
			d.send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		}
	}
	key := func(code rune) tea.Cmd {
		tm, c := d.m.Update(tea.KeyPressMsg(tea.Key{Code: code}))
		if fm, ok := tm.(app.Model); ok {
			d.m = fm
		}
		return c
	}

	// frame 1 — "/th": filtered command menu (/theme /themes /thinking)
	typeIn("/th")
	fmt.Println("===== UI SHOT · SLASH A — typed \"/th\": the popover prefix-filters to /theme /themes /thinking =====")
	frameA := d.m.Frame()
	fmt.Println(frameA)
	fmt.Println("===== UI SHOT =====")
	for _, want := range []string{"commands", "› /theme", "/themes", "/thinking", "switch theme (persists)", "/theme <name>"} {
		if !strings.Contains(frameA, want) {
			return fail("slashpop A: frame missing %q", want)
		}
	}
	for _, not := range []string{"/tools", "/clear", "/zen"} {
		if strings.Contains(frameA, not) {
			return fail("slashpop A: unfiltered command %q still shows for fragment \"/th\"", not)
		}
	}

	// Enter applies /theme → "/theme " prefill + the THEME picker opens
	key(tea.KeyEnter)
	fmt.Println("===== UI SHOT · SLASH B — Enter on /theme: \"› /theme \" prefill, live theme list =====")
	frameB := d.m.Frame()
	fmt.Println(frameB)
	fmt.Println("===== UI SHOT =====")
	for _, want := range []string{"theme preview", "noir", "paper", "dracula", "commit + persist"} {
		if !strings.Contains(frameB, want) {
			return fail("slashpop B: frame missing %q", want)
		}
	}
	if !strings.Contains(ansi.Strip(frameB), "› /theme ") {
		return fail("slashpop B: textarea prefill %q missing (raw frame is style-split)", "› /theme ")
	}
	if cur := chrome.CurrentTheme().Name; cur != orig {
		return fail("slashpop B: theme switched on Enter-apply (%s) — preview must wait for arrows", cur)
	}

	// THEME PREVIEW state 1 — one ↓: the second theme paints live
	key(tea.KeyDown)
	p1 := chrome.CurrentTheme().Name
	fmt.Printf("===== UI SHOT · SLASH C — preview state 1 (↓ once): active theme is now %q — PAINTED LIVE =====\n", p1)
	frameC := d.m.Frame()
	fmt.Println(frameC)
	fmt.Println("===== UI SHOT =====")
	if p1 != names[1] {
		return fail("slashpop C: expected live preview %q after one ↓, got %q", names[1], p1)
	}

	// THEME PREVIEW state 2 — another ↓: the third theme paints live
	key(tea.KeyDown)
	p2 := chrome.CurrentTheme().Name
	fmt.Printf("===== UI SHOT · SLASH D — preview state 2 (↓ again): active theme is now %q — PAINTED LIVE =====\n", p2)
	frameD := d.m.Frame()
	fmt.Println(frameD)
	fmt.Println("===== UI SHOT =====")
	if p2 != names[2] {
		return fail("slashpop D: expected live preview %q after two ↓, got %q", names[2], p2)
	}

	// esc cancels the preview session back to the original theme
	key(tea.KeyEscape)
	if cur := chrome.CurrentTheme().Name; cur != orig {
		return fail("slashpop esc: preview was not cancelled back — got %q, want %q", cur, orig)
	}

	// retype to re-open in theme mode, ↓ previews the second theme, Enter
	// COMMITS through the plain /theme path (persist + office notice)
	for i := 0; i < 20; i++ {
		key(tea.KeyBackspace)
	}
	typeIn("/theme ")
	key(tea.KeyDown)
	picked := chrome.CurrentTheme().Name
	drainCmd(d, key(tea.KeyEnter), 0)
	fmt.Println("===== UI SHOT · SLASH E — after commit: draft cleared, office notice \"theme → …\" =====")
	frameE := d.m.Frame()
	fmt.Println(frameE)
	fmt.Println("===== UI SHOT =====")
	if picked != names[1] {
		return fail("slashpop E: commit leg previews %q after one ↓, got %q", names[1], picked)
	}
	if !strings.Contains(frameE, "theme → "+picked) {
		return fail("slashpop E: office notice %q missing after commit", "theme → "+picked)
	}
	if cur := chrome.CurrentTheme().Name; cur != picked {
		return fail("slashpop E: committed theme %q not active (%q)", picked, cur)
	}
	fmt.Println("asserts: OK — \"/\" opens the popover, \"/th\" filters live, /theme prefill flips to the theme picker, arrows preview live (two states printed), esc cancels back, enter commits + persists via the plain slash path")
	return nil
}

// --- employee thinking inside worker threads (--threads-think) --------------
// tekton-1's EvThought entries merge per CallID into its OWN work thread:
// a dim-italic "thinking · N lines" row among the tool rows while the
// thread is live, a "· N think" count in the collapsed summary after
// EvReturned, a full body under ctrl+g. The boss's EvThought path stays
// byte-identical (flow "thinking · N lines", no thread).

func runThreadsThinkProof() error {
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	d := newFocusDriver()
	d.send(state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — threads-think stub online"})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"wire the sse stream", false)})
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("bossmsg-m1", "boss",
		"On it — tekton-1 is wiring the handler.", false)})
	// boss think: unchanged flow rendering (proves the boss path is intact)
	d.send(state.Event{Kind: state.EvThought, EmployeeID: "boss", CallID: "bk-1",
		Text: "weighing the fan-out", Done: false})
	d.send(state.Event{Kind: state.EvThought, EmployeeID: "boss", CallID: "bk-1",
		Text: "weighing the fan-out\nsent it out", Done: true})
	// tekton-1: dispatch, tools, AND a streamed employee thought
	d.send(state.Event{Kind: state.EvDispatch, EmployeeID: "dev-1",
		Task: state.BoardTask{ID: "t1", Title: "Wire the SSE stream", At: time.Now().UnixMilli()}})
	d.send(state.Event{Kind: state.EvWorking, EmployeeID: "dev-1", TaskID: "t1"})
	d.send(focusTool("dev-1", "tekton-1", "call-t1", "read", "internal/room/manager.go", "done"))
	d.send(state.Event{Kind: state.EvThought, EmployeeID: "dev-1", EmployeeName: "tekton-1", CallID: "tk-1",
		Text: "scanning options\nchoosing approach", Done: false})
	d.send(focusTool("dev-1", "tekton-1", "call-t2", "edit", "internal/room/handler.go", "done"))
	d.send(state.Event{Kind: state.EvThought, EmployeeID: "dev-1", EmployeeName: "tekton-1", CallID: "tk-1",
		Text: "scanning options\nchoosing approach\nwriting the patch", Done: true})
	d.pump(4)
	fmt.Println("===== UI SHOT · THINK A — live thread: employee thought merged per CallID as \"thinking · N lines\" =====")
	frameA := d.m.Frame()
	fmt.Println(frameA)
	fmt.Println("===== UI SHOT =====")
	for _, want := range []string{
		"┌ tekton-1 · Wire the SSE stream",           // thread header
		"│ [tool] read · internal/room/manager.go ✓", // tool row, unchanged
		"│ thinking · 3 lines",                       // employee thought, one merged row
	} {
		if !strings.Contains(frameA, want) {
			return fail("threads-think A: frame missing %q", want)
		}
	}
	if !strings.Contains(ansi.Strip(frameA), "thinking · 2 lines") {
		return fail("threads-think A: boss thought missing its collapsed flow row (style-split raw text)")
	}
	if strings.Contains(ansi.Strip(frameA), "│ thinking · 2 lines") {
		return fail("threads-think A: the BOSS's thought leaked into a worker thread")
	}
	if strings.Contains(frameA, "writing the patch") {
		return fail("threads-think A: live view must show the one-line count, not the body")
	}

	// EvReturned → collapsed summary KEEPS the think count
	d.send(state.Event{Kind: state.EvReturned, EmployeeID: "dev-1", TaskID: "t1",
		Mail: mail("m1", "tekton-1", "boss", "return: sse stream", "stream is live.", state.MailReturn)})
	d.pump(1)
	fmt.Println("===== UI SHOT · THINK B — collapsed thread: \"· 2 tool calls · 1 think ✓ done\" =====")
	frameB := d.m.Frame()
	fmt.Println(frameB)
	fmt.Println("===== UI SHOT =====")
	if !strings.Contains(frameB, "tekton-1 · Wire the SSE stream (· 2 tool calls · 1 think ✓ done)") {
		return fail("threads-think B: collapsed summary with the think count missing")
	}
	if strings.Contains(frameB, "│ thinking") {
		return fail("threads-think B: think row visible under a collapsed thread")
	}

	// ctrl+g: full expand covers tools AND thoughts (body in natural order)
	d.send(tea.KeyPressMsg(tea.Key{Code: 'g', Mod: tea.ModCtrl}))
	fmt.Println("===== UI SHOT · THINK C — ctrl+g: full expand covers tools AND the thinking body =====")
	frameC := d.m.Frame()
	fmt.Println(frameC)
	fmt.Println("===== UI SHOT =====")
	for _, want := range []string{
		"┌ tekton-1 · Wire the SSE stream",
		"│ [tool] edit · internal/room/handler.go ✓",
		"│ thinking",
		"writing the patch", // body renders on full expand
	} {
		if !strings.Contains(frameC, want) {
			return fail("threads-think C: frame missing %q", want)
		}
	}
	// natural order: read row < think body < edit row (chat arrival order —
	// the thought merged in place between the two tool calls)
	if strings.Index(frameC, "│ [tool] read") > strings.Index(frameC, "writing the patch") ||
		strings.Index(frameC, "writing the patch") > strings.Index(frameC, "│ [tool] edit") {
		return fail("threads-think C: think body must sit in natural chat order (between the read and edit rows)")
	}
	fmt.Println("asserts: OK — employee EvThought merges per CallID into the agent's thread (live one-liner), collapsed summary keeps the count, ctrl+g expands tools + thoughts in natural order, boss path byte-identical")
	return nil
}

// --- clickable agents (--click) ----------------------------------------------
// Scripted bubbletea v2 mouse clicks through the REAL model: (S) a click on
// tekton-1's floor sprite selects it — activity tab opens, the agents tab
// pins a ▸ marker, an office notice names it; (D) a double-click on the
// same sprite toggles that agent's chat thread (and jumps there); (H) a
// click on a worker thread's "┌" header row in chat toggles it too.

// clickPairGap — the model's double-click window (400ms); the proof sleeps
// past it between the select phase and the double-click phase.
const clickPairGap = 400 * time.Millisecond

func runClickProof() error {
	fail := func(format string, args ...any) error { return fmt.Errorf(format, args...) }
	d := newFocusDriver()
	d.send(state.Event{Kind: state.EvStatus, Text: "[grafeio] demo — click stub online"})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "sco-1", Name: "skopos-1", Role: state.RoleScout, Sprite: state.SpriteAtDesk}})
	d.send(state.Event{Kind: state.EvChatUser, Msg: chatMsg("u1", "user",
		"wire the sse stream", false)})
	d.send(state.Event{Kind: state.EvChatBoss, Msg: chatMsg("bossmsg-m1", "boss",
		"Both workers are on it.", false)})
	d.send(state.Event{Kind: state.EvDispatch, EmployeeID: "dev-1",
		Task: state.BoardTask{ID: "t1", Title: "Wire the SSE stream", At: time.Now().UnixMilli()}})
	d.send(state.Event{Kind: state.EvWorking, EmployeeID: "dev-1", TaskID: "t1"})
	d.send(state.Event{Kind: state.EvDispatch, EmployeeID: "sco-1",
		Task: state.BoardTask{ID: "t2", Title: "Scan the repo", At: time.Now().UnixMilli()}})
	d.send(state.Event{Kind: state.EvWorking, EmployeeID: "sco-1", TaskID: "t2"})
	d.send(focusTool("dev-1", "tekton-1", "call-t1", "read", "internal/room/manager.go", "done"))
	d.send(focusTool("sco-1", "skopos-1", "call-s1", "grep", "SSE, 12 hits", "done"))
	d.send(focusTool("dev-1", "tekton-1", "call-t2", "edit", "internal/room/handler.go", "done"))
	d.send(focusTool("sco-1", "skopos-1", "call-s2", "read", "internal/api/room.go", "done"))
	d.pump(30) // walkers settle at their anchors; the plan is built
	_ = d.m.Frame()

	clickFloor := func(id string) {
		p, ok := office.SpritePosition(id)
		if !ok {
			return
		}
		// screen coords: floor X is absolute, +1 row for the topbar
		d.send(tea.MouseClickMsg(tea.Mouse{X: p.X + 1, Y: p.Y + 1, Button: tea.MouseLeft}))
	}

	// (S) single click on tekton-1's sprite → selection
	clickFloor("dev-1")
	fmt.Println("===== UI SHOT · CLICK S — floor click on tekton-1: activity tab opened, agent selected =====")
	frameS := d.m.Frame()
	fmt.Println(frameS)
	fmt.Println("===== UI SHOT =====")
	if !strings.Contains(frameS, "ACTIVITY") {
		return fail("click S: the activity tab did not open on a floor click")
	}
	if !strings.Contains(frameS, "tekton-1") {
		return fail("click S: activity log shows no tekton-1 entries")
	}
	// the notice lives in chat history; the marker on the agents tab
	if !d.m.SelectTab("agents") {
		return fail("click S: agents tab not selectable")
	}
	frameSA := d.m.Frame()
	if !strings.Contains(ansi.Strip(frameSA), "▸ tekton-1") {
		return fail("click S: agents tab missing the ▸ selection marker on tekton-1")
	}
	if !d.m.SelectTab("chat") {
		return fail("click S: chat tab not selectable")
	}
	frameSC := d.m.Frame()
	if !strings.Contains(frameSC, "tekton-1 selected") {
		return fail("click S: office notice \"tekton-1 selected\" missing from chat")
	}
	fmt.Println("--- agents tab (▸ marker) + chat notice verified ---")

	// frame chrome: clicks on the topbar/statusbar rows do NOTHING
	d.send(tea.MouseClickMsg(tea.Mouse{X: 40, Y: 0, Button: tea.MouseLeft}))
	d.send(tea.MouseClickMsg(tea.Mouse{X: 40, Y: shotRows - 1, Button: tea.MouseLeft}))
	if strings.Contains(d.m.Frame(), "boss selected") || strings.Contains(d.m.Frame(), "▸ boss") {
		return fail("click chrome: a topbar/statusbar click leaked into a selection")
	}

	// tekton-1 returns; its thread auto-collapses
	d.send(state.Event{Kind: state.EvReturned, EmployeeID: "dev-1", TaskID: "t1",
		Mail: mail("m1", "tekton-1", "boss", "return: sse stream", "stream is live.", state.MailReturn)})
	d.pump(1)
	if !strings.Contains(d.m.Frame(), "tekton-1 · Wire the SSE stream (· 2 tool calls ✓ done)") {
		return fail("click D setup: tekton-1 thread did not collapse after EvReturned")
	}
	time.Sleep(clickPairGap + 100*time.Millisecond) // out of the double-click window

	// (D) double-click the sprite → thread expansion toggles, chat opens
	clickFloor("dev-1")
	clickFloor("dev-1")
	fmt.Println("===== UI SHOT · CLICK D — double-click tekton-1: its collapsed thread re-expands =====")
	frameD := d.m.Frame()
	fmt.Println(frameD)
	fmt.Println("===== UI SHOT =====")
	if !strings.Contains(frameD, "┌ tekton-1 · Wire the SSE stream") {
		return fail("click D: double-click did not re-expand tekton-1's thread (chat should show the ┌ header)")
	}
	if !strings.Contains(frameD, "│ [tool] read · internal/room/manager.go ✓") {
		return fail("click D: expanded thread missing its tool rows")
	}
	if d.m.ActiveTabIndex() != 0 {
		return fail("click D: double-click must jump to the chat tab")
	}

	// (H) click the skopos-1 "┌" header row in chat → its thread collapses
	// (find the header's actual screen row in the rendered frame)
	_, _, _, floorW := d.m.LayoutInfo()
	headerY := -1
	for i, ln := range strings.Split(frameD, "\n") {
		if strings.Contains(ln, "┌ skopos-1") {
			headerY = i
			break
		}
	}
	if headerY < 0 {
		return fail("click H setup: skopos-1 header row not found in the frame")
	}
	d.send(tea.MouseClickMsg(tea.Mouse{X: floorW + 5, Y: headerY, Button: tea.MouseLeft}))
	fmt.Println("===== UI SHOT · CLICK H — chat click on the skopos-1 ┌ header: its thread collapses =====")
	frameH := d.m.Frame()
	fmt.Println(frameH)
	fmt.Println("===== UI SHOT =====")
	if !strings.Contains(frameH, "skopos-1 · Scan the repo (· 2 tool calls ✓ done)") {
		return fail("click H: header click did not collapse skopos-1's thread")
	}
	if strings.Contains(frameH, "│ [tool] grep · SSE, 12 hits ✓") {
		return fail("click H: tool rows still expanded after the header click")
	}
	// click the collapsed SUMMARY row → re-expands (the toggle round-trips;
	// the summary is the thread's header while collapsed)
	summaryY := -1
	for i, ln := range strings.Split(frameH, "\n") {
		if strings.Contains(ln, "skopos-1 · Scan the repo (· 2 tool calls ✓ done)") {
			summaryY = i
			break
		}
	}
	if summaryY < 0 {
		return fail("click H: collapsed summary row not found in the frame")
	}
	d.send(tea.MouseClickMsg(tea.Mouse{X: floorW + 5, Y: summaryY, Button: tea.MouseLeft}))
	frameH2 := d.m.Frame()
	if !strings.Contains(frameH2, "┌ skopos-1 · Scan the repo") {
		return fail("click H: clicking the collapsed summary did not re-expand skopos-1's thread")
	}
	fmt.Println("asserts: OK — floor click selects (activity tab + ▸ marker + office notice), double-click toggles the thread + jumps to chat, thread-header clicks toggle round-trip, chrome rows ignore clicks")
	return nil
}

func main() {
	tab := flag.String("tab", defaultTab, "active tab: chat|terminal|agents|board|mail|activity")
	theme := flag.String("theme", "", "force a ui theme: "+strings.Join(chrome.ThemeNames(), "|"))
	slash := flag.Bool("slash", false, "simulate typing /theme dracula + /themes (exercises slash dispatch + theme persist)")
	perm := flag.Bool("perm", false, "auto-answer the boss permission prompt with 'once' at 3s (open → answered)")
	diffs := flag.Bool("diffs", false, "press ctrl+d to expand all diff entries")
	debug := flag.Bool("debug", false, "queue flush proof: resolves the pending boss so the queue drains; prints [queue] trace lines")
	think := flag.Bool("think", false, "think-stream proof: one CallID streamed in accumulated updates, prints BOTH frames (t=2.0s mid-stream expanded, t=3.2s collapsed after Done)")
	thinkStop := flag.String("think-stop", "", "with --think: print ONE frame only (mid = t=2.0s streaming, done = t=3.2s collapsed) for the gallery shot")
	stream := flag.Bool("stream", false, "chat-stream proof: one \"bossmsg-m1\" reply streamed as 5 accumulated pending updates then pinned; prints frame mid-stream (bubble growing in the viewport, typing row live below the divider) and after done (single settled bubble) plus the enqueue/done/flush ordering trace")
	askAnswer := flag.Bool("ask-answer", false, "question-hold proof: boss EvQuestion opens the answer modal (typing placeholder removed, park status line); typing + enter routes through AnswerQuestion — prints BOTH frames (modal open / after answered) + the stub capture log")
	askEsc := flag.Bool("ask-esc", false, "question-hold proof: esc defers the modal (notice), /question re-opens it, the answer still routes through AnswerQuestion")
	askQueue := flag.Bool("ask-queue", false, "queue-hold proof: a message typed while the question hold is outstanding must ENQUEUE; AnswerQuestion → resolved → completed boss reply → flush, ordering trace printed")
	batch := flag.Bool("batch", false, "intelligent-backlog proof: three messages enqueue while the boss is busy; the flush is ONE composed [BATCH DISPATCH] send (frame + batch text + stub logs + trace)")
	batchRespawn := flag.Bool("batch-respawn", false, "failure-respawn proof: the first batch Send is rejected once — the app must ResetPrimary(true) and resend the SAME batch exactly once")
	power := flag.String("power", "", "power-governor proof: 6s scripted window per mode (auto|saver|performance|all) — tick counts, floor frame-cache hit %, TickDelay table, /power + /model slash demo, custom boss-name frame")
	social := flag.Bool("social", false, "social-clock proof: synchronous tick pump — three frames (tea ask / both walking / gossip chain), banter chain trace, question-modal gate assert, tick-seeded determinism check")
	layout := flag.Bool("layout", false, "layout-modes proof: three frames over the same window — NORMAL (sidebar 44), compact (sidebar 30, short tab labels, 2-row chat input, compressed topbar), wide 56 — with computed width asserts per frame")
	terminal := flag.Bool("terminal", false, "terminal-tab proof: the stub TermPanel wires through app.SpawnTerminal — lazy-spawn on first visit, keys routed into the shell surface, frame + asserts")
	focus := flag.Bool("focus", false, "fix-wave proof, THREE synchronous-tick frames: (a) empty pending bubble — typing row below the divider (above the input), NO caret anywhere; (b) streaming partial bubble — text grows in the viewport while the typing row STAYS below the divider for the whole pending period (still no caret); (c) two concurrent agents — per-agent work threads grouped (headers + merged rows), boss tool line still inline, boss idle at the placeholder in delegating state (dim row in the same below-divider slot, [delegat] nameplate). Every frame: no \"▌\", every chat row inside the divider's width budget")
	persist := flag.Bool("persist", false, "office-session DEMO regression: seed a fresh session.json for cwd in a scratch GRAFEIO_HOME, run the standard demo shot, assert NO restore notice surfaces and the file is untouched (restore is live-only) — prints PERSIST-DEMO-SKIP: OK|FAIL")
	slashpop := flag.Bool("slashpop", false, "slash-popover proof: type \"/th\" → filtered menu (/theme /themes /thinking), Enter pre-fills \"/theme \" → theme picker, arrows preview LIVE (two states printed), esc cancels back, Enter commits + persists via the plain slash path")
	threadsThink := flag.Bool("threads-think", false, "employee-thinking-in-threads proof: tekton-1 EvThought merges per CallID into its work thread (live \"thinking · N lines\" row), collapsed summary keeps the count (\"· 1 think\"), ctrl+g expands tools + thoughts — boss path byte-identical")
	click := flag.Bool("click", false, "mouse proof: scripted clicks — floor sprite click selects the agent (activity tab + ▸ marker + office notice), double-click toggles its thread + jumps to chat, chat thread-header/summary clicks toggle round-trip, chrome rows ignore clicks")
	flag.Parse()

	if *persist {
		if err := runPersistDemoSkipProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *focus {
		if err := runFocusProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *slashpop {
		if err := runSlashPopProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *threadsThink {
		if err := runThreadsThinkProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *click {
		if err := runClickProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *layout {
		if err := runLayoutProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *terminal {
		if err := runTerminalProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *social {
		if err := runSocialProof(); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if *power != "" {
		if err := runPowerProof(*power); err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		return
	}

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

	if *batch || *batchRespawn {
		frame, trace, sends, team, err := runBatchShot(*batchRespawn, 5600*time.Millisecond)
		if err != nil {
			fmt.Fprintf(os.Stderr, "uishot: %v\n", err)
			os.Exit(1)
		}
		label := "--batch (flush = ONE composed batch send)"
		if *batchRespawn {
			label = "--batch-respawn (first Send rejected → ResetPrimary + resend once)"
		}
		fmt.Printf("===== UI SHOT · %s =====\n", label)
		fmt.Println(frame)
		fmt.Println("===== UI SHOT =====")
		fmt.Println("--- ordering trace ---")
		for _, ln := range trace {
			fmt.Println(ln)
		}
		fmt.Println("--- stub Send calls ---")
		for i, s := range sends {
			fmt.Printf("Send call %d:\n%s\n", i+1, s)
		}
		fmt.Println("--- team seam log ---")
		for _, ln := range team {
			fmt.Println(ln)
		}
		// asserts — the intelligent-backlog contract
		fail := func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "uishot: "+format+"\n", args...)
			os.Exit(1)
		}
		wantSends := 1
		if *batchRespawn {
			wantSends = 2
		}
		if len(sends) != wantSends {
			fail("expected exactly %d Send call(s), got %d", wantSends, len(sends))
		}
		if !strings.HasPrefix(sends[0], "[BATCH DISPATCH — 3 requests arrived") {
			fail("expected ONE batch-composed send of 3 items, got: %q", sends[0])
		}
		for _, item := range []string{"1. fix the badge", "2. ship v2", "3. write the release notes"} {
			if !strings.Contains(sends[0], item) {
				fail("composed batch missing numbered item %q", item)
			}
		}
		if *batchRespawn {
			if sends[1] != sends[0] {
				fail("respawned batch differs from the original")
			}
			hasReset := false
			for _, ln := range team {
				if ln == "ResetPrimary(true)" {
					hasReset = true
				}
			}
			if !hasReset {
				fail("expected ResetPrimary(true) after the rejected batch send")
			}
		}
		for _, id := range []string{"demo-1", "demo-2", "demo-3"} {
			if !strings.Contains(" "+strings.Join(team, " ")+" ", "QueueItemDone("+id+")") {
				fail("expected QueueItemDone(%s) after the batch turn completed", id)
			}
		}
		fmt.Println("asserts: OK — ONE composed batch send, board rows started/done, no second send until flush")
		return
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
			{1250 * time.Millisecond, "frame 1 — t=1.25s (MID-STREAM: grown bubble, typing row below the divider, one message enqueued)"},
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
	m := app.New(backend, config.Default())
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
