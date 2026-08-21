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
//	grafeio-headless --batch-probe
//	                            queue-flush contract probe (forces live):
//	                            mirrors TWO queue items onto the agentmemory
//	                            board via the QueueItemStart seam, sends ONE
//	                            composed batch prompt like the app would,
//	                            waits up to 60s for the completed boss reply,
//	                            asserts the reply covers BOTH queued items,
//	                            then marks the board actions done. Prints
//	                            BATCH-PHASE lines + "BATCH-PROBE: OK" (exit 0)
//	                            or "BATCH-PROBE: FAIL" (exit 1).
//	grafeio-headless --answer   after the first permission event prints,
//	                            call backend.AnswerPermission(pid, "once") and
//	                            print the result (demo: clears tekton-1's block)
//	grafeio-headless --cfg path/to/brain.json
//	                            use an explicit brain.json for this run
//	                            (defaults-filled, never written back); without
//	                            it, config.Load() reads GRAFEIO_HOME just like
//	                            the UI binaries. A [cfg] summary line prints
//	                            the loaded Boss/Backend before anything else.
//	grafeio-headless --efficiency
//	                            simulate 11 board-poll cadence decisions (8
//	                            unchanged syncs, then a change) using the same
//	                            BackoffInterval helper the live backend runs,
//	                            printing the interval growth. EFFICIENCY: OK.
//	grafeio-headless --ask      question-loop regression probe (forces live):
//	                            auto-sends the question-tool prompt, answers the
//	                            FIRST pending EvQuestion via
//	                            backend.AnswerQuestion 2s after it surfaces, then
//	                            asserts inside a 15s TOTAL budget (measured from
//	                            process start; the serve spawn eats into it):
//	                            the question resolved event AND a final
//	                            completed chat-boss whose text references the
//	                            answer. Prints "QUESTION-LOOP: FIXED" (exit 0) or
//	                            "QUESTION-LOOP: STUCK" (exit 1).
//
//	grafeio-headless --persist-demo
//	                            office-session persist proof, run 1 (forces
//	                            live): scratch GRAFEIO_HOME (created when
//	                            unset, path printed as [persist-home]), Start,
//	                            send "say pineapple", wait up to 60s for the
//	                            completed boss bubble, persist the office
//	                            session, print session.json verbatim. Prints
//	                            "PERSIST: SAVED" (exit 0) or exits 1.
//	grafeio-headless --persist-restore
//	                            office-session persist proof, run 2 (forces
//	                            live; requires the SAME GRAFEIO_HOME + cwd as
//	                            run 1): LoadSession + PrimaryOverride + Start,
//	                            asserts the restore notice line AND that the
//	                            SAME primary id got reused (session under the
//	                            50-msg stale guard). Prints "PERSIST: RESTORED".
//	grafeio-headless --persist-new
//	                            office-session persist proof, run 3 (forces
//	                            live; same GRAFEIO_HOME + cwd): /new leg —
//	                            NewOffice() must mint a FRESH primary id (!=
//	                            the saved one), print the /new notice, and
//	                            prove the overwrite keeps the latest primary
//	                            in session.json. Prints "PERSIST: NEW".
//
// The plain demo run also auto-answers its scripted boss question
// (que-demo-1, 800ms after it surfaces) so the registration + resolved
// lines always show.
//
// Every state.Event is printed as one line (kind + key fields) so a smoke
// run can assert the floor contract without a renderer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/app"
	"github.com/theboringhumane/grafeio/internal/backend"
	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/state"
)

