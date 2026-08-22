// threads_opencode.go — the opencode-style subagent thread renderer.
//
// A subagent's merged wtool/wthink entries (workerGroup) draw as flat
// lines — no bordered card. COLLAPSED (the default, live threads
// included):
//
//	⠿ Explore Task — Scout question kinds recon
//	  ↳ Read internal/panels/chat.go
//
// The header owns a 2-cell glyph field: an ANIMATED braille run
// (threadLiveFrames ⠿⠷⠯⠟⠻⠽⠾ in the house accent — cycled by the
// OFFICE TICK, c.tick%len, the same timer-free drive thinkFrames and
// pendingBlockBar already use) while the thread is LIVE — roster sprite
// busy AND the freshest wtool/wthink meta-tick inside
// c.tick-wtoolStaleTicks, the exact rule the "team is working" loading
// row mirrors — a dim "✓" once DONE (no animation), a dim-red "✗" after
// /stop. The title reads "<Kind> Task — <task>" — Kind from the roster
// role the SetState rollup already captures (a scout runs recon:
// opencode's "Explore"), the task from the sticky workerTasks/agentView
// title — with "Subagent Task — <agent name>" as the no-task fallback.
// A LIVE header carries the glyph + title and NOTHING else — the rollup
// shows only once the thread SETTLES: the collapsed done/stopped header
// KEEPS the old summary card's trailing rollup, dim: "(· N tool calls
// [· M think] ✓ done)" — or "✗ stopped" (dim-red) for a /stop unwind —
// the exact counts the bordered card's "▾ …" line carried. The header
// is always exactly ONE row: a long title elides at the panel width
// with clipPlain, it never wraps to a hanging continuation.
//
// The row below is a dim "  ↳ <Verb> <rest>" sneak peek at the thread's
// NEWEST TOOL entry — BARE: no " ✓" / " ✗" / " … running" state mark
// (the state lives in the header's glyph and the settled thread's
// rollup). The reducer writes tool text as "<lowercase verb> · <rest>"
// ("read · internal/panels/chat.go"); a display-side shaping
// (shapeToolText) renders it as opencode's "<Verb> <rest>" form
// ("Read internal/panels/chat.go") — already-shaped text passes through
// unchanged, so the shaping is IDEMPOTENT. A trailing wthink never
// steals the peek (thoughts stay rolled up in the summary counts), but
// a think-ONLY thread falls back to "thinking · N lines". The sneak is
// always exactly ONE row, elided with clipPlain like the header, and
// shows for EVERY thread, live or done, collapsed or EXPANDED (there it
// trails the tool list as the "current task" line).
//
// A per-agent click on the header row or the sneak row (the threadRows
// hit-map registers exactly those two rows in BOTH states; expanded
// tool rows and the closing summary are never clickable) or the ctrl+g
// baseline expands the thread to list its merged rows — "[tool] <Verb>
// <rest> <state mark>" in ToolStyle (the same display-side shaping, the
// ✓/✗/✗ aborted/… running mark kept), think rows dim-italic with bodies
// only on a FULL expand — all 2-cell indented under the header and
// STILL wrapping with a 4-cell hanging continuation — followed by the
// ↳ sneak and a dim closing summary line ("  · N tool calls[ · M think]
// ✓ done", the same rollup wording as the settled collapsed header).
// While at least one RENDERED thread is live a dim-italic "ctrl+g ·
// view subagents" hint row trails the last thread block of the
// timeline.
//
// This file also owns the pending row's breathing block bar
// (pendingBlockBar) — the opencode "Build · …" loading line's vibe for
// "<boss> is typing…": height blocks ▁…█ sweeping left-to-right off the
// office tick, deterministic per tick, one row exactly.
package panels

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// threadHintText — the dim-italic hint row trailing the last thread block
// while ≥1 rendered thread is live (ctrl+g is the app keymap's thread
// toggle).
const threadHintText = "ctrl+g · view subagents"

