// threads_opencode_test.go — behavior proofs for the opencode-style
// thread renderer (threads_opencode.go), LOCKED frame:
//
//	⠿ Explore Task — Scout question kinds recon
//	  ↳ Read internal/panels/chat.go
//
//	(a) a LIVE collapsed thread renders the office-tick braille glyph
//	    (c.tick%len(threadLiveFrames)) + its "<Kind> Task — <task>" title
//	    and NOTHING else — no rollup while running — and its second line
//	    is the dim BARE "  ↳ <Verb> <rest>" sneak at the NEWEST tool
//	    (the state mark is gone from the peek);
//	(b) a DONE thread dims a "✓", KEEPS the collapsed rollup
//	    ("(· N tool calls ✓ done)"), and no live-glyph braille frame ever
//	    shows while nothing is live;
//	(c) an expanded thread lists its "[tool] <shaped> <state mark>" rows
//	    under the SAME header, then the ↳ sneak AGAIN (still bare) as the
//	    "current task" line, then the dim closing summary;
//	(d) the "ctrl+g · view subagents" hint row appears ONLY while ≥1
//	    rendered thread is live — gone when the threads go stale and when
//	    /tools is off;
//	(e) ClickRow on the ONE header row toggles, on the ONE sneak row
//	    toggles in BOTH states, on an EXPANDED internal tool row does NOT
//	    (false);
//	(f) the pending row renders the breathing block-glyph column + the
//	    existing typing text in EXACTLY one row (SetSize budget intact);
//	(g) the live glyph is a pure function of the office tick: tick 20 →
//	    threadLiveFrames[6] "⠾", tick 21 → frame 0 "⠿" — no timers;
//	(h) a trailing wthink never steals the ↳ sneak — the peek pins the
//	    thread's NEWEST TOOL line, display-SHAPED ("edit · lex.go" →
//	    "Edit lex.go"), the thought rolls up in the "· M think" count
//	    (and a think-ONLY thread falls back to "thinking · N lines");
//	(i) a /stop-stopped thread reads "✗ <title> (· … ✗ stopped)",
//	    force-collapses under the ctrl+g baseline, and re-opens only on
//	    an explicit per-agent expand (closing line "· … ✗ stopped");
//	(j) shapeToolText is IDEMPOTENT: the reducer's "<verb> · <rest>"
//	    shape maps to "<Verb> <rest>", target-shaped text rides through.
//
// No clocks, no sleeps: every office tick and wtool meta-tick is a
// literal (Meta carries "state␟tick" like the reducer writes —
// parseWtoolMeta reads it back), and the live glyph is c.tick-indexed.
package panels

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

// newOpencodeChat builds the deterministic two-thread fixture every
// opencode proof walks at office tick 20: skopos-1 (SCOUT role →
// "Explore") LIVE with its newest tool call still running, tekton-1
// (DEVELOPER) DONE, and a pending EMPTY boss reply so the typing row
// carries the block bar. Thread birth slots interleave the user turn by
// At; the done thread logs activity 10+ ticks back but inside
// wtoolStaleTicks — its IDLE sprite is what settles it. The wtool texts
// ride the ASPIRATIONAL target shape ("Read internal/panels/chat.go")
// to prove the shaping is idempotent; proof (h) feeds the reducer's own
// "<verb> · <rest>" shape for the mapping half.
func newOpencodeChat(t *testing.T, w, h int) *Chat {
	t.Helper()
	return newOpencodeChatAtTick(t, w, h, 20)
}

// newOpencodeChatAtTick — newOpencodeChat at an arbitrary office tick.
// The live glyph is c.tick%len(threadLiveFrames): tick 20 → frame 6
// ("⠾"), tick 21 → frame 0 ("⠿" — the locked frame's opener). The
// fixture's meta-ticks stay 1-11 ticks back, so every tick up to ~130
// keeps skopos-1 LIVE (busy sprite + activity inside wtoolStaleTicks).
func newOpencodeChatAtTick(t *testing.T, w, h, tick int) *Chat {
	t.Helper()
	c := NewChat(nil)
	c.SetSize(w, h)
	c.SetState(state.OfficeState{
		Tick: tick,
		Employees: []state.Employee{
			{ID: "sco-1", Name: "skopos-1", Role: state.RoleScout,
				Sprite: state.SpriteWorking, Task: "Scout question kinds recon"}, // LIVE
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Extend state+backend question kinds"}, // returned
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "ship the question kinds", At: 10},
			// skopos-1 — LIVE: busy sprite, freshest activity 1 tick old
			{ID: "s1", From: "skopos-1", Kind: wtoolKind, Text: "List internal/panels", Meta: "done\x1f18", At: 20},
			{ID: "s2", From: "skopos-1", Kind: wtoolKind, Text: "Read internal/panels/chat.go", Meta: "running\x1f19", At: 30},
			// tekton-1 — DONE: idle sprite settles the thread
			{ID: "x1", From: "tekton-1", Kind: wtoolKind, Text: "Grep questionKind", Meta: "done\x1f9", At: 40},
			{ID: "x2", From: "tekton-1", Kind: wtoolKind, Text: "Read internal/backend/backend.go", Meta: "done\x1f10", At: 50},
			{ID: "b1", From: "boss", Kind: "boss", Pending: true, At: 60}, // the typing row (block bar)
		},
	})
	return c
}

