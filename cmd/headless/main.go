// headless — verification binary for the Grafeio backend layer.
//
//	grafeio-headless            demo backend (default), ~7.2s of events, exit 0
//	grafeio-headless --live     real `opencode serve` spawn + agentmemory probe,
//	                            print startup events for 3s, stop, exit 0
//	grafeio-headless --live --prompt "text"
//	                            send the prompt once the primary session is
//	                            ready, run 14s so thought/tool events + the
//	                            boss reply stream through, stop, exit 0
//	grafeio-headless --answer   after the first permission event prints,
//	                            call backend.AnswerPermission(pid, "once") and
//	                            print the result (demo: clears tekton-1's block)
//
// Every state.Event is printed as one line (kind + key fields) so a smoke
// run can assert the floor contract without a renderer.
package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/backend"
	"github.com/theboringhumane/grafeio/internal/state"
)

func main() {
	demo := flag.Bool("demo", true, "run the scripted demo backend (default)")
	live := flag.Bool("live", false, "spawn a real opencode serve and run live for 3s")
	prompt := flag.String("prompt", "", "live mode: send this prompt after the primary session is ready and run 14s")
	answer := flag.Bool("answer", false, "auto-answer the first permission prompt with \"once\" and print the result")
	flag.Parse()

	var b state.Backend
	var runFor time.Duration
	if *live {
		dir, err := os.Getwd()
		if err != nil {
			fail("getwd", err)
		}
		b = backend.NewLive("", dir)
		runFor = 3 * time.Second
		if *prompt != "" {
			runFor = 14 * time.Second
		}
	} else if *demo {
		b = backend.NewDemo()
		runFor = 7200 * time.Millisecond
	} else {
		fmt.Fprintln(os.Stderr, "either --demo or --live is required")
		os.Exit(2)
	}

	var mu sync.Mutex
	ticks := 0
	var answerPID string // first PENDING permission id seen, under mu
	emit := func(e state.Event) {
		// --answer: capture the first pending permission id now; the actual
		// backend call happens OUTSIDE the emit lock (demo emits
		// synchronously from inside AnswerPermission — calling here would
		// deadlock on mu).
		answerNow := false
		mu.Lock()
		if *answer && answerPID == "" && e.Kind == state.EvPermission && e.ToolState != "resolved" {
			answerPID = e.PermissionID
			answerNow = true
		}
		if e.Kind == state.EvTick {
			ticks++
			if ticks%10 == 1 {
				fmt.Printf("[tick] #%d\n", ticks)
			}
			mu.Unlock()
			return
		}
		printEvent(e)
		mu.Unlock()
		if answerNow {
			// One event's worth of separation, then the reply round-trip.
			time.AfterFunc(50*time.Millisecond, func() {
				err := b.AnswerPermission(answerPID, "once")
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fmt.Printf("[answer] permission %s -> ERROR: %v\n", answerPID, err)
				} else {
					fmt.Printf("[answer] permission %s -> ok (backend.AnswerPermission(\"%s\", \"once\"))\n", answerPID, answerPID)
				}
			})
		}
	}

	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fail("start", err)
	}

	// --prompt: Start only returns after the primary session exists, so the
	// prompt is safe to send immediately.
	if *prompt != "" {
		if !*live {
			fmt.Fprintln(os.Stderr, "--prompt requires --live")
			os.Exit(2)
		}
		fmt.Printf("[prompt] %q\n", *prompt)
		if err := b.Send(*prompt); err != nil {
			fail("send", err)
		}
	}

	time.Sleep(runFor)

	if err := b.Stop(); err != nil {
		fail("stop", err)
	}
	fmt.Println("[done] backend stopped")
}

func fail(stage string, err error) {
	fmt.Printf("[fatal] %s: %v\n", stage, err)
	os.Exit(1)
}

func trunc(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}

func printEvent(e state.Event) {
	switch e.Kind {
	case state.EvStatus:
		fmt.Printf("[status] %s\n", e.Text)
	case state.EvHire:
		fmt.Printf("[hire] %s role=%s seat=%s sprite=%s\n", e.Employee.Name, e.Employee.Role, e.Employee.Seat, e.Employee.Sprite)
	case state.EvFire:
		fmt.Printf("[fire] %s\n", e.EmployeeID)
	case state.EvDispatch:
		fmt.Printf("[dispatch] %s -> %s %q\n", e.Task.ID, e.EmployeeID, e.Task.Title)
	case state.EvWorking:
		fmt.Printf("[working] %s task=%s\n", e.EmployeeID, e.TaskID)
	case state.EvReturned:
		fmt.Printf("[returned] %s task=%s mail=%q body=%q\n", e.EmployeeID, e.TaskID, e.Mail.Subject, trunc(e.Mail.Body, 100))
	case state.EvIdleDrift:
		fmt.Printf("[idle-drift] %s\n", e.EmployeeID)
	case state.EvBlocked:
		fmt.Printf("[blocked] %s note=%s\n", e.EmployeeID, e.Text)
	case state.EvTask:
		fmt.Printf("[task] %s %q status=%s owner=%s\n", e.Task.ID, e.Task.Title, e.Task.Status, e.Task.Owner)
	case state.EvMail:
		fmt.Printf("[mail] %s -> %s %q body=%q\n", e.Mail.From, e.Mail.To, e.Mail.Subject, trunc(e.Mail.Body, 100))
	case state.EvChatUser:
		fmt.Printf("[chat-user] %s %q\n", e.Msg.ID, e.Msg.Text)
	case state.EvChatBoss:
		fmt.Printf("[chat-boss] %s pending=%v %q\n", e.Msg.ID, e.Msg.Pending, trunc(e.Msg.Text, 120))
	case state.EvBubble:
		fmt.Printf("[bubble] %s %q\n", e.EmployeeID, e.Text)
	case state.EvThought:
		fmt.Printf("[thought] %s done=%v %q\n", e.EmployeeName, e.Done, trunc(e.Text, 120))
	case state.EvTool:
		fmt.Printf("[tool] %s %s %s %q\n", e.EmployeeName, e.ToolName, e.ToolState, trunc(e.ToolSummary, 80))
	case state.EvPermission:
		fmt.Printf("[permission] %s %s %s %q state=%s\n", e.PermissionID, e.EmployeeName, e.ToolName, trunc(e.ToolSummary, 80), e.ToolState)
	case state.EvQuestion:
		fmt.Printf("[question] %s %s %q options=%q state=%s\n", e.QuestionID, e.EmployeeName, trunc(e.Text, 120), trunc(e.ToolSummary, 80), e.ToolState)
	case state.EvFileDiff:
		fmt.Printf("[diff] %s %s +%d/-%d\n%s\n", e.EmployeeName, e.DiffPath, e.DiffAdd, e.DiffDel, trunc(e.DiffBody, 300))
	default:
		fmt.Printf("[%s]\n", e.Kind)
	}
}