func main() {
	demo := flag.Bool("demo", true, "run the scripted demo backend (default)")
	live := flag.Bool("live", false, "spawn a real opencode serve and run live for 3s")
	prompt := flag.String("prompt", "", "live mode: send this prompt after the primary session is ready and wait up to 60s for the completion")
	prompt2 := flag.String("prompt2", "", "live mode: after prompt completes, send this second prompt and run the stale-reply assertions (prints STALE-REPRO: FIXED|BUG)")
	answer := flag.Bool("answer", false, "auto-answer the first permission prompt with \"once\" and print the result")
	ask := flag.Bool("ask", false, "live mode: question-loop probe — send the question-tool prompt, AnswerQuestion the first pending question after 2s, assert resolution (15s budget, QUESTION-LOOP: FIXED|STUCK)")
	batchProbe := flag.Bool("batch-probe", false, "live mode: queue-flush batch probe — board-mirror 2 queue items, send one composed batch, assert the boss covers both (BATCH-PROBE: OK|FAIL)")
	cfgPath := flag.String("cfg", "", "path to a brain.json for this run (else config.Load() honors GRAFEIO_HOME)")
	efficiency := flag.Bool("efficiency", false, "simulate 11 board-poll cadence decisions (8 unchanged syncs, then a change) and print the exponential backoff, then exit")
	persistDemo := flag.Bool("persist-demo", false, "office-session persist proof run 1 (live): send 'say pineapple', wait for the completed bubble, persist the office session, print session.json (PERSIST: SAVED)")
	persistRestore := flag.Bool("persist-restore", false, "office-session persist proof run 2 (live): restore boot — restore notice + SAME primary id reused (PERSIST: RESTORED)")
	persistNew := flag.Bool("persist-new", false, "office-session persist proof run 3 (live): /new — fresh primary id != saved id + /new notice + latest-wins overwrite proof (PERSIST: NEW)")
	flag.Parse()

	if *efficiency {
		runEfficiencySim()
		return
	}

	// Office-session persist probes run in their own harness (own emit,
	// own chat capture) and resolve GRAFEIO_HOME BEFORE loadConfig — run 1
	// with an unset env creates the scratch home first, so the brain.json
	// first-boot write lands in the scratch, never in the real home.
	if *persistDemo || *persistRestore || *persistNew {
		runPersistProbe(*cfgPath, *persistDemo, *persistRestore, *persistNew)
		return
	}

	// brain.json for this run. --cfg points at an explicit file; otherwise
	// config.Load() reads (and first-boots) GRAFEIO_HOME/.grafeio/configs/
	// brain.json — identical to how the UI binaries load it.
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fail("config", err)
	}
	fmt.Printf("[cfg] boss=%q model=%q backend: server=%q agentmemoryUrl=%q agentmemoryPollS=%d\n",
		cfg.Boss.Name, cfg.Boss.Model, cfg.Backend.Server, cfg.Backend.AgentmemoryURL, cfg.Backend.AgentmemoryPollS)

	if *prompt2 != "" && *prompt == "" {
		fmt.Fprintln(os.Stderr, "--prompt2 requires --prompt")
		os.Exit(2)
	}
	if *ask && (*prompt != "" || *prompt2 != "") {
		fmt.Fprintln(os.Stderr, "--ask is a standalone probe: do not combine with --prompt/--prompt2")
		os.Exit(2)
	}
	if *batchProbe && (*prompt != "" || *prompt2 != "" || *ask) {
		fmt.Fprintln(os.Stderr, "--batch-probe is a standalone probe: do not combine with --prompt/--prompt2/--ask")
		os.Exit(2)
	}
	if *ask || *batchProbe {
		*live = true
		*demo = false
	}
	// 15s TOTAL budget for --ask, measured from before the serve spawn.
	askDeadline := time.Now().Add(15 * time.Second)

	var b state.Backend
	var runFor time.Duration
	if *live {
		dir, err := os.Getwd()
		if err != nil {
			fail("getwd", err)
		}
		b = backend.NewLive("", dir, cfg)
		runFor = 3 * time.Second
	} else if *demo {
		b = backend.NewDemo(cfg)
		runFor = 7200 * time.Millisecond
	} else {
		fmt.Fprintln(os.Stderr, "either --demo or --live is required")
		os.Exit(2)
	}

	var mu sync.Mutex
	ticks := 0
	var answerPID string // first PENDING permission id seen, under mu
	// Question auto-answer: the plain demo run always answers its scripted
	// boss question; live mode answers ONLY under --ask. askDelay/askAnswers
	// differ per mode (--ask spec: 2s and the one-line wire answer).
	autoAsk := !*live || *ask
	askDelay := 800 * time.Millisecond
	askAnswers := []string{"internal/state/state.go"}
	if *live {
		askDelay = 2 * time.Second
		askAnswers = []string{"the recommended per-block toggle"}
	}
	var askQID string         // first PENDING question id seen, under mu
	var questionResolved bool // under mu: a resolved EvQuestion arrived
	var askErr string         // under mu: AnswerQuestion failure text
	// Every completed (non-pending) chat-boss body, in arrival order — the
	// stale-repro assertions read this; emit feeds bossCh non-blocking.
	bossCh := make(chan string, 256)
	// Per-CallID thought counters make a STREAMING thought obvious: the same
	// CallID reappears with a growing (accum N chars) figure until done=true.
	thoughtCounts := make(map[string]int)
	// Per-Msg.ID boss-bubble counters make the STREAMING chat answer just as
	// obvious: the same ID ("bossmsg-"+messageID / demo "boss-N") reappears
	// Pending:true with a growing (accum N chars) figure, then one
	// [boss-final] line pins it.
	bossStreamCounts := make(map[string]int)
	emit := func(e state.Event) {
		// --answer: capture the first pending permission id now; the actual
		// backend call happens OUTSIDE the emit lock (demo emits
		// synchronously from inside AnswerPermission — calling here would
		// deadlock on mu).
		answerNow := false
		askNow := false
		mu.Lock()
		if *answer && answerPID == "" && e.Kind == state.EvPermission && e.ToolState != "resolved" {
			answerPID = e.PermissionID
			answerNow = true
		}
		// Question auto-answer capture: the backend call happens OUTSIDE the
		// emit lock (same deadlock reasoning as --answer above).
		if autoAsk && askQID == "" && e.Kind == state.EvQuestion && e.ToolState == "pending" {
			askQID = e.QuestionID
			askNow = true
		}
		if e.Kind == state.EvQuestion && e.ToolState == "resolved" {
			questionResolved = true
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
		if e.Kind == state.EvChatBoss {
			if !e.Msg.Pending {
				select {
				case bossCh <- e.Msg.Text:
				default:
				}
			}
			switch {
			case e.Msg.Pending:
				// Stream update (delta growth or the Send placeholder): one
				// line per emit, same ID, growing accum — NEVER a new bubble.
				bossStreamCounts[e.Msg.ID]++
				n := bossStreamCounts[e.Msg.ID]
				fmt.Printf("[boss-stream#%d (accum %d chars)] id=%s %q\n",
					n, len([]rune(e.Msg.Text)), e.Msg.ID, tail(e.Msg.Text, 80))
				mu.Unlock()
				return
			case strings.HasPrefix(e.Msg.ID, "bossmsg-") || e.Msg.Kind == "boss":
				// The completion pin (or an interrupted-stream flush).
				fmt.Printf("[boss-final] %s %q\n", e.Msg.ID, trunc(e.Msg.Text, 120))
				mu.Unlock()
				return
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
		if askNow {
			qid := askQID
			fmt.Printf("[ask] question %s captured; auto-answering in %s with %q\n", qid, askDelay, askAnswers)
			time.AfterFunc(askDelay, func() {
				err := b.AnswerQuestion(qid, askAnswers)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					askErr = err.Error()
					fmt.Printf("[ask] question %s -> ERROR: %v\n", qid, err)
				} else {
					fmt.Printf("[ask] question %s -> ok (backend.AnswerQuestion(%q, %q))\n", qid, qid, askAnswers)
				}
			})
		}
	}

	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fail("start", err)
	}

	// --batch-probe: queue-flush contract probe. Mirror the (hardcoded)
	// queue onto the agentmemory board via the QueueItemStart/Done seam the
	// app type-asserts, send ONE composed batch exactly like the app's
	// flush does, wait up to 60s for the completed boss reply, and assert
	// the reply covers BOTH queued items.
	if *batchProbe {
		qb, _ := b.(queueBoard)
		items := []string{
			"Answer this quiz line: which planet is known as the Red Planet? (answer: Mars)",
			"Answer this quiz line: which metal element has the symbol W? (answer: Tungsten)",
		}
		boardIDs := make([]string, len(items))
		for i, title := range items {
			if qb != nil {
				boardIDs[i] = qb.QueueItemStart(i+1, title)
			}
			fmt.Printf("BATCH-PHASE queue  QUE-%d %q board=%q\n", i+1, title, boardIDs[i])
		}
		// The composed batch literal, in the shape the app's queue-flush
		// composes: one header naming the count, one line per QUE item.
		var sb strings.Builder
		fmt.Fprintf(&sb, "[grafeio office] QUEUE FLUSH: %d queued items arrived together. Work ALL %d items in this one turn, then reply confirming EACH item by its QUE id and short answer.\n", len(items), len(items))
		for i, title := range items {
			fmt.Fprintf(&sb, "QUE-%d: %s\n", i+1, title)
		}
		fmt.Printf("BATCH-PHASE compose\n%s", sb.String())
		fmt.Println("BATCH-PHASE send")
		if err := b.Send(sb.String()); err != nil {
			fail("send", err)
		}
		fmt.Println("BATCH-PHASE awaiting-boss (60s budget)")
		turn := collectTurn(bossCh, 60*time.Second)
		fmt.Printf("BATCH-PHASE boss-replies %d completed bubble(s)\n", len(turn))
		for i, t := range turn {
			fmt.Printf("[assert]   #%d %q\n", i+1, trunc(t, 200))
		}
		joined := strings.ToLower(strings.Join(turn, "\n"))
		failures := 0
		check := func(ok bool, label string) {
			if ok {
				fmt.Printf("[assert] PASS %s\n", label)
			} else {
				failures++
				fmt.Printf("[assert] FAIL %s\n", label)
			}
		}
		check(len(turn) > 0, "boss produced a completed reply inside the budget")
		check(strings.Contains(joined, "mars"), "boss reply covers QUE-1 (Mars)")
		check(strings.Contains(joined, "tungsten"), "boss reply covers QUE-2 (Tungsten)")
		for i, id := range boardIDs {
			if qb != nil && id != "" {
				qb.QueueItemDone(id)
				fmt.Printf("BATCH-PHASE board-done QUE-%d %s\n", i+1, id)
			}
		}
		if err := b.Stop(); err != nil {
			fail("stop", err)
		}
		fmt.Println("[done] backend stopped")
		if failures == 0 {
			fmt.Println("BATCH-PROBE: OK")
			return
		}
		fmt.Printf("BATCH-PROBE: FAIL (%d check(s) failed)\n", failures)
		os.Exit(1)
	}

	// --ask: question-loop regression probe. Send the question-tool prompt,
	// the emit callback auto-answers the first pending question (2s delay,
	// per spec), then assert against the 15s total budget measured from
	// process start (the serve spawn already consumed part of it).
	if *ask {
		askPrompt := "use the question tool to ask me exactly ONE question and then, after my answer, confirm back what I answered."
		fmt.Printf("[ask] prompt %q\n", askPrompt)
		if err := b.Send(askPrompt); err != nil {
			fail("send", err)
		}
		remaining := time.Until(askDeadline)
		if remaining < 0 {
			remaining = 0
		}
		turn := collectTurn(bossCh, remaining)
		mu.Lock()
		qid := askQID
		resolved := questionResolved
		aerr := askErr
		mu.Unlock()
		final := ""
		if len(turn) > 0 {
			final = turn[len(turn)-1]
		}
		failures := 0
		check := func(ok bool, label string) {
			if ok {
				fmt.Printf("[assert] PASS %s\n", label)
			} else {
				failures++
				fmt.Printf("[assert] FAIL %s\n", label)
			}
		}
		check(qid != "", "a question request surfaced (EvQuestion pending)")
		check(aerr == "", "backend.AnswerQuestion round-trip succeeded")
		check(resolved, "question resolved event arrived (EvQuestion resolved)")
		check(len(turn) > 0 && strings.Contains(strings.ToLower(final), "toggle"),
			"final completed chat-boss references the answer (\"toggle\")")
		if len(turn) > 0 {
			for i, t := range turn {
				fmt.Printf("[assert] completed bubble #%d %q\n", i+1, trunc(t, 200))
			}
		}
		if err := b.Stop(); err != nil {
			fail("stop", err)
		}
		fmt.Println("[done] backend stopped")
		if failures == 0 {
			fmt.Println("QUESTION-LOOP: FIXED")
			return
		}
		fmt.Printf("QUESTION-LOOP: STUCK (%d check(s) failed)\n", failures)
		os.Exit(1)
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

// queueBoard is the board-mirror seam backends expose OUTSIDE
// state.Backend (the app headlessly type-asserts it): one pending
// agentmemory action per queued office item, marked done on completion.
type queueBoard interface {
	QueueItemStart(index int, title string) string
	QueueItemDone(boardID string)
}

// loadConfig resolves the run's brain.json: an explicit --cfg path wins
// (defaults-filled, read-only — never written back), otherwise the standard
// loader honors GRAFEIO_HOME like every other grafeio binary.
func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return config.Load()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := config.Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Boss.Name == "" {
		cfg.Boss.Name = "boss (oikonomos)"
	}
	if cfg.Backend.AgentmemoryURL == "" {
		cfg.Backend.AgentmemoryURL = "http://localhost:3111"
	}
	return cfg, nil
}

// runEfficiencySim plays the agentmemory poll backoff with no server: 8
// consecutive no-change syncs (interval doubles after every 5, capped at 4x
// base), then a change (cadence snaps back to base). The printed lines are
// byte-shaped exactly like the live backend's status messages so a live
// probe can be grepped against the same wording later.
func runEfficiencySim() {
	base := 5 * time.Second
	interval := base
	noChange := 0
	fmt.Printf("[efficiency] base poll %s; %d consecutive no-change syncs double the interval (cap %dx base)\n",
		base.Round(time.Second), 5, 4)
	for i := 1; i <= 11; i++ {
		changed := i == 11
		if changed {
			fmt.Printf("[efficiency] #%d: change observed -> next poll in %s\n", i, base.Round(time.Second))
			noChange, interval = 0, base
			continue
		}
		noChange++
		if next := backend.BackoffInterval(base, interval, noChange); next != interval {
			interval = next
			fmt.Printf("[efficiency] #%d no-change x%d -> next poll in %s (backoff)\n", i, noChange, interval.Round(time.Second))
		} else {
			fmt.Printf("[efficiency] #%d no-change x%d -> next poll in %s\n", i, noChange, interval.Round(time.Second))
		}
	}
	fmt.Println("EFFICIENCY: OK")
}

// --- office-session persist probes -------------------------------------------

// persistSeam is the office-session seam the live backend exposes
// (type-asserted; internal/backend PrimaryOverride/PrimaryID +
// internal/app sessions.go). officeSpawnSeam is the /new leg.
type persistSeam interface {
	PrimaryOverride(id string)
	PrimaryID() string
}
type officeSpawnSeam interface {
	NewOffice() (string, error)
}

// runPersistProbe drives the three-run office-session proof:
//
//	run 1 --persist-demo    (own GRAFEIO_HOME): boot live, send "say
//	                        pineapple", wait for the completed bubble,
//	                        persist the office session, print session.json.
//	                        Verdict: PERSIST: SAVED.
//	run 2 --persist-restore (SAME GRAFEIO_HOME + cwd as run 1): rebuild the
//	                        boot exactly as app.New does — LoadSession,
//	                        PrimaryOverride before Start — then assert the
//	                        restore notice line and that the SAME primary id
//	                        got reused (the saved session sits far under the
//	                        50-msg stale guard). Verdict: PERSIST: RESTORED.
//	run 3 --persist-new     (same home + cwd): the /new leg — NewOffice()
//	                        must mint a FRESH primary (!= the saved one),
//	                        print the /new notice, and prove that a persist
//	                        overwrite keeps the LATEST id in session.json
//	                        (always-latest-wins). Verdict: PERSIST: NEW.
func runPersistProbe(cfgPath string, demo, restore, fresh bool) {
	count := 0
	for _, on := range []bool{demo, restore, fresh} {
		if on {
			count++
		}
	}
	if count != 1 {
		fmt.Fprintln(os.Stderr, "exactly one of --persist-demo / --persist-restore / --persist-new")
		os.Exit(2)
	}

	// Scratch home: run 1 creates one when GRAFEIO_HOME is unset (and
	// prints it — runs 2/3 MUST be invoked with that same home exported);
	// runs 2/3 hard-require it so a stray run cannot read/write the real
	// ~/.grafeio/sessions.
	home := os.Getenv("GRAFEIO_HOME")
	if home == "" {
		if !demo {
			fmt.Fprintln(os.Stderr, "GRAFEIO_HOME is required for --persist-restore / --persist-new (export the [persist-home] path printed by run 1)")
			os.Exit(2)
		}
		var err error
		home, err = os.MkdirTemp("", "grafeio-persist-home")
		if err != nil {
			fail("persist home", err)
		}
		if err := os.Setenv("GRAFEIO_HOME", home); err != nil {
			fail("persist home env", err)
		}
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		fail("persist home mkdir", err)
	}
	fmt.Printf("[persist-home] %s\n", home)

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		fail("config", err)
	}
	dir, err := os.Getwd()
	if err != nil {
		fail("getwd", err)
	}
	fmt.Printf("[cfg] boss=%q model=%q dir=%q session=%s\n",
		cfg.Boss.Name, cfg.Boss.Model, dir, app.SessionPath(dir))

	switch {
	case demo:
		persistRunSave(cfg, dir)
	case restore:
		persistRunRestore(cfg, dir)
	case fresh:
		persistRunNew(cfg, dir)
	}
}