// staleChat re-feeds the fixture far past the staleness horizon with idle
// sprites at 80 cols (every settled header's rollup fits ONE row — exact
// pins, no clips): both threads go done, the hint row and the loading
// row hide.
func staleChat(t *testing.T) *Chat {
	t.Helper()
	c := NewChat(nil)
	c.SetSize(80, 24)
	c.SetState(state.OfficeState{
		Tick: 1000,
		Employees: []state.Employee{
			{ID: "sco-1", Name: "skopos-1", Role: state.RoleScout,
				Sprite: state.SpriteAtDesk, Task: "Scout question kinds recon"},
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Extend state+backend question kinds"},
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "ship the question kinds", At: 10},
			{ID: "s1", From: "skopos-1", Kind: wtoolKind, Text: "List internal/panels", Meta: "done\x1f18", At: 20},
			{ID: "s2", From: "skopos-1", Kind: wtoolKind, Text: "Read internal/panels/chat.go", Meta: "done\x1f19", At: 30},
			{ID: "x1", From: "tekton-1", Kind: wtoolKind, Text: "Grep questionKind", Meta: "done\x1f9", At: 40},
			{ID: "x2", From: "tekton-1", Kind: wtoolKind, Text: "Read internal/backend/backend.go", Meta: "done\x1f10", At: 50},
		},
	})
	return c
}

// TestThreadLiveSpinnerTitleAndSneak is proof (a): the live thread's
// header is the office-tick braille glyph (tick 20 → frame 6 "⠾") +
// "Explore Task — Scout question kinds recon" (scout maps to opencode's
// Explore kind) and NOTHING ELSE — no rollup while running — and its
// collapsed second line is the dim BARE "  ↳ Read
// internal/panels/chat.go" sneak at the RUNNING latest call, not the
// older one, with no state mark trailing.
func TestThreadLiveSpinnerTitleAndSneak(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	convo := ansi.Strip(c.renderConversation())
	for _, want := range []string{
		"⠾ Explore Task — Scout question kinds recon",
		"  ↳ Read internal/panels/chat.go",
	} {
		if !strings.Contains(convo, want) {
			t.Fatalf("live thread missing shape %q:\n%s", want, convo)
		}
	}
	// row-seam rules the neighbor DONE thread can't shadow: the LIVE
	// header carries NO rollup, and its sneak is BARE (no ✓/✗/… running)
	for _, ln := range strings.Split(convo, "\n") {
		if strings.Contains(ln, "Explore Task — Scout question kinds recon") && strings.Contains(ln, "(·") {
			t.Fatalf("a LIVE collapsed header must not carry the rollup, got %q", ln)
		}
		if strings.Contains(ln, "↳ Read internal/panels/chat.go") {
			if strings.Contains(ln, "… running") || strings.Contains(ln, "✓") || strings.Contains(ln, "✗") {
				t.Fatalf("the sneak is BARE — no state mark trails it, got %q", ln)
			}
		}
	}
	// the OLDER tool call must not be the sneak (it's the expanded-only
	// history) — and nothing renders the expanded list while collapsed
	if strings.Contains(convo, "[tool] ") || strings.Contains(convo, "↳ List") {
		t.Fatalf("collapsed live thread must sneak ONLY the newest entry:\n%s", convo)
	}
}