// threadSpinnerStyle — the thread glyph's ink: the house accent
// (chrome.Accent — opencode's spinner rides the accent hue). Re-pointed
// on /theme switches via RefreshTheme — read through a constructor so
// the SGR pair lands again.
func threadSpinnerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(chrome.Accent)
}

// threadLiveFrames — the braille run the LIVE header glyph walks:
// opencode's chunky full-dot cycle, OPENED by ⠿ (the locked gallery
// frame's glyph). Cycled by the office tick (c.tick%len — the same
// timer-free drive thinkFrames and pendingBlockBar use), so SetState's
// existing tick pump is the only animator and a test pins the exact
// frame arithmetically (tick 21 → frame 0 → "⠿").
var threadLiveFrames = []string{"⠿", "⠷", "⠯", "⠟", "⠻", "⠽", "⠾"}

// threadLiveGlyph — the accent-styled braille frame the LIVE header
// shows at office tick `tick`. Pure function of state: no goroutines,
// no timers, deterministic per tick.
func threadLiveGlyph(tick int) string {
	return threadSpinnerStyle().Render(threadLiveFrames[tick%len(threadLiveFrames)])
}

// threadKindLabel — the title's role-ish Kind label, derived from the
// roster seat the SetState rollup already captures (no new state, no
// invented API): a scout runs recon — opencode's read-only "Explore"
// agent — a developer develops; the rest read as their seat. "" marks an
// unknown seat → the caller falls back to "Subagent Task".
func threadKindLabel(role state.EmployeeRole) string {
	switch role {
	case state.RoleScout:
		return "Explore"
	case state.RoleDeveloper:
		return "Developer"
	case state.RoleReviewer:
		return "Reviewer"
	case state.RoleRunner:
		return "Runner"
	case state.RoleHR:
		return "HR"
	}
	return ""
}

// threadLive — the render half of the staleness rule the "team is
// working" row mirrors (chat_loading.go's anyThreadActive): roster sprite
// busy + the group's freshest wtool/wthink meta-tick inside the
// wtoolStaleTicks horizon. Pure read over c.agents + c.tick.
func (c *Chat) threadLive(g workerGroup) bool {
	av, ok := c.agents[g.name]
	return ok && av.active && c.tick-g.lastTick <= wtoolStaleTicks
}

// threadTitle — the header's text: "<Kind> Task — <task>". The task is
// the STICKY last-known dispatch title (workerTasks survives EvReturned's
// task clear, agentView overrides while the roster still carries it);
// with no task at all the agent's NAME is the title
// ("Subagent Task — <agent name>"), and with no known role the Kind half
// falls back to "Subagent".
func (c *Chat) threadTitle(name string) string {
	task := c.workerTasks[name]
	role := state.EmployeeRole("")
	if av, ok := c.agents[name]; ok {
		role = av.role
		if av.task != "" {
			task = av.task
		}
	}
	if task == "" {
		return "Subagent Task — " + name
	}
	if kind := threadKindLabel(role); kind != "" {
		return kind + " Task — " + task
	}
	return "Subagent Task — " + task
}

// threadSummary — the rollup wording the bordered card's collapsed
// summary row carried (no caret, no parens): "· N tool calls ✓ done",
// joined by "· M think" when thoughts ride the thread, and swung to
// "✗ stopped" for the /stop unwind. A SETTLED (done/stopped) collapsed
// header wraps it in parens ("(· 2 tool calls ✓ done)") — a LIVE header
// never carries it — and the expanded thread's closing line renders it
// bare. The counts rule matches the old summary's exactly: tools always
// counted (singular "tool call" at 1), thinks only when present.
func (c *Chat) threadSummary(g workerGroup, stopped bool) string {
	tools, thinks := 0, 0
	for _, m := range g.lines {
		if m.Kind == wthinkKind {
			thinks++
		} else {
			tools++
		}
	}
	unit := "tool calls"
	if tools == 1 {
		unit = "tool call"
	}
	summary := "· " + itoa(tools) + " " + unit
	if thinks > 0 {
		summary += " · " + itoa(thinks) + " think"
	}
	if stopped {
		return summary + " ✗ stopped"
	}
	return summary + " ✓ done"
}