// persistLiveBoot spawns/attaches the live backend exactly like a real UI
// boot, returning the backend plus a boss-reply channel. The emit also
// mirrors the chat-user/BOSS final bubbles into chatLog so run 1 can
// persist the transcript the office actually showed.
func persistLiveBoot(cfg *config.Config, dir string, chatLog *[]state.ChatMsg) (state.Backend, chan string) {
	bossCh := make(chan string, 64)
	var mu sync.Mutex
	emit := func(e state.Event) {
		printEvent(e)
		mu.Lock()
		switch e.Kind {
		case state.EvChatUser:
			*chatLog = append(*chatLog, e.Msg)
		case state.EvChatBoss:
			if !e.Msg.Pending && !strings.HasPrefix(e.Msg.ID, "boss-") {
				*chatLog = append(*chatLog, e.Msg)
				select {
				case bossCh <- e.Msg.Text:
				default:
				}
			}
		}
		mu.Unlock()
	}
	b := backend.NewLive("", dir, cfg)
	if err := b.Start(emit); err != nil {
		fail("start", err)
	}
	return b, bossCh
}

// persistRunSave — run 1: live boot, one prompt, persist, print the file.
func persistRunSave(cfg *config.Config, dir string) {
	var chat []state.ChatMsg
	b, bossCh := persistLiveBoot(cfg, dir, &chat)
	fmt.Printf("[prompt] %q\n", "say pineapple")
	if err := b.Send("say pineapple"); err != nil {
		fail("send", err)
	}
	turn := collectTurn(bossCh, 60*time.Second)
	if len(turn) == 0 {
		_ = b.Stop()
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — no completed boss bubble within 60s")
		os.Exit(1)
	}
	for i, t := range turn {
		fmt.Printf("[persist] completed bubble #%d %q\n", i+1, trunc(t, 200))
	}
	ps, ok := b.(persistSeam)
	if !ok {
		_ = b.Stop()
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — live backend does not expose the PrimaryOverride/PrimaryID seam")
		os.Exit(1)
	}
	primaryID := ps.PrimaryID()
	if primaryID == "" {
		_ = b.Stop()
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — no primary session id after Start")
		os.Exit(1)
	}
	// Snapshot + atomic write — the exact same seam the UI's quit path runs
	// (app.PersistSession → Snapshot + SaveSession).
	sf := app.Snapshot(dir, primaryID, state.OfficeState{Chat: chat})
	if err := app.SaveSession(dir, sf); err != nil {
		fail("persist write", err)
	}
	if err := b.Stop(); err != nil {
		fail("stop", err)
	}
	fmt.Println("[done] backend stopped")
	raw, err := os.ReadFile(app.SessionPath(dir))
	if err != nil {
		fail("session.json read-back", err)
	}
	fmt.Printf("--- session.json (%s) ---\n%s\n", app.SessionPath(dir), raw)
	fmt.Printf("PERSIST: SAVED (primary=%s msgs=%d)\n", primaryID, len(chat))
}