// TestThreadDoneCheckNoSpinner is proof (b): settled threads dim a "✓"
// glyph, the collapsed header KEEPS the old summary card's rollup
// ("(· N tool calls ✓ done)" — exact at 80 cols), the sneak is the bare
// shaped peek, the DEVELOPER role reads as "Developer", and NO
// live-glyph braille frame (the whole threadLiveFrames set is scanned)
// appears anywhere once nothing is live.
func TestThreadDoneCheckNoSpinner(t *testing.T) {
	c := staleChat(t)
	convo := ansi.Strip(c.renderConversation())
	for _, want := range []string{
		"✓ Explore Task — Scout question kinds recon (· 2 tool calls ✓ done)",
		"✓ Developer Task — Extend state+backend question kinds (· 2 tool calls ✓ done)",
		"  ↳ Read internal/panels/chat.go",
		"  ↳ Read internal/backend/backend.go",
	} {
		if !strings.Contains(convo, want) {
			t.Fatalf("done thread missing shape %q:\n%s", want, convo)
		}
	}
	for _, frame := range threadLiveFrames {
		if strings.Contains(convo, frame) {
			t.Fatalf("a DONE thread must never show live-glyph frame %q:\n%s", frame, convo)
		}
	}
}

// TestThreadExpandedListsToolRows is proof (c): a per-agent expand keeps
// the SAME (rollup-free) header, lists the merged "[tool] <shaped>
// <state mark>" rows 2-cell indented beneath it, then re-shows the ↳
// sneak — still BARE — as the "current task" line AFTER the last tool
// row, and closes with the dim summary line — in that exact order.
func TestThreadExpandedListsToolRows(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	c.ToggleThread("skopos-1")
	convo := ansi.Strip(c.renderConversation())
	fmt.Println("---- OPENCODE EXPANDED THREAD (60 cols: skopos-1 zoomed live, ansi-stripped) ----")
	for _, ln := range strings.Split(convo, "\n") {
		if strings.Contains(ln, "Explore Task") || strings.Contains(ln, "[tool] ") ||
			strings.Contains(ln, "  ↳ ") || strings.Contains(ln, "  · ") {
			fmt.Printf("%2d|%s|\n", len([]rune(ln)), ln)
		}
	}
	fmt.Println("---- END THREAD ----")
	for _, want := range []string{
		"⠾ Explore Task — Scout question kinds recon",
		"  [tool] List internal/panels ✓",
		"  [tool] Read internal/panels/chat.go … running",
		"  ↳ Read internal/panels/chat.go",
		"  · 2 tool calls ✓ done",
	} {
		if !strings.Contains(convo, want) {
			t.Fatalf("expanded thread missing row %q:\n%s", want, convo)
		}
	}
	// the expanded sneak is BARE too — only the [tool] rows carry marks
	for _, ln := range strings.Split(convo, "\n") {
		if strings.Contains(ln, "↳ Read internal/panels/chat.go") && strings.Contains(ln, "… running") {
			t.Fatalf("the ↳ sneak never carries the state mark, got %q", ln)
		}
	}
	// ORDER: header < tool rows < ↳ sneak (the current-task line) <
	// closing summary
	iHead := strings.Index(convo, "⠾ Explore Task")
	iTool := strings.Index(convo, "  [tool] List internal/panels ✓")
	iSneak := strings.Index(convo, "  ↳ Read internal/panels/chat.go")
	iClose := strings.Index(convo, "  · 2 tool calls ✓ done")
	if !(iHead < iTool && iTool < iSneak && iSneak < iClose) {
		t.Fatalf("expanded thread must run header → tools → sneak → closing (h=%d t=%d s=%d c=%d):\n%s",
			iHead, iTool, iSneak, iClose, convo)
	}
	// the quiet neighbor stays collapsed while its sibling zooms
	if !strings.Contains(convo, "✓ Developer Task — Extend state+backend question kinds") ||
		!strings.Contains(convo, "  ↳ Read internal/backend/backend.go") {
		t.Fatalf("the done thread must survive its neighbor's expand:\n%s", convo)
	}
}

// TestThreadHintRowOnlyWhileLive is proof (d): the dim
// "ctrl+g · view subagents" hint trails the LAST thread block only while
// ≥1 rendered thread is live; stale threads drop it, and /tools off (no
// threads rendered) drops it too.
func TestThreadHintRowOnlyWhileLive(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	convo := ansi.Strip(c.renderConversation())
	iTekton := strings.Index(convo, "Developer Task —")
	iHint := strings.Index(convo, threadHintText)
	if iHint < 0 || iHint < iTekton {
		t.Fatalf("the hint row must trail the LAST thread block (hint at %d, last thread at %d):\n%s", iHint, iTekton, convo)
	}

	stale := staleChat(t)
	if convo := ansi.Strip(stale.renderConversation()); strings.Contains(convo, threadHintText) {
		t.Fatalf("the hint row must die with the last LIVE thread:\n%s", convo)
	}

	off := newOpencodeChat(t, 60, 24)
	off.SetShowTools(false)
	convo = ansi.Strip(off.renderConversation())
	if strings.Contains(convo, threadHintText) || strings.Contains(convo, "Task —") {
		t.Fatalf("/tools off renders no threads, so no hint row may show:\n%s", convo)
	}
}

