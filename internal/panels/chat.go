// chat.go — THE STAR of the sidebar: the chat tab.
//
// Layout inside the tab panel (top → bottom):
//
//	viewport  — the whole conversation, glamour-rendered markdown.
//	            user turns are plain-wrapped and prefixed cyan "you › ";
//	            boss turns are rendered THROUGH GLAMOUR as markdown
//	            (**bold**, lists, fences format + wrap) with a yellow
//	            "boss › " hanging indent.
//	            Kind "think" entries render as dim-italic thinking blocks,
//	            COLLAPSED to "thinking · N lines" until ctrl+t expands all;
//	            Kind "tool" entries render as dim one-liners merged by CallID;
//	            From "office" entries render as dim local notices (red when
//	            Meta == "error").
//	spinner   — shown only while a boss reply is pending (" boss is typing…")
//	divider
//	textarea  — multiline input; Enter sends, Shift+Enter (or Ctrl+J) is a
//	            newline, placeholder "talk to the boss…". NEVER locked: while
//	            the boss is typing, Enter ENQUEUES (the app owns the queue)
//	            and the placeholder reads "boss is typing… · N queued".
//	            While a permission prompt is open, the prompt MODAL replaces
//	            this region: y/a/n answers, esc defers; every other key still
//	            types into the (hidden) textarea.
//
// Scroll: mouse wheel + PgUp/PgDn always scroll the conversation; ↑/↓ move
// inside a multi-line draft and scroll the conversation otherwise. ctrl+t
// expands/collapses ALL thinking blocks, ctrl+d expands/collapses ALL diff
// entries (both handled by the app keymap).
package panels

import (
	"image/color"
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
	userPrefix = "you › "
	bossPrefix = "boss › "

	placeholderIdle = "talk to the boss…"
	placeholderBusy = "boss is typing…"

	textareaH = 3 // rows of multiline input at the bottom of the tab
)

// thinkKind / toolKind / questionKind / diffKind / officeFrom / errMeta —
// chat entry markers the app reducer tags onto state.ChatMsg so this panel
// can style them without touching the user/boss turn paths.
const (
	thinkKind    = "think"
	toolKind     = "tool"
	questionKind = "question"
	diffKind     = "diff"
	officeFrom   = "office"
	errMeta      = "error"
)

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

// Chat is the chat tab panel.
type Chat struct {
	vp     viewport.Model
	ta     textarea.Model
	sp     spinner.Model
	onSend func(text string) tea.Cmd

	// Queue + permission seams (set by the app at build time).
	onEnqueue    func(text string) tea.Cmd // Enter while boss pending
	onPermAnswer func(response string) tea.Cmd
	onPermLater  func() tea.Cmd // esc defers the prompt
	perm         *PermissionView
	queueLen     int

	chat    []state.ChatMsg // rendered snapshot
	pending bool
	follow  bool // stick to the bottom unless the user scrolled up

	showThinking  bool // /thinking on|off — collected blocks visible (default true)
	showTools     bool // /tools on|off    — tool one-liners visible (default true)
	thinkExpanded bool // ctrl+t — thinking expanded; DEFAULT false (collapsed)
	diffExpanded  bool // ctrl+d or /diffs on|off — diffs expanded; DEFAULT false

	w, h      int
	md        *glamour.TermRenderer
	mdWidth   int
	renderRev uint64 // cheap changed-detection for SetState

	diffCache map[string]diffCacheEntry // parsed+hilighted diff rows by msg ID
}

// NewChat builds the panel; onSend is invoked on Enter with a non-empty
// draft (the app turns it into backend.Send + chat-user/pending events).
func NewChat(onSend func(text string) tea.Cmd) *Chat {
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
		showThinking: true, showTools: true, diffCache: map[string]diffCacheEntry{}}
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

// SetEnqueue wires the app's enqueue callback (Enter while a boss reply is
// pending enqueues instead of sending).
func (c *Chat) SetEnqueue(fn func(text string) tea.Cmd) { c.onEnqueue = fn }

