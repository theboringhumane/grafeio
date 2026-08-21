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
//	grafeio-headless --charter-probe
//	                            oikonomos-charter wiring probe: scratch dir,
//	                            EnsureCharter twice (identical bytes, second
//	                            run changed=false), spawn a REAL opencode
//	                            serve rooted in the scratch with a curated
//	                            env (GRAFEIO_*/OPENCODE_SERVER stripped),
//	                            create a session, ask the boss who it is +
//	                            the dispatch minimum, assert the reply names
//	                            (manager|oikonomos) AND (three|3). Prints
//	                            CHARTER-PROBE: ACTIVE (exit 0) else exit 1.
//	grafeio-headless --abort-probe
//	                            /stop probe (forces live): print the opencode
//	                            serve /doc abort route excerpt, then send the
//	                            long-running prompt "write a 2000-word essay
//	                            about kelp", wait 2s plus (up to 10s) until the
//	                            essay is visibly streaming so the abort bites
//	                            DURING generation, call the backend's
//	                            AbortSessions (state.SessionAborter seam), and
//	                            assert within a 6s watch: a stopped marker
//	                            (placeholder close or interrupted-stream
//	                            flush) AND no further boss-bubble stream
//	                            growth beyond a 1.5s in-flight grace. Prints
//	                            "STOP: OK" (exit 0) or "STOP: FAIL" (exit 1).
//	grafeio-headless --concierge-probe
//	                            office concierge / busy-boss probe (forces
//	                            live): boss send "count from 1 to 300 with one
//	                            number per line, then stop" (still working),
//	                            2s later SendConcierge("what is 6x7") — assert
//	                            a completed office bubble carrying "42" within
//	                            25s; then SendConcierge("create
//	                            /tmp/gfx-conc-test.txt with one line: grapes")
//	                            — assert within 60s a child hire (of the
//	                            concierge) AND the file on disk AND an office
//	                            message acknowledging it. A quiet-boss gate
//	                            asserts the concierge session is created
//	                            lazily (never by boss traffic). Prints
//	                            "CONCIERGE: OK" (exit 0) else "CONCIERGE: FAIL"
//	                            (exit 1).
//	grafeio-headless --sse-sim
//	                            SSE reconnect sim (D1): a fake standard-library
//	                            serve emulates session list/create plus a
//	                            scripted /event flap profile (clean closes,
//	                            then HTTP 500s, then revival); the live
//	                            backend's full Start runs against it. Asserts
//	                            the reconnect ladder reads 1s,2s,5s,10s,30s,30s
//	                            (capped) measured server-side, resets to 1s
//	                            after revival, and the outage emits exactly
//	                            one closed note + one error-class note + one
//	                            "event stream: reconnected". Prints
//	                            "SSE-SIM: OK" (exit 0) else exit 1.
//
// The plain demo run also auto-answers its scripted boss question
// (que-demo-1, 800ms after it surfaces) so the registration + resolved
// lines always show.
//
// Every state.Event is printed as one line (kind + key fields) so a smoke
// run can assert the floor contract without a renderer.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/grafeio/internal/app"
	"github.com/theboringhumane/grafeio/internal/backend"
	"github.com/theboringhumane/grafeio/internal/charter"
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
	charterProbe := flag.Bool("charter-probe", false, "manager-charter wiring probe: scratch dir, EnsureCharter twice (bytes identical), spawn a REAL serve rooted in the scratch, ask the boss who it is + the dispatch minimum, assert (manager|oikonomos)+(three|3) — CHARTER-PROBE: ACTIVE (exit 0) else exit 1")
	abortProbe := flag.Bool("abort-probe", false, "live mode: /stop probe — /doc abort excerpt, send the 2000-word kelp essay, wait 2s, AbortSessions via the state.SessionAborter seam, assert stopped marker + stream growth stops (STOP: OK|FAIL)")
	sseSim := flag.Bool("sse-sim", false, "SSE reconnect sim (D1): fake flapping serve, assert backoff ladder 1s/2s/5s/10s/30s capped + ladder reset + exactly one closed note + one error-class note + one reconnected note (SSE-SIM: OK|FAIL)")
	conciergeProbe := flag.Bool("concierge-probe", false, "live mode: office concierge probe — busy boss ('count 1..300') + SendConcierge: assert quiet-boss laziness (no concierge session), a completed office bubble answering 42 within 25s, and (grapes file probe) a concierge-dispatched worker hire + /tmp/gfx-conc-test.txt + an office acknowledgment within 60s (CONCIERGE: OK|FAIL)")
	flag.Parse()

	// --charter-probe is a standalone probe with its own harness (own
	// scratch dir, own serve, own asserts) — it never loads brain.json.
	if *charterProbe {
		runCharterProbe()
		return
	}

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
	// --sse-sim is a fully standalone probe (own fake serve, own harness,
	// no brain.json knobs worth reading) — it runs before any live wiring.
	if *sseSim {
		os.Exit(runSSESim())
	}
	if *abortProbe && (*prompt != "" || *prompt2 != "" || *ask || *batchProbe) {
		fmt.Fprintln(os.Stderr, "--abort-probe is a standalone probe: do not combine with --prompt/--prompt2/--ask/--batch-probe")
		os.Exit(2)
	}
	if *ask || *batchProbe {
		*live = true
		*demo = false
	}

	// --abort-probe: /stop regression probe with its own harness (own emit;
	// the shared one below is shaped for demo/stale-repro runs).
	if *abortProbe {
		os.Exit(runAbortProbe(cfg))
	}
	if *conciergeProbe && (*prompt != "" || *prompt2 != "" || *ask || *batchProbe) {
		fmt.Fprintln(os.Stderr, "--concierge-probe is a standalone probe: do not combine with --prompt/--prompt2/--ask/--batch-probe")
		os.Exit(2)
	}
	// --concierge-probe: office concierge / busy-boss probe with its own
	// harness (own emit, own live backend — never the demo).
	if *conciergeProbe {
		os.Exit(runConciergeProbe(cfg))
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

// probeHTTPClient is the probe's plain control client (no SSE needed).
var probeHTTPClient = &http.Client{Timeout: 30 * time.Second}

// probeEnv is os.Environ() minus everything the probe must not inherit:
// GRAFEIO_* (the strip rule) and OPENCODE_SERVER (must not redirect the
// spawned-child resolution; serve reads it, ditto GRAFEIO_SERVER).
func probeEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if strings.HasPrefix(k, "GRAFEIO_") || k == "OPENCODE_SERVER" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// spawnServeForProbe mirrors internal/backend's spawnServe ("opencode
// serve --port 0 --hostname 127.0.0.1", URL scanned from stdout, 10s
// budget) but rooted in dir and run under probeEnv(). Kept local to the
// probe: the backend's spawnServe is unexported, and the probe needs the
// clean-env spawn the app itself does NOT do.
func spawnServeForProbe(dir string) (string, *exec.Cmd, error) {
	cmd := exec.Command("opencode", "serve", "--port", "0", "--hostname", "127.0.0.1")
	cmd.Dir = dir
	cmd.Env = probeEnv()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("probe serve spawn failed: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("probe serve spawn failed: %w", err)
	}
	urlRe := regexp.MustCompile(`https?://\S+`)
	type result struct{ url string }
	urlCh := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			fmt.Printf("[serve] %s\n", line)
			if m := urlRe.FindString(line); m != "" {
				m = regexp.MustCompile(`[.,;)\]]+$`).ReplaceAllString(m, "")
				select {
				case urlCh <- result{url: m}:
				default:
				}
			}
		}
	}()
	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()
	select {
	case r := <-urlCh:
		return r.url, cmd, nil
	case err := <-exitCh:
		return "", nil, fmt.Errorf("probe serve exited before printing a URL: %v", err)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-exitCh
		return "", nil, fmt.Errorf("probe serve: no listening URL within 15s")
	}
}