// persistRunRestore — run 2: the restore boot. Mirrors app.New exactly:
// LoadSession + Fresh gate + PrimaryOverride BEFORE Start; after Start the
// saved primary id MUST have won (server still has the session — the
// 404/fetch-failure degrade path is commented, not exercised here).
func persistRunRestore(cfg *config.Config, dir string) {
	sf, ok := app.LoadSession(dir)
	if !ok {
		fmt.Fprintf(os.Stderr, "PERSIST: FAIL — no session.json for %s under %s (run --persist-demo first)\n", dir, os.Getenv("GRAFEIO_HOME"))
		os.Exit(1)
	}
	if !sf.Fresh() {
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — session.json is stale (older than 4 days)")
		os.Exit(1)
	}

	b := backend.NewLive("", dir, cfg)
	ps, ok := b.(persistSeam)
	if !ok {
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — live backend does not expose the PrimaryOverride/PrimaryID seam")
		os.Exit(1)
	}
	// The app.New ordering contract: override lands BEFORE Start.
	ps.PrimaryOverride(sf.PrimaryID)
	if err := b.Start(func(e state.Event) { printEvent(e) }); err != nil {
		fail("start", err)
	}
	restored := ps.PrimaryID()
	if err := b.Stop(); err != nil {
		fail("stop", err)
	}
	fmt.Println("[done] backend stopped")

	notice := app.RestoreNotice(sf)
	fmt.Printf("[notice] %s\n", notice)
	failures := 0
	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}
	check(restored == sf.PrimaryID && restored != "",
		fmt.Sprintf("same primary id reused (saved %s -> live %s)", sf.PrimaryID, restored))
	check(len(sf.Chat) < 50, fmt.Sprintf("session under the 50-msg stale guard (%d msgs)", len(sf.Chat)))
	check(strings.Contains(notice, "restored office session from") && strings.Contains(notice, "/new for a fresh office"),
		"restore notice line is the office session wording")
	if failures == 0 {
		fmt.Println("PERSIST: RESTORED")
		return
	}
	fmt.Printf("PERSIST: RESTORE-FAIL (%d check(s) failed)\n", failures)
	os.Exit(1)
}

