// chat_render_test.go — render proofs for the chat panel's two hygiene
// rules: (1) there is no blinking caret ANYWHERE — the typing row lives
// below the divider (above the input) for the WHOLE pending period;
// (2) every chat bubble type WRAPS at the panel width instead of
// overflowing/clipping — markdown fences, unbreakable URLs, tool
// one-liners, workers-thread rows.
package panels

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

// chatDividerRow — the View row index of the divider (a run of cells that
// is nothing but "─"), -1 when absent.
func chatDividerRow(rows []string) int {
	for i, r := range rows {
		t := strings.TrimSpace(r)
		if t != "" && strings.Trim(t, "─") == "" {
			return i
		}
	}
	return -1
}

// typingRowIdx — the ONLY row carrying needle that is NOT a textarea
// prompt row (the busy placeholder quotes the same "… is typing…" text,
// but always behind the "›" prompt). -1 when absent.
func typingRowIdx(rows []string, needle string) int {
	idx := -1
	for i, r := range rows {
		if !strings.Contains(r, needle) {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(r), "›") {
			continue // prompt row quoting the busy text — not the typing row
		}
		if idx >= 0 {
			return -2 // more than one renderer-owned row carries it
		}
		idx = i
	}
	return idx
}

// TestNoCaretTypingRowAboveInput pins the caret eviction + the typing
// row's new home: pending boss (empty text) → row below the divider;
// pending boss WITH streamed text → the row STAYS (whole pending period);
// settled → the row is gone. In every state: no "▌", and the drawn row
// count equals the SetSize budget exactly.
func TestNoCaretTypingRowAboveInput(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(40, 20)

	assertState := func(tag string, wantRow bool) {
		view := ansi.Strip(c.View())
		if strings.Contains(view, "▌") {
			t.Fatalf("%s: the stream caret must not exist in ANY chat state:\n%s", tag, view)
		}
		rows := strings.Split(view, "\n")
		if len(rows) != 20 {
			t.Fatalf("%s: View drew %d rows, want the SetSize budget 20:\n%s", tag, len(rows), view)
		}
		di := chatDividerRow(rows)
		if di < 0 {
			t.Fatalf("%s: no divider row found:\n%s", tag, view)
		}
		ri := typingRowIdx(rows, "is typing…")
		if !wantRow {
			if ri >= 0 {
				t.Fatalf("%s: typing row present after settle:\n%s", tag, view)
			}
			return
		}
		if ri < 0 {
			t.Fatalf("%s: typing row missing below the divider:\n%s", tag, view)
		}
		if ri != di+1 {
			t.Fatalf("%s: typing row must be the FIRST row below the divider (divider at row %d, typing row at %d):\n%s",
				tag, di, ri, view)
		}
	}

	// pending boss, EMPTY text → the typing row speaks for it
	c.SetState(state.OfficeState{Tick: 2, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Kind: "boss", Pending: true},
	}})
	assertState("empty pending", true)

	// streamed text lands (with an attachment chip staged, to show where
	// the chips row sits) — the bubble grows in the viewport but the
	// typing row STAYS (liveness for the whole pending period), and the
	// row budget does not move
	c.addAttachment(chatAttachment{name: "sse.png", mime: "image/png", path: "/tmp/x/sse.png"})
	c.SetState(state.OfficeState{Tick: 3, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Kind: "boss", Pending: true, Text: "working on it —"},
	}})
	streamView := ansi.Strip(c.View())
	if !strings.Contains(streamView, "working on it —") {
		t.Fatalf("streaming pending: the partial text must render in the viewport:\n%s", streamView)
	}
	assertState("streaming pending", true)
	fmt.Println("---- CHAT PANEL (40 cols, boss streaming, ansi-stripped) ----")
	fmt.Print(streamView)
	fmt.Println("---- END PANEL ----")

	// settle: typing row gone, budget returns
	c.atts = nil
	c.SetState(state.OfficeState{Tick: 4, Chat: []state.ChatMsg{
		{ID: "u1", From: "user", Kind: "user", Text: "hi"},
		{ID: "b1", From: "boss", Text: "working on it — done"},
	}})
	assertState("settled", false)
}

