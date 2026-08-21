// chat.go — THE STAR of the sidebar: the chat tab.
//
// Layout inside the tab panel (top → bottom):
//
//		viewport  — the whole conversation, glamour-rendered markdown.
//		            user turns are plain-wrapped and prefixed cyan "you › ";
//		            boss turns are rendered THROUGH GLAMOUR as markdown
//		            (**bold**, lists, fences format + wrap) with a yellow
//		            "boss › " hanging indent, then HARD-FOLDED to the panel
//		            width (glamour's wrap misses code fences and unbreakable
//		            URLs — foldStyledLines refolds those, ANSI-aware).
//		            Kind "think" entries render as dim-italic thinking blocks —
//		            LIVE while their CallID streams (spinner header, growing
//		            tail, always expanded), then COLLAPSED to
//		            "thinking · N lines" until ctrl+t expands all;
//		            Kind "tool" entries (BOSS/primary) render as dim one-liners
//		            merged by CallID (wrapped with a hanging indent, never
//		            clipped); Kind "wtool" entries (EMPLOYEE tools)
//		            never touch the flow — they group into the workers-thread
//		            region at the END of the conversation: one group per
//		            agent (newest at the bottom), header "┌ <agent> · <task>"
//		            over indented "│ [tool] …" rows while the agent is active
//		            (long rows WRAP with "│ " continuations, never truncate),
//		            auto-collapsing to "<agent> · <task> (· N tool calls ✓
//		            done)" on EvReturned or 120 ticks of quiet, with ctrl+g
//		            expanding/collapsing all completed threads;
//		            Kind "office" entries (the concierge's EvChatOffice seam)
//		            render as INFO-cyan "office ›" markdown bubbles — a real
//		            turn, streamed replace-by-ID like the boss, with a dim
//		            "office is answering…" placeholder while pending-empty;
//		            From "office" entries with no Kind render as dim local
//		            notices (red when Meta == "error").
//		divider
//		spinner   — the typing row, shown for the WHOLE pending period:
//		            while ANY boss reply is outstanding (" <boss> is
//		            typing…", named from brain.json boss.name) — with or
//		            without streamed text. A pending boss bubble WITH text
//		            keeps rendering in the viewport as a streaming "boss ›"
//		            turn (glamour markdown re-rendered per delta); the row
//		            below is the liveness signal now — there is NO caret.
//		            While BossDelegating holds (boss quiet, workers busy)
//		            the row swaps to a settled dim " <boss>: delegating ·
//		            N busy" — no spinner.
//		textarea  — multiline input; Enter sends, Shift+Enter (or Ctrl+J) is a
//		            newline, placeholder "talk to the boss…". NEVER locked: while
//		            the boss is busy, Enter FREE-SENDS (the app routes the prompt
//		            straight to the backend, which queues it server-side and
//		            drains it after the current turn — no client queue, nothing
//		            hides); the placeholder reads "<boss> is typing…", and from
//		            the second direct send on "<boss>: turn N · your message
//		            rides next" while the status line carries "busy · N queued
//		            (server)". A client-side backlog item is created ONLY for
//		            roadblocks: a permission prompt or a question hold
//		            outstanding. While a permission prompt is open, the prompt
//		            MODAL replaces this region: y/a/n answers, esc defers; every
//		            other key still types into the (hidden) textarea.
//		question  — while a boss question hold is open, the QUESTION MODAL
//		            replaces this region: a free-text input (enter submits via
//		            AnswerQuestion, esc defers → /question re-opens); the
//		            textarea is DISABLED. While the hold is outstanding
//		            (open or deferred) the placeholder reads "boss is waiting
//		            for your answer… · N queued" and Enter still enqueues.
//
// Scroll: mouse wheel + PgUp/PgDn always scroll the conversation; ↑/↓ move
// inside a multi-line draft and scroll the conversation otherwise. ctrl+t
// expands/collapses ALL thinking blocks, ctrl+d expands/collapses ALL diff
// entries (both handled by the app keymap).
package panels

import (
	"image/color"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	chlexers "github.com/alecthomas/chroma/v2/lexers"
	chstyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	userPrefix   = "you › "
	bossPrefix   = "boss › "
	officePrefix = "office › "

	placeholderIdle = "talk to the boss…"
	placeholderWait = "boss is waiting for your answer…"

	// defaultBossShort — the busy placeholder's boss word when no config
	// short name is set ("boss is typing…"; SetBossShortName personalizes).
	defaultBossShort = "boss"

	textareaH = 3 // rows of multiline input at the bottom of the tab
)

// thinkKind / toolKind / wtoolKind / questionKind / diffKind / officeFrom /
// errMeta — chat entry markers the app reducer tags onto state.ChatMsg so
// this panel can style them without touching the user/boss turn paths.
// wtoolKind is the EMPLOYEE tool line: never rendered inline; grouped into
// the workers-thread region at the end of the conversation. wthinkKind is
// the EMPLOYEE EvThought line (the app reducer merges one per agent+CallID,
// same stream mechanics as the boss's thinkKind): it joins the SAME worker
// thread — a dim-italic "thinking · N lines" row while the thread is
// live, a "· N think" count in the collapsed summary, a full body under
// ctrl+g.
const (
	thinkKind    = "think"
	toolKind     = "tool"
	wtoolKind    = "wtool"
	wthinkKind   = "wthink"
	questionKind = "question"
	diffKind     = "diff"
	officeFrom   = "office"
	errMeta      = "error"
	// officeKind — concierge chat bubbles (the EvChatOffice seam: From
	// "office" + Kind "office"). A concierge answer is a REAL conversation
	// turn — INFO-colored "office ›" label over glamour markdown, same
	// replace-by-ID streaming mechanics as the boss — NOT a dim notice
	// (notices keep Kind "" and land in renderNotice).
	officeKind = "office"
)

// wtoolStaleTicks — a workers thread auto-collapses after this many ticks
// with no tool activity from its agent (EvReturned collapses instantly via
// the sprite). ctrl+g re-expands completed threads.
const wtoolStaleTicks = 120

// diffMetaSep splits the diff ChatMsg.Meta carrier: path ␟ +adds ␟ -dels
// (unit separator — paths may contain spaces). Written by the app reducer,
// read only here.
const diffMetaSep = "\x1f"

// diffClip is the max body lines shown in an expanded diff before
// "+N more" truncation.
const diffClip = 30

// PermissionView is the open permission prompt the chat panel renders in
// place of the textarea (set/cleared by the app via SetPermission).
type PermissionView struct {
	ID       string
	ToolName string
	Summary  string
}

// QuestionView is the open boss question modal the chat panel renders in
// place of the textarea (set/cleared by the app via SetQuestion). Unlike
// the permission prompt — choice keys, text stays live — the question
// modal OWNS every typed key into its own free-text input line: the turn
// is parked at the question reply API, so the textarea is disabled until
// the answer goes through AnswerQuestion (or esc defers).
type QuestionView struct {
	ID      string // pending wire request id ("que-…")
	Text    string // the boss's question
	Options string // dim options list ("a | b | c"), may be empty
}

// Chat is the chat tab panel.
type Chat struct {
	vp     viewport.Model
	ta     textarea.Model
	sp     spinner.Model
	onSend func(text string, atts []state.Attachment) tea.Cmd

	// Queue + permission + question + free-send seams (set by the app at
	// build time). onEnqueue is for ROADBLOCKS only: Enter while a question
	// hold is outstanding / a permission modal is open enqueues (the turn
	// is parked at the reply API — a chat prompt would not resume it).
	// onBusySend is FREE-QUEUING: Enter while the boss is merely busy goes
	// straight to the backend, which queues the prompt server-side.
	onEnqueue    func(text string, atts []state.Attachment) tea.Cmd
	onBusySend   func(text string, atts []state.Attachment) tea.Cmd
	onPermAnswer func(response string) tea.Cmd
	onPermLater  func() tea.Cmd // esc defers the prompt
	perm         *PermissionView
	queueLen     int

	// Question modal — open boss question hold replaces the textarea
	// entirely (questionWaiting survives esc-defer: the turn stays parked
	// and typed text ENQUEUES until the resolved + completed boss reply).
	question         *QuestionView
	onQuestionAnswer func(text string) tea.Cmd
	onQuestionLater  func() tea.Cmd
	qInput           string // the modal's own free-text input buffer
	questionWaiting  bool   // question hold outstanding (open or deferred)

	chat    []state.ChatMsg // rendered snapshot
	pending bool
	// bossShort — the boss's display short name (first word of brain.json
	// boss.name), used by the busy placeholder + typing spinner line.
	bossShort string
	// pendingSpin — the typing row (" boss is typing…" spinner) shows for
	// the WHOLE pending period: any outstanding boss reply, text or not.
	// The streaming bubble in the viewport carries the partial text; this
	// row (below the divider, above the input) carries the liveness.
	pendingSpin bool
	follow      bool // stick to the bottom unless the user scrolled up

	showThinking    bool // /thinking on|off — collected blocks visible (default true)
	showTools       bool // /tools on|off    — tool one-liners visible (default true)
	thinkExpanded   bool // ctrl+t — thinking expanded; DEFAULT false (collapsed)
	diffExpanded    bool // ctrl+d or /diffs on|off — diffs expanded; DEFAULT false
	threadsExpanded bool // ctrl+g — completed worker threads expanded; DEFAULT false
	compactRows     bool // /compact layout — the textarea trims to 2 visible rows

	// agents — per-name worker rollup captured from st.Employees each
	// SetState (task title for the thread header, sprite-active for the
	// live/collapsed decision). The reducer's wtool entries carry only the
	// name; the roster is the source of their decoration.
	agents map[string]agentView
	// workerTasks — sticky last-known task title per worker name. The
	// EvReturned reducer CLEARS Employee.Task, but a collapsed thread's
	// summary must still read "tekton-1 · Wire… (· N tool calls ✓ done)".
	workerTasks map[string]string

	// delegating/delegatingN — P3: BossDelegating flips the pending-spin
	// row from the typing spinner to a settled " <boss>: delegating ·
	// N busy" (dim, no spinner). N = hired (non-manager) employees in
	// working/to-manager/meeting sprites.
	delegating  bool
	delegatingN int

	// serverTurn — the app's free-queuing tally: prompts sent DIRECT to the
	// backend during the current busy turn (the serve queues them
	// natively). 0 = none this turn. From the second send on the busy
	// placeholder reads "<boss>: turn N · your message rides next".
	serverTurn int

	// streamingThink — CallIDs with an OPEN boss EvThought stream (set by
	// the app from its model-owned active set). Streaming blocks always
	// render expanded (live transcript) regardless of ctrl+t; when the
	// stream closes (Done / new boss turn) they collapse to "thinking ·
	// N lines". tick drives the header spinner — no extra timer.
	streamingThink map[string]bool
	tick           int

	w, h      int
	md        *glamour.TermRenderer
	mdWidth   int
	renderRev uint64 // cheap changed-detection for SetState

	// Attachment state (chat_attach.go): staged chips above the textarea
	// plus the @ file-picker popover. The chips drain into onSend/onEnqueue
	// on Enter; temp paste dirs are removed by the app's send closures (or
	// here when a chip is dropped before ever sending).
	atts             []chatAttachment
	atOpen           bool                      // @ picker visible
	atFrag           string                    // live "@fragment" filter (tail-derived)
	atFiles          []string                  // walked repo files; nil until the walk answers
	atFiltered       []string                  // refilterAttach's visible slice
	atSel            int                       // selected row inside atFiltered
	pasteSeq         int                       // paste-N.png naming for image attaches
	pasteUnsupported bool                      // non-darwin notice latch (fire once)
	onNotice         func(text string) tea.Cmd // office-notice seam (cap/drop/platform)

	// Slash popover (popover.go): "/" at a word start opens the command
	// menu; "/theme " flips it into the live-preview theme picker.
	slashOpen      bool
	slashMode      int // slashModeCmd | slashModeTheme
	slashFrag      string
	slashSel       int
	slashCmds      []slashCommand // refilterSlash's visible slice (cmd mode)
	slashThemes    []string       // refilterSlash's visible slice (theme mode)
	slashPrevTheme string         // theme captured when a preview session opened

	// click/expansion bookkeeping for the workers-thread region:
	// threadExpand holds PER-AGENT explicit expansion (double-click an
	// agent on the floor / click a thread header — a set entry wins over
	// the live+ctrl+g default); threadRows maps rendered content line →
	// agent name for the expanded-thread "┌" headers (mouse hit lookup).
	// threadStop holds the /stop markers: a stopped thread force-collapses
	// and its summary reads "✗ stopped" until an explicit expand re-opens
	// the rows.
	threadExpand map[string]bool
	threadRows   map[int]string
	threadStop   map[string]bool

	diffCache map[string]diffCacheEntry // parsed+hilighted diff rows by msg ID
}

