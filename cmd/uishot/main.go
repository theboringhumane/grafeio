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
	flushQueue bool   // --debug: script resolves the round-2 pending boss
	thinkMode  bool   // --think: script streams one think CallID instead
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
	if b.thinkMode {
		b.scriptThink(at)
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
	at(3050, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b2", "boss", "", true)})
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
	at(200, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b0", "boss", "", true)})
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

func main() {
	tab := flag.String("tab", defaultTab, "active tab: chat|agents|board|mail|activity")
	theme := flag.String("theme", "", "force a ui theme: "+strings.Join(chrome.ThemeNames(), "|"))
	slash := flag.Bool("slash", false, "simulate typing /theme dracula + /themes (exercises slash dispatch + theme persist)")
	perm := flag.Bool("perm", false, "auto-answer the boss permission prompt with 'once' at 3s (open → answered)")
	diffs := flag.Bool("diffs", false, "press ctrl+d to expand all diff entries")
	debug := flag.Bool("debug", false, "queue flush proof: resolves the pending boss so the queue drains; prints [queue] trace lines")
	think := flag.Bool("think", false, "think-stream proof: one CallID streamed in accumulated updates, prints BOTH frames (t=2.0s mid-stream expanded, t=3.2s collapsed after Done)")
	thinkStop := flag.String("think-stop", "", "with --think: print ONE frame only (mid = t=2.0s streaming, done = t=3.2s collapsed) for the gallery shot")
	flag.Parse()

	// keystroke workloads only reach the textarea / prompt on the chat tab
	if (*slash || *perm || *diffs || *debug || *think) && *tab == defaultTab {
		*tab = "chat"
	}

	if *theme != "" {
		if !chrome.SetTheme(*theme) {
			fmt.Fprintf(os.Stderr, "uishot: unknown theme %q (%s)\n", *theme,
				strings.Join(chrome.ThemeNames(), ", "))
			os.Exit(2)
		}
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