// TestChatConvoWrapsAtWidth — 28 columns, the hostile message set: a
// fenced code line of 40 cells, a 35-cell unbreakable URL in prose, a
// long boss tool one-liner and a workers thread with a deep path. Every
// ansi-stripped View row must fit the panel, everything wrapped glyph
// must SURVIVE (never clipped), and the markdown hanging indent aligns
// under the bubble text start (prefix cell width, not byte length).
func TestChatConvoWrapsAtWidth(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(28, 30)

	codeToken := "export TOKEN=" + strings.Repeat("q", 27) // 40 cells, unbreakable tail
	urlToken := "https://x.co/" + strings.Repeat("z", 22)  // 35 cells, unbreakable
	deepPath := "internal/panels/some/really/deep/file.go"
	wPath := "internal/components/very/deep/file.go"

	c.SetState(state.OfficeState{
		Tick: 3,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteWorking, Task: "Wire it"},
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "show the wire-up"},
			{ID: "b1", From: "boss", Kind: "boss", Text: "**plan** — see " + urlToken + " then run:\n```sh\n" + codeToken + "\n```"},
			{ID: "t1", From: "boss", Kind: "tool", Text: "read · " + deepPath, Meta: "done"},
			{ID: "w1", From: "tekton-1", Kind: "wtool", Text: "read · " + wPath, Meta: "done\x1f3"},
		},
	})
	// threads are COLLAPSED BY DEFAULT now — live ones included — so this
	// fixture's live thread needs the explicit per-agent gesture to render
	// its expanded card (the same seam the app's click path runs); the
	// wrap coverage below pins the EXPANDED shape inside the bordered box.
	c.ExpandThread("tekton-1", true)

	view := ansi.Strip(c.View())
	rows := strings.Split(view, "\n")
	fmt.Println("---- CHAT PANEL (28 cols, hostile wrap set, ansi-stripped) ----")
	for _, r := range rows {
		fmt.Printf("%2d|%s|\n", len([]rune(r)), r)
	}
	fmt.Println("---- END PANEL ----")

	// (1) every row inside the column budget
	for i, r := range rows {
		if w := len([]rune(r)); w > 28 {
			t.Fatalf("row %d overflows the 28-col budget (%d cells): %q\nfull view:\n%s", i, w, r, view)
		}
	}

	// (2) nothing clipped: squash all whitespace out of the render AND
	// the tokens (fold boundaries legitimately eat their join space,
	// the viewport pads rows) — every long token must reconstruct
	// contiguously
	joined := ""
	for _, r := range rows {
		joined += strings.TrimSpace(r)
	}
	squash := strings.NewReplacer(" ", "", "│", "").Replace
	for _, tok := range []string{urlToken, codeToken, deepPath, wPath} {
		if !strings.Contains(squash(joined), squash(tok)) {
			t.Fatalf("a long token was CLIPPED somewhere (%q not fully present):\n%s", tok, view)
		}
	}

	// (3) the markdown hanging indent is the prefix CELL width (7 for
	// "boss › ", not its 9 bytes) — the folded URL row hangs right under
	// the bubble text start
	if !strings.Contains(view, "\n       https://x.co/") {
		t.Fatalf("the URL continuation must hang under the bubble text (7-cell indent):\n%s", view)
	}

	// (4) the boss tool one-liner keeps its first-line shape and its
	// continuation hangs under "[tool] " (7 spaces)
	if !strings.Contains(view, "[tool] read ·") {
		t.Fatalf("the tool one-liner lost its first-line prefix/symbol shape:\n%s", view)
	}
	if !strings.Contains(view, "\n       internal/panels/some") {
		t.Fatalf("the tool continuation must hang 7 cells in:\n%s", view)
	}

	// (5) the workers thread WRAPS with a hanging indent instead of
	// truncating the path — opencode-style: 2-cell indented under the
	// header, continuations 4 cells in, no card rails anywhere; the
	// reducer's "read · <path>" text is display-SHAPED to "Read <path>"
	if !strings.Contains(view, "  [tool] Read") {
		t.Fatalf("the workers-thread row lost its shaped first-line shape:\n%s", view)
	}
	if !strings.Contains(view, "\n    internal/components/very") {
		t.Fatalf("a long workers-thread row must continue with a 4-cell hanging indent:\n%s", view)
	}
}