// shapeToolText — the display-side shaping that turns the reducer's
// tool text ("<lowercase verb> · <rest>" — internal/app/model.go emits
// "read · internal/panels/chat.go") into opencode's "<Verb> <rest>"
// form ("Read internal/panels/chat.go"): the head TitleCased, the " · "
// join swallowed. Only text matching ^([a-z][a-z ._-]*) · (.*)$ is
// shaped; anything else — the already-shaped target form included
// ("Read internal/panels/chat.go"), "[x] y", empty heads — passes
// through UNCHANGED, making the shaping IDEMPOTENT.
func shapeToolText(s string) string {
	i := strings.Index(s, " · ")
	if i <= 0 {
		return s
	}
	head := s[:i]
	for j, r := range head {
		switch {
		case r >= 'a' && r <= 'z':
		case j > 0 && (r == ' ' || r == '.' || r == '_' || r == '-'):
		default:
			return s // head is not the reducer's lowercase verb — pass through
		}
	}
	return strings.ToUpper(head[:1]) + head[1:] + " " + s[i+len(" · "):]
}

// wtoolStateSuffix — the state mark the EXPANDED tool rows carry:
// " ✓" done, " ✗" error, " ✗ aborted" the /stop unwind, " … running"
// anything live (or unrecognized). The ↳ sneak dropped it: a peek is
// BARE — the state lives in the header's glyph and the settled thread's
// trailing rollup.
func wtoolStateSuffix(meta string) string {
	toolState, _ := parseWtoolMeta(meta)
	switch toolState {
	case "done":
		return " ✓"
	case "error":
		return " ✗"
	case "aborted": // /stop unwind swung a running call here
		return " ✗ aborted"
	default: // running (or anything unexpected)
		return " … running"
	}
}

// workerToolLine renders one merged employee tool entry — the boss's
// inline one-liner shape built from the reducer's "<verb> · <rest>"
// text SHAPED to opencode's "<Verb> <rest>" form ("[tool] Read x ✓",
// "[tool] Read y … running", "[tool] Edit z ✗ aborted").
func workerToolLine(m state.ChatMsg) string {
	return "[tool] " + shapeToolText(m.Text) + wtoolStateSuffix(m.Meta)
}

// threadHeaderRows — the ONE title row (single-row contract: the header
// NEVER wraps — a long title elides at the panel width with clipPlain,
// no hanging continuation): a 2-cell glyph field, then the title. LIVE
// heads carry the office-tick braille glyph (threadLiveGlyph — every
// live thread pulses the same frame per tick) with the title in DEFAULT
// ink and NOTHING ELSE (no rollup while the thread is running); done
// heads dim the "✓" and the whole row; /stop-stopped heads swap to a
// dim-red "✗" with the "✗ stopped" summary. Only a SETTLED collapsed
// head trails the dim rollup "(· N tool calls[ · M think] ✓ done)" —
// clipped along with the title when the panel is narrow.
func (c *Chat) threadHeaderRows(g workerGroup, live, stopped, collapsed bool) []string {
	body := c.threadTitle(g.name)
	var glyph string
	switch {
	case stopped:
		glyph = chrome.ErrText.Faint(true).Render("✗")
	case live:
		glyph = threadLiveGlyph(c.tick) // office-tick braille run
	default:
		glyph = chrome.DimText.Render("✓")
	}
	if collapsed && (stopped || !live) {
		body += " (" + c.threadSummary(g, stopped) + ")"
	}
	body = clipPlain(body, c.w-2) // 2-cell glyph field eats the head
	switch {
	case stopped:
		body = chrome.ErrText.Faint(true).Render(body)
	case !live:
		body = chrome.DimText.Render(body)
	}
	return []string{glyph + " " + body}
}

