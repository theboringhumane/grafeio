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
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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

// bossDiffBody — a diff long enough to prove the 30-line "+N more" clip
// when expanded (34 body lines).
const bossDiffBody = `--- a/internal/app/model.go
+++ b/internal/app/model.go
@@ -52,11 +52,14 @@ type Model struct {
 	backend state.Backend
 	st      state.OfficeState
-	width, height int
-	middleH       int
-	sidebar       int
-	floorW        int
-	tabs          *panels.Tabs
-	chat          *panels.Chat
-	activity      *panels.Activity
-	keys          KeyMap
+	width, height int
+	middleH       int
+	sidebar       int
+	floorW        int
+	tabs          *panels.Tabs
+	chat          *panels.Chat
+	activity      *panels.Activity
+	keys          KeyMap
+	// Message queue (model-level so it survives tab switches).
+	queue []string
+	// Permission prompts (boss/primary session only).
+	perm     *permPrompt
+	permEscd *permPrompt
 }
@@ -219,7 +222,11 @@ func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
 	case sendErrMsg:
-		m.applyEvent(state.Event{
-			Kind: state.EvStatus,
-			Text: "send failed",
-		})
+		cmds = append(cmds, m.applyEvent(state.Event{
+			Kind: state.EvStatus,
+			Text: fmt.Sprintf("[grafeio] send failed: %v", msg.err),
+		}))
+	case enqueueMsg:
+		m.queue = append(m.queue, msg.text)
+	case queueFlushMsg:
+		cmds = append(cmds, m.flushQueued())
 	}
 	return m, tea.Batch(cmds...)
 }`

// employeeDiffBody — small child-session diff (also lands in chat + an
// activity line).
const employeeDiffBody = `--- a/src/main.go
+++ b/src/main.go
@@ -1,4 +1,5 @@
 package main
 func main() {
-	println("hi")
+	println("hello, grafeio")
+	run()
 }`

// stubBackend is the deterministic scripted backend for the shot.
type stubBackend struct {
	emit       func(state.Event)
	done       chan struct{}
	start      time.Time
	flushQueue bool   // --debug: script resolves the round-2 pending boss
	permAnswer string // recorded by AnswerPermission for the final print
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
	at(600, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b0", "boss", "", true)})
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
	at(1920, state.Event{Kind: state.EvQuestion, EmployeeName: "boss", QuestionID: "q-1",
		Text:        "Which DB should the leaderboard use?",
		ToolSummary: "postgres | sqlite | keep it in memory"})
	at(1960, state.Event{Kind: state.EvQuestion, EmployeeName: "tekton-1", QuestionID: "q-2",
		Text: "employee question — activity line only, no chat entry"})
	at(2000, state.Event{Kind: state.EvFileDiff, EmployeeName: "boss",
		DiffPath: "internal/app/model.go", DiffAdd: 40, DiffDel: 12, DiffBody: bossDiffBody})
	at(2050, state.Event{Kind: state.EvFileDiff, EmployeeName: "tekton-1",
		DiffPath: "src/main.go", DiffAdd: 5, DiffDel: 1, DiffBody: employeeDiffBody})

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
	at(3050, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b2", "boss", "", true)})
	at(3300, state.Event{Kind: state.EvMail, Mail: mail("m3", "hr", "all",
		"roster synced", "3 agents seated.", state.MailNotice)})
	if b.flushQueue {
		at(3900, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b3", "boss",
			"Ship it — and keep typing, I'll keep up.", false)})
	}
}

func (b *stubBackend) Mode() state.Mode { return state.ModeDemo }

func (b *stubBackend) Start(emit func(state.Event)) error {
	b.emit = emit
	b.start = time.Now()
	go b.script()
	return nil
}

// Send answers any interactive prompt deterministically (600ms ack).
func (b *stubBackend) Send(_ string) error {
	if b.emit != nil {
		time.AfterFunc(600*time.Millisecond, func() {
			b.emit(state.Event{Kind: state.EvChatBoss,
				Msg: chatMsg("bx", "boss", "Roger that.", false)})
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

func main() {
	tab := flag.String("tab", defaultTab, "active tab: chat|agents|board|mail|activity")
	theme := flag.String("theme", "", "force a ui theme: "+strings.Join(chrome.ThemeNames(), "|"))
	slash := flag.Bool("slash", false, "simulate typing /theme dracula + /themes (exercises slash dispatch + theme persist)")
	perm := flag.Bool("perm", false, "auto-answer the boss permission prompt with 'once' at 3s (open → answered)")
	diffs := flag.Bool("diffs", false, "press ctrl+d to expand all diff entries")
	debug := flag.Bool("debug", false, "queue flush proof: resolves the pending boss so the queue drains; prints [queue] trace lines")
	flag.Parse()

	// keystroke workloads only reach the textarea / prompt on the chat tab
	if (*slash || *perm || *diffs || *debug) && *tab == defaultTab {
		*tab = "chat"
	}

	if *theme != "" {
		if !chrome.SetTheme(*theme) {
			fmt.Fprintf(os.Stderr, "uishot: unknown theme %q (%s)\n", *theme,
				strings.Join(chrome.ThemeNames(), ", "))
			os.Exit(2)
		}
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

	if *perm {
		fmt.Printf("perm answered: %s\n", backend.permAnswer)
	}

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