// TestThreadClickToggleSemantics is proof (e): the ONE header row toggles
// and the ONE ↳ sneak row toggles in BOTH states (both are single-row
// under the clip contract) — while collapsed the sneak is the thread's
// second line, while expanded it is the "current task" line under the
// tool rows — but the expanded internal TOOL rows and the closing
// summary are NEVER clickable: a click there falls through unclaimed.
func TestThreadClickToggleSemantics(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	rows := func(agent string) []int {
		var lines []int
		for i := 0; i < 50; i++ {
			if c.threadRows[i] == agent {
				lines = append(lines, i)
			}
		}
		return lines
	}
	// collapsed: ONE header row + ONE sneak row (single-row contract)
	scout := rows("skopos-1")
	if len(scout) != 2 {
		t.Fatalf("a collapsed thread must register its header + sneak (2 rows), got %v", scout)
	}
	// header toggles
	if !c.ClickRow(3, scout[0]) {
		t.Fatal("click on the header row was not claimed")
	}
	tbAssertExpanded(t, c, "skopos-1", true, "after header click")
	// expanded: header (1 row) + the sneak row under the tool list; the
	// internal tool rows between them must NOT toggle
	scout = rows("skopos-1")
	if len(scout) != 2 {
		t.Fatalf("an expanded thread must register header + sneak (2 rows), got %v", scout)
	}
	if c.ClickRow(3, scout[0]+1) {
		t.Fatalf("click on an expanded internal tool row (line %d) must not be claimed", scout[0]+1)
	}
	tbAssertExpanded(t, c, "skopos-1", true, "after internal-row click (no-op)")
	// the expanded sneak row toggles too (it represents the thread)
	if !c.ClickRow(3, scout[1]) {
		t.Fatal("click on the expanded sneak row was not claimed")
	}
	tbAssertExpanded(t, c, "skopos-1", false, "after expanded-sneak click")
	// and collapsed again the sneak row is back to the second line —
	// it toggles from there as well
	scout = rows("skopos-1")
	if len(scout) != 2 {
		t.Fatalf("re-collapsed thread must register header + sneak again, got %v", scout)
	}
	if !c.ClickRow(3, scout[1]) {
		t.Fatal("click on the collapsed sneak row was not claimed")
	}
	tbAssertExpanded(t, c, "skopos-1", true, "after collapsed-sneak click")
}

// TestPendingRowBlockBarOneRow is proof (f): the typing row is the
// breathing block-glyph column + the existing "<boss> is typing…" text in
// EXACTLY one row — the SetSize budget (whole View == h rows) does not
// move, and the retired caret glyph never comes back.
func TestPendingRowBlockBarOneRow(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	view := ansi.Strip(c.View())
	rows := strings.Split(view, "\n")
	if len(rows) != 24 {
		t.Fatalf("the restyle must not move the row budget: View drew %d rows, want 24:\n%s", len(rows), view)
	}
	ri := typingRowIdx(rows, "is typing…")
	if ri < 0 {
		t.Fatalf("the typing row is missing:\n%s", view)
	}
	di := chatDividerRow(rows)
	if ri != di+1 {
		t.Fatalf("the typing row must be the FIRST row below the divider (divider %d, typing %d):\n%s", di, ri, view)
	}
	// the block column: the deterministic tick-20 frame of the breathing
	// bar, bold-magenta in front of the unchanged busy text
	want := pendingBlockBar(20) + " boss is typing…"
	if !strings.Contains(rows[ri], want) {
		t.Fatalf("the pending row must be the block bar + existing text %q, got %q:\n%s", want, rows[ri], view)
	}
	if strings.Contains(view, "▌") {
		t.Fatalf("the retired caret glyph must never appear:\n%s", view)
	}
	if r := pendingBlockBar(0); r != "█▇▆▅▄▃▂▁" {
		t.Fatalf("the deterministic first bar frame must be %q, got %q", "█▇▆▅▄▃▂▁", r)
	}
	if pendingBlockBar(1) == pendingBlockBar(0) {
		t.Fatal("the bar must BREATHE across ticks")
	}
}

