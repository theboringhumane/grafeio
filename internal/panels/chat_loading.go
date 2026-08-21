// chat_loading.go — the "team is working…" status row: a one-line,
// COLORFUL, animated indicator for the chat tab, shown only while at
// least one subagent thread is LIVE. Claude Code cycles spinner verbs
// while the model chews; grafeio does the same for the whole team: a
// shimmering "✦" glyph, a dim "team is working", and a fun gerund that
// rotates off the office tick (Brewing → Churning → Pondering → …), the
// word's ink rotating with it.
//
// Liveness mirrors renderWorkerGroup's exact live rule (chat.go ~L1645,
// the same rule threadExpandedNow evaluates at ~L425-443): a thread is
// live when its roster sprite is busy (agentView.active, fed by
// workerSpriteActive in SetState) AND the max wtool/wthink meta-tick of
// its entries sits inside c.tick-wtoolStaleTicks. No extra state wiring:
// c.agents + c.chat + c.tick already carry everything.
//
// Width safety: the composed (already-styled) row is hard-folded to the
// panel width with foldStyledRows — the ANSI-aware utility every other
// chat row uses — so escape sequences are consumed atomically and a
// narrow panel truncates cleanly instead of shredding an SGR pair.
package panels

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
)

// loadingWordTicks — ticks per fun-word rotation. The office tick fires
// every 150–180ms while the team is busy (internal/app/power.go:
// tickPerf 150ms, tickBusyAuto 180ms), so 6 ticks ≈ 0.9–1.1s per word —
// Claude Code's snappy verb cadence instead of a languid crawl.
const loadingWordTicks = 6

// loadingWords — the rotating fun gerunds (~Claude Code's spinner verbs).
// The word maps deterministically off the tick, so a given tick always
// renders the same word+color pair (tests pin tick → word exactly).
var loadingWords = []string{
	"Brewing", "Churning", "Pondering", "Forging", "Crafting",
	"Scheming", "Sprinting", "Weaving", "Hacking", "Plotting",
}

// loadingInks — the cycling color palette (≥3 distinct inks in every
// hue-carrying theme: noir 3/2/6/5/11). Read at RENDER time from the
// re-pointable chrome vars, so a live /theme switch re-colors the row on
// the next tick. Mono theme falls back to grayscale emphasis by design.
func loadingInks() []color.Color {
	return []color.Color{chrome.Accent, chrome.OK, chrome.Info, chrome.Magenta, chrome.Question}
}

// loadingColor picks ink i of the cycle (word ink rotates with the word
// index; the glyph shimmers with the raw tick).
func loadingColor(i int) color.Color {
	inks := loadingInks()
	return inks[i%len(inks)]
}

// anyThreadActive reports whether at least one subagent thread is LIVE
// right now — the exact live half of renderWorkerGroup's expand rule
// (chat.go: active && c.tick-g.lastTick <= wtoolStaleTicks, see ~L1645;
// the same predicate threadExpandedNow walks at ~L425-443): roster-sprite
// busy + the agent's freshest wtool/wthink activity inside the staleness
// horizon. Uses only the chat snapshot (c.chat) and tick (c.tick).
func (c *Chat) anyThreadActive() bool {
	lastTick := map[string]int{}
	for _, m := range c.chat {
		if m.Kind != wtoolKind && m.Kind != wthinkKind {
			continue
		}
		if _, tk := parseWtoolMeta(m.Meta); tk > lastTick[m.From] {
			lastTick[m.From] = tk
		}
	}
	for name, tk := range lastTick {
		if av, ok := c.agents[name]; ok && av.active && c.tick-tk <= wtoolStaleTicks {
			return true
		}
	}
	return false
}

// loadingRow renders the "team is working…" status row for insertion
// directly above the chat input separator. Returns "" when NO subagent
// thread is currently live (the caller skips the row entirely — zero
// height cost). width is respected ANSI-aware; width < 1 yields "".
func (c *Chat) loadingRow(width int) string {
	if width < 1 || !c.anyThreadActive() {
		return ""
	}
	wi := (c.tick / loadingWordTicks) % len(loadingWords)
	word := loadingWords[wi]
	// ✦ shimmers EVERY tick (a fresh ink each ~0.18s while busy); the
	// word's ink rides the word index, so the color rotates WITH the
	// word. The base text stays dim — the moving pieces carry the fire.
	glyph := lipgloss.NewStyle().Foreground(loadingColor(c.tick)).Bold(true).Render("✦")
	wordStyled := lipgloss.NewStyle().Foreground(loadingColor(wi)).Bold(true).Render(word + "…")
	row := glyph + chrome.DimText.Render(" team is working — ") + wordStyled
	// ANSI-aware clip: escapes are consumed atomically, never split.
	return foldStyledRows(row, width, width)[0]
}