// TestThreadsCollapsedByDefaultSpinnerAndSneak pins the opencode-style
// thread renderer (threads_opencode.go) at 40 columns: (1) EVERY thread
// is collapsed by DEFAULT — proven on a LIVE thread (busy sprite + fresh
// activity, NO explicit expand gesture anywhere); (2) the header is the
// SINGLE row: 2-cell glyph field (office-tick braille frame — tick 6 →
// threadLiveFrames[6] "⠾") + "<Kind> Task — <task>", a LIVE head
// carrying NOTHING else; the DONE head dim-✓'s and keeps the old
// summary card's rollup, CLIPPED at the width budget (no wrap) — Kind
// from the roster role (developer = "Developer"); (3) each collapsed
// thread's second line is the SINGLE-row dim "  ↳ <Verb> <rest>" sneak
// peek at its NEWEST TOOL entry — BARE (no state mark) and display-
// SHAPED ("edit · lex.go" → "Edit lex.go"), a trailing thought never
// stealing the peek; (4) the threadRows click map still toggles the
// thread from its NEW coordinates (the one header row + the one sneak
// row), it rebuilds across renders, and expanded internal rows NEVER
// toggle. No clocks: every timestamp is a literal.
func TestThreadsCollapsedByDefaultSpinnerAndSneak(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(40, 30)
	c.SetState(state.OfficeState{
		Tick: 6,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: state.SpriteWorking, Task: "Fix the lexer"}, // LIVE
			{ID: "dev-2", Name: "tekton-2", Role: state.RoleDeveloper,
				Sprite: state.SpriteAtDesk, Task: "Wire the tests"}, // returned
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "one", At: 100},
			{ID: "b1", From: "boss", Kind: "boss", Text: "on it", At: 200},
			// tekton-1 — LIVE: busy sprite + activity 0-1 ticks old,
			// with a thought riding the thread between the tools
			{ID: "x1", From: "tekton-1", Kind: wtoolKind, Text: "read · lex.go", Meta: "done\x1f5", At: 300},
			{ID: "x2", From: "tekton-1", Kind: wthinkKind, Text: "tiny thought", Meta: "c1\x1f6", At: 400},
			{ID: "x3", From: "tekton-1", Kind: wtoolKind, Text: "edit · lex.go", Meta: "done\x1f6", At: 450},
			// tekton-2 — completed, one tool + one thought (the thought
			// is the newest entry, but the sneak still pins the last TOOL
			// line — thoughts only fuel the "· 1 think" rollup count)
			{ID: "y1", From: "tekton-2", Kind: wtoolKind, Text: "read · wire.go", Meta: "done\x1f2", At: 500},
			{ID: "y2", From: "tekton-2", Kind: wthinkKind, Text: "tiny thought", Meta: "c1\x1f2", At: 550},
		},
	})

	// (1) collapsed BY DEFAULT at the state seam — the LIVE thread too,
	// with no explicit gesture of any kind
	tbAssertExpanded(t, c, "tekton-1", false, "live, no gesture")
	tbAssertExpanded(t, c, "tekton-2", false, "completed, no gesture")

	// …and in the render: header+sneak lines only, no expanded face
	view := ansi.Strip(c.View())
	fmt.Println("---- CHAT PANEL (40 cols, threads collapsed by default, ansi-stripped) ----")
	fmt.Print(view)
	fmt.Println("---- END PANEL ----")
	for i, r := range strings.Split(view, "\n") {
		if w := len([]rune(r)); w > 40 {
			t.Fatalf("row %d overflows the 40-col budget (%d cells): %q\nfull view:\n%s", i, w, r, view)
		}
	}
	if strings.Contains(view, "[tool] ") {
		t.Fatalf("collapsed-by-default: no thread may render its expanded tool list:\n%s", view)
	}
	for _, want := range []string{
		// (2) LIVE thread: office-tick braille glyph (tick 6 →
		// threadLiveFrames[6] "⠾") + role-kind title — no rollup while
		// running; DONE thread: dim "✓" + title, its rollup CLIPPED into
		// the single row (no hanging continuation survives)
		"⠾ Developer Task — Fix the lexer",
		"✓ Developer Task — Wire the tests",
		// (3) each collapsed thread's sneak is its NEWEST TOOL entry —
		// shaped + BARE; tekton-2's trailing thought rolls up in the
		// "· 1 think" count instead of leading the peek
		"  ↳ Edit lex.go",
		"  ↳ Read wire.go",
		// the live thread's hint row trails the last thread block
		"ctrl+g · view subagents",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("collapsed thread missing opencode shape %q:\n%s", want, view)
		}
	}
	// single-row contract at 40 cols: no rollup text survives (the LIVE
	// head carries none, the DONE head's is clipped mid-word), no sneak
	// continuation rows
	if strings.Contains(view, "✓ done") || strings.Contains(view, "  tool call") {
		t.Fatalf("headers/sneaks are SINGLE rows — no rollup wrap may leak at 40 cols:\n%s", view)
	}

	// (4) the click map: every collapsed thread registers its ONE
	// header row AND its ONE sneak row (2 threads × 2 rows), and a
	// click at those NEW coordinates toggles that agent's thread
	if len(c.threadRows) != 4 {
		t.Fatalf("two collapsed threads must register 4 clickable rows (1 header + 1 sneak each), got %v", c.threadRows)
	}
	clickAgent := func(agent string) {
		t.Helper()
		row := -1
		for line, name := range c.threadRows {
			if name == agent {
				row = line
				break
			}
		}
		if row < 0 {
			t.Fatalf("no clickable row registered for %q", agent)
		}
		if !c.ClickRow(2, row) {
			t.Fatalf("click at the registered thread row %d was not claimed", row)
		}
	}
	clickAgent("tekton-1")
	tbAssertExpanded(t, c, "tekton-1", true, "after header click")
	tbAssertExpanded(t, c, "tekton-2", false, "after header click (other thread)")

	// the expanded thread: same header on top, then the merged tool rows
	// 2-cell indented — with the per-agent override the thought's body
	// rides along (full expand) — then the ↳ sneak AGAIN as the
	// "current task" line and the dim closing summary, while the quiet
	// thread keeps its collapsed header+sneak pair
	view = ansi.Strip(c.View())
	fmt.Println("---- CHAT PANEL (40 cols, tekton-1 thread expanded by click, ansi-stripped) ----")
	fmt.Print(view)
	fmt.Println("---- END PANEL ----")
	for i, r := range strings.Split(view, "\n") {
		if w := len([]rune(r)); w > 40 {
			t.Fatalf("row %d overflows the 40-col budget (%d cells): %q\nfull view:\n%s", i, w, r, view)
		}
	}
	for _, want := range []string{
		"⠾ Developer Task — Fix the lexer",
		"  [tool] Read lex.go ✓",
		"  [tool] Edit lex.go ✓",
		"  thinking",
		"    tiny thought",
		"  ↳ Edit lex.go",
		"  · 2 tool calls · 1 think ✓ done",
		"✓ Developer Task — Wire the tests",
		"  ↳ Read wire.go",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("expanded thread missing shape %q:\n%s", want, view)
		}
	}

	// expanded internal TOOL rows NEVER toggle: the first tool row
	// (header line + 1) has no threadRows entry, so its click falls
	// through
	headerLine := -1
	for line, name := range c.threadRows {
		if name == "tekton-1" && (headerLine < 0 || line < headerLine) {
			headerLine = line
		}
	}
	if headerLine < 0 {
		t.Fatal("expanded tekton-1 must still register its header row")
	}
	if c.ClickRow(2, headerLine+1) {
		t.Fatalf("click on an expanded internal row (line %d) must not be claimed", headerLine+1)
	}
	tbAssertExpanded(t, c, "tekton-1", true, "after internal-row click (no-op)")

	// the map REBUILDS each render: a fresh click on the header folds the
	// thread back to its collapsed header+sneak pair
	clickAgent("tekton-1")
	tbAssertExpanded(t, c, "tekton-1", false, "after second header click")
	view = ansi.Strip(c.View())
	if strings.Contains(view, "[tool] ") || !strings.Contains(view, "  ↳ Edit lex.go") {
		t.Fatalf("second click must restore the collapsed sneak:\n%s", view)
	}
}

