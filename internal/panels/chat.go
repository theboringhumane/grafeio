// chat.go — THE STAR of the sidebar: the chat tab.
//
// Layout inside the tab panel (top → bottom):
//
//	viewport  — the whole conversation, glamour-rendered markdown.
//	            user turns are plain-wrapped and prefixed cyan "you › ";
//	            boss turns are rendered THROUGH GLAMOUR as markdown
//	            (**bold**, lists, fences format + wrap) with a yellow
//	            "boss › " hanging indent.
//	spinner   — shown only while a boss reply is pending (" boss is typing…")
//	divider
//	textarea  — multiline input; Enter sends, Shift+Enter (or Ctrl+J) is a
//	            newline, placeholder "talk to the boss…". Locked while the
//	            boss is typing (placeholder swaps to "boss is typing…").
//
// Scroll: mouse wheel + PgUp/PgDn always scroll the conversation; ↑/↓ move
// inside a multi-line draft and scroll the conversation otherwise.
package panels

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	glan "charm.land/glamour/v2/ansi"
	glst "charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
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

// markdownStyle is glamour's dark style minus the outer document margin so a
// boss reply fills a 42-col sidebar instead of wasting 4 cells on margins.
func markdownStyle() glan.StyleConfig {
	s := glst.DarkStyleConfig
	zero := uint(0)
	sp := func(v string) *string { return &v }
	s.Document = glan.StyleBlock{
		StylePrimitive: glan.StylePrimitive{Color: sp("252")},
		Margin:         &zero,
	}
	return s
}

// Chat is the chat tab panel.
type Chat struct {
	vp     viewport.Model
	ta     textarea.Model
	sp     spinner.Model
	onSend func(text string) tea.Cmd

	chat    []state.ChatMsg // rendered snapshot
	pending bool
	follow  bool // stick to the bottom unless the user scrolled up

	w, h      int
	md        *glamour.TermRenderer
	mdWidth   int
	renderRev uint64 // cheap changed-detection for SetState
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
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Prompt = lipgloss.NewStyle().Foreground(chrome.Accent)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(chrome.Dim)
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Text = lipgloss.NewStyle()
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(chrome.Accent).Faint(true)
	ta.SetStyles(styles)
	ta.Focus()

	sp := spinner.New(
		spinner.WithSpinner(spinner.Line),
		spinner.WithStyle(chrome.AccentText),
	)

	c := &Chat{vp: vp, ta: ta, sp: sp, onSend: onSend, follow: true}
	c.SetSize(30, 10)
	return c
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
		if c.pending {
			c.ta.Blur()
			c.ta.Placeholder = placeholderBusy
		} else {
			c.ta.Focus()
			c.ta.Placeholder = placeholderIdle
		}
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
		switch msg.String() {
		case "enter":
			if c.pending {
				return nil
			}
			text := strings.TrimSpace(c.ta.Value())
			if text == "" {
				return nil
			}
			c.ta.Reset()
			c.follow = true
			c.vp.GotoBottom()
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
	b.WriteString(c.ta.View())
	return b.String()
}

// renderConversation rebuilds the full glamour-rendered transcript.
func (c *Chat) renderConversation() string {
	if len(c.chat) == 0 {
		return chrome.DimText.Render("  no messages yet — ask the boss for something.")
	}
	var b strings.Builder
	for i, m := range c.chat {
		if m.From == "boss" && m.Pending {
			continue // the spinner line speaks for the typing placeholder
		}
		if i > 0 {
			b.WriteString("\n")
		}
		if m.From == "user" {
			prefix := chrome.Fg(chrome.Info, userPrefix)
			lines := strings.Split(strings.TrimRight(wrapPlain(m.Text, c.mdWidth+1), "\n"), "\n")
			writePrefixed(&b, prefix, strings.Repeat(" ", len(userPrefix)), lines)
			continue
		}
		prefix := chrome.Fg(chrome.Accent, bossPrefix)
		writePrefixed(&b, prefix, strings.Repeat(" ", len(bossPrefix)),
			cleanMarkdown(c.renderMarkdown(m.Text)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderMarkdown runs a boss turn through glamour with the sidebar wrap.
func (c *Chat) renderMarkdown(text string) string {
	if c.md == nil {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(markdownStyle()),
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

// revision is a cheap change tick for the chat slice: length + last message
// identity + pending flag. chat-boss replaces pending placeholders, and the
// replacement changes the last-id/flag, so this catches every real change.
func revision(chat []state.ChatMsg) uint64 {
	if len(chat) == 0 {
		return 0
	}
	last := chat[len(chat)-1]
	var flag uint64
	if last.Pending {
		flag = 1
	}
	return uint64(len(chat))<<33 | uint64(len(last.ID))<<17 | uint64(len(last.Text))<<1 | flag
}

func cloneChat(in []state.ChatMsg) []state.ChatMsg {
	out := make([]state.ChatMsg, len(in))
	copy(out, in)
	return out
}