// persistRunNew — run 3: the /new leg. NewOffice() must mint a FRESH
// primary (!= the saved one), the /new notice must be the office-session
// wording, and the overwrite must keep the LATEST primary in session.json.
func persistRunNew(cfg *config.Config, dir string) {
	sf, ok := app.LoadSession(dir)
	if !ok {
		fmt.Fprintf(os.Stderr, "PERSIST: FAIL — no session.json for %s (run --persist-demo first)\n", dir)
		os.Exit(1)
	}
	b := backend.NewLive("", dir, cfg)
	// /new does NOT restore (the member asked for a fresh office) — no
	// PrimaryOverride here. Start's normal find-or-create runs; in this
	// directory that already reuses the previous session, which makes the
	// NewOffice id difference the strong assert.
	if err := b.Start(func(e state.Event) { printEvent(e) }); err != nil {
		fail("start", err)
	}
	ob, ok := b.(officeSpawnSeam)
	if !ok {
		_ = b.Stop()
		fmt.Fprintln(os.Stderr, "PERSIST: FAIL — live backend does not expose the NewOffice seam")
		os.Exit(1)
	}
	newID, err := ob.NewOffice()
	if err != nil {
		_ = b.Stop()
		fmt.Fprintf(os.Stderr, "PERSIST: FAIL — NewOffice: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[notice] %s\n", app.NewOfficeNotice)
	if ps, ok := b.(persistSeam); ok {
		fmt.Printf("[persist] primary now %s (was %s)\n", ps.PrimaryID(), sf.PrimaryID)
	}
	// The app's /new + quit path: the NEXT persist writes the fresh
	// session (the previous transcript was archived, never deleted — and
	// this overwrite proves always-latest-wins from THIS dir onward).
	if err := app.SaveSession(dir, app.Snapshot(dir, newID, state.OfficeState{})); err != nil {
		fail("persist overwrite", err)
	}
	if err := b.Stop(); err != nil {
		fail("stop", err)
	}
	fmt.Println("[done] backend stopped")

	failures := 0
	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}
	check(newID != "" && newID != sf.PrimaryID,
		fmt.Sprintf("fresh primary id differs from the saved one (saved %s -> new %s)", sf.PrimaryID, newID))
	check(strings.Contains(app.NewOfficeNotice, "new office spawned") &&
		strings.Contains(app.NewOfficeNotice, "archived (kept on disk)"),
		"/new notice is the office-session wording")
	// always-latest-wins: the re-written session.json threads the NEW id.
	sf2, ok2 := app.LoadSession(dir)
	check(ok2 && sf2.PrimaryID == newID, "session.json overwrite keeps the new primary (always-latest-wins)")
	raw, _ := os.ReadFile(app.SessionPath(dir))
	fmt.Printf("--- session.json (%s) ---\n%s\n", app.SessionPath(dir), raw)
	if failures == 0 {
		fmt.Println("PERSIST: NEW")
		return
	}
	fmt.Printf("PERSIST: NEW-FAIL (%d check(s) failed)\n", failures)
	os.Exit(1)
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