// NewChat builds the panel; onSend is invoked on Enter with a non-empty
// draft (or any staged attachments) — the app turns it into backend.Send +
// chat-user/pending events. Attachments ride the second argument (nil for
// a plain text send).
func NewChat(onSend func(text string, atts []state.Attachment) tea.Cmd) *Chat {
	vp := viewport.New(viewport.WithWidth(10), viewport.WithHeight(5))
	vp.SoftWrap = true
	vp.MouseWheelEnabled = true

	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = placeholderIdle
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(textareaH)
	ta.SetWidth(30)
	// Enter SENDs; Shift+Enter (kitty) or Ctrl+J (universal) inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("shift+enter", "ctrl+j"),
		key.WithHelp("shift+enter", "newline"),
	)
	applyTextareaStyles(&ta)
	ta.Focus()

	sp := spinner.New(
		spinner.WithSpinner(spinner.Line),
		spinner.WithStyle(chrome.AccentText),
	)

	c := &Chat{vp: vp, ta: ta, sp: sp, onSend: onSend, follow: true,
		showThinking: true, showTools: true, diffCache: map[string]diffCacheEntry{},
		workerTasks: map[string]string{}}
	c.SetSize(30, 10)
	return c
}

// applyTextareaStyles points the textarea at the live chrome palette —
// called at build time AND on every /theme switch (RefreshTheme).
func applyTextareaStyles(ta *textarea.Model) {
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(chrome.Accent)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(chrome.Dim)
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Text = lipgloss.NewStyle()
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(chrome.Accent).Faint(true)
	ta.SetStyles(styles)
}

// RefreshTheme re-points every cached style-derived surface at the active
// theme: textarea styles, spinner color, and the glamour renderer (rebuilt
// lazily at the next boss turn). Called by the app on /theme switches.
func (c *Chat) RefreshTheme() {
	applyTextareaStyles(&c.ta)
	c.sp.Style = chrome.AccentText
	c.md = nil
	c.diffCache = map[string]diffCacheEntry{} // syntax colours are theme-bound
	c.forceRender()
}

// ToggleThink expands/collapses ALL thinking blocks in the conversation
// (ctrl+t, routed by the app keymap).
func (c *Chat) ToggleThink() {
	c.thinkExpanded = !c.thinkExpanded
	c.forceRender()
}

// ToggleDiffs expands/collapses ALL diff entries in the conversation
// (ctrl+d, routed by the app keymap).
func (c *Chat) ToggleDiffs() {
	c.diffExpanded = !c.diffExpanded
	c.forceRender()
}

// ToggleThreads expands/collapses ALL completed worker threads in the
// conversation (ctrl+g, routed by the app keymap). Independent of the
// thinking toggles — live threads always render expanded regardless.
func (c *Chat) ToggleThreads() {
	c.threadsExpanded = !c.threadsExpanded
	c.forceRender()
}

// ExpandThread sets ONE agent's work-thread expansion explicitly (mouse:
// double-click the agent's floor sprite / click the thread's "┌" header).
// A set per-agent override wins over the live/ctrl+g default until the
// next call — ctrl+g still moves the global baseline underneath it.
func (c *Chat) ExpandThread(agent string, expanded bool) {
	if c.threadExpand == nil {
		c.threadExpand = map[string]bool{}
	}
	c.threadExpand[agent] = expanded
	c.forceRender()
}

// ToggleThread flips ONE agent's thread from its CURRENT effective state
// (what renderWorkers shows for it right now).
func (c *Chat) ToggleThread(agent string) {
	c.ExpandThread(agent, !c.threadExpandedNow(agent))
}

// threadExpandedNow — the thread's effective expansion under the same
// rule renderWorkers applies: per-agent override wins, else live (busy
// sprite + activity inside the staleness horizon), else ctrl+g.
func (c *Chat) threadExpandedNow(name string) bool {
	if v, ok := c.threadExpand[name]; ok {
		return v
	}
	if av, ok := c.agents[name]; ok && av.active {
		lastTick := 0
		for _, m := range c.chat {
			if (m.Kind == wtoolKind || m.Kind == wthinkKind) && m.From == name {
				if _, tk := parseWtoolMeta(m.Meta); tk > lastTick {
					lastTick = tk
				}
			}
		}
		if c.tick-lastTick <= wtoolStaleTicks {
			return true
		}
	}
	return c.threadsExpanded
}

// ClickRow handles a mouse click at (x, y) IN CHAT CONTENT COORDS
// (viewport row 0 at the top of the chat panel; the app translated the
// screen coords over the tab/border chrome). A hit on an expanded worker
// thread's "┌" header row toggles that agent's thread. Returns true when
// the click was claimed.
func (c *Chat) ClickRow(x, y int) bool {
	if y < 0 || y >= c.vp.Height() {
		return false
	}
	line := y + c.vp.YOffset()
	name, ok := c.threadRows[line]
	if !ok {
		return false
	}
	c.ToggleThread(name)
	return true
}

// SetDiffsExpanded shows diffs expanded (on) or collapsed (off) —
// /diffs on|off.
func (c *Chat) SetDiffsExpanded(on bool) {
	if c.diffExpanded == on {
		return
	}
	c.diffExpanded = on
	c.forceRender()
}

// DiffsExpanded reports whether diff entries render expanded.
func (c *Chat) DiffsExpanded() bool { return c.diffExpanded }

// SetEnqueue wires the app's enqueue callback — for ROADBLOCKS only (Enter
// while a question hold is outstanding / a permission modal is open). A
// plain busy turn does NOT enqueue: it free-sends via onBusySend. Staged
// attachments ride along so a queued prompt keeps its files.
func (c *Chat) SetEnqueue(fn func(text string, atts []state.Attachment) tea.Cmd) { c.onEnqueue = fn }

// SetBusySend wires the app's FREE-SEND callback (free-queuing / the
// anti-stuck flow): Enter while a boss reply is pending sends DIRECTLY to
// the backend — the serve queues the prompt server-side and drains it
// after the current turn, so the prompt never hides in a client queue.
func (c *Chat) SetBusySend(fn func(text string, atts []state.Attachment) tea.Cmd) { c.onBusySend = fn }

// SetServerTurn hands the panel the app's free-queuing tally for the
// current busy turn (0 = none / idle again). The busy placeholder reflects
// it (from the second send on: "<boss>: turn N · your message rides next").
func (c *Chat) SetServerTurn(n int) {
	if c.serverTurn == n {
		return
	}
	c.serverTurn = n
	c.refreshPlaceholder()
}

// MarkThreadStopped force-collapses one agent's worker thread (the /stop
// unwind): its summary reads "✗ stopped" until an explicit per-agent
// expand (mouse click / double-click) re-opens the rows.
func (c *Chat) MarkThreadStopped(name string) {
	if c.threadStop == nil {
		c.threadStop = map[string]bool{}
	}
	if c.threadStop[name] {
		return
	}
	c.threadStop[name] = true
	c.forceRender()
}

// SetPermissionHandlers wires the app's permission answer/defer callbacks
// for the y/a/n/esc keys captured while a prompt is open.
func (c *Chat) SetPermissionHandlers(answer func(response string) tea.Cmd, later func() tea.Cmd) {
	c.onPermAnswer, c.onPermLater = answer, later
}

// SetPermission opens (non-nil) or closes (nil) the permission prompt that
// replaces the textarea region. Opening closes the @ picker (the modal owns
// the region now); staged chips stay — they belong to the draft.
func (c *Chat) SetPermission(p *PermissionView) {
	if p != nil {
		c.closeAttachPicker()
		c.closeSlashPicker(true) // the modal owns the region now
	}
	c.perm = p
}

// SetQuestionHandlers wires the app's question answer/defer callbacks:
// Enter submits the modal input (→ AnswerQuestion), esc defers (/question
// re-opens).
func (c *Chat) SetQuestionHandlers(answer func(text string) tea.Cmd, later func() tea.Cmd) {
	c.onQuestionAnswer, c.onQuestionLater = answer, later
}

// SetQuestion opens (non-nil) or closes (nil) the boss question modal that
// replaces the textarea region. Opening clears the modal's input buffer;
// the modal height differs from the textarea's, so the layout splits again.
func (c *Chat) SetQuestion(q *QuestionView) {
	c.question = q
	if q != nil {
		c.qInput = ""
		c.closeAttachPicker()     // the modal owns the region now (chips stay)
		c.closeSlashPicker(true) // same for the slash popover
	}
	c.SetSize(c.w, c.h)
}

// SetQuestionWaiting marks whether a boss question hold is outstanding
// (open or esc-deferred). While waiting the placeholder reads "boss is
// waiting for your answer…" and Enter ENQUEUES — the turn is parked at
// the question reply API, not typing.
func (c *Chat) SetQuestionWaiting(on bool) {
	c.questionWaiting = on
	c.refreshPlaceholder()
}