// probeDoJSON is the probe's minimal x-opencode-directory control call.
func probeDoJSON(baseURL, dir, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-opencode-directory", url.QueryEscape(dir))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := probeHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", res.StatusCode, string(data)[:min(200, len(data))])
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// probeCreateSession makes one root session titled title.
func probeCreateSession(baseURL, dir, title string) (struct{ ID string }, error) {
	var created struct{ ID string }
	body, _ := json.Marshal(map[string]any{"title": title})
	err := probeDoJSON(baseURL, dir, http.MethodPost, "/session", body, &created)
	return created, err
}

// probePrompt posts one prompt and polls until an assistant message with a
// text part appears (bounded by timeout), returning that text.
func probePrompt(baseURL, dir, sessionID, text string, timeout time.Duration) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"parts": []map[string]any{{"type": "text", "text": text}},
	})
	if err := probeDoJSON(baseURL, dir, http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var rows []struct {
			Info struct {
				Role string `json:"role"`
			} `json:"info"`
			Parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"parts"`
		}
		if err := probeDoJSON(baseURL, dir, http.MethodGet, "/session/"+sessionID+"/message", nil, &rows); err != nil {
			return "", err
		}
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Info.Role != "assistant" {
				continue
			}
			for _, p := range rows[i].Parts {
				if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
					return strings.TrimSpace(p.Text), nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("no assistant text within %s", timeout)
}

// --- manager charter probe --------------------------------------------------

// runCharterProbe proves the oikonomos charter is wired end-to-end:
//
//  1. scratch temp dir; print the EnsureCharter notes per run;
//  2. dump the created/merged files' bytes (.opencode/oikonomos.md first
//     40 lines + the whole opencode.json);
//  3. run EnsureCharter a SECOND time: changed must be false and the bytes
//     must be identical (idempotence);
//  4. spawn a REAL opencode serve rooted in the scratch dir (fresh clean
//     env for the child: OPENCODE_SERVER + GRAFEIO_* stripped — the probe
//     proves the WIRING, not the user's machine), create a session, and
//     prompt: "in 2 sentences: who are you supposed to be and how many
//     sub-agents minimum for real work";
//  5. assert the reply mentions (manager|oikonomos) AND (three|3)
//     (case-insensitive). Prints CHARTER-PROBE: ACTIVE (exit 0) else the
//     failed checks + exit 1.
func runCharterProbe() {
	os.Exit(charterProbeMain())
}

// charterProbeMain is the probe body with a real return value: every exit
// path runs through it so the spawned serve is ALWAYS killed+reaped before
// the process dies (a bare os.Exit(1) under a running serve orphans the
// child, which keeps the parent's shell pipe open AND burns a port).
func charterProbeMain() int {
	failures := 0
	check := func(ok bool, label string) {
		if ok {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}

	// 1-3: the EnsureCharter pass + idempotence, in a scratch dir.
	scratch, err := os.MkdirTemp("", "grafeio-charter-probe")
	if err != nil {
		fmt.Printf("[fatal] scratch dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(scratch)
	fmt.Printf("[charter-probe] scratch %s\n", scratch)

	changed1, notes1 := backend.EnsureCharter(scratch)
	fmt.Printf("[charter-probe] EnsureCharter #1 changed=%v notes=%v\n", changed1, notes1)
	check(changed1, "first EnsureCharter reports changed=true")
	check(len(notes1) > 0 && strings.Contains(notes1[len(notes1)-1], "manager charter: wired"),
		"first run's notes announce the wiring")

	chartPath := filepath.Join(scratch, ".opencode", "oikonomos.md")
	cfgPath := filepath.Join(scratch, ".opencode", "opencode.json")
	chart1, err1 := os.ReadFile(chartPath)
	cfg1, err2 := os.ReadFile(cfgPath)
	check(err1 == nil, "oikonomos.md exists after run 1")
	check(err2 == nil, "opencode.json exists after run 1")
	check(string(chart1) == charter.Text, "oikonomos.md is byte-exact the embedded charter")

	// File dumps: the merged config in full, the charter's head.
	fmt.Printf("--- %s ---\n%s\n", cfgPath, string(cfg1))
	lines := strings.Split(string(chart1), "\n")
	head := lines
	if len(head) > 40 {
		head = append(head[:40], "... (truncated, "+fmt.Sprint(len(lines)-40)+" more lines)")
	}
	fmt.Printf("--- %s (head %d/%d lines) ---\n%s\n", chartPath, len(head), len(lines), strings.Join(head, "\n"))

	changed2, notes2 := backend.EnsureCharter(scratch)
	fmt.Printf("[charter-probe] EnsureCharter #2 changed=%v notes=%v\n", changed2, notes2)
	chart2, _ := os.ReadFile(chartPath)
	cfg2, _ := os.ReadFile(cfgPath)
	check(!changed2, "second EnsureCharter reports changed=false (idempotent)")
	check(string(chart2) == string(chart1), "oikonomos.md bytes identical after run 2")
	check(string(cfg2) == string(cfg1), "opencode.json bytes identical after run 2")
	check(charter.ContainsPhrases([]string{"MANAGER", "oikonomos", "MINIMUM 3"}),
		"charter subset probe: manager + oikonomos + MINIMUM 3 all present")

	// 4: the ground-truth serve pass. A fresh child env strips
	// OPENCODE_SERVER and every GRAFEIO_* so the probe measures ONLY what
	// the wiring itself does (the user's OPENCODE_SERVER could point at a
	// serve that loaded other instructions).
	if failures > 0 {
		fmt.Printf("CHARTER-PROBE: FAIL (%d wiring check(s) failed) — skipping the serve pass\n", failures)
		return 1
	}
	fmt.Printf("[charter-probe] spawning real serve rooted in %s\n", scratch)
	baseURL, proc, err := spawnServeForProbe(scratch)
	if err != nil {
		fmt.Printf("CHARTER-PROBE: FAIL — serve spawn: %v\n", err)
		return 1
	}
	// kill+reap runs via defer inside charterProbeMain — a real return, so
	// every exit path (fail or pass) reaps the serve, never os.Exit(1) with
	// a live child.
	defer func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
			_ = proc.Wait()
		}
	}()
	fmt.Printf("[charter-probe] serve at %s\n", baseURL)

	sess, err := probeCreateSession(baseURL, scratch, "charter probe")
	if err != nil {
		fmt.Printf("CHARTER-PROBE: FAIL — session create: %v\n", err)
		return 1
	}
	fmt.Printf("[charter-probe] session %s\n", sess.ID)

	prompt := "in 2 sentences: who are you supposed to be and how many sub-agents minimum for real work"
	fmt.Printf("[charter-probe] prompt %q\n", prompt)
	reply, err := probePrompt(baseURL, scratch, sess.ID, prompt, 90*time.Second)
	if err != nil {
		fmt.Printf("CHARTER-PROBE: FAIL — prompt: %v\n", err)
		return 1
	}
	fmt.Printf("[charter-probe] boss reply: %q\n", reply)

	lower := strings.ToLower(reply)
	check(strings.Contains(lower, "manager") || strings.Contains(lower, "oikonomos"),
		"reply mentions manager|oikonomos")
	check(strings.Contains(lower, "three") || strings.Contains(lower, "3"),
		"reply mentions the 3-dispatch minimum (three|3)")

	if failures == 0 {
		fmt.Println("CHARTER-PROBE: ACTIVE")
		return 0
	}
	fmt.Printf("CHARTER-PROBE: FAIL (%d check(s) failed)\n", failures)
	return 1
}

// --- /stop abort probe --------------------------------------------------------

// runDocAbortExcerpt spawns a short-lived probe serve, fetches its /doc, and
// prints the abort route excerpt (the route AbortSessions is about to call —
// proof it exists on THIS opencode build before the probe relies on it).
func runDocAbortExcerpt(dir string) {
	fmt.Println("[doc] probing spawned opencode serve /doc for the abort route ...")
	baseURL, proc, err := spawnServeForProbe(dir)
	if err != nil {
		fmt.Printf("[doc] WARN: serve spawn failed: %v (skipping /doc excerpt)\n", err)
		return
	}
	defer func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
			_ = proc.Wait()
		}
	}()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/doc", nil)
	if err != nil {
		fmt.Printf("[doc] WARN: /doc request: %v\n", err)
		return
	}
	res, err := probeHTTPClient.Do(req)
	if err != nil {
		fmt.Printf("[doc] WARN: /doc fetch: %v\n", err)
		return
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Printf("[doc] WARN: /doc read: %v\n", err)
		return
	}
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Responses   map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		fmt.Printf("[doc] WARN: /doc parse: %v\n", err)
		return
	}
	fmt.Println("[doc] --- abort route excerpt (spawned opencode serve /doc) ---")
	found := false
	var paths []string
	for path := range doc.Paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		var methods []string
		for m := range doc.Paths[path] {
			methods = append(methods, m)
		}
		sort.Strings(methods)
		for _, m := range methods {
			op := doc.Paths[path][m]
			if !strings.Contains(strings.ToLower(path+" "+op.OperationID), "abort") {
				continue
			}
			found = true
			fmt.Printf("[doc] %s %s  operationId=%s\n", strings.ToUpper(m), path, op.OperationID)
			fmt.Printf("[doc]   summary:     %s\n", op.Summary)
			fmt.Printf("[doc]   description: %s\n", op.Description)
			var codes []string
			for c := range op.Responses {
				codes = append(codes, c)
			}
			sort.Strings(codes)
			for _, c := range codes {
				fmt.Printf("[doc]   response %s: %s\n", c, op.Responses[c].Description)
			}
		}
	}
	if !found {
		fmt.Println("[doc] WARN: NO abort route found in /doc — the AbortSessions seam cannot work against this build")
	}
	fmt.Println("[doc] --- end /doc excerpt ---")
}

// runAbortProbe — the --abort-probe harness. Boot the REAL live backend
// (own emit so the run is self-contained), send a prompt whose turn
// outlives the watch window, wait 2s for the turn to engage, call
// AbortSessions via the additive state.SessionAborter seam, then watch 6s.
// PASS requires: the seam exists, the abort round-trip returned nil, a
// stopped marker landed (the "[grafeio] stopped (turn aborted)" placeholder
// close or an interrupted-stream flush — the serve does NOT forward
// primary session.idle as a state.Event, so the marker is the
// app-observable abort proof), and no boss-bubble stream growth past a
// 1.5s in-flight delta grace.
func runAbortProbe(cfg *config.Config) int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Printf("[fatal] getwd: %v\n", err)
		return 1
	}

	// /doc excerpt first: the route the seam calls, printed by the build
	// under test.
	runDocAbortExcerpt(dir)

	// Board offline for a deterministic transcript.
	oldAM := os.Getenv("AGENTMEMORY_URL")
	_ = os.Setenv("AGENTMEMORY_URL", "http://127.0.0.1:9")
	defer func() {
		if oldAM == "" {
			_ = os.Unsetenv("AGENTMEMORY_URL")
		} else {
			_ = os.Setenv("AGENTMEMORY_URL", oldAM)
		}
	}()

	var mu sync.Mutex
	var (
		abortAt     time.Time
		marker      string    // first stopped/interrupted marker text seen post-abort
		growthAfter []float64 // seconds-since-abort of every boss-stream growth update
		streamed    bool      // any boss-stream growth at all (sanity: the essay streams)
		errLines    []string  // post-abort "boss error" bubbles (the serve's "Aborted" echo must be suppressed)
	)
	b := backend.NewLive("", dir, cfg)
	emit := func(e state.Event) {
		mu.Lock()
		switch {
		case e.Kind == state.EvChatBoss && e.Msg.Pending && strings.HasPrefix(e.Msg.ID, "bossmsg-"):
			streamed = true
			if !abortAt.IsZero() {
				growthAfter = append(growthAfter, time.Since(abortAt).Seconds())
			}
		case e.Kind == state.EvChatBoss && !e.Msg.Pending && !abortAt.IsZero():
			if strings.Contains(e.Msg.Text, "[grafeio] stopped") || strings.Contains(e.Msg.Text, "[grafeio] stream interrupted") {
				if marker == "" {
					marker = e.Msg.Text
				}
			}
			if strings.Contains(e.Msg.Text, "boss error:") {
				errLines = append(errLines, e.Msg.Text)
			}
		}
		mu.Unlock()
		printEvent(e)
	}

	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fmt.Printf("[fatal] start: %v\n", err)
		return 1
	}
	prompt := "write a 2000-word essay about kelp"
	fmt.Printf("[abort-probe] prompt %q\n", prompt)
	if err := b.Send(prompt); err != nil {
		_ = b.Stop()
		fmt.Printf("[fatal] send: %v\n", err)
		return 1
	}
	sentAt := time.Now()
	// Wait 2s (the spec), then — so the abort provably bites DURING
	// generation rather than during the model's quiet preamble — up to 10s
	// more for the first boss-stream delta; abort as soon as the essay is
	// visibly streaming. A model that never streams aborts at 12s anyway.
	time.Sleep(2 * time.Second)
	streamWait := time.Now().Add(10 * time.Second)
	for time.Now().Before(streamWait) {
		mu.Lock()
		s := streamed
		mu.Unlock()
		if s {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	streamedPre := streamed
	mu.Unlock()
	fmt.Printf("[abort-probe] boss-stream active pre-abort: %v (%.1fs after send)\n", streamedPre, time.Since(sentAt).Seconds())

	ab, ok := b.(state.SessionAborter)
	abortErr := error(nil)
	if ok {
		mu.Lock()
		abortAt = time.Now()
		mu.Unlock()
		fmt.Println("[abort-probe] calling AbortSessions() ...")
		abortErr = ab.AbortSessions()
		fmt.Printf("[abort-probe] AbortSessions() -> %v\n", abortErr)
	} else {
		fmt.Println("[abort-probe] live backend does NOT expose state.SessionAborter")
	}
	// The 6s watch: markers and trailing growth stamps land via emit.
	time.Sleep(6 * time.Second)
	if err := b.Stop(); err != nil {
		fmt.Printf("[fatal] stop: %v\n", err)
		return 1
	}
	fmt.Println("[done] backend stopped")

	mu.Lock()
	mk := marker
	growth := append([]float64(nil), growthAfter...)
	str := streamed
	errs := append([]string(nil), errLines...)
	mu.Unlock()
	fmt.Printf("[abort-probe] stream growth stamps after abort (s): %v (streamed pre-abort: %v)\n", growth, str)
	if mk != "" {
		fmt.Printf("[abort-probe] stopped marker: %q\n", trunc(mk, 120))
	}

	failures := 0
	check := func(cond bool, label string) {
		if cond {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}
	check(ok, "live backend exposes the state.SessionAborter seam")
	if ok {
		check(abortErr == nil, "AbortSessions round-trip returned nil (all sessions aborted)")
	}
	check(mk != "", "stopped marker within the watch window ([grafeio] stopped / stream interrupted)")
	late := 0
	for _, g := range growth {
		if g > 1.5 { // in-flight delta grace: a delta already on the wire may land
			late++
		}
	}
	check(late == 0, fmt.Sprintf("no boss-bubble stream growth beyond the 1.5s grace (%d late update(s))", late))
	for _, e := range errs {
		fmt.Printf("[abort-probe] post-abort error bubble: %q\n", trunc(e, 120))
	}
	check(len(errs) == 0, fmt.Sprintf("serve's session.error \"Aborted\" echo suppressed — no 'boss error' bubble post-abort (%d seen)", len(errs)))

	if failures == 0 {
		fmt.Println("STOP: OK")
		return 0
	}
	fmt.Printf("STOP: FAIL (%d check(s) failed)\n", failures)
	return 1
}

// --- office concierge / busy-boss probe ---------------------------------------

// conciergeSeam is the additive office-concierge seam the live backend
// exposes (state.ConciergeCapable plus the laziness read-out): the probe
// asserts SendConcierge delivers AND that no concierge session exists
// before first use (the quiet-boss gate).
type conciergeSeam interface {
	state.ConciergeCapable
	ConciergeID() string
}

// waitOffice scans the completed-office-bubble log for a bubble satisfying
// match, polling until timeout. Returns (matched text, true) or ("", false).
func waitOffice(mu *sync.Mutex, texts *[]string, match func(string) bool, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		mu.Lock()
		for _, t := range *texts {
			if match(t) {
				mu.Unlock()
				return t, true
			}
		}
		mu.Unlock()
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runConciergeProbe — the --concierge-probe harness. Boot the REAL live
// backend (own emit), then in order:
//
//  0. QUIET-BOSS / lazy gate: ConciergeID is printed PROACTIVELY before any
//     traffic and again 2s into a pure boss send ("count from 1 to 300 with
//     one number per line, then stop", a turn long enough to still be busy) —
//     boss traffic must NEVER spin the concierge: the id must still be "".
//  1. SendConcierge("what is 6x7") while the boss turn is still streaming;
//     assert a completed EvChatOffice bubble carrying "42" lands within 25s
//     (boss-stream lines keep landing in parallel — the busy boss is visible
//     in the transcript the whole time).
//  2. SendConcierge("create /tmp/gfx-conc-test.txt with one line: grapes");
//     assert within 60s: (a) a child hire (of the concierge — the boss's
//     own children would ALSO satisfy it, but the counting boss never
//     dispatches), (b) the probe file exists on disk with the grapes line,
//     (c) an office message acknowledges the work (references grape/file).
//
// Any permission/question surfacing during the run is auto-answered
// ("once" / a short affirmative) like an attending member, so a dispatched
// child never parks on the modal inside a headless harness. Prints the
// laziness line plus three PASS lines, then "CONCIERGE: OK" (exit 0) or
// "CONCIERGE: FAIL" (exit 1).
func runConciergeProbe(cfg *config.Config) int {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Printf("[fatal] getwd: %v\n", err)
		return 1
	}

	// Board offline for a deterministic transcript (same posture as the
	// abort probe).
	oldAM := os.Getenv("AGENTMEMORY_URL")
	_ = os.Setenv("AGENTMEMORY_URL", "http://127.0.0.1:9")
	defer func() {
		if oldAM == "" {
			_ = os.Unsetenv("AGENTMEMORY_URL")
		} else {
			_ = os.Setenv("AGENTMEMORY_URL", oldAM)
		}
	}()

	const probeFile = "/tmp/gfx-conc-test.txt"
	_ = os.Remove(probeFile) // fresh slate — the probe proves THIS run created it

	var mu sync.Mutex
	var officeTexts []string // completed office bubbles, in order
	var hires []string       // "name(id)" of every employee hire beyond the fixed seats
	b := backend.NewLive("", dir, cfg)
	emit := func(e state.Event) {
		printEvent(e)
		mu.Lock()
		answeredNow := ""
		questionNow := ""
		switch {
		case e.Kind == state.EvChatOffice && !e.Msg.Pending && strings.HasPrefix(e.Msg.ID, "office-"):
			officeTexts = append(officeTexts, e.Msg.Text)
		case e.Kind == state.EvHire && e.Employee.ID != "hr" && e.Employee.Seat != "manager":
			hires = append(hires, e.Employee.Name+"("+e.Employee.ID+")")
		case e.Kind == state.EvPermission && e.ToolState == "pending":
			answeredNow = e.PermissionID
		case e.Kind == state.EvQuestion && e.ToolState == "pending":
			questionNow = e.QuestionID
		}
		mu.Unlock()
		// Attending-member auto-answers, OUTSIDE the emit lock (the demo
		// twin emits synchronously from inside the reply — same deadlock
		// reasoning as --answer above).
		if answeredNow != "" {
			pid := answeredNow
			time.AfterFunc(100*time.Millisecond, func() {
				fmt.Printf("[concierge-probe] auto-answer permission %s -> %v\n", pid, b.AnswerPermission(pid, "once"))
			})
		}
		if questionNow != "" {
			qid := questionNow
			time.AfterFunc(100*time.Millisecond, func() {
				fmt.Printf("[concierge-probe] auto-answer question %s -> %v\n", qid, b.AnswerQuestion(qid, []string{"yes, proceed"}))
			})
		}
	}

	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fmt.Printf("[fatal] start: %v\n", err)
		return 1
	}

	failures := 0
	check := func(cond bool, label string) {
		if cond {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}

	cs, ok := b.(conciergeSeam)
	if !ok {
		_ = b.Stop()
		fmt.Println("[concierge-probe] live backend does NOT expose the office-concierge seam (state.ConciergeCapable + ConciergeID)")
		fmt.Println("CONCIERGE: FAIL (seam missing)")
		return 1
	}

	// 0. Quiet-boss / lazy gate: printed proactively BEFORE anything so the
	// transcript proves the concierge does not exist yet.
	fmt.Printf("[concierge-probe] status: concierge session before any traffic = %q (must be empty — lazy first-use)\n", cs.ConciergeID())
	bossPrompt := "count from 1 to 300 with one number per line, then stop"
	fmt.Printf("[concierge-probe] boss prompt (keeps the turn busy) %q\n", bossPrompt)
	if err := b.Send(bossPrompt); err != nil {
		fmt.Printf("[fatal] boss send: %v\n", err)
		_ = b.Stop()
		return 1
	}
	time.Sleep(2 * time.Second)
	lazyID := cs.ConciergeID()
	fmt.Printf("[concierge-probe] status: 2s into the busy boss turn, concierge session = %q (quiet-boss gate: must STILL be empty)\n", lazyID)
	check(cs.ConciergeID() == "", "quiet-boss: boss traffic left the concierge asleep (lazy first-use — no session created by Send)")

	// 1. The trivial question while the boss is busy.
	fmt.Println(`[concierge-probe] SendConcierge("what is 6x7") while the boss is still counting`)
	if err := cs.SendConcierge("what is 6x7"); err != nil {
		fmt.Printf("[fatal] SendConcierge: %v\n", err)
		_ = b.Stop()
		return 1
	}
	mathText, mathOK := waitOffice(&mu, &officeTexts, func(t string) bool {
		return strings.Contains(t, "42")
	}, 25*time.Second)
	if mathOK {
		fmt.Printf("[concierge-probe] office answered: %q\n", trunc(mathText, 120))
	}
	check(mathOK, `office bubble answered 6x7 ("42") within 25s while the boss was busy`)

	// 2. The real work: must dispatch a worker, not queue on the boss.
	fmt.Println(`[concierge-probe] SendConcierge("create /tmp/gfx-conc-test.txt with one line: grapes")`)
	if err := cs.SendConcierge("create /tmp/gfx-conc-test.txt with one line: grapes"); err != nil {
		fmt.Printf("[fatal] SendConcierge #2: %v\n", err)
		_ = b.Stop()
		return 1
	}
	deadline := time.Now().Add(60 * time.Second)
	var hiredNow, ackText string
	fileOK := false
	for time.Now().Before(deadline) && (hiredNow == "" || ackText == "" || !fileOK) {
		mu.Lock()
		if len(hires) > 0 {
			hiredNow = hires[0]
		}
		mu.Unlock()
		if ackText == "" {
			mu.Lock()
			for _, t := range officeTexts {
				l := strings.ToLower(t)
				if strings.Contains(l, "grape") || strings.Contains(l, "gfx-conc-test") || strings.Contains(l, "file") {
					ackText = t
					break
				}
			}
			mu.Unlock()
		}
		if data, err := os.ReadFile(probeFile); err == nil && strings.Contains(strings.TrimSpace(string(data)), "grapes") {
			fileOK = true
		}
		time.Sleep(300 * time.Millisecond)
	}
	if data, err := os.ReadFile(probeFile); err == nil {
		fmt.Printf("[concierge-probe] probe file %s content: %q\n", probeFile, string(data))
	}
	mu.Lock()
	hiresSeen := append([]string(nil), hires...)
	mu.Unlock()
	fmt.Printf("[concierge-probe] hires observed: %v\n", hiresSeen)
	check(hiredNow != "" && fileOK,
		fmt.Sprintf("concierge dispatched a worker for the real task (hire %q) and %s exists containing 'grapes'", hiredNow, probeFile))
	if ackText != "" {
		fmt.Printf("[concierge-probe] office acknowledged: %q\n", trunc(ackText, 120))
	}
	check(ackText != "", "an office message acknowledged the work (references grape/file) within the 60s budget")

	if err := b.Stop(); err != nil {
		fmt.Printf("[fatal] stop: %v\n", err)
		return 1
	}
	fmt.Println("[done] backend stopped")

	if failures == 0 {
		fmt.Println("CONCIERGE: OK")
		return 0
	}
	fmt.Printf("CONCIERGE: FAIL (%d check(s) failed)\n", failures)
	return 1
}

// --- SSE reconnect sim (D1) ---------------------------------------------------

// sseAttempt is one /event attach in the sim ledger: arrival time is the
// backoff-cadence evidence, release the moment the handler finished with
// the connection (the post-revival reset is measured release -> arrival).
type sseAttempt struct {
	n       int
	arrival time.Time
	release time.Time
	profile string
}

// sseSimServer emulates just enough opencode serve for the live backend's
// full Start over plain net/http: GET /session (empty), POST /session
// (create), and GET /event with a scripted flap profile:
//
//	attempts 1-2   200 with an empty body (clean EOF before any frame —
//	               class "closed"; twice, to prove same-class dedupe)
//	attempts 3-7   HTTP 500 (error class change -> ONE error note, then
//	               silent retries through the ladder)
//	attempt 8      one heartbeat frame, then close (revival: recovery note
//	               + ladder reset)
//	attempt 9+     stay open (post-reset attach)
//
// The attempts ledger IS the ladder measurement: consecutive gaps must
// read 1s, 2s, 5s, 10s, 30s, 30s (capped), and gap(attempt8.release ->
// attempt9.arrival) must read ~1s (reset on first frame).
type sseSimServer struct {
	baseURL  string
	srv      *http.Server
	mu       sync.Mutex
	attempts []sseAttempt
}

func newSSESimServer() *sseSimServer {
	sim := &sseSimServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/event", sim.handleEvent)
	mux.HandleFunc("/session", sim.handleSession)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	sim.baseURL = "http://" + ln.Addr().String()
	sim.srv = &http.Server{Handler: mux}
	go func() { _ = sim.srv.Serve(ln) }()
	return sim
}

func (s *sseSimServer) close() { _ = s.srv.Close() }

func (s *sseSimServer) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		_, _ = io.WriteString(w, `{"id":"ses-sim","parentID":"","title":"sse-sim","time":{"created":1,"updated":1}}`)
		return
	}
	_, _ = io.WriteString(w, `[]`)
}

func (s *sseSimServer) handleEvent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	n := len(s.attempts) + 1
	att := sseAttempt{n: n, arrival: time.Now()}
	if n > 1 {
		fmt.Printf("[sse-sim] /event attempt #%d (gap %.2fs since #%d)\n",
			n, att.arrival.Sub(s.attempts[n-2].arrival).Seconds(), n-1)
	} else {
		fmt.Printf("[sse-sim] /event attempt #%d (first attach)\n", n)
	}
	s.attempts = append(s.attempts, att)
	s.mu.Unlock()

	markRelease := func(profile string) {
		s.mu.Lock()
		s.attempts[n-1].release = time.Now()
		s.attempts[n-1].profile = profile
		s.mu.Unlock()
		fmt.Printf("[sse-sim] /event attempt #%d profile=%s\n", n, profile)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	switch {
	case n <= 2:
		markRelease("close-clean") // 200, empty body: EOF before any frame
	case n <= 7:
		w.WriteHeader(http.StatusInternalServerError)
		markRelease("http-500")
	case n == 8:
		fmt.Fprintf(w, "data: {\"type\":\"server.heartbeat\",\"properties\":{}}\n\n")
		flusher.Flush()
		time.Sleep(300 * time.Millisecond)
		markRelease("revive-then-close")
	default:
		fmt.Fprintf(w, "data: {\"type\":\"server.heartbeat\",\"properties\":{}}\n\n")
		flusher.Flush()
		<-r.Context().Done()
		markRelease("stay-open")
	}
}

// runSSESim — the --sse-sim harness. Attaches the REAL live backend to the
// fake flapping serve (the full Start path: charter, primary resolve, SSE
// pump), lets the outage play out, and asserts the backoff ladder, the
// post-revival ladder reset, and the deduped status-note contract.
func runSSESim() int {
	sim := newSSESimServer()
	defer sim.close()
	scratch, err := os.MkdirTemp("", "grafeio-sse-sim")
	if err != nil {
		fmt.Printf("[fatal] scratch dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(scratch)

	// Board offline for a deterministic transcript.
	oldAM := os.Getenv("AGENTMEMORY_URL")
	_ = os.Setenv("AGENTMEMORY_URL", "http://127.0.0.1:9")
	defer func() {
		if oldAM == "" {
			_ = os.Unsetenv("AGENTMEMORY_URL")
		} else {
			_ = os.Setenv("AGENTMEMORY_URL", oldAM)
		}
	}()

	var mu sync.Mutex
	var streamNotes []string // EvStatus texts mentioning "event stream", in order
	reconnectSeen := false
	emit := func(e state.Event) {
		printEvent(e)
		if e.Kind == state.EvStatus && strings.Contains(e.Text, "event stream") {
			mu.Lock()
			streamNotes = append(streamNotes, e.Text)
			if strings.Contains(e.Text, "reconnected") {
				reconnectSeen = true
			}
			mu.Unlock()
		}
	}

	cfg := config.Default()
	cfg.Backend.AgentmemoryURL = "http://127.0.0.1:9"
	b := backend.NewLive(sim.baseURL, scratch, cfg)
	fmt.Printf("[mode] %s\n", b.Mode())
	if err := b.Start(emit); err != nil {
		fmt.Printf("[fatal] start: %v\n", err)
		return 1
	}
	fmt.Printf("[sse-sim] live backend attached to fake serve %s (scratch %s)\n", sim.baseURL, scratch)
	fmt.Println("[sse-sim] outage profile: 2 close-clean, 5x HTTP 500, revive at attempt 8, reset probe at attempt 9")

	// Watch until the revival note landed and the post-reset attach (attempt
	// 9) arrived, or the budget dies.
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		sim.mu.Lock()
		n := len(sim.attempts)
		sim.mu.Unlock()
		mu.Lock()
		rec := reconnectSeen
		mu.Unlock()
		if n >= 9 && rec {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := b.Stop(); err != nil {
		fmt.Printf("[fatal] stop: %v\n", err)
		return 1
	}
	fmt.Println("[done] backend stopped")

	mu.Lock()
	notes := append([]string(nil), streamNotes...)
	mu.Unlock()
	return sim.evaluate(notes)
}

// evaluate scores the attempt ledger + the status-note transcript.
func (s *sseSimServer) evaluate(streamNotes []string) int {
	s.mu.Lock()
	atts := append([]sseAttempt(nil), s.attempts...)
	s.mu.Unlock()

	failures := 0
	check := func(cond bool, label string) {
		if cond {
			fmt.Printf("[assert] PASS %s\n", label)
		} else {
			failures++
			fmt.Printf("[assert] FAIL %s\n", label)
		}
	}

	fmt.Println("[sse-sim] --- attempt ledger ---")
	for _, a := range atts {
		fmt.Printf("[sse-sim]   attempt #%d  arrival=+%.2fs  profile=%s\n",
			a.n, a.arrival.Sub(atts[0].arrival).Seconds(), a.profile)
	}

	wantGaps := []float64{1, 2, 5, 10, 30, 30}
	check(len(atts) >= 9, fmt.Sprintf("at least 9 /event attempts observed (got %d)", len(atts)))
	for i, want := range wantGaps {
		if i+1 >= len(atts) {
			break
		}
		gap := atts[i+1].arrival.Sub(atts[i].arrival).Seconds()
		check(math.Abs(gap-want) <= 0.8,
			fmt.Sprintf("ladder gap #%d->#%d ≈ %gs (got %.2fs)", i+1, i+2, want, gap))
	}
	if len(atts) >= 9 {
		gap := atts[8].arrival.Sub(atts[7].release).Seconds()
		check(math.Abs(gap-1) <= 0.9,
			fmt.Sprintf("post-revival ladder reset: attempt 9 arrives ~1s after attempt 8 closed (got %.2fs)", gap))
	}

	fmt.Println("[sse-sim] --- event-stream status notes (in order) ---")
	for i, n := range streamNotes {
		fmt.Printf("[sse-sim]   note #%d %q\n", i+1, n)
	}
	recIdx := -1
	for i, n := range streamNotes {
		if strings.Contains(n, "reconnected") {
			recIdx = i
			break
		}
	}
	check(recIdx >= 0, "recovery note '[grafeio] event stream: reconnected' emitted")
	if recIdx >= 0 {
		before := streamNotes[:recIdx]
		check(len(before) == 2,
			fmt.Sprintf("exactly 2 event-stream notes during the whole outage, retries silent (got %d)", len(before)))
		if len(before) >= 2 {
			check(strings.Contains(before[0], "event stream closed"),
				"outage note #1 is the clean-close class")
			check(strings.Contains(before[1], "HTTP 500"),
				"outage note #2 is the error-class change (HTTP 500)")
		}
	}

	if failures == 0 {
		fmt.Println("SSE-SIM: OK")
		return 0
	}
	fmt.Printf("SSE-SIM: FAIL (%d check(s) failed)\n", failures)
	return 1
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
	case state.EvChatOffice:
		fmt.Printf("[chat-office] %s pending=%v %q\n", e.Msg.ID, e.Msg.Pending, trunc(e.Msg.Text, 120))
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
