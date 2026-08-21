// headless — verification binary for the Grafeio backend layer.
//
//	grafeio-headless            demo backend (default), ~7.2s of events, exit 0
//	grafeio-headless --live     real `opencode serve` spawn + agentmemory probe,
//	                            print startup events for 3s, stop, exit 0
//	grafeio-headless --live --prompt "text"
//	                            send the prompt once the primary session is
//	                            ready, wait up to 60s for the completed boss
//	                            reply, stop, exit 0
//	grafeio-headless --live --prompt "text" --prompt2 "text2"
//	                            stale-reply repro: send prompt, wait up to 60s
//	                            for its completed boss text, send prompt2, wait
//	                            likewise, then assert the two turns' texts are
//	                            distinct and operation-appropriate. Prints
//	                            "STALE-REPRO: FIXED" (exit 0) when all checks
//	                            pass, "STALE-REPRO: BUG" (exit 1) otherwise.
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
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/backend"
	"github.com/theboringhumane/grafeio/internal/state"
)

func main() {
	demo := flag.Bool("demo", true, "run the scripted demo backend (default)")
	live := flag.Bool("live", false, "spawn a real opencode serve and run live for 3s")
	prompt := flag.String("prompt", "", "live mode: send this prompt after the primary session is ready and wait up to 60s for the completion")
	prompt2 := flag.String("prompt2", "", "live mode: after prompt completes, send this second prompt and run the stale-reply assertions (prints STALE-REPRO: FIXED|BUG)")
	answer := flag.Bool("answer", false, "auto-answer the first permission prompt with \"once\" and print the result")
	flag.Parse()

	if *prompt2 != "" && *prompt == "" {
		fmt.Fprintln(os.Stderr, "--prompt2 requires --prompt")
		os.Exit(2)
	}

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
	var answerPID string // first PENDING permission id seen, under mu
	// Every completed (non-pending) chat-boss body, in arrival order — the
	// stale-repro assertions read this; emit feeds bossCh non-blocking.
	bossCh := make(chan string, 256)
	// Per-CallID thought counters make a STREAMING thought obvious: the same
	// CallID reappears with a growing (accum N chars) figure until done=true.
	thoughtCounts := make(map[string]int)
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
		if e.Kind == state.EvThought {
			thoughtCounts[e.CallID]++
			n := thoughtCounts[e.CallID]
			fmt.Printf("[thought#%d (accum %d chars) %s done=%v %q] call=%s\n",
				n, len([]rune(e.Text)), e.EmployeeName, e.Done, tail(e.Text, 60), e.CallID)
			mu.Unlock()
			return
		}
		if e.Kind == state.EvChatBoss && !e.Msg.Pending {
			select {
			case bossCh <- e.Msg.Text:
			default:
			}
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
	// prompt is safe to send immediately. With --prompt2 the run is the
	// stale-reply repro: each turn waits up to 60s for its completed boss
	// texts (an 800ms drain collects multi-message turns), then the four
	// assertions decide STALE-REPRO: FIXED vs BUG.
	if *prompt != "" {
		if !*live {
			fmt.Fprintln(os.Stderr, "--prompt requires --live")
			os.Exit(2)
		}
		fmt.Printf("[prompt] %q\n", *prompt)
		if err := b.Send(*prompt); err != nil {
			fail("send", err)
		}
		turn1 := collectTurn(bossCh, 60*time.Second)
		if *prompt2 == "" {
			time.Sleep(2 * time.Second) // let trailing tool/diff events print
		} else {
			fmt.Printf("[prompt2] %q\n", *prompt2)
			if err := b.Send(*prompt2); err != nil {
				fail("send2", err)
			}
			turn2 := collectTurn(bossCh, 60*time.Second)
			fmt.Println("[assert] turn1 completed bubbles:")
			for i, t := range turn1 {
				fmt.Printf("[assert]   #%d %q\n", i+1, t)
			}
			fmt.Println("[assert] turn2 completed bubbles:")
			for i, t := range turn2 {
				fmt.Printf("[assert]   #%d %q\n", i+1, t)
			}
			failures := staleReproChecks(turn1, turn2)
			if failures == 0 {
				fmt.Println("STALE-REPRO: FIXED")
			} else {
				fmt.Printf("STALE-REPRO: BUG (%d check(s) failed)\n", failures)
			}
			if err := b.Stop(); err != nil {
				fail("stop", err)
			}
			fmt.Println("[done] backend stopped")
			if failures > 0 {
				os.Exit(1)
			}
			return
		}
	} else {
		time.Sleep(runFor)
	}

	if err := b.Stop(); err != nil {
		fail("stop", err)
	}
	fmt.Println("[done] backend stopped")
}

// collectTurn waits up to timeout for the FIRST completed boss bubble, then
// keeps draining further completions until one 4s gap passes with no new
// completion or the overall timeout expires. A turn is routinely
// multi-message in opencode: the tool-call assistant message completes
// (emits nothing — finish=="tool-calls") and the real text arrives in a
// continuation message seconds later, so an 800ms drain is not a turn.
// Returns every completed body in order; the turn's text is the LAST one.
func collectTurn(bossCh <-chan string, timeout time.Duration) []string {
	var texts []string
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for len(texts) == 0 {
		select {
		case t := <-bossCh:
			texts = append(texts, t)
		case <-deadline.C:
			return texts
		}
	}
	quiet := time.NewTimer(4 * time.Second)
	defer quiet.Stop()
	for {
		select {
		case t := <-bossCh:
			texts = append(texts, t)
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(4 * time.Second)
		case <-quiet.C:
			return texts
		case <-deadline.C:
			return texts
		}
	}
}

// staleReproChecks runs the four stale-reply assertions. Returns the number
// of failed checks (0 == STALE-REPRO: FIXED):
//  1. both turns produced completions and their final texts DIFFER
//  2. turn2's FIRST completed bubble is not a repeat of turn1's final text
//  3. each turn's final text mentions its own operation (loose grep:
//     create-ish for turn1, delete-ish for turn2)
//  4. no completed chat-boss body is byte-identical to an earlier one
func staleReproChecks(turn1, turn2 []string) int {
	failures := 0
	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}
	check(len(turn1) > 0 && len(turn2) > 0, "both turns produced a completed boss bubble")
	if len(turn1) == 0 || len(turn2) == 0 {
		return failures
	}
	t1final := turn1[len(turn1)-1]
	t2first := turn2[0]
	t2final := turn2[len(turn2)-1]
	check(t1final != t2final, "(1) the two turns' final texts differ")
	check(t2first != t1final, "(2) turn2's first bubble does not repeat turn1's text")
	t1l := strings.ToLower(t1final)
	t2l := strings.ToLower(t2final)
	check(strings.Contains(t1l, "alpha") || strings.Contains(t1l, "creat"),
		"(3a) turn1 mentions its own operation (create-ish)")
	check(strings.Contains(t2l, "delet") || strings.Contains(t2l, "remov"),
		"(3b) turn2 mentions its own operation (delete-ish)")
	seen := make(map[string]bool)
	dups := false
	for _, t := range append(append([]string(nil), turn1...), turn2...) {
		if seen[t] {
			dups = true
		}
		seen[t] = true
	}
	check(!dups, "(4) no chat-boss body is byte-identical to an earlier one")
	return failures
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

// tail returns the LAST max runes of s — for streaming thoughts the tail is
// where the growth shows.
func tail(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return "..." + string(r[len(r)-max:])
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