// SetPermissionHandlers wires the app's permission answer/defer callbacks
// for the y/a/n/esc keys captured while a prompt is open.
func (c *Chat) SetPermissionHandlers(answer func(response string) tea.Cmd, later func() tea.Cmd) {
	c.onPermAnswer, c.onPermLater = answer, later
}

// SetPermission opens (non-nil) or closes (nil) the permission prompt that
// replaces the textarea region.
func (c *Chat) SetPermission(p *PermissionView) { c.perm = p }

// SetQueueLen updates the queue count shown in the busy placeholder
// ("boss is typing… · N queued"). The queue itself lives in the app model.
func (c *Chat) SetQueueLen(n int) {
	c.queueLen = n
	c.refreshPlaceholder()
}

// refreshPlaceholder recomputes the textarea placeholder from pending +
// queue state.
func (c *Chat) refreshPlaceholder() {
	if !c.pending {
		c.ta.Placeholder = placeholderIdle
		return
	}
	if c.queueLen > 0 {
		c.ta.Placeholder = placeholderBusy + " · " + itoa(c.queueLen) + " queued"
		return
	}
	c.ta.Placeholder = placeholderBusy
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

// SetSize implements Tab: splits content height across viewport / spinner /
// divider / textarea.
func (c *Chat) SetSize(w, h int) {
	if w < 4 {
		w = 4
	}
	c.w, c.h = w, h
	spH := 0
	if c.pending {
		spH = 1
	}
	vpH := h - textareaH - 1 /* divider */ - spH
	if vpH < 1 {
		vpH = 1
	}
	c.vp.SetWidth(w)
	c.vp.SetHeight(vpH)
	c.ta.SetWidth(w)
	c.mdWidth = w - len(bossPrefix) - 1
	if c.mdWidth < 10 {
		c.mdWidth = 10
	}
	c.md = nil // rebuilt lazily at the new wrap width
}

// SetState implements Tab: keeps the latest chat slice, re-renders the
// conversation when it changed, and keeps scroll pinned to the bottom.
func (c *Chat) SetState(st state.OfficeState) {
	rev := revision(st.Chat)
	if rev == c.renderRev && len(st.Chat) == len(c.chat) {
		return
	}
	c.renderRev = rev
	c.chat = cloneChat(st.Chat)

	wasPending := c.pending
	c.pending = false
	for _, m := range c.chat {
		if m.From == "boss" && m.Pending {
			c.pending = true
		}
	}
	if c.pending != wasPending {
		// the textarea NEVER locks — typing while the boss works is the
		// whole point of the queue; only the placeholder + spinner row react
		c.refreshPlaceholder()
		c.SetSize(c.w, c.h) // spinner row appears/disappears
	}

	c.vp.SetContent(c.renderConversation())
	if c.follow {
		c.vp.GotoBottom()
	}
}

// Update implements Interactive: Enter sends, Shift+Enter newline, wheel +
// pgup/pgdn + (single-line) arrows scroll the conversation.
func (c *Chat) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
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
		switch msg.String() {
		case "enter":
			text := strings.TrimSpace(c.ta.Value())
			if text == "" {
				return nil
			}
			c.ta.Reset()
			c.follow = true
			c.vp.GotoBottom()
			if strings.HasPrefix(text, "/") {
				// slash commands are local and always immediate — /queue
				// and /perm must work while the boss is typing
				if c.onSend != nil {
					return c.onSend(text)
				}
				return nil
			}
			if c.pending && c.onEnqueue != nil {
				return c.onEnqueue(text)
			}
			if c.onSend != nil {
				return c.onSend(text)
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
			var cmd tea.Cmd
			c.ta, cmd = c.ta.Update(msg)
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
	}
	return nil
}

// View implements Tab.
func (c *Chat) View() string {
	var b strings.Builder
	b.WriteString(c.vp.View())
	if c.pending {
		b.WriteString("\n")
		b.WriteString(c.sp.View())
		b.WriteString(chrome.AccentText.Render(" boss is typing…"))
	}
	b.WriteString("\n")
	b.WriteString(chrome.DimText.Render(fitPlain(strings.Repeat("─", c.w), c.w)))
	b.WriteString("\n")
	if c.perm != nil {
		// the permission modal REPLACES the textarea region, full width
		b.WriteString(c.renderPermission())
	} else {
		b.WriteString(c.ta.View())
	}
	return b.String()
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
func (c *Chat) renderConversation() string {
	visible := make([]state.ChatMsg, 0, len(c.chat))
	for _, m := range c.chat {
		if m.From == "boss" && m.Pending {
			continue // the spinner line speaks for the typing placeholder
		}
		if m.Kind == thinkKind && !c.showThinking {
			continue
		}
		if m.Kind == toolKind && !c.showTools {
			continue
		}
		visible = append(visible, m)
	}
	if len(visible) == 0 {
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
			b.WriteString(renderTool(m))
		case m.Kind == questionKind:
			c.renderQuestion(&b, m)
		case m.Kind == diffKind:
			c.renderDiff(&b, m)
		case m.From == officeFrom:
			c.renderNotice(&b, m)
		case m.From == "user":
			prefix := chrome.Fg(chrome.Info, userPrefix)
			lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
			writePrefixed(&b, prefix, strings.Repeat(" ", len(userPrefix)), lines)
		default:
			prefix := chrome.Fg(chrome.Accent, bossPrefix)
			writePrefixed(&b, prefix, strings.Repeat(" ", len(bossPrefix)),
				cleanMarkdown(c.renderMarkdown(m.Text)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderThink renders one Kind="think" entry: dim-italic "thinking" header +
// greyed body when expanded; a single "thinking · N lines" line when
// collapsed (the default).
func (c *Chat) renderThink(b *strings.Builder, m state.ChatMsg) {
	think := chrome.DimText.Italic(true)
	body := chrome.DimText
	lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
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

// renderTool renders one Kind="tool" one-liner, merged by CallID upstream:
// "[tool] read · src/main.go ✓" (done) / "… running" / red "✗" (error).
func renderTool(m state.ChatMsg) string {
	line := "[tool] " + m.Text
	switch m.Meta {
	case "done":
		return chrome.ToolStyle.Render(line + " ✓")
	case "error":
		return chrome.ErrText.Faint(true).Render(line + " ✗")
	default: // running (or anything unexpected)
		return chrome.ToolStyle.Render(line + " … running")
	}
}

// renderQuestion renders one Kind="question" entry (boss question tool):
// yellow "boss asks › <text>", dim options inline when present, and the
// "(answer by typing below)" hint.
func (c *Chat) renderQuestion(b *strings.Builder, m state.ChatMsg) {
	qPrefix := "boss asks › "
	indent := strings.Repeat(" ", len(qPrefix))
	wrapW := c.w - len(qPrefix) - 1 // prefix + panel padding, before vp soft-wrap
	lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, wrapW), "\n"), "\n")
	for i := range lines {
		lines[i] = chrome.QuestionText.Render(lines[i])
	}
	writePrefixed(b, chrome.QuestionText.Bold(true).Render(qPrefix), indent, lines)
	if m.Meta != "" {
		for _, ln := range strings.Split(strings.TrimRight(wrapPlain("("+m.Meta+")", wrapW), "\n"), "\n") {
			b.WriteString(indent + chrome.DimText.Render(ln) + "\n")
		}
	}
	b.WriteString(indent + chrome.DimText.Italic(true).Render("(answer by typing below)"))
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
	writePrefixed(b, style.Render("office › "), strings.Repeat(" ", len("office › ")), lines)
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

// wrapPlain greedy word-wraps plain (member-typed) text to w cells. No
// semantics — just a visual fold for the user side of the transcript.
func wrapPlain(s string, w int) string {
	if w < 4 {
		w = 4
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		var cur strings.Builder
		curW := 0
		for _, word := range strings.Fields(para) {
			ww := lipgloss.Width(word)
			switch {
			case curW == 0:
				cur.WriteString(word)
				curW = ww
			case curW+1+ww <= w:
				cur.WriteString(" " + word)
				curW += 1 + ww
			default:
				out = append(out, cur.String())
				cur.Reset()
				cur.WriteString(word)
				curW = ww
			}
		}
		out = append(out, cur.String())
	}
	return strings.Join(out, "\n")
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