// TestLiveGlyphCyclesOffTheOfficeTick is proof (g): the live header's
// braille glyph is a PURE FUNCTION of the office tick — no spinner
// model, no timer: tick 20 renders threadLiveFrames[6] ("⠾"), tick 21
// wraps to frame 0 ("⠿" — the locked frame's opener), and the done
// thread's "✓" never moves. Every frame is exactly ONE cell wide (the
// 2-cell glyph field's budget).
func TestLiveGlyphCyclesOffTheOfficeTick(t *testing.T) {
	c := newOpencodeChat(t, 60, 24)
	if convo := ansi.Strip(c.renderConversation()); !strings.Contains(convo, "⠾ Explore Task") {
		t.Fatalf("tick 20 must render threadLiveFrames[6] (⠾):\n%s", convo)
	}
	c21 := newOpencodeChatAtTick(t, 60, 24, 21)
	convo := ansi.Strip(c21.renderConversation())
	if !strings.Contains(convo, "⠿ Explore Task") {
		t.Fatalf("tick 21 must wrap around to threadLiveFrames[0] (⠿):\n%s", convo)
	}
	if strings.Contains(convo, "⠾") {
		t.Fatalf("the old frame must be gone at the new tick:\n%s", convo)
	}
	if !strings.Contains(convo, "✓ Developer Task") {
		t.Fatalf("the done thread's ✓ must never animate:\n%s", convo)
	}
	for _, f := range threadLiveFrames {
		if w := ansi.StringWidth(f); w != 1 {
			t.Fatalf("live frame %q must be 1 cell wide, got %d", f, w)
		}
	}
}

// TestThreadSneakPinsLatestToolOverThink is proof (h): when a thread's
// NEWEST entry is a thought, the collapsed ↳ sneak still peeks the last
// TOOL line (thoughts roll up in the "· M think" summary counts — they
// never lead the peek); the peek is the reducer's text SHAPED
// ("edit · lex.go" → "Edit lex.go") and BARE; a thread with NO tool
// line at all falls back to the "thinking · N lines" peek so it keeps a
// second row.
func TestThreadSneakPinsLatestToolOverThink(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(80, 24) // wide: every settled header + rollup fits one row — exact pins
	c.SetState(state.OfficeState{
		Tick: 50,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Fix the lexer"},
			{ID: "dev-2", Name: "tekton-2", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Muse only"},
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "one", At: 10},
			// tekton-1 — tool THEN thought: the thought is newest, the
			// sneak still pins the (shaped) tool line
			{ID: "x1", From: "tekton-1", Kind: wtoolKind, Text: "edit · lex.go", Meta: "done\x1f5", At: 20},
			{ID: "x2", From: "tekton-1", Kind: wthinkKind, Text: "a thought\nover two lines", Meta: "c1\x1f6", At: 30},
			// tekton-2 — think-ONLY thread: the fallback peek
			{ID: "y1", From: "tekton-2", Kind: wthinkKind, Text: "musing", Meta: "c2\x1f7", At: 40},
		},
	})
	convo := ansi.Strip(c.renderConversation())
	for _, want := range []string{
		// the LAST TOOL line leads the sneak, shaped and bare…
		"  ↳ Edit lex.go",
		// …and the thought rolls up in the settled header's rollup count
		"✓ Developer Task — Fix the lexer (· 1 tool call · 1 think ✓ done)",
		// think-ONLY thread: the fallback peek + its zero-tool rollup
		"  ↳ thinking · 1 lines",
		"✓ Developer Task — Muse only (· 0 tool calls · 1 think ✓ done)",
	} {
		if !strings.Contains(convo, want) {
			t.Fatalf("sneak must pin the last tool line (shape %q):\n%s", want, convo)
		}
	}
	// …and a thought NEVER leads the peek of a tool-bearing thread
	if strings.Contains(convo, "↳ a thought") || strings.Contains(convo, "↳ thinking · 2 lines") {
		t.Fatalf("a thought must never lead the sneak of a tool-bearing thread:\n%s", convo)
	}
}

