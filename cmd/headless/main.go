// headless — verification binary for the Grafeio backend layer.
//
//	grafeio-headless            demo backend (default), ~7.2s of events, exit 0
//	grafeio-headless --live     real `opencode serve` spawn + agentmemory probe,
//	                            print startup events for 3s, stop, exit 0
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
	} else if *demo {
		b = backend.NewDemo()
		runFor = 7200 * time.Millisecond
	} else {
		fmt.Fprintln(os.Stderr, "either --demo or --live is required")
		os.Exit(2)
	}

	var mu sync.Mutex
	ticks := 0
	emit := func(e state.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.Kind == state.EvTick {
			ticks++
			if ticks%10 == 1 {
				fmt.Printf("[tick] #%d\n", ticks)
			}
			return
		}
		printEvent(e)
	}

	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fail("start", err)
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
	default:
		fmt.Printf("[%s]\n", e.Kind)
	}
}
