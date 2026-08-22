// chat_loading_test.go — behavior proofs for the "team is working…"
// status row: HIDDEN ("") when no subagent thread is live, SHOWN the
// instant one is (busy roster sprite + tool activity inside the
// wtoolStaleTicks horizon — renderWorkerGroup's live rule, mirrored);
// the fun word rotates DETERMINISTICALLY off the tick (tick 1 → Brewing,
// tick 7 → Churning, tick 50 → Hacking, tick 61 wraps to Brewing); the
// base text always carries "team is working"; and a narrow panel clips
// the row ANSI-safely to its width. No clocks, no sleeps: every tick and
// wtool meta-tick is a literal (Meta carries "state␟tick" like the
// reducer writes — parseWtoolMeta reads it back).
package panels

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

// newLoadingChat builds one deterministic fixture: worker tekton-1 at the
// given sprite, having logged one wtool entry whose meta-tick is
// lastToolTick, at office tick `tick` (SetState's own feed path — c.tick,
// c.agents and c.chat all land exactly as production writes them).
func newLoadingChat(t *testing.T, tick, lastToolTick int, sprite state.SpriteState) *Chat {
	t.Helper()
	c := NewChat(nil)
	c.SetSize(60, 14)
	c.SetState(state.OfficeState{
		Tick: tick,
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper,
				Sprite: sprite, Task: "Fix the widget"},
		},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "go fix the widget", At: 10},
			{ID: "w1", From: "tekton-1", Kind: wtoolKind,
				Text: "read · widget.go", Meta: "done\x1f" + itoa(lastToolTick), At: 1_000},
		},
	})
	return c
}

// TestLoadingRowHidesWhenNoThreadActive pins the OFF half of the live
// rule: an idle sprite, a stale activity horizon (tick-lastTool >
// wtoolStaleTicks, including exactly 121 — one past the boundary), and an
// empty conversation all render "" — zero height cost.
func TestLoadingRowHidesWhenNoThreadActive(t *testing.T) {
	// sprite idle at its desk: the worker exists and logged tools, but
	// renderWorkerGroup's `active` half fails → no row
	c := newLoadingChat(t, 50, 48, state.SpriteAtDesk)
	if row := c.loadingRow(60); row != "" {
		t.Fatalf("idle sprite must hide the row, got %q", row)
	}
	if c.anyThreadActive() {
		t.Fatal("idle sprite: anyThreadActive must be false")
	}

	// busy sprite but the horizon lapsed: 500-5 = 495 > 120
	c = newLoadingChat(t, 500, 5, state.SpriteWorking)
	if row := c.loadingRow(60); row != "" {
		t.Fatalf("stale activity (495 ticks quiet) must hide the row, got %q", row)
	}

	// boundary: exactly 121 quiet ticks — ONE past the <=120 rule
	c = newLoadingChat(t, 126, 5, state.SpriteWorking)
	if row := c.loadingRow(60); row != "" {
		t.Fatalf("121 quiet ticks is past wtoolStaleTicks, must hide, got %q", row)
	}

	// nothing at all on the floor: no chat, no employees
	empty := NewChat(nil)
	empty.SetSize(60, 14)
	empty.SetState(state.OfficeState{Tick: 9})
	if row := empty.loadingRow(60); row != "" {
		t.Fatalf("empty state must hide the row, got %q", row)
	}
}

// TestLoadingRowShowsWhileThreadLive pins the ON half: a busy sprite with
// activity inside the horizon shows the row — including exactly AT the
// boundary (tick-lastTool == wtoolStaleTicks, the rule is <=) — with the
// base text "team is working", the ✦ glyph, and the tick-mapped word.
func TestLoadingRowShowsWhileThreadLive(t *testing.T) {
	c := newLoadingChat(t, 50, 48, state.SpriteWorking)
	row := c.loadingRow(60)
	if row == "" {
		t.Fatal("live thread (busy sprite + fresh tool) must show the row")
	}
	plain := ansi.Strip(row)
	for _, want := range []string{"✦", "team is working", "Hacking"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("row at tick 50 must contain %q, got %q", want, plain)
		}
	}
	if !c.anyThreadActive() {
		t.Fatal("live thread: anyThreadActive must be true")
	}

	// boundary: exactly wtoolStaleTicks (120) quiet — still live (<=)
	c = newLoadingChat(t, 125, 5, state.SpriteWorking)
	if row := c.loadingRow(60); row == "" {
		t.Fatal("120 quiet ticks is AT the horizon boundary (<= 120): must still show")
	}
	if plain := ansi.Strip(c.loadingRow(60)); !strings.Contains(plain, "team is working") {
		t.Fatalf("boundary row must keep the base text, got %q", plain)
	}

	// to-manager + meeting sprites are busy too (workerSpriteActive)
	for _, sprite := range []state.SpriteState{state.SpriteToManager, state.SpriteMeeting} {
		c = newLoadingChat(t, 50, 48, sprite)
		if row := c.loadingRow(60); row == "" {
			t.Fatalf("sprite %q is a busy sprite: the row must show", sprite)
		}
	}
}