// TestShapeToolTextIdempotent is proof (j): the display-side shaping
// turns the reducer's "<lowercase verb> · <rest>" tool texts into
// opencode's "<Verb> <rest>" — and ONLY those: anything already in the
// target shape (the aspirational texts this package's fixtures carry),
// a tagged line, or a head outside the verb regex rides through
// UNCHANGED. Every output re-shaped is itself: the shaping is
// IDEMPOTENT.
func TestShapeToolTextIdempotent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"read · internal/panels/chat.go", "Read internal/panels/chat.go"}, // the reducer's own emit
		{"bash · go test", "Bash go test"},
		{"grep -rn foo · internal/", "Grep -rn foo internal/"},
		{"read_file · x", "Read_file x"},
		{"Read internal/panels/chat.go", "Read internal/panels/chat.go"}, // the target form — untouched
		{"List internal/panels", "List internal/panels"},                 // no " · " join
		{"read", "read"},
		{"[tool] read · x", "[tool] read · x"}, // tagged — head busts the verb regex
		{"PR #7 · fix", "PR #7 · fix"},         // capital head — not the reducer's verb
		{"", ""},
	} {
		if got := shapeToolText(tc.in); got != tc.want {
			t.Errorf("shapeToolText(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := shapeToolText(tc.want); got != tc.want {
			t.Errorf("shapeToolText(%q) = %q — the shaping must be IDEMPOTENT", tc.want, got)
		}
	}
}

// TestThreadStoppedCheckAndRollup is proof (i): a /stop-stopped thread
// dims-red a "✗" glyph with the "✗ stopped" rollup on its collapsed
// header, FORCE-collapses under the ctrl+g baseline, and re-opens only
// on an explicit per-agent expand — the same stopped wording the old
// summary card carried.
func TestThreadStoppedCheckAndRollup(t *testing.T) {
	c := newOpencodeChat(t, 80, 24)
	c.MarkThreadStopped("skopos-1")
	convo := ansi.Strip(c.renderConversation())
	if !strings.Contains(convo, "✗ Explore Task — Scout question kinds recon (· 2 tool calls ✗ stopped)") {
		t.Fatalf("stopped thread must read ✗ + the ✗ stopped rollup:\n%s", convo)
	}
	tbAssertExpanded(t, c, "skopos-1", false, "stopped, no gesture")
	// the ctrl+g baseline does NOT re-open a stopped thread
	c.ToggleThreads()
	tbAssertExpanded(t, c, "skopos-1", false, "stopped under the ctrl+g baseline")
	if convo := ansi.Strip(c.renderConversation()); strings.Contains(convo, "[tool] List internal/panels") {
		t.Fatalf("a stopped thread must stay folded under ctrl+g:\n%s", convo)
	}
	// an explicit per-agent expand re-opens it — closing line carries
	// the stopped wording too
	c.ToggleThreads() // baseline back off (isolation from the next line)
	c.ToggleThread("skopos-1")
	convo = ansi.Strip(c.renderConversation())
	for _, want := range []string{
		"✗ Explore Task — Scout question kinds recon",
		"  [tool] List internal/panels ✓",
		"  · 2 tool calls ✗ stopped",
	} {
		if !strings.Contains(convo, want) {
			t.Fatalf("expanded stopped thread missing shape %q:\n%s", want, convo)
		}
	}
}

// TestThreadOpencodeFrame prints the canonical gallery frame — one LIVE
// thread (the ⠿-opened braille glyph at tick 21 + bare sneak, the
// LOCKED two-line shot), one DONE thread (✓ + rollup + sneak), the hint
// row, and the block-bar typing row — for eyeball review.
func TestThreadOpencodeFrame(t *testing.T) {
	c := newOpencodeChatAtTick(t, 60, 24, 21)
	view := ansi.Strip(c.View())
	fmt.Println("---- OPENCODE THREADS (60 cols: live + done + hint + pending bar, ansi-stripped) ----")
	for _, r := range strings.Split(view, "\n") {
		fmt.Printf("%2d|%s|\n", len([]rune(r)), r)
	}
	fmt.Println("---- END PANEL ----")
	for i, r := range strings.Split(view, "\n") {
		if w := len([]rune(r)); w > 60 {
			t.Fatalf("row %d overflows the 60-col budget (%d cells): %q", i, w, r)
		}
	}
	// the LOCKED collapsed-live frame: the two lines CONTIGUOUS, header
	// rollup-free, sneak bare (the unpadded transcript — View pads rows)
	convo := ansi.Strip(c.renderConversation())
	if !strings.Contains(convo, "⠿ Explore Task — Scout question kinds recon\n  ↳ Read internal/panels/chat.go") {
		t.Fatalf("the locked collapsed-live frame is not on screen:\n%s", convo)
	}
}