// threadSneakRows — the thread's peek at its NEWEST TOOL entry:
// "  ↳ <Verb> <rest>" — BARE (no " ✓/✗/… running" state mark; the
// header's glyph and the settled rollup carry the state), the reducer's
// "<verb> · <rest>" text run through shapeToolText. A trailing wthink
// never steals the peek — thoughts roll up in the summary counts, per
// the old summary's rule — but a think-ONLY thread peeks "thinking · N
// lines" so every thread keeps its second line. Everything dim
// (thoughts dim-italic), always exactly ONE row elided at the panel
// width with clipPlain. Rendered for EVERY thread: collapsed threads as
// the second line, expanded threads as the "current task" line under
// the tool list.
func (c *Chat) threadSneakRows(g workerGroup) []string {
	if len(g.lines) == 0 {
		return nil
	}
	lastTool := -1
	for i := len(g.lines) - 1; i >= 0; i-- {
		if g.lines[i].Kind != wthinkKind {
			lastTool = i
			break
		}
	}
	textStyle := chrome.DimText
	var text string
	if lastTool >= 0 {
		text = shapeToolText(g.lines[lastTool].Text)
	} else {
		think := g.lines[len(g.lines)-1]
		textStyle = chrome.DimText.Italic(true)
		text = "thinking · " + countLines(foldStyledRows(think.Text, c.w-4, c.w-4)) + " lines"
	}
	return []string{chrome.DimText.Render("  ↳ ") + textStyle.Render(clipPlain(text, c.w-4))}
}

// threadExpandedRows — the thread's merged tool/think rows, 2-cell
// indented under the header (continuations hang 4 cells in, under the
// text): "[tool] <Verb> <rest> <state mark>" in ToolStyle (workerToolLine
// shapes the reducer's "<verb> · <rest>" text), thoughts via wthinkRows
// (bodies only on a FULL expand). These rows still WRAP, never
// truncate — the single-row contract binds ONLY the header and sneak.
func (c *Chat) threadExpandedRows(g workerGroup, full bool) []string {
	var rows []string
	for _, m := range g.lines {
		if m.Kind == wthinkKind {
			rows = append(rows, c.wthinkRows(m, full)...)
			continue
		}
		for j, ln := range foldStyledRows(workerToolLine(m), c.w-2, c.w-4) {
			prefix := "  "
			if j > 0 {
				prefix = "    "
			}
			rows = append(rows, chrome.ToolStyle.Render(prefix+ln))
		}
	}
	return rows
}

// threadClosingRows — the expanded thread's last line: the same rollup
// wording as the collapsed trailing info, bare (no parens), 2-cell
// indented under the header ("  · N tool calls[ · M think] ✓ done"),
// dim — dim-red faint once /stop stopped the thread. Never clickable
// (it is the tail of the tool list, not the thread's toggle).
func (c *Chat) threadClosingRows(g workerGroup, stopped bool) []string {
	style := chrome.DimText
	if stopped {
		style = chrome.ErrText.Faint(true)
	}
	rows := foldStyledRows(c.threadSummary(g, stopped), c.w-2, c.w-4)
	out := make([]string, 0, len(rows))
	for i, r := range rows {
		prefix := "  "
		if i > 0 {
			prefix = "    "
		}
		out = append(out, style.Render(prefix+r))
	}
	return out
}