// SetQueueLen updates the queue count shown in the busy placeholder
// ("<boss> is typing… · N queued"). The queue itself lives in the app model.
func (c *Chat) SetQueueLen(n int) {
	c.queueLen = n
	c.refreshPlaceholder()
}

// SetBossShortName personalizes the busy placeholder and the typing spinner
// line from the config boss name's first word ("jorge is typing…"). Empty
// restores "boss".
func (c *Chat) SetBossShortName(name string) {
	if c.bossShort == name {
		return
	}
	c.bossShort = name
	c.refreshPlaceholder()
}

// typingText — the busy-state wording: "<bossShort> is typing…".
func (c *Chat) typingText() string {
	name := c.bossShort
	if name == "" {
		name = defaultBossShort
	}
	return name + " is typing…"
}

// delegatingText — the P3 delegation wording in place of the typing
// spinner: "<bossShort>: delegating · <n> busy".
func (c *Chat) delegatingText() string {
	name := c.bossShort
	if name == "" {
		name = defaultBossShort
	}
	return name + ": delegating · " + itoa(c.delegatingN) + " busy"
}

// turnText — the busy placeholder's free-queuing wording once a second
// prompt is already queued server-side this turn ("boss: turn 2 · your
// message rides next").
func (c *Chat) turnText() string {
	name := c.bossShort
	if name == "" {
		name = defaultBossShort
	}
	return name + ": turn " + itoa(c.serverTurn) + " · your message rides next"
}

// refreshPlaceholder recomputes the textarea placeholder from pending +
// queue + question-hold + server-turn state. A parked question turn
// (WAITING, not typing) wins over the typing text in both wording and
// queue badge; the free-queuing turn text wins over the plain typing text
// from the second direct send on.
func (c *Chat) refreshPlaceholder() {
	base := ""
	switch {
	case c.questionWaiting:
		base = placeholderWait
	case c.pending:
		base = c.typingText()
		if c.serverTurn >= 2 {
			base = c.turnText()
		}
	default:
		c.ta.Placeholder = placeholderIdle
		return
	}
	if c.queueLen > 0 {
		c.ta.Placeholder = base + " · " + itoa(c.queueLen) + " queued"
		return
	}
	c.ta.Placeholder = base
}

// SetShowThinking shows/hides collected thinking blocks (/thinking on|off).
func (c *Chat) SetShowThinking(on bool) {
	if c.showThinking == on {
		return
	}
	c.showThinking = on
	c.forceRender()
}

// ShowThinking reports whether thinking blocks render.
func (c *Chat) ShowThinking() bool { return c.showThinking }

// SetStreamingThink hands the panel the live think-stream set — CallIDs
// whose boss EvThought hasn't seen Done yet. Called by the app right
// before SetState (render consults the set there). Defensive copy: the
// caller keeps mutating its own map.
func (c *Chat) SetStreamingThink(ids map[string]bool) {
	if len(ids) == 0 {
		c.streamingThink = nil
		return
	}
	next := make(map[string]bool, len(ids))
	for id := range ids {
		next[id] = true
	}
	c.streamingThink = next
}

// SetShowTools shows/hides tool one-liners (/tools on|off).
func (c *Chat) SetShowTools(on bool) {
	if c.showTools == on {
		return
	}
	c.showTools = on
	c.forceRender()
}

// ShowTools reports whether tool one-liners render.
func (c *Chat) ShowTools() bool { return c.showTools }

// forceRender re-renders the conversation outside the SetState revision gate
// (toggles change the pixels, not the state).
func (c *Chat) forceRender() {
	c.vp.SetContent(c.renderConversation())
	if c.follow {
		c.vp.GotoBottom()
	}
}

// Title implements Tab.
func (c *Chat) Title() string { return "chat" }

// Pending reports whether a boss reply is outstanding.
func (c *Chat) Pending() bool { return c.pending }

// SpinnerKick returns the cmd that starts the typing animation. The app
// calls this when pending flips false → true.
func (c *Chat) SpinnerKick() tea.Cmd { return c.sp.Tick }

// inputRows is the textarea's visible height: textareaH normally, trimmed
// to 2 rows in the /compact layout. The permission modal always renders
// textareaH rows (it is sized for them), so it keeps the full region.
func (c *Chat) inputRows() int {
	if c.compactRows && c.perm == nil {
		return 2
	}
	return textareaH
}

// SetCompact switches the input region between the full 3-row textarea and
// the compact 2-row one (the app calls it on /compact + /mode changes).
func (c *Chat) SetCompact(on bool) {
	if c.compactRows == on {
		return
	}
	c.compactRows = on
	c.ta.SetHeight(c.inputRows())
	c.SetSize(c.w, c.h) // viewport takes/relinquishes the extra row
}

// SetSize implements Tab: splits content height across viewport / divider /
// spinner / bottom region. The typing (spinner) row sits BELOW the divider,
// one row while pendingSpin. The bottom region is the textarea, permission
// modal, or question modal (the question modal is the one region with a
// DIFFERENT row count) — PLUS the attachment surfaces above it: the chips
// row(s) when files are staged, and the @ picker box while open. Chips and
// picker consume rows exactly like questionModalH does, so the viewport
// shrinks for them instead of overlapping.
func (c *Chat) SetSize(w, h int) {
	if w < 4 {
		w = 4
	}
	c.w, c.h = w, h
	spH := 0
	if c.pendingSpin {
		spH = 1
	}
	regionH := c.inputRows()
	if c.question != nil {
		regionH = c.questionModalH()
	}
	regionH += c.chipsH() + c.popoverH() + c.slashH()
	vpH := h - regionH - 1 /* divider */ - spH
	if vpH < 1 {
		vpH = 1
	}
	c.vp.SetWidth(w)
	c.vp.SetHeight(vpH)
	c.ta.SetWidth(w)
	// cellWidth, not len: bossPrefix's "›" is 3 bytes but 1 cell — the
	// byte count would rob the wrap of 2 columns.
	c.mdWidth = w - cellWidth(bossPrefix) - 1
	if c.mdWidth < 10 {
		c.mdWidth = 10
	}
	c.md = nil // rebuilt lazily at the new wrap width
}

// cellWidth is the display-cell count of s (runes here — prefixes are all
// single-cell glyphs; lipgloss.Width on a styled/cursed string belongs to
// the ansi-aware foldStyledLines instead).
func cellWidth(s string) int { return len([]rune(s)) }

// SetState implements Tab: keeps the latest chat slice, re-renders the
// conversation when it changed, and keeps scroll pinned to the bottom.
// Also captures the roster rollup (workers-thread decoration) and the
// delegation state (P3 spinner-row swap) from the incoming state.
func (c *Chat) SetState(st state.OfficeState) {
	c.tick = st.Tick
	agents := make(map[string]agentView, len(st.Employees))
	busy := 0
	for _, e := range st.Employees {
		if e.Role == state.RoleManager {
			continue
		}
		av := agentView{task: e.Task, active: workerSpriteActive(e.Sprite)}
		agents[e.Name] = av
		if av.active {
			busy++
		}
	}
	c.agents = agents
	c.delegating = st.BossDelegating
	c.delegatingN = busy
	if c.workerTasks == nil {
		c.workerTasks = map[string]string{}
	}
	for _, e := range st.Employees {
		if e.Role != state.RoleManager && e.Task != "" {
			c.workerTasks[e.Name] = e.Task // sticky: survives EvReturned's task clear
		}
	}
	rev := revision(st.Chat)
	// fold the stream set into the revision (order-independent) so closing a
	// stream re-renders even when Done's final text equals the last update.
	srev := uint64(14695981039346656037)
	for id := range c.streamingThink {
		h := uint64(14695981039346656037)
		for i := 0; i < len(id); i++ {
			h ^= uint64(id[i])
			h *= 1099511628211
		}
		srev ^= h
	}
	rev ^= srev
	// fold the roster rollup + delegation flag in (order-independent) — a
	// dispatch filling a thread header's task or a delegating flip changes
	// pixels without touching the chat slice.
	arev := uint64(14695981039346656037)
	for name, a := range c.agents {
		h := uint64(14695981039346656037)
		for _, s := range []string{name, a.task} {
			for i := 0; i < len(s); i++ {
				h ^= uint64(s[i])
				h *= 1099511628211
			}
		}
		if a.active {
			h ^= 1
			h *= 1099511628211
		}
		arev ^= h
	}
	rev ^= arev
	if c.delegating {
		rev ^= 0x9e3779b97f4a7c15
	}
	rev ^= uint64(uint32(c.delegatingN))
	// a streaming boss bubble does NOT need per-tick re-renders: the old
	// blinking caret is gone — every pixel a pending bubble draws comes
	// straight from its text, and text changes always land in rev (when a
	// partial survives into a later tick its own diff re-triggers). Think
	// streams DO still animate per tick — see len(c.streamingThink) below.
	// a worker thread within its staleness horizon must re-render every
	// tick — the auto-collapse boundary is tick-relative, invisible to rev.
	wtoolRecent := false
	for _, m := range st.Chat {
		if m.Kind != wtoolKind && m.Kind != wthinkKind {
			continue
		}
		_, tk := parseWtoolMeta(m.Meta)
		if c.tick-tk <= wtoolStaleTicks+2 {
			wtoolRecent = true
			break
		}
	}
	if rev == c.renderRev && len(st.Chat) == len(c.chat) && len(c.streamingThink) == 0 && !wtoolRecent {
		return
	}
	c.renderRev = rev
	c.chat = cloneChat(st.Chat)

	wasPending := c.pending
	wasSpin := c.pendingSpin
	c.pending, c.pendingSpin = false, false
	for _, m := range c.chat {
		if m.From == "boss" && m.Pending {
			// the typing row runs for the WHOLE pending period — the
			// streaming bubble shows the text, the row shows the life
			c.pending, c.pendingSpin = true, true
		}
	}
	if c.pending != wasPending {
		// the textarea NEVER locks — typing while the boss works is the
		// whole point of the queue; only the placeholder text reacts
		c.refreshPlaceholder()
	}
	if c.pendingSpin != wasSpin {
		c.SetSize(c.w, c.h) // typing row appears/disappears
	}

	c.vp.SetContent(c.renderConversation())
	if c.follow {
		c.vp.GotoBottom()
	}
}

