// uishot — deterministic UI shot harness for Grafeio v2.
//
// Runs the REAL app model against a scripted stub backend (fixed event
// script: hires/dispatches/working/returned+mail/blocked/bubbles, boss
// EvThought + EvTool chains, and two chat rounds — one boss reply contains
// markdown). Fixed 130x32, ~4s, then prints the final frame between
// ===== UI SHOT ===== markers.
//
//	go run ./cmd/uishot [--tab chat|agents|board|mail|activity]
//	                    [--theme noir|paper|mono|dracula|solarized]
//	                    [--slash]   (also simulates typing /theme + /themes)
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
	defaultTab  = "agents"
	bossReplyMD = "**Done** — created `hello.html`:\n" +
		"- dark navy bg\n" +
		"- white text\n" +
		"```sh\n" +
		"echo done\n" +
		"```"
)

// stubBackend is the deterministic scripted backend for the shot.
type stubBackend struct {
	emit func(state.Event)
	done chan struct{}
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
// deterministic given the same clock. Ends ~3.3s into the ~4s window.
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

	// a return: desk walk + done task + return mail
	at(2100, state.Event{Kind: state.EvReturned, EmployeeID: "dev-1", TaskID: "t1",
		Mail: mail("m1", "tekton-1", "boss", "return: build hello.html", "hello.html is up.", state.MailReturn)})
	at(2300, state.Event{Kind: state.EvMail, Mail: mail("m2", "boss", "tekton-1",
		"brief: footer next", "add a footer", state.MailBrief)})

	at(2500, state.Event{Kind: state.EvIdleDrift, EmployeeID: "sco-1"})
	at(2700, state.Event{Kind: state.EvBlocked, EmployeeID: "rev-1", Text: "needs the staging key"})
	at(2750, state.Event{Kind: state.EvBubble, EmployeeID: "rev-1", Text: "anyone seen the staging key?", TTL: 40})

	// round 2: boss still typing when the frame freezes (spinner visible)
	at(3000, state.Event{Kind: state.EvChatUser, Msg: chatMsg("u2", "user",
		"and add a footer please", false)})
	at(3050, state.Event{Kind: state.EvChatBoss, Msg: chatMsg("b2", "boss", "", true)})
	at(3300, state.Event{Kind: state.EvMail, Mail: mail("m3", "hr", "all",
		"roster synced", "3 agents seated.", state.MailNotice)})
}

func (b *stubBackend) Mode() state.Mode { return state.ModeDemo }

func (b *stubBackend) Start(emit func(state.Event)) error {
	b.emit = emit
	go b.script()
	return nil
}

// Send answers any interactive prompt deterministically (unused by the
// script path, but keeps the seam honest if the model sends).
func (b *stubBackend) Send(_ string) error {
	if b.emit != nil {
		time.AfterFunc(600*time.Millisecond, func() {
			b.emit(state.Event{Kind: state.EvChatBoss,
				Msg: chatMsg("bx", "boss", "Roger that.", false)})
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

func main() {
	tab := flag.String("tab", defaultTab, "active tab: chat|agents|board|mail|activity")
	theme := flag.String("theme", "", "force a ui theme: "+strings.Join(chrome.ThemeNames(), "|"))
	slash := flag.Bool("slash", false, "simulate typing /theme dracula + /themes (exercises slash dispatch + theme persist)")
	flag.Parse()

	// slash keystrokes only reach the textarea on the chat tab
	if *slash && *tab == defaultTab {
		*tab = "chat"
	}

	if *theme != "" {
		if !chrome.SetTheme(*theme) {
			fmt.Fprintf(os.Stderr, "uishot: unknown theme %q (%s)\n", *theme,
				strings.Join(chrome.ThemeNames(), ", "))
			os.Exit(2)
		}
	}

	backend := &stubBackend{done: make(chan struct{})}
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
	go func() {
		time.Sleep(shotDur)
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