// wthinkRows renders one merged employee thinking entry as thread content
// rows (2-cell indent under the header, bodies 4-cell). While the thread
// shows tool rows it is a single dim-italic "thinking · N lines" row; on
// a FULL expand (ctrl+g / a per-agent mouse override) the body renders
// too — capped to the stream tail (the freshest thinkStreamLines lines),
// matching the boss's collapsed-vs-expanded thinking shape.
func (c *Chat) wthinkRows(m state.ChatMsg, full bool) []string {
	think := chrome.DimText.Italic(true)
	// fold at the FULL body budget ("    " is 4 cells) so no row clips
	lines := foldStyledRows(m.Text, c.w-4, c.w-4)
	if !full {
		return []string{chrome.DimText.Render("  ") + think.Render("thinking · "+countLines(lines)+" lines")}
	}
	rows := []string{chrome.DimText.Render("  ") + think.Render("thinking")}
	shown := lines
	if more := len(lines) - thinkStreamLines; more > 0 {
		rows = append(rows, think.Render("    … "+itoa(more)+" more above"))
		shown = lines[more:]
	}
	for _, ln := range shown {
		rows = append(rows, chrome.DimText.Render("    "+ln))
	}
	return rows
}

// renderWorkerGroup draws ONE agent's thread INLINE at its timeline slot
// (mergeChatTimeline interleaves threads with the conversation — there is
// no docked region) as an opencode-style thread: header row + ↳ sneak
// row while COLLAPSED (the default — live threads included), or header +
// the merged tool/think rows + the ↳ "current task" sneak + the dim
// closing summary while EXPANDED. A per-agent override wins the ctrl+g
// baseline outright, and a /stop stopped thread force-collapses until an
// explicit expand re-opens it. The threadRows hit-map registers the
// ONE header row plus the ONE sneak row in BOTH states — clicking
// either toggles the thread; expanded tool rows and the closing summary
// never register. The block carries its OWN leading "\n" like every
// other timeline row group.
func (c *Chat) renderWorkerGroup(b *strings.Builder, g workerGroup) {
	stopped := c.threadStop[g.name]
	expanded := c.threadsExpanded
	full := c.threadsExpanded
	if v, ok := c.threadExpand[g.name]; ok {
		expanded = v
		full = v
	}
	if stopped {
		if _, ok := c.threadExpand[g.name]; !ok {
			expanded = false
		}
	}
	live := c.threadLive(g)
	header := c.threadHeaderRows(g, live, stopped, !expanded)
	sneak := c.threadSneakRows(g)
	var rows []string
	rows = append(rows, header...)
	sneakAt := len(rows)
	if expanded {
		rows = append(rows, c.threadExpandedRows(g, full)...)
		sneakAt = len(rows)
		rows = append(rows, sneak...)
		rows = append(rows, c.threadClosingRows(g, stopped)...)
	} else {
		rows = append(rows, sneak...)
	}
	if c.threadRows != nil {
		base := strings.Count(b.String(), "\n")
		for i := range header {
			c.threadRows[base+1+i] = g.name
		}
		for i := range sneak {
			c.threadRows[base+1+sneakAt+i] = g.name
		}
	}
	b.WriteString("\n" + strings.Join(rows, "\n"))
}

// pendingRamp — the height-block ramp the pending bar walks: the head
// cell is solid, the wave falls to the baseline behind it. HEIGHT blocks
// only, never width blocks: "▌" is the retired stream caret — banned from
// every chat state (TestNoCaretTypingRowAboveInput pins the ban).
var pendingRamp = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// pendingBarCells — the bar's width in cells (one full ramp — the head
// wraps seamlessly past the tail).
const pendingBarCells = 8

// pendingBlockBar renders the typing row's block-glyph column for office
// tick `tick`: the solid head sits at tick%pendingBarCells with the ramp
// trailing behind it — a left-to-right breathing wave, one cell of
// progress per tick. Deterministic per tick (tick 0 gives
// "█▇▆▅▄▃▂▁"), so tests pin the frame exactly and SetState's office-tick
// pump is the ONLY driver it needs.
func pendingBlockBar(tick int) string {
	var b strings.Builder
	for i := 0; i < pendingBarCells; i++ {
		behind := (i - (tick % pendingBarCells) + 2*pendingBarCells) % pendingBarCells
		b.WriteString(pendingRamp[pendingBarCells-1-behind])
	}
	return b.String()
}