// Update implements Interactive: Enter sends, Shift+Enter newline, wheel +
// pgup/pgdn + (single-line) arrows scroll the conversation. Paste follows
// the macOS OS defaults: cmd+v is the user's gesture, and it arrives three
// ways — kitty-protocol terminals report it as a "super+v" key, every
// other terminal converts it to bracketed paste (tea.PasteMsg), and an
// image-only clipboard delivers NOTHING in Terminal.app/iTerm2 (the
// app-side image trigger is the only probe path there). ctrl+v and
// super+v both probe the clipboard for an image (attach as a chip on a
// hit, replay text paste on a miss). tea.PasteMsg gets its own smart arm:
// a Finder file copy pastes as (escaped) path text and stages chips, an
// empty/whitespace-only paste on darwin probes for an image (the miss
// re-feeds the original bytes), everything else inserts as plain text.
// "@" at a word start opens the attach-file popover, which owns
// ↑/↓/enter/tab/esc while open (the question-modal precedence pattern) —
// every other key still reaches the textarea. Non-key messages the panel
// doesn't claim (the textarea's own clipboard pasteMsgs) fall through the
// default arm INTO the textarea.
func (c *Chat) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// While a boss question modal is open, the normal textarea is
		// DISABLED entirely: typed text edits the modal's OWN input line,
		// enter submits the answer, esc defers. Scrolling still works.
		if c.question != nil {
			switch msg.String() {
			case "enter":
				text := strings.TrimSpace(c.qInput)
				if text == "" {
					return nil
				}
				c.follow = true
				c.vp.GotoBottom()
				if c.onQuestionAnswer != nil {
					return c.onQuestionAnswer(text)
				}
				return nil
			case "esc":
				if c.onQuestionLater != nil {
					return c.onQuestionLater()
				}
				return nil
			case "backspace":
				if r := []rune(c.qInput); len(r) > 0 {
					c.qInput = string(r[:len(r)-1])
				}
				return nil
			case "ctrl+u":
				c.qInput = ""
				return nil
			case "up", "down", "pgup", "pgdown":
				var cmd tea.Cmd
				c.vp, cmd = c.vp.Update(msg)
				if msg.String() == "down" || msg.String() == "pgdown" {
					if c.vp.AtBottom() {
						c.follow = true
					}
				} else {
					c.follow = false
				}
				return cmd
			default:
				if msg.Text != "" {
					c.qInput += msg.Text
				}
				return nil
			}
		}
		// While a permission prompt is open, y/a/n/esc are RESERVED (they
		// never reach the textarea); every other key keeps typing normally
		// — the prompt is non-modal to text.
		if c.perm != nil {
			switch msg.String() {
			case "y":
				if c.onPermAnswer != nil {
					return c.onPermAnswer("once")
				}
				return nil
			case "a":
				if c.onPermAnswer != nil {
					return c.onPermAnswer("always")
				}
				return nil
			case "n":
				if c.onPermAnswer != nil {
					return c.onPermAnswer("reject")
				}
				return nil
			case "esc":
				if c.onPermLater != nil {
					return c.onPermLater()
				}
				return nil
			}
		}
		// The @ popover owns its nav/attach keys while open (the question-
		// modal precedence pattern: claimed FIRST, nothing reaches the
		// textarea); every other key still goes to the textarea below.
		// The slash popover owns its nav/apply keys while open (claimed
		// first, like the modal/picker precedence above); every other key
		// still goes to the textarea below.
		if c.slashOpen {
			switch msg.String() {
			case "up":
				c.slashMove(-1) // theme mode: arrows PREVIEW live
				return nil
			case "down":
				c.slashMove(1)
				return nil
			case "enter", "tab":
				// a zero-match cmd-mode filter means the typed tail is a
				// command the popover does not KNOW (its menu is a curated
				// subset of applySlash's grammar): close the box and let
				// Enter fall through to the normal send path — sinking the
				// first Enter here would force a re-press.
				if msg.String() == "enter" && c.slashMode == slashModeCmd && c.slashCount() == 0 {
					c.closeSlashPicker(false)
					break
				}
				return c.slashPicked()
			case "esc":
				c.closeSlashPicker(true) // keeps the fragment; unwinds a preview
				return nil
			}
		}
		if c.atOpen {
			switch msg.String() {
			case "up":
				c.atMove(-1)
				return nil
			case "down":
				c.atMove(1)
				return nil
			case "enter", "tab":
				return c.attachPicked()
			case "esc":
				c.closeAttachPicker() // keeps the typed fragment
				return nil
			}
		}
		switch msg.String() {
		case "ctrl+v", "super+v":
			// Image paste probe (async tea.Cmd): a clipboard holding an
			// image ATTACHES it as a chip; a text/empty clipboard replays
			// the textarea's own text paste (see onClipPaste). Two names,
			// one gesture: "super+v" is cmd+v as reported by kitty-
			// keyboard-protocol terminals (Terminal.app/iTerm2 swallow
			// CMD entirely — cmd+v there arrives as tea.PasteMsg below,
			// or as nothing at all when the clipboard is image-only,
			// making ctrl+v the only trigger that still reaches us).
			c.closeSlashPicker(true) // pasting is not fragment typing
			return c.startImagePaste()
		case "backspace":
			if c.atOpen {
				// fragment editing: the picker lives/dies by the tail
				// recheck after the textarea eats the key
				var cmd tea.Cmd
				c.ta, cmd = c.ta.Update(msg)
				c.afterDraftEdit()
				return cmd
			}
			if c.slashOpen {
				// same tail-recheck contract — backspace over the lone "/"
				// breaks the match and closes the popover
				var cmd tea.Cmd
				c.ta, cmd = c.ta.Update(msg)
				c.afterSlashEdit()
				return cmd
			}
			// an EMPTY draft + staged chips: backspace pops the newest
			// attachment (the quiet undo for a mis-paste)
			if strings.TrimSpace(c.ta.Value()) == "" && len(c.atts) > 0 {
				return c.popAttachment()
			}
			var cmd tea.Cmd
			c.ta, cmd = c.ta.Update(msg)
			return cmd
		case "enter":
			text := strings.TrimSpace(c.ta.Value())
			if text == "" && len(c.atts) == 0 {
				return nil
			}
			c.ta.Reset()
			c.follow = true
			c.vp.GotoBottom()
			if strings.HasPrefix(text, "/") {
				// slash commands are local and always immediate — /queue
				// and /perm must work while the boss is typing. Chips are
				// NOT eaten: a stray /help must not sink a staged paste —
				// they belong to the next real prompt (/clear drops them).
				if c.onSend != nil {
					return c.onSend(text, nil)
				}
				return nil
			}
			atts := c.drainAttachments()
			// The client queue is ONLY for roadblocks: an outstanding
			// question hold (a plain Send would park the opencode loop at
			// the question reply API — the reported deadlock) or an open
			// permission modal. Everything else sends DIRECTLY — free-
			// queuing: while the boss is busy the prompt goes straight to
			// the backend, which queues it server-side and drains it after
			// the current turn (no hide, no client-side flush cadence).
			if (c.questionWaiting || c.perm != nil) && c.onEnqueue != nil {
				return c.onEnqueue(text, atts)
			}
			if c.pending && c.onBusySend != nil {
				return c.onBusySend(text, atts)
			}
			if c.onSend != nil {
				return c.onSend(text, atts)
			}
			return nil
		case "up", "down":
			if c.ta.LineCount() > 1 {
				var cmd tea.Cmd
				c.ta, cmd = c.ta.Update(msg)
				return cmd
			}
			fallthrough
		case "pgup", "pgdown":
			var cmd tea.Cmd
			c.vp, cmd = c.vp.Update(msg)
			sawBottom := c.vp.AtBottom()
			if msg.String() == "down" || msg.String() == "pgdown" {
				if sawBottom {
					c.follow = true
				}
			} else {
				c.follow = false
			}
			return cmd
		default:
			// "@" at a word boundary opens the attach picker AFTER the
			// textarea takes the rune (the fragment starts empty); "/" at a
			// word boundary opens the slash popover the same way. Other
			// typed keys re-derive the fragment from the draft tail; nav
			// keys (arrows, ctrl-word ops, home/end) close the popovers —
			// they move the cursor off the tail the popovers track.
			opening := !c.atOpen && msg.Text == "@" && c.atWordBoundary()
			slashOpening := !c.slashOpen && msg.Text == "/" && c.atWordBoundary()
			var cmd tea.Cmd
			if c.atOpen && msg.Text == "" {
				c.closeAttachPicker()
			}
			if c.slashOpen && msg.Text == "" {
				c.closeSlashPicker(true)
			}
			c.ta, cmd = c.ta.Update(msg)
			if opening {
				return tea.Batch(cmd, c.openAttachPicker())
			}
			if slashOpening {
				return tea.Batch(cmd, c.openSlashPicker())
			}
			if c.atOpen && msg.Text != "" {
				c.afterDraftEdit()
			}
			if c.slashOpen && msg.Text != "" {
				c.afterSlashEdit()
			}
			return cmd
		}
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		c.vp, cmd = c.vp.Update(msg)
		c.follow = c.vp.AtBottom()
		return cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		c.sp, cmd = c.sp.Update(msg)
		return cmd
	case clipPasteMsg:
		// the image probe answered (chat_attach.go) — fired by the
		// ctrl+v/super+v key trigger or the tea.PasteMsg reprobe below
		return c.onClipPaste(msg)
	case attachWalkMsg:
		// the @ picker's file walk answered
		c.onAttachWalk(msg)
		return nil
	case tea.PasteMsg:
		// Bracketed paste — on macOS this IS cmd+v (Terminal.app/iTerm2
		// swallow the CMD key and convert the paste themselves). Smart
		// classification, in order:
		if paths, ok := pasteFilePaths(msg.Content); ok {
			// 1) Finder file copy: the terminal delivered the copied
			//    file(s) as (escaped) path text — stage one chip per
			//    path, type nothing into the draft.
			return c.attachPastedFiles(paths)
		}
		if runtime.GOOS == "darwin" && strings.TrimSpace(msg.Content) == "" {
			// 2) Empty/whitespace-only paste on darwin: cmd+v with an
			//    image-only clipboard has no bytes to send — probe for
			//    image bytes; a miss re-feeds the ORIGINAL content into
			//    the textarea (see onClipPaste's reprobe arm).
			return c.startImagePasteReprobe(msg.Content)
		}
		// 3) plain text — the textarea's own bracketed-paste handling
		//    inserts it cursor-correctly.
		var cmd tea.Cmd
		c.ta, cmd = c.ta.Update(msg)
		return cmd
	default:
		// The textarea's own internal pasteMsg/pasteErrMsg (its ctrl+v
		// clipboard path) are NOT key presses — everything unclaimed
		// forwards to the textarea so plain-text paste reaches the
		// input. (This arm is the paste regression fix: before it, all
		// of the above were dropped here. tea.PasteMsg itself moved UP
		// into its own smart arm above — Finder copies and image
		// clipboards now beat plain insertion.)
		var cmd tea.Cmd
		c.ta, cmd = c.ta.Update(msg)
		return cmd
	}
}