// TestLoadingRowWordRotatesWithTick pins the deterministic tick→word
// mapping: tick/6 % 10 indexes loadingWords, so tick 1 is Brewing, tick 7
// Churning, tick 50 Hacking — and a full 60-tick cycle realigns to
// Brewing. Same-tick renders are identical (no wall-clock involvement).
func TestLoadingRowWordRotatesWithTick(t *testing.T) {
	cases := []struct {
		tick int
		word string
	}{
		{1, "Brewing"},    // 1/6 = 0
		{7, "Churning"},   // 7/6 = 1
		{13, "Pondering"}, // 13/6 = 2
		{50, "Hacking"},   // 50/6 = 8
		{61, "Brewing"},   // 61/6 = 10 → wraps to 0
	}
	for _, tc := range cases {
		c := newLoadingChat(t, tc.tick, tc.tick, state.SpriteWorking)
		plain := ansi.Strip(c.loadingRow(60))
		if !strings.Contains(plain, tc.word+"…") {
			t.Fatalf("tick %d must render %q, got %q", tc.tick, tc.word+"…", plain)
		}
	}
	// the rotation is ACTUAL change: far-apart ticks show different words
	a := ansi.Strip(newLoadingChat(t, 1, 1, state.SpriteWorking).loadingRow(60))
	b := ansi.Strip(newLoadingChat(t, 7, 7, state.SpriteWorking).loadingRow(60))
	if a == b {
		t.Fatalf("tick 1 vs 7 must rotate the word, both rendered %q", a)
	}
	// …and DETERMINISTIC: two fresh panels at the same tick agree to the byte
	c1 := newLoadingChat(t, 50, 48, state.SpriteWorking).loadingRow(60)
	c2 := newLoadingChat(t, 50, 48, state.SpriteWorking).loadingRow(60)
	if c1 != c2 {
		t.Fatalf("same tick must render byte-identically:\n%q\nvs\n%q", c1, c2)
	}
}

// TestLoadingRowColorPalette proofs the COLORFUL contract: the cycling
// palette holds ≥3 DISTINCT theme inks (noir maps them to 3/2/6/5/11) and
// word→ink mapping is deterministic — wi and wi+len(inks) always match.
func TestLoadingRowColorPalette(t *testing.T) {
	inks := loadingInks()
	distinct := map[string]bool{}
	for _, ink := range inks {
		// %#v disambiguates structurally-different color.Color values too
		distinct[fmt.Sprintf("%#v", ink)] = true
	}
	if len(distinct) < 3 {
		t.Fatalf("palette must cycle ≥3 distinct inks, got %d: %v", len(distinct), inks)
	}
	for wi := 0; wi < len(loadingWords); wi++ {
		if loadingColor(wi) != loadingColor(wi+len(inks)) {
			t.Fatalf("word ink must repeat every %d words (wi=%d)", len(inks), wi)
		}
	}
}

// TestLoadingRowRespectsWidth: at width 12 the row clips to ≤12 cells
// ANSI-safely (foldStyledRows — escape sequences consumed atomically, the
// first hard-folded row returned); at full width the whole row survives.
// The row is ALWAYS a single line, never a wrapped block.
func TestLoadingRowRespectsWidth(t *testing.T) {
	c := newLoadingChat(t, 50, 48, state.SpriteWorking)

	narrow := c.loadingRow(12)
	if narrow == "" {
		t.Fatal("width 12 clip must not vanish the row")
	}
	if w := ansi.StringWidth(narrow); w > 12 {
		t.Fatalf("row must respect width: ansi width %d > 12 (%q)", w, narrow)
	}
	if strings.Contains(narrow, "\n") {
		t.Fatalf("the status row must stay one line, got %q", narrow)
	}
	if ansi.StringWidth(ansi.Strip(narrow)) == 0 {
		t.Fatal("clip must keep some visible cells")
	}

	full := c.loadingRow(60)
	if plain := ansi.Strip(full); !strings.Contains(plain, "Hacking…") {
		t.Fatalf("full-width row must survive unclipped, got %q", plain)
	}
	if strings.Contains(full, "\n") {
		t.Fatalf("full-width row must stay one line, got %q", full)
	}
}