// TestChatKeepsFullTranscriptPastTheOldCap — the WhatsApp retention
// contract at the RENDER seam: a conversation longer than the old 30-entry
// in-memory cap renders EVERY turn into the transcript, oldest at top /
// newest at bottom (strictly ascending render positions). State-side
// retention itself is proven in internal/app/chat_retention_test.go.
func TestChatKeepsFullTranscriptPastTheOldCap(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(50, 14)
	msgs := make([]state.ChatMsg, 0, 40)
	for i := 1; i <= 40; i++ {
		from, kind := "user", "user"
		if i%2 == 0 {
			from, kind = "boss", "boss"
		}
		msgs = append(msgs, state.ChatMsg{
			ID: fmt.Sprintf("m%d", i), From: from, Kind: kind,
			Text: fmt.Sprintf("keepturn%02d", i), At: int64(i)})
	}
	c.SetState(state.OfficeState{Tick: 1, Chat: msgs})

	// the FULL transcript string (what the viewport scrolls over) carries
	// all 40 turns — none eaten — in strictly ascending order.
	full := ansi.Strip(c.renderConversation())
	prev := -1
	for i := 1; i <= 40; i++ {
		needle := fmt.Sprintf("keepturn%02d", i)
		idx := strings.Index(full, needle)
		if idx < 0 {
			t.Fatalf("turn %d (%s) was eaten from the transcript", i, needle)
		}
		if idx < prev {
			t.Fatalf("turn %d renders ABOVE an older turn — order must be oldest top / newest bottom", i)
		}
		prev = idx
	}
	if !(strings.Index(full, "keepturn01") < strings.Index(full, "keepturn40")) {
		t.Fatalf("oldest must render above newest:\n%s", full)
	}
}