// View implements Tab. Below the divider (top → bottom): the typing row
// (while a boss reply is pending — spinner + "… is typing…", or the dim
// delegating line), the dim attachment chips row (when files are staged),
// the @ picker box (while open), then the input region — the question
// modal, permission modal, or textarea. SetSize budgets exactly the rows
// this writes.
func (c *Chat) View() string {
	var b strings.Builder
	b.WriteString(c.vp.View())
	b.WriteString("\n")
	b.WriteString(chrome.DimText.Render(fitPlain(strings.Repeat("─", c.w), c.w)))
	if c.pendingSpin {
		// the typing row — glued to the input, not the transcript: the
		// viewport holds the words, this row holds the pulse
		b.WriteString("\n")
		if c.delegating {
			// P3 — the boss dispatched out and went quiet: a settled dim
			// delegation row, NO spinner blinking for minutes
			b.WriteString(chrome.DimText.Render(" " + c.delegatingText()))
		} else {
			b.WriteString(c.sp.View())
			b.WriteString(chrome.AccentText.Render(" " + c.typingText()))
		}
	}
	b.WriteString("\n")
	if chips := c.renderAttachChips(); chips != "" {
		// staged attachments sit right above the input they will attach to
		b.WriteString(chips)
		b.WriteString("\n")
	}
	if c.atOpen {
		b.WriteString(c.renderAttachPopover())
		b.WriteString("\n")
	}
	if c.slashOpen {
		b.WriteString(c.renderSlashPopover())
		b.WriteString("\n")
	}
	if c.question != nil {
		// the question modal REPLACES the textarea region, full width
		b.WriteString(c.renderQuestionModal())
	} else if c.perm != nil {
		// the permission modal REPLACES the textarea region, full width
		b.WriteString(c.renderPermission())
	} else {
		b.WriteString(c.ta.View())
	}
	return b.String()
}

// questionModalH — rows the open question modal occupies: header + dim
// options (when present) + 2 input rows (wrap budget) + footer hint.
func (c *Chat) questionModalH() int {
	n := 2 + 2 // header + footer hint + input budget
	if c.question != nil && c.question.Options != "" {
		n++
	}
	return n
}

// renderQuestionModal draws the boss question modal in the textarea region
// (questionModalH rows, full width): yellow bold "boss asks: <question>"
// header with a dim options list, then the free-text input line (2-row
// wrap budget — longer answers scroll to their tail), then the hint.
func (c *Chat) renderQuestionModal() string {
	lines := make([]string, 0, c.questionModalH())
	lines = append(lines, chrome.QuestionText.Bold(true).Render(
		fitPlain("boss asks: "+c.question.Text, c.w)))
	if c.question.Options != "" {
		lines = append(lines, chrome.DimText.Render(
			fitPlain("  "+c.question.Options, c.w)))
	}
	wrapped := strings.Split(strings.TrimRight(wrapPlain(c.qInput, c.w-2), "\n"), "\n")
	if len(wrapped) > 2 {
		// over budget: keep the tail visible (the caret end)
		wrapped = wrapped[len(wrapped)-2:]
	}
	for i := 0; i < 2; i++ {
		row := ""
		if i < len(wrapped) {
			row = wrapped[i]
		}
		prefix := chrome.Fg(chrome.Accent, "› ")
		if i == 1 {
			prefix = "  " // wrap continuation hangs under the prompt
			if row == "" {
				prefix = ""
			}
		}
		lines = append(lines, fitPlain(prefix+row, c.w))
	}
	lines = append(lines, chrome.DimText.Italic(true).Render(
		fitPlain("enter: answer · esc: answer later", c.w)))
	return strings.Join(lines, "\n")
}

// renderPermission draws the permission prompt modal in the textarea
// region (textareaH rows, full width): amber bold header with the tool
// request, then the key hint wrapped over the remaining rows.
func (c *Chat) renderPermission() string {
	head := "PERMISSION: boss wants " + c.perm.ToolName
	if c.perm.Summary != "" {
		head += " · " + c.perm.Summary
	}
	lines := [textareaH]string{}
	lines[0] = chrome.WarnBold.Render(fitPlain(head, c.w))
	hint := strings.Split(strings.TrimRight(wrapPlain(
		"[y] allow once  [a] always  [n] reject  [esc] later", c.w), "\n"), "\n")
	for i := 1; i < textareaH; i++ {
		if i-1 < len(hint) {
			lines[i] = chrome.DimText.Render(fitPlain(hint[i-1], c.w))
		} else {
			lines[i] = fitPlain("", c.w)
		}
	}
	return strings.Join(lines[:], "\n")
}

// renderConversation rebuilds the full glamour-rendered transcript.
// Employee tool entries (wtoolKind) never join the flow — they collect
// into per-agent worker groups rendered as the workers-thread region at
// the END of the conversation, so a sub-agent tool storm can't drown the
// boss turns.
func (c *Chat) renderConversation() string {
	visible := make([]state.ChatMsg, 0, len(c.chat))
	var workers []workerGroup
	workerIdx := map[string]int{}
	c.threadRows = map[int]string{} // mouse hit-map, rebuilt every render
	for _, m := range c.chat {
		if m.Kind == wtoolKind || m.Kind == wthinkKind {
			// /tools off hides the whole workers region; /thinking off
			// takes only the think rows (the tools keep rendering)
			if m.Kind == wtoolKind && !c.showTools {
				continue
			}
			if m.Kind == wthinkKind && !c.showThinking {
				continue
			}
			idx, ok := workerIdx[m.From]
			if !ok {
				idx = len(workers)
				workerIdx[m.From] = idx
				workers = append(workers, workerGroup{name: m.From})
			}
			workers[idx].lines = append(workers[idx].lines, m)
			if _, tk := parseWtoolMeta(m.Meta); tk > workers[idx].lastTick {
				workers[idx].lastTick = tk
			}
			continue
		}
		if m.From == "boss" && m.Pending && m.Text == "" {
			continue // the spinner line speaks for the EMPTY typing placeholder;
			// a pending bubble WITH text renders below (streaming reply)
		}
		if m.Kind == thinkKind && !c.showThinking {
			continue
		}
		if m.Kind == toolKind && !c.showTools {
			continue
		}
		visible = append(visible, m)
	}
	if len(visible) == 0 && len(workers) == 0 {
		return chrome.DimText.Render("  no messages yet — ask the boss for something.")
	}
	var b strings.Builder
	for i, m := range visible {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case m.Kind == thinkKind:
			c.renderThink(&b, m)
		case m.Kind == toolKind:
			// boss inline tool one-liner — WRAPPED, never clipped and
			// never burst mid-glyph by the vp soft-wrap: the first row
			// flows tight against the bubble above (no leading indent),
			// continuations hang under the tool text start.
			toolW := c.w - 1
			indent := strings.Repeat(" ", cellWidth(toolWrapPrefix))
			lines := foldStyledRows(renderTool(m), toolW, toolW-cellWidth(toolWrapPrefix))
			b.WriteString(lines[0])
			for _, ln := range lines[1:] {
				b.WriteString("\n" + indent + ln)
			}
		case m.Kind == questionKind:
			c.renderQuestion(&b, m)
		case m.Kind == diffKind:
			c.renderDiff(&b, m)
		case m.Kind == officeKind:
			// concierge (EvChatOffice) — a real turn, not a notice: the
			// INFO "office ›" case above renderNotice's dim-office line
			c.renderOffice(&b, m)
		case m.From == officeFrom:
			c.renderNotice(&b, m)
		case m.From == "user":
			prefix := chrome.Fg(chrome.Info, userPrefix)
			lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
			if names, ok := state.ParseAttachMeta(m.Meta); ok && len(names) > 0 {
				// the backend's chat-user echo carries the attachment
				// names in Meta — history shows the dim " · 📎 N" suffix
				lines[len(lines)-1] += chrome.DimText.Render(" · 📎 " + itoa(len(names)))
			}
			lines = foldStyledLines(lines, c.mdWidth+1)
			writePrefixed(&b, prefix, strings.Repeat(" ", cellWidth(userPrefix)), lines)
		default:
			prefix := chrome.Fg(chrome.Accent, bossPrefix)
			lines := cleanMarkdown(c.renderMarkdown(m.Text))
			// a streaming reply is just the bubble itself growing — no
			// caret, no extra row: the typing row below the divider is the
			// liveness signal for the whole pending period.
			lines = foldStyledLines(lines, c.mdWidth)
			writePrefixed(&b, prefix, strings.Repeat(" ", cellWidth(bossPrefix)), lines)
		}
	}
	c.renderWorkers(&b, workers)
	return strings.TrimRight(b.String(), "\n")
}

// agentView — a roster rollup entry for the workers-thread decoration:
// the agent's dispatch task (header) and sprite-liveness (collapse rule).
type agentView struct {
	task   string
	active bool
}

// workerGroup — one agent's merged wtool entries (chat order) plus the
// latest activity tick, rendered as one work thread.
type workerGroup struct {
	name     string
	lines    []state.ChatMsg
	lastTick int
}

// workerSpriteActive — the sprite states that count as "the agent is still
// at work" for the thread's live/expanded decision.
func workerSpriteActive(s state.SpriteState) bool {
	switch s {
	case state.SpriteWorking, state.SpriteToManager, state.SpriteMeeting:
		return true
	}
	return false
}

// parseWtoolMeta decodes the employee-tool Meta carrier the reducer
// writes: toolState ␟ tick. A Meta without the separator yields tick 0
// (immediately stale → collapsed).
func parseWtoolMeta(meta string) (toolState string, tick int) {
	if i := strings.IndexByte(meta, diffMetaSep[0]); i >= 0 {
		return meta[:i], atoiHead(meta[i+1:])
	}
	return meta, 0
}

// workerToolLine renders one merged employee tool entry, same shape as
// the boss's inline one-liner ("[tool] read · x ✓/✗/✗ aborted/… running").
func workerToolLine(m state.ChatMsg) string {
	toolState, _ := parseWtoolMeta(m.Meta)
	line := "[tool] " + m.Text
	switch toolState {
	case "done":
		return line + " ✓"
	case "error":
		return line + " ✗"
	case "aborted": // /stop unwind swung a running call here
		return line + " ✗ aborted"
	default: // running (or anything unexpected)
		return line + " … running"
	}
}

// renderWorkers draws the workers-thread region after the conversation:
// one group per agent, newest at the bottom. An ACTIVE agent (busy sprite
// + activity within wtoolStaleTicks) renders its header and merged tool
// lines — employee thoughts join the SAME thread as dim-italic
// "thinking · N lines" rows (one per merged CallID), in natural chat
// order among the tool rows. On EvReturned/the quiet horizon the thread
// auto-collapses to one dim summary line that KEEPS the think count
// ("· 9 tools · 3 think ✓ done"). ctrl+g re-expands every completed
// thread at once — and a full expand (ctrl+g or a per-agent mouse
// override) covers both tools AND thoughts, the thoughts with their
// body text. Expanded "┌" header rows are recorded in threadRows for
// the mouse hit-map (click toggles that agent's thread).
func (c *Chat) renderWorkers(b *strings.Builder, groups []workerGroup) {
	for _, g := range groups {
		task := c.workerTasks[g.name] // sticky: a returned agent keeps its task
		active := false
		if av, ok := c.agents[g.name]; ok {
			active = av.active
			if av.task != "" {
				task = av.task
			}
		}
		// expanded = whether the thread renders its rows at all; full =
		// whether think entries show their BODIES (ctrl+g / a full mouse
		// expand). A set per-agent override wins OUTRIGHT over the
		// live/ctrl+g default (a mouse-collapsed live thread stays
		// collapsed until re-clicked). A /stop stopped thread
		// force-collapses — only an explicit per-agent gesture re-opens it.
		stopped := c.threadStop[g.name]
		expanded := (active && c.tick-g.lastTick <= wtoolStaleTicks) || c.threadsExpanded
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
		head := g.name
		if task != "" {
			head += " · " + task
		}
		if expanded {
			if c.threadRows != nil {
				// the header row under construction is the NEXT line of b
				c.threadRows[strings.Count(b.String(), "\n")+1] = g.name
			}
			// long threads WRAP with aligned continuations — never
			// truncate a path the user might need to read
			for j, ln := range foldStyledRows("┌ "+head, c.w, c.w-2) {
				if j == 0 {
					b.WriteString("\n" + chrome.DimText.Render(ln))
				} else {
					b.WriteString("\n" + chrome.DimText.Render("  "+ln))
				}
			}
			for _, m := range g.lines {
				if m.Kind == wthinkKind {
					c.renderWThink(b, m, full)
					continue
				}
				for j, ln := range foldStyledRows("│ "+workerToolLine(m), c.w, c.w-2) {
					if j == 0 {
						b.WriteString("\n" + chrome.ToolStyle.Render(ln))
					} else {
						b.WriteString("\n" + chrome.ToolStyle.Render("│ "+ln))
					}
				}
			}
			continue
		}
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
		summary := head + " (· " + itoa(tools) + " " + unit
		if thinks > 0 {
			// the collapsed summary KEEPS the think count ("· 3 think");
			// a summary without thoughts renders byte-identical to before
			summary += " · " + itoa(thinks) + " think"
		}
		summaryStyle := chrome.DimText
		if stopped {
			// /stop unwind: the thread collapsed because the sessions were
			// aborted, not because the work returned
			summary += " ✗ stopped)"
			summaryStyle = chrome.ErrText.Faint(true)
		} else {
			summary += " ✓ done)"
		}
		if c.threadRows != nil {
			// the collapsed summary IS the thread's header — clickable too
			// (click a collapsed thread to re-expand it)
			c.threadRows[strings.Count(b.String(), "\n")+1] = g.name
		}
		for j, ln := range foldStyledRows(summary, c.w, c.w-2) {
			if j == 0 {
				b.WriteString("\n" + summaryStyle.Render(ln))
			} else {
				b.WriteString("\n" + summaryStyle.Render("  "+ln))
			}
		}
	}
}

// renderWThink renders one merged employee thinking entry inside a work
// thread. In the LIVE view (thread expanded because the agent is working)
// it is a single dim-italic "│ thinking · N lines" row; on a FULL expand
// (ctrl+g / per-agent mouse override) the body renders too — the same
// collapsed-vs-expanded shape as the boss's thinking blocks, capped to
// the stream tail (the freshest thinkStreamLines lines).
func (c *Chat) renderWThink(b *strings.Builder, m state.ChatMsg, full bool) {
	think := chrome.DimText.Italic(true)
	// fold at the FULL body budget ("│   " is 4 cells) so no row clips
	lines := foldStyledRows(m.Text, c.w-4, c.w-4)
	if !full {
		b.WriteString("\n" + think.Render(clipPlain("│ thinking · "+countLines(lines)+" lines", c.w)))
		return
	}
	b.WriteString("\n" + think.Render(clipPlain("│ thinking", c.w)))
	shown := lines
	if more := len(lines) - thinkStreamLines; more > 0 {
		b.WriteString("\n" + think.Render(clipPlain("│   … "+itoa(more)+" more above", c.w)))
		shown = lines[more:]
	}
	for _, ln := range shown {
		b.WriteString("\n" + chrome.DimText.Render("│   "+ln))
	}
}

// thinkStreamLines caps the visible body of a STREAMING think block —
// the tail of the accumulated text (the freshest lines) stays visible.
const thinkStreamLines = 10

// thinkFrames cycles the streaming header's spinner char, one step per
// office tick (no extra timer — render consults c.tick).
var thinkFrames = []string{"|", "/", "-", "\\"}

// renderThink renders one Kind="think" entry in one of three shapes:
//
//	STREAMING  — dim-italic "⠿ thinking…" spinner header + the accumulated
//	             text dim-italic, LAST thinkStreamLines lines max with a
//	             "… N more above" first line; always expanded (ctrl+t
//	             cannot collapse a live transcript).
//	collapsed  — one dim "thinking · N lines" line (the default once the
//	             stream's Done update lands / a new boss turn starts).
//	expanded   — dim-italic "thinking" header + greyed body (ctrl+t).
func (c *Chat) renderThink(b *strings.Builder, m state.ChatMsg) {
	think := chrome.DimText.Italic(true)
	body := chrome.DimText
	lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
	if m.Meta != "" && c.streamingThink[m.Meta] {
		frame := thinkFrames[c.tick%len(thinkFrames)]
		if frame < "" {
			frame = "|"
		}
		b.WriteString(think.Render(frame + " thinking…"))
		shown := lines
		if more := len(lines) - thinkStreamLines; more > 0 {
			b.WriteString("\n  ")
			b.WriteString(think.Render("… " + itoa(more) + " more above"))
			shown = lines[more:]
		}
		for _, ln := range shown {
			b.WriteString("\n  ")
			b.WriteString(think.Render(ln))
		}
		return
	}
	if !c.thinkExpanded {
		b.WriteString(think.Render("thinking · ") + body.Render(countLines(lines)+" lines"))
		return
	}
	b.WriteString(think.Render("thinking"))
	for _, ln := range lines {
		b.WriteString("\n  ")
		b.WriteString(body.Render(ln))
	}
}

// countLines is the display count for a collapsed thinking block.
func countLines(lines []string) string {
	n := len(lines)
	if n > 1 {
		return itoa(n)
	}
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return "0"
	}
	return "1"
}

// itoa avoids fmt for a digit-only render.
func itoa(n int) string {
	if n >= 10 {
		return itoa(n/10) + string(rune('0'+n%10))
	}
	return string(rune('0' + n%10))
}

// toolWrapPrefix starts every boss inline tool one-liner — continuation
// rows of a wrapped line hang exactly this many cells in (under the text).
const toolWrapPrefix = "[tool] "

// renderTool renders one Kind="tool" one-liner, merged by CallID upstream:
// "[tool] read · src/main.go ✓" (done) / "… running" / red "✗" (error) /
// dim-red "✗ aborted" (/stop). The whole line comes out as ONE styled
// blob; the call site folds it with foldStyledRows — escape sequences are
// consumed atomically there, so a fold boundary never shreds one.
func renderTool(m state.ChatMsg) string {
	line := toolWrapPrefix + m.Text
	switch m.Meta {
	case "done":
		return chrome.ToolStyle.Render(line + " ✓")
	case "error":
		return chrome.ErrText.Faint(true).Render(line + " ✗")
	case "aborted": // /stop unwind swung a running call here
		return chrome.ErrText.Faint(true).Render(line + " ✗ aborted")
	default: // running (or anything unexpected)
		return chrome.ToolStyle.Render(line + " … running")
	}
}

// renderQuestion renders one Kind="question" entry (boss question tool):
// yellow "boss asks › <text>", dim options inline when present, then the
// "(answer by typing below)" hint while pending — or a dim "✓ answered"
// suffix once the resolved event landed (the app reducer marks Meta with
// a trailing ␟answered unit-separator token, keeping the options intact).
func (c *Chat) renderQuestion(b *strings.Builder, m state.ChatMsg) {
	options := m.Meta
	answered := false
	if parts := strings.Split(m.Meta, diffMetaSep); len(parts) == 2 && parts[1] == "answered" {
		options, answered = parts[0], true
	}
	qPrefix := "boss asks › "
	indent := strings.Repeat(" ", cellWidth(qPrefix))
	// cellWidth, not len — "›" is 3 bytes but 1 cell; the byte count would
	// shave 2 columns off the wrap budget and misalign the hanging indent
	wrapW := c.w - cellWidth(qPrefix) - 1 // prefix + panel padding
	lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, wrapW), "\n"), "\n")
	for i := range lines {
		lines[i] = chrome.QuestionText.Render(lines[i])
	}
	lines = foldStyledLines(lines, wrapW)
	writePrefixed(b, chrome.QuestionText.Bold(true).Render(qPrefix), indent, lines)
	if options != "" {
		optLines := strings.Split(strings.TrimRight(wrapPlain("("+options+")", wrapW), "\n"), "\n")
		for i := range optLines {
			optLines[i] = chrome.DimText.Render(optLines[i])
		}
		for _, ln := range foldStyledLines(optLines, wrapW) {
			b.WriteString(indent + ln + "\n")
		}
	}
	if answered {
		b.WriteString(indent + chrome.DimText.Render("✓ answered") + "\n")
	} else {
		b.WriteString(indent + chrome.DimText.Italic(true).Render("(answer by typing below)"))
	}
}

// renderDiff renders one Kind="diff" entry opencode-style. Collapsed
// (default): a single "diff · path +A -D" line with green/red counts.
// Expanded (ctrl+d / /diffs on): a dim-bold "← Edit|New file|Delete <path>"
// header over LINE-NUMBERED unified rows — deletion rows tinted DiffDelBg
// (dark red), addition rows tinted DiffAddBg (dark green) to the FULL panel
// width, context rows dim with no tint, @@ hunk headers dim italic with no
// gutter number. The +/- marker sits inside the tinted row. Text inside the
// rows is syntax-coloured through chroma (theme-mapped style) on top of the
// tint when a lexer matches the file. Body is clipped to diffClip rows with
// a "+N more" trailer.
func (c *Chat) renderDiff(b *strings.Builder, m state.ChatMsg) {
	path, adds, dels := parseDiffMeta(m.Meta)
	if !c.diffExpanded {
		header := chrome.DimText.Render("diff · " + path)
		if adds != "" {
			header += " " + chrome.OKText.Render(adds)
		}
		if dels != "" {
			header += " " + chrome.ErrText.Render(dels)
		}
		b.WriteString(header)
		return
	}
	rows, op := c.diffRows(m, path)
	opWord := "Edit"
	switch op {
	case diffOpNew:
		opWord = "New file"
	case diffOpDel:
		opWord = "Delete"
	}
	b.WriteString(clipStyled(chrome.DimText.Bold(true), "← "+opWord+" "+path, c.w))

	maxNum := 0
	for _, r := range rows {
		if r.num > maxNum {
			maxNum = r.num
		}
	}
	gutterW := 5
	if n := len(itoa(maxNum)); n > gutterW {
		gutterW = n
	}
	shown := rows
	more := 0
	if len(rows) > diffClip {
		shown = rows[:diffClip]
		more = len(rows) - diffClip
	}
	for i := range shown {
		b.WriteString("\n")
		b.WriteString(renderDiffRow(shown[i], gutterW, c.w))
	}
	if more > 0 {
		b.WriteString("\n" + strings.Repeat(" ", gutterW+3) +
			chrome.DimText.Italic(true).Render("+"+itoa(more)+" more"))
	}
}