// TestChatScrollBackFullTranscriptAndBottomGlue — scrollability + follow:
// in a VIEWPORT-height conversation (60 turns in a 10-row viewport) the
// newest turn is glued into view while following; pgup walks the FULL
// transcript back to turn 1 (follow releases); a message arriving while
// scrolled up does NOT yank the reader to the bottom; pgdown back to the
// bottom re-arms the follow and the next arrival is glued again.
func TestChatScrollBackFullTranscriptAndBottomGlue(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(50, 14)
	feed := func(n int) {
		msgs := make([]state.ChatMsg, 0, n)
		for i := 1; i <= n; i++ {
			from, kind := "user", "user"
			if i%2 == 0 {
				from, kind = "boss", "boss"
			}
			msgs = append(msgs, state.ChatMsg{
				ID: fmt.Sprintf("m%d", i), From: from, Kind: kind,
				Text: fmt.Sprintf("keepturn%02d", i), At: int64(i)})
		}
		c.SetState(state.OfficeState{Tick: n, Chat: msgs})
	}
	key := func(k rune) { c.Update(tea.KeyPressMsg(tea.Key{Code: k})) }

	// 1) following at the bottom: the NEWEST turn is on screen.
	feed(60)
	if !c.follow {
		t.Fatal("a fresh panel must follow the tail")
	}
	if view := ansi.Strip(c.View()); !strings.Contains(view, "keepturn60") {
		t.Fatalf("at the bottom the newest turn must be glued into view:\n%s", view)
	}

	// 2) pgup scrolls the FULL transcript back to turn 1 and releases
	// the follow — every entry between is traversable, nothing clipped.
	for guard := 0; !c.vp.AtTop() && guard < 100; guard++ {
		key(tea.KeyPgUp)
	}
	if !c.vp.AtTop() {
		t.Fatal("pgup must reach the very top of the full transcript")
	}
	if c.follow {
		t.Fatal("scrolling up must release the bottom-follow")
	}
	if view := ansi.Strip(c.View()); !strings.Contains(view, "keepturn01") {
		t.Fatalf("at the top the OLDEST turn must be visible:\n%s", view)
	}

	// 3) a new message while scrolled back must NOT yank the reader down.
	yoff := c.vp.YOffset()
	feed(61)
	if c.vp.YOffset() != yoff {
		t.Fatalf("arrival must not yank a scrolled-back reader (YOffset %d -> %d)", yoff, c.vp.YOffset())
	}
	if view := ansi.Strip(c.View()); !strings.Contains(view, "keepturn01") {
		t.Fatalf("the reader's position must survive an arrival:\n%s", view)
	}

	// 4) pgdown back to the bottom re-arms the follow; the next arrival
	// is glued to the bottom again.
	for guard := 0; !c.vp.AtBottom() && guard < 100; guard++ {
		key(tea.KeyPgDown)
	}
	if !c.follow {
		t.Fatal("returning to the bottom must re-arm the follow")
	}
	feed(62)
	if view := ansi.Strip(c.View()); !strings.Contains(view, "keepturn62") {
		t.Fatalf("a followed arrival must stay glued to the bottom:\n%s", view)
	}
	if !c.vp.AtBottom() {
		t.Fatal("a followed arrival must leave the viewport at the bottom")
	}
}