// parseDiffMeta decodes the diff Meta carrier (path ␟ +adds ␟ -dels)
// written by the app reducer.
func parseDiffMeta(meta string) (path, adds, dels string) {
	parts := strings.Split(meta, diffMetaSep)
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return meta, "", ""
}

// --- opencode-style diff rows -------------------------------------------------

type diffOp int

const (
	diffOpEdit diffOp = iota // --- a/path +++ b/path
	diffOpNew                // --- /dev/null (file created)
	diffOpDel                // +++ /dev/null (file removed)
)

type diffRowKind int

const (
	dkContext diffRowKind = iota
	dkAdd
	dkDel
	dkHunk // @@ header or "\ No newline…" note — no gutter number
)

// diffSpan is one styled text segment inside a diff row. fg is "" to
// inherit the row ink, else "#rrggbb" from the chroma style.
type diffSpan struct {
	text                    string
	fg                      string
	bold, italic, underline bool
}

// diffRow — one display row of a parsed unified diff. num is the gutter
// line number (old-side for deletions+context, new-side for additions; 0
// for hunk rows). oldLine/newLine index the row's text inside the old-side
// and new-side source streams (-1 when absent). spans covers the text
// portion AFTER the +/- marker.
type diffRow struct {
	kind             diffRowKind
	num              int
	oldLine, newLine int
	spans            []diffSpan
}

// diffCacheEntry — parsed+highlighted diff rows keyed by chat msg ID;
// syntax colours are theme-bound so RefreshTheme clears the map.
type diffCacheEntry struct {
	theme string
	rows  []diffRow
	op    diffOp
}

// diffRows parses m.Text (unified diff body) into rows and paints chroma
// spans from the matching lexer; results are cached per msg ID + theme.
func (c *Chat) diffRows(m state.ChatMsg, path string) ([]diffRow, diffOp) {
	if ent, ok := c.diffCache[m.ID]; ok && ent.theme == chrome.CurrentTheme().Name {
		return ent.rows, ent.op
	}
	rows, op, oldBody, newBody := parseDiffBody(m.Text)
	lx := chlexers.Match(path)
	if lx != nil && lx.Config() != nil && lx.Config().Name == "fallback" {
		lx = nil
	}
	st := chstyles.Get(chrome.DiffChromaStyle)
	oldSpans := tokenizeSide(lx, st, oldBody)
	newSpans := tokenizeSide(lx, st, newBody)
	for i := range rows {
		if rows[i].kind == dkHunk {
			continue
		}
		line := rows[i].oldLine
		spans := oldSpans
		if rows[i].kind == dkAdd || (line < 0 && rows[i].newLine >= 0) {
			line, spans = rows[i].newLine, newSpans
		}
		// additions read the NEW-side stream, deletions the OLD-side stream;
		// context prefers new (falls back to old for pure-deletion files)
		if rows[i].kind == dkContext && rows[i].newLine >= 0 {
			line, spans = rows[i].newLine, newSpans
		}
		if line >= 0 && line < len(spans) {
			rows[i].spans = spans[line]
		}
	}
	if c.diffCache == nil {
		c.diffCache = map[string]diffCacheEntry{}
	}
	c.diffCache[m.ID] = diffCacheEntry{theme: chrome.CurrentTheme().Name, rows: rows, op: op}
	return rows, op
}

// parseDiffBody decodes a unified diff body into display rows and returns
// the old-side (context+deletions) and new-side (context+additions) source
// texts for chroma; each row records its line index in both streams.
func parseDiffBody(body string) (rows []diffRow, op diffOp, oldBody, newBody string) {
	op = diffOpEdit
	var oldLines, newLines []string
	body = strings.ReplaceAll(body, "\t", "    ") // vp soft-wrap expands tabs
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	oldN, newN := 1, 1
	seenHunk := false
	for _, ln := range lines {
		switch {
		case !seenHunk && strings.HasPrefix(ln, "--- "):
			if strings.TrimSpace(strings.TrimPrefix(ln, "--- ")) == "/dev/null" {
				op = diffOpNew
			}
		case !seenHunk && strings.HasPrefix(ln, "+++ "):
			if strings.TrimSpace(strings.TrimPrefix(ln, "+++ ")) == "/dev/null" {
				op = diffOpDel
			}
		case strings.HasPrefix(ln, "@@"):
			seenHunk = true
			oldN, newN = parseHunkHeader(ln)
			rows = append(rows, diffRow{kind: dkHunk, oldLine: -1, newLine: -1,
				spans: []diffSpan{{text: ln}}})
		case strings.HasPrefix(ln, "\\"):
			// "\ No newline at end of file" — a note, not source text
			rows = append(rows, diffRow{kind: dkHunk, oldLine: -1, newLine: -1,
				spans: []diffSpan{{text: ln}}})
		case strings.HasPrefix(ln, "-"):
			rows = append(rows, diffRow{kind: dkDel, num: oldN,
				oldLine: len(oldLines), newLine: -1})
			oldLines = append(oldLines, ln[1:])
			oldN++
		case strings.HasPrefix(ln, "+"):
			rows = append(rows, diffRow{kind: dkAdd, num: newN,
				oldLine: -1, newLine: len(newLines)})
			newLines = append(newLines, ln[1:])
			newN++
		default: // context: " text" or a bare empty line
			text := ln
			if strings.HasPrefix(ln, " ") {
				text = ln[1:]
			}
			// context rows carry the OLD gutter number and exist in BOTH
			// side streams.
			rows = append(rows, diffRow{kind: dkContext, num: oldN,
				oldLine: len(oldLines), newLine: len(newLines)})
			oldLines = append(oldLines, text)
			newLines = append(newLines, text)
			oldN++
			newN++
		}
	}
	return rows, op, strings.Join(oldLines, "\n"), strings.Join(newLines, "\n")
}

// parseHunkHeader extracts the -o[,l] +n[,m] counters from an @@ header.
func parseHunkHeader(ln string) (oldN, newN int) {
	oldN, newN = 1, 1
	for _, f := range strings.Fields(strings.TrimPrefix(ln, "@@")) {
		if strings.HasPrefix(f, "-") {
			oldN = atoiHead(f[1:])
		} else if strings.HasPrefix(f, "+") {
			newN = atoiHead(f[1:])
		}
	}
	return
}

// atoiHead parses the leading digits of s ("52,11" → 52). 0 on garbage.
func atoiHead(s string) int {
	n := 0
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// tokenizeSide splits `text` into per-line chroma spans using the theme's
// chroma style. fg of every span maps to a lipgloss hex colour or "" (the
// row ink wins); backgrounds from chroma are DROPPED so the row tint stays
// uniform. On any failure (nil lexer, tokenise error) each line is one
// plain span.
func tokenizeSide(lx chroma.Lexer, st *chroma.Style, text string) [][]diffSpan {
	plain := func() [][]diffSpan {
		ls := strings.Split(text, "\n")
		out := make([][]diffSpan, len(ls))
		for i := range ls {
			out[i] = []diffSpan{{text: ls[i]}}
		}
		return out
	}
	if text == "" || lx == nil || st == nil {
		return plain()
	}
	it, err := lx.Tokenise(nil, text)
	if err != nil {
		return plain()
	}
	lines := [][]diffSpan{{}}
	for _, tok := range it.Tokens() {
		entry := st.Get(tok.Type)
		sp := diffSpan{fg: ""}
		if entry.Colour.IsSet() {
			sp.fg = entry.Colour.String()
		}
		sp.bold = entry.Bold == chroma.Yes
		sp.italic = entry.Italic == chroma.Yes
		sp.underline = entry.Underline == chroma.Yes
		parts := strings.Split(tok.Value, "\n")
		for i, p := range parts {
			if i > 0 {
				lines = append(lines, []diffSpan{})
			}
			if p == "" {
				continue
			}
			s := sp
			s.text = p
			lines[len(lines)-1] = append(lines[len(lines)-1], s)
		}
	}
	return lines
}

// renderDiffRow renders one parsed row opencode-style: dim right-aligned
// gutter number, +/- marker INSIDE the row, text clipped to the panel
// width and the whole row padded out with the tint background so it reads
// as a full-width bar. When the theme's bg slot is nil (mono), the tint is
// suppressed and the ink emphasises instead (bold adds / underline dels).
func renderDiffRow(row diffRow, gutterW, width int) string {
	if width < 1 {
		width = 1
	}
	if row.kind == dkHunk {
		textW := width - gutterW - 3
		if textW < 1 {
			textW = 1
		}
		return strings.Repeat(" ", gutterW+3) +
			chrome.DimText.Italic(true).Render(clipPlain(row.spans[0].text, textW))
	}
	var fg color.Color = chrome.DiffCtxFg
	var bg color.Color
	switch row.kind {
	case dkAdd:
		fg, bg = chrome.DiffAddFg, chrome.DiffAddBg
	case dkDel:
		fg, bg = chrome.DiffDelFg, chrome.DiffDelBg
	}
	base := lipgloss.NewStyle().Foreground(fg)
	if bg != nil {
		base = base.Background(bg)
	} else {
		// tint suppressed (mono): bold additions / underlined deletions
		switch row.kind {
		case dkAdd:
			base = base.Bold(true)
		case dkDel:
			base = base.Underline(true)
		}
	}
	gstyle := lipgloss.NewStyle().Foreground(chrome.DiffGutterFg)
	if bg != nil {
		gstyle = gstyle.Background(bg)
	}
	gut := itoa(row.num)
	for len(gut) < gutterW {
		gut = " " + gut
	}
	marker := " "
	switch row.kind {
	case dkAdd:
		marker = "+"
	case dkDel:
		marker = "-"
	}
	var sb strings.Builder
	sb.WriteString(gstyle.Render(gut))
	sb.WriteString(base.Render(" " + marker + " "))
	textW := width - gutterW - 3 // gutter + " " + marker + " "
	if textW < 1 {
		textW = 1
	}
	used := 0
	for _, sp := range clipSpans(row.spans, textW) {
		st := base
		if sp.fg != "" {
			st = st.Foreground(lipgloss.Color(sp.fg))
		}
		if sp.bold {
			st = st.Bold(true)
		}
		if sp.italic {
			st = st.Italic(true)
		}
		if sp.underline {
			st = st.Underline(true)
		}
		sb.WriteString(st.Render(sp.text))
		used += lipgloss.Width(sp.text)
	}
	if bg != nil && used < textW {
		sb.WriteString(base.Render(strings.Repeat(" ", textW-used)))
	}
	return sb.String()
}

// clipSpans truncates a span run to w display cells, splitting spans at the
// boundary. (spans are plain text — no ANSI.)
func clipSpans(spans []diffSpan, w int) []diffSpan {
	if w < 0 {
		w = 0
	}
	var out []diffSpan
	used := 0
	for _, sp := range spans {
		remain := w - used
		if remain <= 0 {
			break
		}
		sw := lipgloss.Width(sp.text)
		if sw <= remain {
			out = append(out, sp)
			used += sw
			continue
		}
		s := sp
		s.text = clipPlain(sp.text, remain)
		if s.text != "" {
			out = append(out, s)
		}
		break
	}
	return out
}

// clipStyled clips plain text to w cells then renders it with style.
func clipStyled(style lipgloss.Style, s string, w int) string {
	return style.Render(clipPlain(s, w))
}

// renderOffice renders one Kind="office" concierge bubble: an INFO-colored
// (cyan) "office ›" label over glamour-rendered markdown — visually a
// peer of the boss's yellow turn, but its own voice. A pending entry with
// no text yet draws the dim "office is answering…" placeholder; a pending
// entry WITH text streams in place (glamour re-rendered per accumulated
// delta, the completion pin settling it — the same replace-by-ID contract
// as the boss stream, and like the boss stream there is NO caret: the
// agents roster's "answering" word carries the liveness).
func (c *Chat) renderOffice(b *strings.Builder, m state.ChatMsg) {
	prefix := chrome.Fg(chrome.Info, officePrefix)
	indent := strings.Repeat(" ", cellWidth(officePrefix))
	if m.Pending && m.Text == "" {
		b.WriteString(prefix)
		b.WriteString(chrome.DimText.Render("office is answering…"))
		b.WriteString("\n")
		return
	}
	lines := cleanMarkdown(c.renderMarkdown(m.Text))
	lines = foldStyledLines(lines, c.mdWidth)
	writePrefixed(b, prefix, indent, lines)
}

// renderNotice renders a local From="office" notice (slash-command output):
// dim by default, red when Meta == "error".
func (c *Chat) renderNotice(b *strings.Builder, m state.ChatMsg) {
	style := chrome.DimText
	if m.Meta == errMeta {
		style = chrome.ErrText
	}
	lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
	for i := range lines {
		lines[i] = style.Render(lines[i])
	}
	lines = foldStyledLines(lines, c.mdWidth+1)
	writePrefixed(b, style.Render(officePrefix), strings.Repeat(" ", cellWidth(officePrefix)), lines)
}

// renderMarkdown runs a boss turn through glamour with the sidebar wrap and
// the active theme's glamour style (chrome.MarkdownStyle).
func (c *Chat) renderMarkdown(text string) string {
	if c.md == nil {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(chrome.MarkdownStyle()),
			glamour.WithWordWrap(c.mdWidth),
		)
		if err != nil {
			return text
		}
		c.md = r
	}
	out, err := c.md.Render(text)
	if err != nil {
		return text
	}
	return out
}

// cleanMarkdown trims glamour's frame noise: right-trailing styled spaces on
// every line and trailing lines with no printable text.
func cleanMarkdown(out string) []string {
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " ")
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// writePrefixed writes lines, prefixing the first with `prefix` and hanging
// the rest under it with `indent`.
func writePrefixed(b *strings.Builder, prefix, indent string, lines []string) {
	for i, ln := range lines {
		if i == 0 {
			b.WriteString(prefix + ln)
		} else {
			b.WriteString(indent + ln)
		}
		b.WriteString("\n")
	}
}

// wrapPlain greedy word-wraps text to w cells. No semantics — just a
// visual fold (user turns, question/options rows, think bodies, modals).
// ANSI-aware (foldWrap inside): styled text is safe — escapes are
// consumed atomically and never counted against the width, and a token
// that alone busts the row hard-splits at the cell edge (never overflow,
// never clip: every glyph survives on some row).
func wrapPlain(s string, w int) string {
	return foldWrap(s, w, w)
}

// foldStyledLines hard-folds every line of an ALREADY-SHAPED block (one
// glamour-rendered boss turn, post cleanMarkdown) to w display cells,
// ANSI-aware. Glamour's own word wrap misses code fences and unbreakable
// URLs — without this pass those rows overflow the bubble, and the
// viewport's soft wrap would burst them mid-word/mid-glyph with no
// hanging indent. Word boundaries are kept where they exist; unbreakable
// tokens hard-split at the cell edge. Lines that fit pass through
// VERBATIM (fence indentation survives).
func foldStyledLines(lines []string, w int) []string {
	var out []string
	for _, ln := range lines {
		out = append(out, foldStyledRows(ln, w, w)...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// foldStyledRows folds ONE visual line to firstW cells for the first row
// and contW for every continuation row — write the first row after the
// bubble/hanging prefix and continuations under a matching indent so
// prefix + row cells stay inside the panel. ANSI-aware; each returned row
// is a clean single row (no newlines).
func foldStyledRows(line string, firstW, contW int) []string {
	rows := strings.Split(foldWrap(line, firstW, contW), "\n")
	if len(rows) == 0 {
		return []string{""}
	}
	return rows
}

// foldWrap greedy word-wraps one visual line (or '\n'-joined paragraphs)
// to firstW cells, continuation rows to contW. ANSI escape sequences cost
// 0 cells and are copied through atomically (see seqLen); wide glyphs are
// measured and split at grapheme-cluster boundaries. A paragraph that
// fits is kept VERBATIM (code-fence indentation survives the pass).
func foldWrap(s string, firstW, contW int) string {
	if firstW < 1 {
		firstW = 1
	}
	if contW < 1 {
		contW = 1
	}
	width := firstW
	var out []string
	var cur strings.Builder
	curW := 0
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
		curW = 0
		width = contW
	}
	for pi, para := range strings.Split(s, "\n") {
		if pi > 0 {
			flush() // an explicit newline always owns a row boundary
		}
		if pw := ansi.StringWidth(para); pw <= width {
			cur.WriteString(para)
			curW = pw
			continue
		}
		for _, word := range splitStyledWords(para) {
			ww := ansi.StringWidth(word)
			if curW > 0 {
				if curW+1+ww <= width {
					cur.WriteString(" " + word)
					curW += 1 + ww
					continue
				}
				flush()
			}
			// the word heads a row; an unbreakable token that busts the
			// budget on its own hard-splits at the cell edge
			for ww > width {
				head, rest := cutStyled(word, width)
				if head == "" {
					break // unsplittable glyph wider than the row: overflow it whole, never clip
				}
				out = append(out, head)
				width = contW
				word = rest
				ww = ansi.StringWidth(word)
			}
			cur.WriteString(word)
			curW = ww
		}
	}
	flush()
	return strings.Join(out, "\n")
}

// splitStyledWords cuts a styled line into whitespace-separated words;
// escape sequences ride with the word they style (they split atomically
// via seqLen, so a fold never shreds one). Whitespace itself is always a
// separator — fold boundaries own the spacing.
func splitStyledWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); {
		if n := seqLen(s, i); n > 0 {
			cur.WriteString(s[i : i+n])
			i += n
			continue
		}
		if s[i] == ' ' || s[i] == '\t' {
			flush()
			i++
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	flush()
	return words
}

// cutStyled takes the longest HEAD of word that fits budget display cells
// (escape sequences always ride along, 0 cells; graphemes stay whole —
// ansi.FirstGraphemeCluster measures them). Returns ("", word) when the
// FIRST printable glyph alone busts the budget: there is nothing to cut,
// and the caller must overflow the glyph rather than destroy it.
func cutStyled(word string, budget int) (head, rest string) {
	i, cells := 0, 0
	for i < len(word) {
		if n := seqLen(word, i); n > 0 {
			i += n
			continue
		}
		cl, w := ansi.FirstGraphemeCluster(word[i:], ansi.GraphemeWidth)
		if cells+w > budget {
			if cells == 0 {
				return "", word
			}
			return word[:i], word[i:]
		}
		cells += w
		i += len(cl)
	}
	return word, ""
}

// seqLen returns the byte length of the escape sequence starting at s[i]
// when s[i] is ESC/CSI, 0 otherwise — CSI (ESC [ / 0x9b), OSC (ESC ],
// BEL/ST-terminated), and plain two-byte ESC·final forms. Our rendered
// content is lipgloss SGR, but the splitter must not trip over a
// pasted-content sequence either.
func seqLen(s string, i int) int {
	if s[i] != 0x1b && s[i] != 0x9b {
		return 0
	}
	j := i + 1
	if j >= len(s) {
		return 1
	}
	csi := s[i] == 0x9b
	if !csi && s[j] == '[' {
		csi, j = true, j+1
	}
	if csi {
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++ // parameter bytes
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++ // intermediate bytes
		}
		if j < len(s) {
			j++ // final byte
		}
		return j - i
	}
	if s[j] == ']' { // OSC — runs to BEL or ST
		j++
		for j < len(s) {
			if s[j] == 0x07 {
				return j + 1 - i
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2 - i
			}
			j++
		}
		return len(s) - i
	}
	return 2 // ESC + one final byte
}

// revision is a cheap FNV-1a over every rendered field of the chat slice —
// tool merges replace entries IN PLACE (same ID, changed Meta), and think
// entries append with their own Kind, so a last-message shortcut would miss
// real changes.
func revision(chat []state.ChatMsg) uint64 {
	if len(chat) == 0 {
		return 0
	}
	h := uint64(14695981039346656037)
	mix := func(s string) {
		for i := 0; i < len(s); i++ {
			h ^= uint64(s[i])
			h *= 1099511628211
		}
	}
	for _, m := range chat {
		mix(m.ID)
		mix(m.From)
		mix(m.Kind)
		mix(m.Meta)
		mix(m.Text)
		if m.Pending {
			h ^= 1
			h *= 1099511628211
		}
	}
	return h
}

func cloneChat(in []state.ChatMsg) []state.ChatMsg {
	out := make([]state.ChatMsg, len(in))
	copy(out, in)
	return out
}
