// Package app — the root Bubble Tea model for Grafeio v2: state reducer
// (exact port of node-legacy/src/app.tsx officeReducer + initialState),
// layout, key routing, the power governor, and the backend event seam.
//
// Layout: topbar (1) | middle (floor left flex | right sidebar) | statusbar (1).
// The sidebar holds six tabs — chat | terminal | agents | board | mail |
// activity — and its width is configurable (brain.json ui.sidebarWidth,
// 26..80 clamp, 0 = default 68; /compact mode narrows it to 30). /zen is a
// transient fullscreen-floor mode (sidebar hidden, any key exits).
// Events arrive as state.Event tea.Msgs (backend goroutine → tea.Program.Send);
// the animation tick is a re-arming tea.Tick loop governed by the brain.json
// power posture (power.go): busy = smooth (180ms/150ms/400ms), idle = cheap
// (1s/2s), auto drifts to 3s after 60s of quiet.
package app

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/office"
	"github.com/theboringhumane/grafeio/internal/panels"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	mailCap   = 30
	chatCap   = 30
	thinkCap  = 20 // thinking blocks kept in chat
	toolCap   = 20 // tool one-liners kept in chat
	bubbleCap = 3  // never more than 3 concurrent balloons (drop oldest)

	// Sidebar sizing: the default is 68 cols; brain.json ui.sidebarWidth is
	// clamped to 26..80 (explicit config wins over /compact); the compact
	// layout mode (/compact, ui.compact) narrows the default to 30.
	defaultSidebarW = 68
	compactSidebarW = 30
	sidebarMin      = 26
	sidebarMax      = 80

	degradeCols = 100 // below this, the sidebar shrinks instead of the floor
	minCols     = 40
	minRows     = 12

	// Message queue — the INTELLIGENT BACKLOG: Enter while a boss reply is
	// pending enqueues a numbered item; the turn-complete flush sends the
	// whole backlog as ONE composed [BATCH DISPATCH] prompt (the boss runs
	// manager dispatch discipline over it — trivial inline, parallel
	// sub-agents for the rest). Exactly 1 item keeps the plain FIFO send.
	queueCap = 10

	// batchTitleClip — the QueueItemStart board title is the first 60 chars
	// of the typed text (machine clip, not NL).
	batchTitleClip = 60
	// batchSummaryClip — per-item clip inside the composite user bubble
	// ("you › 3 items: fix the badge; ship v2; …").
	batchSummaryClip = 32
	// batchRespawnWindow — a session.error on the primary inside this window
	// of the batch send counts as "the boss died on the batch": ONE respawn.
	batchRespawnWindow = 5 * time.Second

	// delegatingQuietTicks — boss-side quiet horizon for the delegation
	// state: a pending boss placeholder with no stream/thought/primary-
	// tool activity for MORE than this many ticks (and busy workers on the
	// floor) flips BossDelegating on instead of the lonely "typing…" spin.
	delegatingQuietTicks = 6
)

// batchMarker prefixes the ONE composed batch prompt. Machine format (the
// app writes it, the backend echoes it verbatim) — the chat render rewrites
// ANY chat-user echo carrying it into the compact composite bubble.
const batchMarker = "[BATCH DISPATCH — "

// teamBackend — the backlog/board seam live and demo backends expose beyond
// state.Backend (the backend dev's contract; the app type-asserts it).
// QueueItemStart mirrors one backlog item to the board and returns its id
// ("" when the backend has no board seam — QueueItemDone("") is a no-op);
// QueueItemDone closes the row when the batch's turn completes;
// ResetPrimary(true) respawns a fresh boss session for the one-shot retry.
type teamBackend interface {
	QueueItemStart(index int, title string) string
	QueueItemDone(boardID string)
	ResetPrimary(forceNew bool) error
}

// attachmentBackend — the chat-input attachment seam live and demo backends
// expose beyond state.Backend (same type-assert pattern as teamBackend).
// The interface method stays out of state.Backend on purpose: harness
// stubs (uishot/headless) keep their plain-text Send and simply never
// attach. SendWith sends one prompt carrying file parts for atts (the
// backend reads each Attachment.Path at send time).
type attachmentBackend interface {
	SendWith(text string, atts []state.Attachment) error
}

// sendChat pushes one prompt through the attachment seam when the backend
// implements it, else falls back to the plain-text Send. The fallback can
// only fire in harness stubs — live and demo both implement the seam.
func sendChat(b state.Backend, text string, atts []state.Attachment) error {
	if ab, ok := b.(attachmentBackend); ok {
		return ab.SendWith(text, atts)
	}
	return b.Send(text)
}

// queueEntry — one backlog item: the typed text, its chat-input
// attachments (they must survive the busy wait and ride the flush), and
// the board row id QueueItemStart handed back ("" when the backend has no
// team seam).
type queueEntry struct {
	text    string
	atts    []state.Attachment
	boardID string
}

// QueueDebugf, when set (uisshot --debug only), receives message-queue
// trace lines. Nil in production — the hot path checks before formatting.
var QueueDebugf func(format string, args ...any)

func qdebugf(format string, args ...any) {
	if QueueDebugf != nil {
		QueueDebugf(format, args...)
	}
}

// cleanupAttachments removes panel-created temp dirs (pasted images live in
// os.MkdirTemp "grafeio-paste-*", Attachment.Temp). Best-effort, and ONLY
// ever called after a send has resolved: enqueue must not clean (the flush
// still needs the file), and the batch respawn path keeps them for its one
// retry — the cleanup fires on success or on the terminal failure.
func cleanupAttachments(atts []state.Attachment) {
	seen := map[string]bool{}
	for _, a := range atts {
		if a.Temp != "" && !seen[a.Temp] {
			seen[a.Temp] = true
			_ = os.RemoveAll(a.Temp)
		}
	}
}

// cleanupEntries is cleanupAttachments over a batch of queue entries
// (their attachments concatenate the same way the flush send sees them).
func cleanupEntries(items []queueEntry) {
	for _, it := range items {
		cleanupAttachments(it.atts)
	}
}

// attachNames is the " · "-joined display-name projection of an attachment
// list (board titles; the backend has its own unexported twin — packages
// don't share internals).
func attachNames(atts []state.Attachment) string {
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return strings.Join(names, " · ")
}

// SoundBus — the sound engine seam (the sound dev owns the engine; the app
// only CALLS Play). Nil by default — manager injects via SetSoundBus.
type SoundBus interface {
	Play(name string)
}

// Model is the tea.Model for the whole app.
type Model struct {
	backend state.Backend
	st      state.OfficeState
	cfg     *config.Config // brain.json (nil-tolerant: Default() substituted)
	gov     *governor      // power/caching bookkeeping, shared across copies

	// social — the office's SocialClock (ambient.go). Pointer, so the plan
	// survives the value-copy update loop. lastDispatchTick feeds its
	// "active dispatch in-flight <30 ticks" busy gate.
	social           *SocialClock
	lastDispatchTick int // -1 = no dispatch seen yet this run

	// snd — the sound bus (nil by default; manager injects). Reducer hook
	// points call playSound() which no-ops on nil.
	snd SoundBus

	// bossName/bossShort — the human boss label from cfg.Boss.Name: the full
	// string for roster rows ("jorge (El Jefe)"), its first word for the
	// busy placeholder/spinner ("jorge is typing…").
	bossName  string
	bossShort string

	width, height int
	middleH       int
	sidebar       int
	floorW        int
	tabs          *panels.Tabs
	chat          *panels.Chat
	activity      *panels.Activity
	termTab       *termTabWrap // tab 2: the real OS-shell (lazy PTY, terminal.go)
	keys          KeyMap

	// zen — transient fullscreen-floor mode: sidebar hidden entirely, topbar
	// stays, statusbar minimal; any key exits. Never persisted (the ruling:
	// /zen is a focus session, not a preference).
	zen bool
	// compactLive — the /compact session override: 0 = inherit
	// brain.json ui.compact, 1 = compact on, 2 = normal on. /mode
	// normal|compact writes cfg.UI.Compact (persisted) and clears this.
	compactLive int

	// frameNonce — bumped on every message that can mutate panel ephemera
	// the state digest can't see (textarea draft, scroll, spinner, theme
	// toggles). Part of the frame cache key (digest.go).
	frameNonce   uint64
	activityAdds int // total activity-log appends (digest term)

	// Message backlog (model-level so it survives tab switches): texts typed
	// while a boss reply is pending, each with its board row id.
	queue []queueEntry

	// Batch dispatch bookkeeping (set by dispatchQueued, consumed by the
	// pending→non-pending completion transition):
	//   batchInFlight  — a composed batch is awaiting its turn
	//   batchRespawned — the ONE respawn for the in-flight batch is spent
	//   batchItems     — retained for the ≤5s session.error respawn
	//   batchDoneIDs   — board rows closed by QueueItemDone on completion
	//   batchSummaries — item texts for the composite user-bubble rewrite
	//   batchSentAt    — send time, bounds the session.error window
	//   respawns       — global respawn count for this session run
	batchInFlight  bool
	batchRespawned bool
	batchItems     []queueEntry
	batchDoneIDs   map[string]bool
	batchSummaries []string
	batchSentAt    time.Time
	respawns       int

	// Permission prompts (boss/primary session only): perm is the OPEN
	// prompt replacing the textarea; permEscd is the latest esc'd-but-
	// unanswered prompt /perm can re-open.
	perm     *permPrompt
	permEscd *permPrompt

	// Question holds (boss/primary session only): question is the OPEN
	// hold whose modal replaces the textarea (a free-text input, unlike
	// the y/a/n permission modal); questionEscd is the latest esc'd-but-
	// unanswered hold /question can re-open. questionParked survives
	// defer: the opencode turn is parked at the question reply API (not
	// "typing") until a completed chat-boss arrives after resolution, so
	// the message queue must NOT flush and typed text keeps enqueuing.
	question       *questionHold
	questionEscd   *questionHold
	questionParked bool
	parkedStatus   string // StatusLine saved at park, restored at unpark

	// activeThink — CallIDs with an OPEN boss EvThought stream (Done not
	// yet seen). Model-owned (the reducer stays pure): the chat panel
	// consults this set to render streaming blocks expanded/livecoded
	// while completed ones collapse to "thinking · N lines". Any
	// EvChatBoss (pending placeholder OR answer) clears it — a newer boss
	// turn downgrades older unfinished think entries to collapsed.
	activeThink map[string]bool

	// lastBossActivity — st.Tick of the last boss-side activity (stream
	// delta / thought / primary tool / any boss bubble event). Feeds the
	// delegation reducer hook (applyDelegation, P3).
	lastBossActivity int
}

// permPrompt is a pending boss permission request.
type permPrompt struct {
	ID       string
	ToolName string
	Summary  string
}

// questionHold is a pending boss question hold. IDs batches every pending
// wire request id of one question call (v1: ONE typed answer answers each
// batched id); Text/Options render the modal header + dim options list.
type questionHold struct {
	IDs     []string
	Text    string
	Options string
}

// chatSentMsg fires after backend.Send succeeds — the local user bubble and
// the typing placeholder are appended through the normal reducer path.
type chatSentMsg struct{ text string }

// chatNoticeMsg is the chat panel's office-notice seam (attachment events:
// cap eviction, backspace removal, image-paste platform gaps).
type chatNoticeMsg struct{ text string }

// sendErrMsg fires when the backend rejects a prompt.
type sendErrMsg struct{ err error }

// queueSendErrMsg fires when a backlog flush send (batch or single) is
// rejected. batch + !retry gets ONE respawn (ResetPrimary + resend the same
// composed batch); retry=true (or single) just surfaces the error.
type queueSendErrMsg struct {
	err   error
	items []queueEntry
	batch bool
	retry bool
}

// slashMsg fires when the chat input starts with "/" — local command, never
// sent to the backend.
type slashMsg struct{ text string }

// enqueueMsg fires when Enter lands while a boss reply is pending — the
// text joins the model-level queue instead of reaching the backend. The
// staged attachments ride along so the flush still has their files.
type enqueueMsg struct {
	text string
	atts []state.Attachment
}

// queueFlushMsg (400ms tick chain) flushes the next queued message.
type queueFlushMsg struct{}

// permAnswerMsg fires when the user answers an open permission prompt
// (y/a/n → "once"/"always"/"reject").
type permAnswerMsg struct{ response string }

// permLaterMsg fires on esc — the prompt stays pending, re-openable with
// /perm.
type permLaterMsg struct{}

// questionAnswerMsg fires when the user hits Enter in an open question
// modal — the typed text goes through AnswerQuestion (this is the fix: a
// plain Send parks the opencode loop at the question reply API forever).
type questionAnswerMsg struct{ text string }

// questionLaterMsg fires on esc in an open question modal — the hold
// stays pending, re-openable with /question.
type questionLaterMsg struct{}

// New builds the app around a backend + the brain.json config. cfg is
// nil-tolerant (config.Default() substituted — headless stubs and harnesses).
// backend.Start is NOT called here — main owns that (goroutine →
// tea.Program.Send).
func New(b state.Backend, cfg *config.Config) Model {
	if cfg == nil {
		cfg = config.Default()
	}
	ambientOn = cfg.UI.AmbientChatter

	bossName := cfg.Boss.Name
	if bossName == "" {
		bossName = "boss (oikonomos)"
	}
	bossShort := bossName
	if i := strings.IndexAny(bossShort, " \t"); i > 0 {
		bossShort = bossShort[:i]
	}

	termTab := newTermTabWrap()
	chat := panels.NewChat(func(text string, atts []state.Attachment) tea.Cmd {
		return func() tea.Msg {
			// Slash commands dispatch locally, never touch the backend, and
			// never echo as chat-user.
			if strings.HasPrefix(text, "/") {
				return slashMsg{text: text}
			}
			if b != nil {
				if err := sendChat(b, text, atts); err != nil {
					cleanupAttachments(atts) // nobody will retry this prompt
					return sendErrMsg{err: err}
				}
				cleanupAttachments(atts)
			}
			return chatSentMsg{text: text}
		}
	})
	chat.SetBossShortName(bossShort)
	agents := panels.NewAgents()
	agents.SetBossName(bossName)
	activity := panels.NewActivity()
	m := Model{
		backend:          b,
		cfg:              cfg,
		gov:              &governor{lastBusy: time.Now()},
		bossName:         bossName,
		bossShort:        bossShort,
		st:               initialState(b.Mode()),
		chat:             chat,
		activity:         activity,
		termTab:          termTab,
		activeThink:      map[string]bool{},
		social:           newSocialClock(),
		lastDispatchTick: -1,
		tabs: panels.NewTabs(
			chat,
			termTab,
			agents,
			panels.NewBoard(),
			panels.NewMail(),
			activity,
		),
		keys: NewKeyMap(),
	}
	// Queue + permission seams: the panel owns the keys, the model owns the
	// queue/prompt state; callbacks ferry over tea.Msgs so the model value
	// copy in Update stays the single writer.
	chat.SetEnqueue(func(text string, atts []state.Attachment) tea.Cmd {
		return func() tea.Msg { return enqueueMsg{text: text, atts: atts} }
	})
	// Attachment notices (cap eviction, chip removal, image-paste platform
	// gaps) surface as office chat notices like every other local outcome.
	chat.SetNoticeHandler(func(text string) tea.Cmd {
		return func() tea.Msg { return chatNoticeMsg{text: text} }
	})
	chat.SetPermissionHandlers(
		func(response string) tea.Cmd {
			return func() tea.Msg { return permAnswerMsg{response: response} }
		},
		func() tea.Cmd {
			return func() tea.Msg { return permLaterMsg{} }
		},
	)
	chat.SetQuestionHandlers(
		func(text string) tea.Cmd {
			return func() tea.Msg { return questionAnswerMsg{text: text} }
		},
		func() tea.Cmd {
			return func() tea.Msg { return questionLaterMsg{} }
		},
	)
	m.tabs.SetCompact(m.compact())
	m.chat.SetCompact(m.compact())
	m.tabs.SetState(m.st)
	return m
}

// SelectTab activates a sidebar tab by name ("chat", "terminal", "agents",
// …). Used by harnesses (uishot) before the run starts; selecting the
// terminal tab lazy-spawns its shell on this first visit.
func (m *Model) SelectTab(name string) bool {
	ok := m.tabs.SetActiveByTitle(name)
	if ok {
		m.maybeSpawnTerminal()
	}
	return ok
}

// SetSoundBus injects the sound engine's bus (nil disables sound). The app
// only calls Play — the engine is owned elsewhere.
func (m *Model) SetSoundBus(bus SoundBus) {
	m.snd = bus
}

// playSound — reducer-property sound hook; no-ops while no bus is injected.
func (m *Model) playSound(name string) {
	if m.snd != nil {
		m.snd.Play(name)
	}
}

// State returns the current office state (read-only harness seam — uishot
// --social asserts on bubbles/sprites through it).
func (m Model) State() state.OfficeState {
	return m.st
}

// Init arms the first power-governed tick; applyEvent re-arms every cycle.
func (m Model) Init() tea.Cmd {
	return m.tickCmd()
}

// tickCmd re-arms the animation tick at the delay the governor picks for
// THIS cycle (busy signals from the current state + modals + think stream,
// idle duration from the drift clock). Busy refreshes lastBusy; 60s of
// continuous quiet in auto mode slips into screensaver cadence.
func (m *Model) tickCmd() tea.Cmd {
	modalOpen := m.perm != nil || m.question != nil
	thinkActive := len(m.activeThink) > 0
	now := time.Now()
	if officeBusy(m.st, modalOpen, thinkActive) {
		m.gov.lastBusy = now
	}
	delay := TickDelay(m.st, m.cfg, thinkActive, modalOpen, now.Sub(m.gov.lastBusy))
	m.gov.tickCount++
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return state.Event{Kind: state.EvTick}
	})
}

// Ticks — tick commands armed this run (uisot power proof).
func (m Model) Ticks() int { return m.gov.tickCount }

// Config — the live brain.json (nil-tolerant accessors live elsewhere).
func (m Model) Config() *config.Config { return m.cfg }

// FrameCacheStats — app-frame cache counters (uisot proof).
func (m Model) FrameCacheStats() (hits, misses uint64) {
	return m.gov.frameHits, m.gov.frameMisses
}

// Update routes keys, backend events and component ticks.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		// keys can mutate panel ephemera (textarea, scroll) the state
		// digest can't see — invalidate the frame cache conservatively.
		m.frameNonce++
		if cmd := m.handleKey(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case chatSentMsg:
		// nothing local: backend.Send owns the echo (chat-user + pending boss
		// bubble) via the event stream — applying them here duplicated the bubbles.
		m.playSound("send")
	case chatNoticeMsg:
		// the chat panel's attachment notices join the office notice feed
		m.notice(msg.text)
	case sendErrMsg:
		m.playSound("error")
		cmds = append(cmds, m.applyEvent(state.Event{
			Kind: state.EvStatus,
			Text: fmt.Sprintf("[grafeio] send failed: %v", msg.err),
		}))
	case queueSendErrMsg:
		// FAILURE RESPAWN — one per flush call: the boss session died at
		// Send; reset the primary and resend the SAME composed batch on the
		// fresh session. A retry failure just surfaces the error.
		if msg.batch && !msg.retry && !m.batchRespawned {
			if _, ok := m.team(); ok {
				// no cleanup here — the respawn retry resends the SAME
				// files, so the temp dirs must still exist.
				m.batchRespawned = true
				m.batchSentAt = time.Now()
				m.respawns++
				m.notice("boss went down — respawned a fresh session, resending batch")
				qdebugf("batch send failed (%v) — respawning (respawn #%d)", msg.err, m.respawns)
				cmds = append(cmds, m.resendBatchCmd(msg.items))
				break
			}
		}
		// terminal failure: no retry is coming for these attachments
		cleanupEntries(msg.items)
		m.playSound("error")
		cmds = append(cmds, m.applyEvent(state.Event{
			Kind: state.EvStatus,
			Text: fmt.Sprintf("[grafeio] send failed: %v", msg.err),
		}))
	case slashMsg:
		// slash handlers mutate panel-only visual state (thinking/tools/
		// diffs toggles, theme) — cover the frame cache with the nonce.
		m.frameNonce++
		if cmd := m.applySlash(msg.text); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case enqueueMsg:
		if len(m.queue) >= queueCap {
			m.noticeErr(fmt.Sprintf("backlog full (%d) — wait for the boss to catch up, or /queue clear", queueCap))
			cleanupAttachments(msg.atts) // never queued — nobody else will
		} else {
			n := len(m.queue) + 1
			ent := queueEntry{text: msg.text, atts: msg.atts}
			if tb, ok := m.team(); ok {
				// board row for the backlog item: title is the machine
				// first-N-chars clip of the typed text (an attach-only
				// item derives its title from the attachment names instead).
				title := clipRunes(msg.text, batchTitleClip)
				if msg.text == "" && len(msg.atts) > 0 {
					title = clipRunes("attachments: "+attachNames(msg.atts), batchTitleClip)
				}
				ent.boardID = tb.QueueItemStart(n, title)
			}
			m.queue = append(m.queue, ent)
			if m.chat != nil {
				m.chat.SetQueueLen(len(m.queue))
			}
			m.playSound("queued")
			qdebugf("enqueued %q as item #%d (board=%q, n=%d)", msg.text, n, ent.boardID, len(m.queue))
			m.notice(fmt.Sprintf("queued as item #%d — flushes as a batch when the boss frees up", n))
		}
	case queueFlushMsg:
		if len(m.queue) > 0 {
			cmds = append(cmds, m.flushQueued())
		}
	case permAnswerMsg:
		if m.perm != nil {
			pid, response := m.perm.ID, msg.response
			if m.permEscd != nil && m.permEscd.ID == pid {
				m.permEscd = nil
			}
			m.perm = nil
			m.chat.SetPermission(nil)
			cmds = append(cmds, func() tea.Msg {
				if m.backend != nil {
					if err := m.backend.AnswerPermission(pid, response); err != nil {
						return sendErrMsg{err: err}
					}
				}
				return nil
			})
		}
	case permLaterMsg:
		if m.perm != nil {
			m.permEscd = m.perm
			m.perm = nil
			m.chat.SetPermission(nil)
			m.notice("esc'd permission pending (/perm)")
		}
	case questionAnswerMsg:
		if m.question != nil {
			hold := m.question
			m.question = nil
			m.chat.SetQuestion(nil)
			b := m.backend
			text := msg.text
			cmds = append(cmds, func() tea.Msg {
				// v1: ONE typed string answers every pending QuestionID of
				// the hold — a question call batching several ids still
				// unblocks them all with the same answer.
				if b != nil {
					for _, qid := range hold.IDs {
						if err := b.AnswerQuestion(qid, []string{text}); err != nil {
							return sendErrMsg{err: err}
						}
					}
				}
				return nil
			})
		}
	case questionLaterMsg:
		if m.question != nil {
			m.questionEscd = m.question
			m.question = nil
			m.chat.SetQuestion(nil)
			m.notice("(question deferred — /question to reopen)")
		}
	case state.Event:
		cmds = append(cmds, m.applyEvent(msg))
	default:
		// spinner ticks, mouse wheel, etc. → active tab (panel ephemera →
		// frame-cache nonce, same reasoning as keypresses)
		m.frameNonce++
		if cmd := m.tabs.Update(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

// View builds the final frame for bubbletea v2 (alt-screen + mouse).
func (m Model) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// Frame renders the whole UI as one string — also what snapshot harnesses
// (cmd/uishot) print after the scripted run. Render cost is skipped when
// nothing changed: the digest cache returns the previous frame string
// verbatim, and the floor itself is memoized (office.CachedStyled — the
// same tick+sprites never rebuilds the grid).
func (m Model) Frame() string {
	if m.width == 0 {
		return "grafeio — waiting for terminal size…"
	}
	digest := m.frameDigest()
	if m.gov.frameCached != "" && m.gov.frameKey == digest {
		m.gov.frameHits++
		return m.gov.frameCached
	}
	m.gov.frameMisses++
	top := chrome.TopBar(m.st, m.width)
	if m.compact() {
		top = chrome.TopBarCompact(m.st, m.width)
	}
	var mid, bot string
	if m.zen {
		// zen (/zen · /focus floor) — transient fullscreen floor: sidebar
		// hidden entirely, topbar stays, chat gone, statusline minimal.
		mid = lipgloss.NewStyle().Width(m.width).Height(m.middleH).
			Render(office.CachedStyled(m.st, m.width, m.middleH))
		bot = chrome.StatusBarZen(m.st, m.width)
	} else {
		floor := lipgloss.NewStyle().Width(m.floorW).Height(m.middleH).
			Render(office.CachedStyled(m.st, m.floorW, m.middleH))
		side := lipgloss.NewStyle().Width(m.sidebar).Height(m.middleH).
			Render(m.tabs.View())
		mid = lipgloss.JoinHorizontal(lipgloss.Top, floor, side)
		hint := m.keys.HintLine()
		if m.terminalActive() {
			hint = termHint
		}
		bot = chrome.StatusBar(m.st, hint, len(m.queue), m.width)
	}
	frame := lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
	m.gov.frameKey, m.gov.frameCached = digest, frame
	return frame
}

// handleKey implements the global keymap; unclaimed keys go to the tabs.
//
// The terminal tab has the tightest claim: when it is focused the ONLY keys
// the app keeps are the tab switches (1..6/tab/shift+tab), ctrl+o (the
// release-the-focus badge back to chat) and ctrl+q — every other key, q and
// ctrl+c included, forwards to the REAL shell (term maps ctrl+c to 0x03 →
// SIGINT of the shell's foreground process, not an app quit).
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	chatActive := m.tabs.ActiveIndex() == 0
	termActive := m.terminalActive()

	switch {
	case key == "ctrl+q":
		// app quit works EVERYWHERE — terminal focus included
		m.closeTerminal()
		return tea.Quit
	case m.zen:
		// any key exits zen (transient fullscreen floor); the key does
		// nothing else this press
		m.zen = false
		return nil
	}

	// tab-switch keys work from EVERY tab, terminal included
	switch key {
	case "tab":
		m.tabs.Next()
		m.maybeSpawnTerminal()
		return nil
	case "shift+tab":
		m.tabs.Prev()
		m.maybeSpawnTerminal()
		return nil
	case "ctrl+o":
		// release the terminal focus badge → back to chat
		if termActive {
			m.tabs.SetActive(0)
			return nil
		}
	}
	if !chatActive {
		if idx := m.keys.TabJump(key); idx >= 0 {
			m.tabs.SetActive(idx)
			m.maybeSpawnTerminal()
			return nil
		}
	}

	if termActive {
		// everything else belongs to the shell
		return m.tabs.Update(msg)
	}

	switch key {
	case "ctrl+c":
		m.closeTerminal()
		return tea.Quit
	case "q":
		if !chatActive {
			m.closeTerminal()
			return tea.Quit
		}
	case "ctrl+t":
		if chatActive {
			m.chat.ToggleThink()
			return nil
		}
	case "ctrl+d":
		if chatActive {
			m.chat.ToggleDiffs()
			return nil
		}
	case "ctrl+g":
		if chatActive {
			m.chat.ToggleThreads()
			return nil
		}
	}
	return m.tabs.Update(msg)
}

// terminalActive reports whether the focused tab is the OS-shell tab.
func (m *Model) terminalActive() bool {
	return m.tabs.ActiveIndex() == terminalIndex
}

// maybeSpawnTerminal lazy-spawns the terminal tab's shell on the first
// visit (battery: no PTY until the member asks for one). A spawn failure is
// a soft landing: office notice "terminal spawn failed: <err>" + fallback
// to the chat tab — never a crash.
func (m *Model) maybeSpawnTerminal() {
	if !m.terminalActive() || m.termTab == nil {
		return
	}
	if err := m.termTab.ensure(); err != nil {
		m.playSound("error")
		m.noticeErr(fmt.Sprintf("terminal spawn failed: %v", err))
		m.tabs.SetActive(0)
	}
}

// closeTerminal kills the spawned shell on the app quit path (Close is
// idempotent; a never-visited tab has no PTY to kill).
func (m *Model) closeTerminal() {
	if m.termTab != nil {
		m.termTab.close()
	}
}

// CloseTerminal is the exported quit-path hook for cmd/grafeio (the runtime
// intercepts tea.QuitMsg before Update, so an external p.Quit skips
// handleKey — call CloseTerminal alongside to never leak a shell process).
func (m *Model) CloseTerminal() { m.closeTerminal() }

// LayoutInfo reports the computed frame geometry (uisshot --layout asserts).
func (m Model) LayoutInfo() (width, height, sidebar, floor int) {
	return m.width, m.height, m.sidebar, m.floorW
}

// applyEvent reduces one backend event, feeds panels + activity log, and
// re-arms the animation tick. Returns the next cmd when needed.
func (m *Model) applyEvent(ev state.Event) tea.Cmd {
	// permission prompts + question holds are model-owned UI state (not
	// chat history) — handle before the reducer (the reducer also uses
	// the parked state: a question modal drops the typing placeholder).
	if ev.Kind == state.EvPermission {
		m.handlePermissionEvent(ev)
	}
	if ev.Kind == state.EvQuestion {
		m.handleQuestionEvent(ev)
	}

	// Think-stream bookkeeping (model-owned; the reducer stays pure):
	// open a CallID's stream on EvThought Done=false, close it on
	// Done=true. Defensive: ANY EvChatBoss — a fresh pending placeholder
	// included — downgrades every still-open stream to collapsed.
	switch ev.Kind {
	case state.EvThought:
		if ev.EmployeeID == "boss" && ev.CallID != "" {
			if ev.Done {
				delete(m.activeThink, ev.CallID)
			} else {
				m.activeThink[ev.CallID] = true
			}
		}
	case state.EvChatBoss:
		for id := range m.activeThink {
			delete(m.activeThink, id)
		}
	}

	// The backend echoes the composed batch prompt verbatim as chat-user;
	// the member sees ONE compact composite bubble instead of the raw
	// dispatch text ("you › 3 items: fix the badge; ship v2; …").
	if ev.Kind == state.EvChatUser && strings.HasPrefix(ev.Msg.Text, batchMarker) &&
		len(m.batchSummaries) > 0 {
		titles := make([]string, len(m.batchSummaries))
		for i, t := range m.batchSummaries {
			titles[i] = clipRunes(t, batchSummaryClip)
		}
		ev.Msg.Text = fmt.Sprintf("%d items: %s", len(titles), strings.Join(titles, "; "))
	}

	// session.error on the primary within the window of a batch send = the
	// boss died mid-batch: arm the ONE respawn (consumed at the completion
	// transition below, where a naive close would otherwise fire).
	respawn := ev.Kind == state.EvChatBoss &&
		strings.HasPrefix(ev.Msg.ID, "boss-error-") &&
		m.batchInFlight && !m.batchRespawned &&
		!m.batchSentAt.IsZero() && time.Since(m.batchSentAt) <= batchRespawnWindow

	// social-clock busy gate: remember the tick of the latest dispatch —
	// a dispatch younger than 30 ticks silences the clock (busy !== social).
	if ev.Kind == state.EvDispatch {
		m.lastDispatchTick = m.st.Tick
	}

	// sound hooks (no-op until a bus is injected): reply/dispatch/done/
	// alert/error at their reducer points.
	switch ev.Kind {
	case state.EvChatBoss:
		if !ev.Msg.Pending {
			if strings.HasPrefix(ev.Msg.ID, "boss-error-") {
				m.playSound("error") // session-level failure
			} else {
				m.playSound("reply")
			}
		}
	case state.EvReturned:
		m.playSound("done")
	case state.EvDispatch:
		m.playSound("dispatch")
	case state.EvBlocked:
		m.playSound("alert")
	}

	prevPending := hasPendingBoss(m.st)
	m.st = reducer(m.st, ev)
	m.applyDelegation(ev) // P3 — before panels see the state
	if m.chat != nil {
		m.chat.SetStreamingThink(m.activeThink)
	}
	m.tabs.SetState(m.st)

	// activity: mid-stream deltas are visual growth of ONE bubble — logging
	// each would spam the log (the placeholder's "typing…" and the final's
	// "reply" already bracket the turn). Skip pending-with-text events.
	isStreamDelta := ev.Kind == state.EvChatBoss && ev.Msg.Pending && ev.Msg.Text != ""
	if ev.Kind != state.EvTick && !isStreamDelta {
		m.activity.Add(m.describeEvent(ev))
		m.activityAdds++
	}

	if ev.Kind == state.EvTick {
		// social clock: plans + fires its beats off the tick (EvBubble/
		// EvIdleDrift events through the normal reducer path — ambient.go).
		m.runSocial()
		// governor: the next delay is chosen from the CURRENT cycle's
		// busy/idle posture (power.go).
		return m.tickCmd()
	}
	// A completed boss bubble unblocks a parked question turn: the hold
	// resolved, the server resumed — the chat goes back to "typing" and
	// queued messages flush again.
	if m.questionParked && ev.Kind == state.EvChatBoss && !ev.Msg.Pending {
		m.questionParked = false
		if m.st.StatusLine == "[question] boss is waiting for your answer…" {
			m.st.StatusLine = m.parkedStatus
		}
		if m.chat != nil {
			m.chat.SetQuestionWaiting(false)
		}
		qdebugf("question resolved: completed boss reply unblocks queue")
		if len(m.queue) > 0 {
			return m.flushQueued()
		}
	}
	if !prevPending && hasPendingBoss(m.st) && m.chat != nil {
		return m.chat.SpinnerKick()
	}
	// While a question hold is outstanding the turn is PARKED at the
	// question reply API — not completed — so the queue must NOT flush.
	if prevPending && !hasPendingBoss(m.st) && !m.questionParked {
		// the boss reply landed (or errored out) — turn completed
		if respawn {
			// session.error inside the window: fresh session + the SAME
			// batch, once. The rows stay open for the retry's turn.
			m.batchRespawned = true
			m.batchSentAt = time.Now()
			m.respawns++
			items := append([]queueEntry(nil), m.batchItems...)
			m.notice("boss went down — respawned a fresh session, resending batch")
			qdebugf("session.error inside batch window — respawning (respawn #%d, items=%d)", m.respawns, len(items))
			return m.resendBatchCmd(items)
		}
		// v1: the FIRST completed turn after a batch send closes every
		// board row of the batch — per-item close-outs over multi-turn
		// batches (the boss answering items one turn at a time) are a
		// later wave; good enough now.
		if len(m.batchDoneIDs) > 0 {
			if tb, ok := m.team(); ok {
				for id := range m.batchDoneIDs {
					tb.QueueItemDone(id)
				}
				qdebugf("batch turn completed: %d board row(s) done", len(m.batchDoneIDs))
			}
			m.batchDoneIDs = nil
			m.batchInFlight = false
		}
		if len(m.queue) > 0 {
			// the boss is free: flush the backlog (ONE batch when >1)
			return m.flushQueued()
		}
	}
	return nil
}

// applyDelegation — P3: while the boss dispatched work to children the
// primary session can sit quiet at its typing placeholder for minutes
// (dead feedback: "typing…" while nobody is typing). Track the last
// boss-side activity and, on every event, recompute st.BossDelegating:
// a pending boss placeholder exists AND no boss stream/thought/primary-
// tool activity for > delegatingQuietTicks AND ≥1 hired employee is
// visibly busy (working/to-manager/meeting). It clears instantly — any
// boss stream/thought/tool/bubble event refreshes the clock the same
// reduce, so the >-horizon comparison can never trigger.
func (m *Model) applyDelegation(ev state.Event) {
	if isBossActivity(ev) {
		m.lastBossActivity = m.st.Tick
	}
	busy := 0
	for _, e := range m.st.Employees {
		if e.Role == state.RoleManager {
			continue
		}
		switch e.Sprite {
		case state.SpriteWorking, state.SpriteToManager, state.SpriteMeeting:
			busy++
		}
	}
	m.st.BossDelegating = hasPendingBoss(m.st) && busy > 0 &&
		m.st.Tick-m.lastBossActivity > delegatingQuietTicks
}

// isBossActivity — the boss-side event set that resets the delegation
// quiet clock: any boss chat event (stream delta, placeholder, pinned or
// error bubble), a boss EvThought, a primary-session EvTool.
func isBossActivity(ev state.Event) bool {
	switch ev.Kind {
	case state.EvChatBoss:
		return true
	case state.EvThought:
		return ev.EmployeeID == "boss" || ev.EmployeeName == "" || ev.EmployeeName == "boss"
	case state.EvTool:
		return ev.EmployeeName == "" || ev.EmployeeName == "boss"
	}
	return false
}

// team type-asserts the optional teamBackend seam (live/demo backends).
func (m *Model) team() (teamBackend, bool) {
	if m.backend == nil {
		return nil, false
	}
	tb, ok := m.backend.(teamBackend)
	return tb, ok
}

// composeBatch builds the ONE batch-dispatch prompt the boss session
// decomposes per its manager discipline: numbered independent work items,
// parallel sub-agents for the non-trivial ones, a closing status table.
// Attachment-carrying items get a machine " 📎N" suffix on their numbered
// line; the actual file parts ride the same send (dispatchQueued).
func composeBatch(items []queueEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[BATCH DISPATCH — %d requests arrived while you were busy. "+
		"Treat each as an independent numbered work item: do trivial ones inline; "+
		"for non-trivial independent items DISPATCH PARALLEL SUB-AGENTS per your "+
		"manager discipline; then finalize with a one-line-per-item status table.]\n",
		len(items))
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. %s", i+1, it.text)
		if n := len(it.atts); n > 0 {
			fmt.Fprintf(&sb, " 📎%d", n)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// flushQueued drains the backlog: the intelligent flush after a turn
// completes (or a parked question unblocks).
func (m *Model) flushQueued() tea.Cmd {
	return m.dispatchQueued(false)
}

// dispatchQueued sends the backlog as ONE composed [BATCH DISPATCH] prompt
// when >1 items are queued; exactly 1 item keeps the plain FIFO send path.
// manual=true is /route — force the send NOW, bypassing the busy gate.
// Slash inputs never reach the queue (the chat panel dispatches them
// immediately) — the single-item slash guard stays defensive.
func (m *Model) dispatchQueued(manual bool) tea.Cmd {
	if len(m.queue) == 0 {
		return nil
	}
	items := m.queue
	m.queue = nil
	if m.chat != nil {
		m.chat.SetQueueLen(0)
	}
	if manual {
		if len(items) > 1 {
			m.notice(fmt.Sprintf("routed manually — batch dispatching %d queued items now", len(items)))
		} else {
			m.notice("routed manually — sending now")
		}
	}
	texts := make([]string, len(items))
	var boardIDs []string
	var batchAtts []state.Attachment // every item's chips ride the flush
	for i, it := range items {
		texts[i] = it.text
		batchAtts = append(batchAtts, it.atts...)
		if it.boardID != "" {
			boardIDs = append(boardIDs, it.boardID)
		}
	}
	sendText := texts[0]
	batch := len(texts) > 1
	if batch {
		sendText = composeBatch(items)
		qdebugf("flush: batch dispatching %d items as ONE send", len(texts))
	} else {
		qdebugf("flush %q (plain send, single item)", texts[0])
	}
	m.batchItems = items
	m.batchSummaries = texts
	if batch {
		m.batchInFlight = true
		m.batchRespawned = false
		m.batchSentAt = time.Now()
		if len(boardIDs) > 0 {
			m.batchDoneIDs = map[string]bool{}
			for _, id := range boardIDs {
				m.batchDoneIDs[id] = true
			}
		}
	}
	b := m.backend
	send := func() tea.Msg {
		if !batch && strings.HasPrefix(texts[0], "/") {
			return slashMsg{text: texts[0]}
		}
		if b != nil {
			if err := sendChat(b, sendText, batchAtts); err != nil {
				// no cleanup: a respawn retry (queueSendErrMsg) may still
				// need the files; IT owns the cleanup on terminal failure.
				return queueSendErrMsg{err: err, items: items, batch: batch, retry: false}
			}
			cleanupEntries(items)
		}
		return chatSentMsg{text: sendText}
	}
	return send
}

// resendBatchCmd — the ONE failure respawn: ResetPrimary(true) then resend
// the SAME composed batch (attachments included) on the fresh session.
// Errors come back with retry=true so the loop can never respawn twice for
// one flush call.
func (m *Model) resendBatchCmd(items []queueEntry) tea.Cmd {
	text := composeBatch(items)
	var atts []state.Attachment
	for _, it := range items {
		atts = append(atts, it.atts...)
	}
	b := m.backend
	tb, _ := m.team()
	return func() tea.Msg {
		if tb != nil {
			if err := tb.ResetPrimary(true); err != nil {
				return queueSendErrMsg{err: fmt.Errorf("respawn: %w", err), batch: true, retry: true}
			}
		}
		if b != nil {
			if err := sendChat(b, text, atts); err != nil {
				return queueSendErrMsg{err: err, items: items, batch: true, retry: true}
			}
			cleanupEntries(items)
		}
		return chatSentMsg{text: text}
	}
}

// handlePermissionEvent opens/closes the boss permission prompt. Boss/primary
// requests (pending ToolState) REPLACE the textarea with the answer modal;
// "resolved" closes the matching open (or esc'd) prompt silently. Child-
// session requests never open a modal — they surface as an activity line
// (describeEvent) plus the backend's usual floor blocked sprite.
func (m *Model) handlePermissionEvent(ev state.Event) {
	if ev.ToolState == "resolved" {
		if m.perm != nil && m.perm.ID == ev.PermissionID {
			m.perm = nil
			m.chat.SetPermission(nil)
		}
		if m.permEscd != nil && m.permEscd.ID == ev.PermissionID {
			m.permEscd = nil
		}
		return
	}
	if ev.EmployeeName != "boss" && ev.EmployeeName != "" {
		return // child permission: activity line only, no modal
	}
	m.perm = &permPrompt{ID: ev.PermissionID, ToolName: ev.ToolName, Summary: ev.ToolSummary}
	m.playSound("alert") // boss permission modal opening
	m.chat.SetPermission(&panels.PermissionView{
		ID:       ev.PermissionID,
		ToolName: ev.ToolName,
		Summary:  ev.ToolSummary,
	})
}

// handleQuestionEvent opens/closes the boss question hold. Boss/primary
// requests (pending ToolState) park the turn: the open hold REPLACES the
// textarea with a free-text answer modal (opencode waits at the question
// reply API — typing a chat message would queue but never resume it, the
// reported deadlock). "resolved" closes the matching hold silently.
// Employee questions never open a modal — activity line only, like
// employee thoughts/permissions.
func (m *Model) handleQuestionEvent(ev state.Event) {
	if ev.ToolState == "resolved" {
		if m.question != nil && hasQuestionID(m.question.IDs, ev.QuestionID) {
			m.question = nil
			m.chat.SetQuestion(nil)
		}
		if m.questionEscd != nil && hasQuestionID(m.questionEscd.IDs, ev.QuestionID) {
			m.questionEscd = nil
		}
		return
	}
	if ev.EmployeeName != "" && ev.EmployeeName != "boss" {
		return // child question: activity line only, no modal
	}
	if ev.QuestionID == "" {
		return
	}
	// v1: while ANY hold is outstanding, a fresh pending boss question is
	// folded into it — a question call batching several QuestionIDs emits
	// one event per id; one typed answer unblocks every batched id.
	if m.question != nil {
		m.question.IDs = append(m.question.IDs, ev.QuestionID)
		return
	}
	if m.questionEscd != nil {
		m.questionEscd.IDs = append(m.questionEscd.IDs, ev.QuestionID)
		return
	}
	m.parkForQuestion()
	m.question = &questionHold{IDs: []string{ev.QuestionID},
		Text: ev.Text, Options: ev.ToolSummary}
	m.chat.SetQuestion(&panels.QuestionView{
		ID:      ev.QuestionID,
		Text:    ev.Text,
		Options: ev.ToolSummary,
	})
}

// parkForQuestion marks the turn parked at the question reply API: any
// pending boss typing placeholder ("boss-N") is dropped (the turn is
// WAITING, not typing), the status line reads "waiting for your answer",
// and the chat placeholder/enqueue gate flips to question-waiting.
func (m *Model) parkForQuestion() {
	m.questionParked = true
	kept := make([]state.ChatMsg, 0, len(m.st.Chat))
	for _, c := range m.st.Chat {
		if c.From == "boss" && c.Pending && strings.HasPrefix(c.ID, "boss-") {
			continue
		}
		kept = append(kept, c)
	}
	m.st.Chat = kept
	m.parkedStatus = m.st.StatusLine
	m.st.StatusLine = "[question] boss is waiting for your answer…"
	if m.chat != nil {
		m.chat.SetQuestionWaiting(true)
	}
}

func hasQuestionID(ids []string, id string) bool {
	for _, i := range ids {
		if i == id {
			return true
		}
	}
	return false
}

// compact — the live layout mode: the /compact session override beats
// brain.json ui.compact; compactLive=0 inherits the config.
func (m Model) compact() bool {
	switch m.compactLive {
	case 1:
		return true
	case 2:
		return false
	}
	return m.cfg.UI.Compact
}

// applyLayout pushes the current mode/UI config into every affected
// surface: tab-bar label density, the chat input rows, and the sidebar
// width. Called at build time and after /compact, /mode and /wide.
func (m *Model) applyLayout() {
	compact := m.compact()
	m.tabs.SetCompact(compact)
	m.chat.SetCompact(compact)
	if m.width > 0 {
		m.resize(m.width, m.height)
	}
}

// sidebarBase — the configured sidebar width before the narrow-terminal
// degrade: ui.sidebarWidth clamped to 26..80 wins outright; else the
// compact layout takes 30; else the 68-col default.
func (m Model) sidebarBase() int {
	if n := m.cfg.UI.SidebarWidth; n != 0 {
		if n < sidebarMin {
			n = sidebarMin
		}
		if n > sidebarMax {
			n = sidebarMax
		}
		return n
	}
	if m.compact() {
		return compactSidebarW
	}
	return defaultSidebarW
}

func (m *Model) resize(w, h int) {
	if w < minCols {
		w = minCols
	}
	if h < minRows {
		h = minRows
	}
	m.width, m.height = w, h
	m.middleH = h - 2
	if m.middleH < 1 {
		m.middleH = 1
	}
	base := m.sidebarBase()
	sw := base
	if w < degradeCols {
		// degrade gracefully: narrow terminals get a narrow sidebar
		sw = w / 3
		if sw < 20 {
			sw = 20
		}
		if sw > base {
			sw = base
		}
	}
	if w-sw < 8 {
		sw = w - 8
	}
	m.sidebar = sw
	m.floorW = w - sw
	m.tabs.SetSize(sw, m.middleH)
}

// --- reducer (exact port of node-legacy officeReducer + initialState) ------

func initialState(mode state.Mode) state.OfficeState {
	return state.OfficeState{
		Employees: []state.Employee{
			{ID: "manager", Name: "boss", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk},
			{ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr", Sprite: state.SpriteAtDesk},
		},
		Mode:       mode,
		StatusLine: fmt.Sprintf("[grafeio] %s - booting...", string(mode)),
	}
}

func capList[T any](list []T, maxN int) []T {
	if len(list) > maxN {
		return list[len(list)-maxN:]
	}
	return list
}

// appendChat clones-and-appends one message (chat is never aliased with the
// previous state).
func appendChat(chat []state.ChatMsg, msg state.ChatMsg) []state.ChatMsg {
	return append(append([]state.ChatMsg(nil), chat...), msg)
}

// capChat enforces the global chat cap AND the per-kind caps, so a stream of
// thinking/tool entries can't drown out the conversation (each survives at
// thinkCap/toolCap, oldest of the kind drops first).
func capChat(chat []state.ChatMsg) []state.ChatMsg {
	chat = capList(chat, chatCap)
	chat = capKind(chat, "think", thinkCap)
	chat = capKind(chat, "tool", toolCap)
	return chat
}

// capKind keeps at most maxN entries of the given Kind, dropping the oldest.
func capKind(chat []state.ChatMsg, kind string, maxN int) []state.ChatMsg {
	n := 0
	for _, m := range chat {
		if m.Kind == kind {
			n++
		}
	}
	if n <= maxN {
		return chat
	}
	drop := n - maxN
	out := make([]state.ChatMsg, 0, len(chat)-drop)
	for _, m := range chat {
		if m.Kind == kind && drop > 0 {
			drop--
			continue
		}
		out = append(out, m)
	}
	return out
}

func upsertTask(tasks []state.BoardTask, task state.BoardTask) []state.BoardTask {
	for i, t := range tasks {
		if t.ID == task.ID {
			next := append([]state.BoardTask(nil), tasks...)
			next[i] = task
			return next
		}
	}
	return append(tasks, task)
}

func findEmployee(st state.OfficeState, id string) *state.Employee {
	for i := range st.Employees {
		if st.Employees[i].ID == id {
			return &st.Employees[i]
		}
	}
	return nil
}

func setEmployee(st state.OfficeState, id string, fn func(e *state.Employee)) state.OfficeState {
	for i := range st.Employees {
		if st.Employees[i].ID == id {
			fn(&st.Employees[i])
		}
	}
	return st
}

func reducer(st state.OfficeState, ev state.Event) state.OfficeState {
	switch ev.Kind {
	case state.EvTick:
		{
			tick := st.Tick + 1
			// drop expired balloons
			var bubbles []state.SpeechBubble
			for _, b := range st.Bubbles {
				if b.UntilTick > tick {
					bubbles = append(bubbles, b)
				}
			}
			st.Tick = tick
			st.Bubbles = bubbles
			// The legacy "every 140 ticks" chatter generator is gone — the
			// SocialClock (ambient.go) owns ALL self-originated floor chatter
			// now, planning beats off each EvTick in the model (its (d)
			// water-cooler covers the old solo case; the old line bank moved
			// to socialSoloBank). Explicit EvBubble backend events unchanged.
			return office.AdvanceSprites(st)
		}

	case state.EvHire:
		for _, e := range st.Employees {
			if e.ID == ev.Employee.ID {
				return st // id dedup — already on roster
			}
		}
		taken := make(map[string]bool, len(st.Employees))
		for _, e := range st.Employees {
			taken[e.Seat] = true
		}
		emp := ev.Employee
		emp.Seat = office.AssignSeat(taken, emp.Role)
		return officeStateWithEmployees(st, append(append([]state.Employee(nil), st.Employees...), emp))

	case state.EvFire:
		var emps []state.Employee
		var bubbles []state.SpeechBubble
		for _, e := range st.Employees {
			if e.ID != ev.EmployeeID {
				emps = append(emps, e)
			}
		}
		for _, b := range st.Bubbles {
			if b.EmployeeID != ev.EmployeeID {
				bubbles = append(bubbles, b)
			}
		}
		st.Employees = emps
		st.Bubbles = bubbles
		return st

	case state.EvDispatch:
		{
			ownerName := ev.Task.Owner
			if owner := findEmployee(st, ev.EmployeeID); owner != nil {
				ownerName = owner.Name
			}
			task := ev.Task
			task.Status = state.TaskInProgress
			task.Owner = ownerName
			st.Tasks = upsertTask(st.Tasks, task)
			st = setEmployee(st, ev.EmployeeID, func(e *state.Employee) {
				e.Sprite = state.SpriteToManager
				e.Task = task.Title
			})
			return st
		}

	case state.EvWorking:
		ownerName := ""
		if owner := findEmployee(st, ev.EmployeeID); owner != nil {
			ownerName = owner.Name
		}
		st = setEmployee(st, ev.EmployeeID, func(e *state.Employee) {
			e.Sprite = state.SpriteWorking
		})
		if ev.TaskID != "" {
			for i := range st.Tasks {
				if st.Tasks[i].ID == ev.TaskID {
					st.Tasks[i].Status = state.TaskInProgress
					if st.Tasks[i].Owner == "" {
						st.Tasks[i].Owner = ownerName
					}
				}
			}
		}
		return st

	case state.EvReturned:
		st = setEmployee(st, ev.EmployeeID, func(e *state.Employee) {
			e.Sprite = state.SpriteToDesk
			e.Task = ""
		})
		for i := range st.Tasks {
			if st.Tasks[i].ID == ev.TaskID {
				st.Tasks[i].Status = state.TaskDone
			}
		}
		st.Mails = capList(append(append([]state.MailItem(nil), st.Mails...), ev.Mail), mailCap)
		return st

	case state.EvIdleDrift:
		return setEmployee(st, ev.EmployeeID, func(e *state.Employee) {
			e.Sprite = state.SpriteToCoffee
		})

	case state.EvBlocked:
		st = setEmployee(st, ev.EmployeeID, func(e *state.Employee) {
			e.Sprite = state.SpriteAtMailbox
		})
		st.StatusLine = fmt.Sprintf("[blocked] %s", ev.Text)
		return st

	case state.EvTask:
		st.Tasks = upsertTask(st.Tasks, ev.Task)
		return st

	case state.EvMail:
		st.Mails = capList(append(append([]state.MailItem(nil), st.Mails...), ev.Mail), mailCap)
		return st

	case state.EvChatUser:
		st.Chat = capChat(appendChat(st.Chat, ev.Msg))
		return st

	case state.EvChatBoss:
		st.BossThinking = false // a boss turn ends the thinking affordance

		// typingPlaceholder — the Send-sequenced "boss-N" pending bubble.
		// Real bubbles (stream deltas + the pinned final) carry their own
		// stable ID ("bossmsg-"+messageID) and are never placeholders.
		isPlaceholder := func(m state.ChatMsg) bool {
			return m.From == "boss" && m.Pending && strings.HasPrefix(m.ID, "boss-")
		}
		msg := ev.Msg

		// (a) replace-in-place by ID: streaming deltas land on their stable
		// ID and grow the same bubble; the final re-emits that ID with
		// Pending=false; a duplicated completed event is idempotent. The
		// swap is one atomic slice — the chat count never inflates mid-stream.
		if msg.ID != "" {
			for i, m := range st.Chat {
				if m.ID == msg.ID {
					next := append([]state.ChatMsg(nil), st.Chat...)
					next[i] = msg
					st.Chat = capChat(next)
					return st
				}
			}
		}

		// (b) a fresh typing placeholder appends as-is; it gets replaced by
		// the FIRST real bubble of its reply cycle (branch below).
		if isPlaceholder(msg) {
			st.Chat = capChat(appendChat(st.Chat, msg))
			return st
		}

		// (c) a new real boss bubble: strip every remaining "boss-N" typing
		// placeholder of the send cycle, then append.
		rest := make([]state.ChatMsg, 0, len(st.Chat)+1)
		for _, mgr := range st.Chat {
			if !isPlaceholder(mgr) {
				rest = append(rest, mgr)
			}
		}
		st.Chat = capChat(append(rest, msg))
		return st

	case state.EvThought:
		{
			// boss thoughts: thinking flag + a chat entry (Kind "think", cap 20)
			// keyed by CallID — mid-stream updates REPLACE the entry in place
			// (accumulated text), Done=true is the final update; the model's
			// activeThink set decides streaming vs collapsed at render.
			// employee thoughts: activity line only, no chat.
			if ev.EmployeeID != "boss" {
				return st
			}
			st.BossThinking = !ev.Done
			id := "think-" + ev.CallID
			if ev.CallID == "" {
				// no id to key on — legacy emitters stay append-only
				id = "think-" + nextMsgID()
			}
			entry := state.ChatMsg{
				ID:   id,
				From: "boss",
				Kind: "think",
				Text: ev.Text,
				Meta: ev.CallID, // renderer reads the CallID back from Meta
				At:   time.Now().UnixMilli(),
			}
			next := append([]state.ChatMsg(nil), st.Chat...)
			merged := false
			for i, msg := range next {
				if msg.Kind == "think" && msg.ID == entry.ID {
					next[i] = entry
					merged = true
					break
				}
			}
			if !merged {
				next = append(next, entry)
			}
			st.Chat = capChat(next)
			return st
		}

	case state.EvTool:
		{
			// tool one-liners merge by CallID: running → done replaces the line.
			// BOSS/primary tools keep the classic inline "tool" Kind;
			// EMPLOYEE tools get Kind "wtool" — the chat panel lifts them
			// out of the flow into the per-agent workers-thread region at
			// the end (P2), so a sub-agent storm can't drown the boss
			// conversation. Their Meta carries the tool state plus the
			// latest activity tick (␟ separator) for the staleness
			// auto-collapse; merging is scoped per agent+CallID.
			name := ev.EmployeeName
			if name == "" {
				name = "boss"
			}
			kind := "tool"
			id := "tool-" + ev.CallID
			meta := ev.ToolState
			if name != "boss" {
				kind = "wtool"
				id = "wtool-" + name + "-" + ev.CallID
				meta = ev.ToolState + "\x1f" + strconv.Itoa(st.Tick)
			}
			text := ev.ToolName
			if ev.ToolSummary != "" {
				text += " · " + ev.ToolSummary
			}
			line := state.ChatMsg{
				ID:   id,
				From: name,
				Kind: kind,
				Text: strings.ReplaceAll(text, "\n", " "), // chat rows are one-liners
				Meta: meta,
				At:   time.Now().UnixMilli(),
			}
			merged := false
			next := append([]state.ChatMsg(nil), st.Chat...)
			for i, msg := range next {
				if msg.Kind == line.Kind && msg.ID == line.ID {
					next[i] = line
					merged = true
					break
				}
			}
			if !merged {
				next = append(next, line)
			}
			st.Chat = capChat(next)
			return st
		}

	case state.EvQuestion:
		{
			// a resolved boss question MUTATES the original Kind "question"
			// chat entry in place: Meta gains a trailing "␟answered" unit-
			// separator token (the options stay ahead of it) so the panel
			// renders the dim "✓ answered" suffix instead of the hint.
			if ev.ToolState == "resolved" && ev.QuestionID != "" {
				id := "q-" + ev.QuestionID
				for i, m := range st.Chat {
					if m.Kind == "question" && m.ID == id &&
						!strings.HasSuffix(m.Meta, "\x1fanswered") {
						next := append([]state.ChatMsg(nil), st.Chat...)
						next[i].Meta = m.Meta + "\x1fanswered"
						st.Chat = next
						break
					}
				}
				return st
			}
			// boss questions: Kind "question" chat entry (yellow "boss asks ›").
			// Employee questions are activity-line only (describeEvent), like
			// employee thoughts — the deep-work stream belongs to the boss.
			if ev.EmployeeName != "" && ev.EmployeeName != "boss" {
				return st
			}
			id := "q-" + ev.QuestionID
			if ev.QuestionID == "" {
				id = "q-" + nextMsgID()
			}
			st.Chat = capChat(appendChat(st.Chat, state.ChatMsg{
				ID:   id,
				From: "boss",
				Kind: "question",
				Text: ev.Text,
				// options ride in Meta for the renderer ("a | b | c")
				Meta: ev.ToolSummary,
				At:   time.Now().UnixMilli(),
			}))
			return st
		}

	case state.EvFileDiff:
		{
			name := ev.EmployeeName
			if name == "" {
				name = "boss"
			}
			st.Chat = capChat(appendChat(st.Chat, state.ChatMsg{
				ID:   "diff-" + nextMsgID(),
				From: name,
				Kind: "diff",
				Text: ev.DiffBody,
				// Meta carrier for the collapsed header:
				// path ␟ +adds ␟ -dels (unit separator; panels parses it back)
				Meta: fmt.Sprintf("%s\x1f+%d\x1f-%d", ev.DiffPath, ev.DiffAdd, ev.DiffDel),
				At:   time.Now().UnixMilli(),
			}))
			return st
		}

	case state.EvBubble:
		ttl := ev.TTL
		if ttl == 0 {
			ttl = 40
		}
		bubble := state.SpeechBubble{
			ID:         fmt.Sprintf("bbl-%d-%05d", st.Tick, rand.Intn(100000)),
			EmployeeID: ev.EmployeeID,
			Text:       ev.Text,
			UntilTick:  st.Tick + ttl,
		}
		st.Bubbles = capList(append(append([]state.SpeechBubble(nil), st.Bubbles...), bubble), bubbleCap)
		return st

	case state.EvStatus:
		st.StatusLine = ev.Text
		return st
	}
	return st
}

func officeStateWithEmployees(st state.OfficeState, emps []state.Employee) state.OfficeState {
	st.Employees = emps
	return st
}

func hasPendingBoss(st state.OfficeState) bool {
	for _, m := range st.Chat {
		if m.From == "boss" && m.Pending {
			return true
		}
	}
	return false
}

// --- chat send path ---------------------------------------------------------

var msgSeq atomic.Int64

func nextMsgID() string {
	return fmt.Sprintf("c%d", msgSeq.Add(1))
}

// --- slash commands (local, never sent to the backend) ---------------------

// slashHelp is the /help notice body (office-rendered, dim).
const slashHelp = `commands:
  /help              this list
  /clear             empty the chat
  /theme <name>      switch theme (persists)
  /themes            list themes
  /power [mode]      show/set the power governor (auto|performance|saver)
  /model [ref]       show/set the boss model (provider/model)
  /thinking on|off   show/hide thinking blocks
  /tools on|off      show/hide tool one-liners
  /diffs on|off      expand/collapse file diffs (ctrl+d toggles)
  /compact on|off    compact layout this session (narrow sidebar, short tabs)
  /mode normal|compact  layout mode (persists)
  /wide <n>          sidebar width 26..80 (0 = default 68, persists)
  /zen               fullscreen floor, any key exits (transient)
  /focus floor       alias of /zen
  /queue             show the backlog (numbered items batched on flush)
  /queue clear       drop all queued backlog items
  /route             force-dispatch the backlog now (bypasses the busy gate)
  @<file>            attach file (popover picker) · ctrl+v pastes images
  /perm              re-open an esc'd permission prompt
  /question          re-open a deferred boss question
  /status            office status
  /quit              exit grafeio`

// applySlash dispatches one slash command. Slash input never echoes as
// chat-user; every outcome surfaces as a From "office" chat notice.
func (m *Model) applySlash(input string) tea.Cmd {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.ToLower(fields[0])
	switch cmd {
	case "/help":
		m.notice(slashHelp)
	case "/clear":
		m.st.Chat = nil
		if m.chat != nil {
			m.chat.ClearAttachments() // staged chips die with the visible chat
		}
		m.tabs.SetState(m.st)
	case "/theme":
		if len(fields) < 2 {
			m.noticeErr("/theme: usage /theme <name>  (" + strings.Join(chrome.ThemeNames(), ", ") + ")")
			return nil
		}
		name := fields[1]
		if !chrome.SetTheme(name) {
			m.noticeErr(fmt.Sprintf("/theme: unknown theme %q (/themes)", name))
			return nil
		}
		_ = chrome.PersistTheme() // best effort
		office.SetTheme(name)     // floor palette follows chrome
		m.chat.RefreshTheme()
		m.tabs.SetState(m.st)
		m.notice("theme → " + chrome.CurrentTheme().Name)
	case "/themes":
		m.notice("themes: " + strings.Join(chrome.ThemeNames(), "  ") +
			"  (current: " + chrome.CurrentTheme().Name + ")")
	case "/power":
		if len(fields) < 2 {
			m.notice(fmt.Sprintf("power: %s (%s) · current tick %s — /power auto|performance|saver",
				PowerMode(m.cfg), powerDescribe(PowerMode(m.cfg)), m.currentTick()))
			return nil
		}
		mode := config.PowerMode(strings.ToLower(fields[1]))
		switch mode {
		case config.PowerAuto, config.PowerPerformance, config.PowerSaver:
		default:
			m.noticeErr(fmt.Sprintf("/power: unknown mode %q (auto|performance|saver)", fields[1]))
			return nil
		}
		m.cfg.UI.Power = mode
		m.notice(fmt.Sprintf("power → %s (%s) · current tick %s · %s",
			mode, powerDescribe(mode), m.currentTick(), m.persistCfg()))
	case "/model":
		if len(fields) < 2 {
			cur := string(m.cfg.Boss.Model)
			if cur == "" {
				cur = "server default"
			}
			m.notice(fmt.Sprintf("boss model: %s — set with /model provider/model (the backend honors it on the next send)", cur))
			return nil
		}
		ref := fields[1]
		if !strings.Contains(ref, "/") {
			m.noticeErr("/model: usage /model provider/model (e.g. anthropic/claude-haiku-4-5)")
			return nil
		}
		m.cfg.Boss.Model = config.ModelRef(ref)
		m.notice(fmt.Sprintf("boss model → %s (the backend honors it on the next send) · %s", ref, m.persistCfg()))
	case "/thinking":
		m.applyToggle("/thinking", fields, func(on bool) {
			m.chat.SetShowThinking(on)
		})
	case "/tools":
		m.applyToggle("/tools", fields, func(on bool) {
			m.chat.SetShowTools(on)
		})
	case "/diffs":
		m.applyToggle("/diffs", fields, func(on bool) {
			m.chat.SetDiffsExpanded(on)
		})
	case "/compact":
		// live toggle — session override only (/mode persists the choice)
		if len(fields) < 2 || (fields[1] != "on" && fields[1] != "off") {
			m.noticeErr("/compact: usage /compact on|off  (/mode normal|compact persists)")
			return nil
		}
		if fields[1] == "on" {
			m.compactLive = 1
		} else {
			m.compactLive = 2
		}
		m.applyLayout()
		m.notice(fmt.Sprintf("compact → %s (this session · /mode %s persists)",
			fields[1], fields[1]))
	case "/mode":
		if len(fields) < 2 || (fields[1] != "normal" && fields[1] != "compact") {
			m.noticeErr("/mode: usage /mode normal|compact")
			return nil
		}
		m.cfg.UI.Compact = fields[1] == "compact"
		m.compactLive = 0 // cfg is the source again
		m.applyLayout()
		m.notice(fmt.Sprintf("layout mode → %s · %s", fields[1], m.persistCfg()))
	case "/wide":
		if len(fields) < 2 {
			m.noticeErr(fmt.Sprintf("/wide: usage /wide <%d..%d> (0 = default %d)", sidebarMin, sidebarMax, defaultSidebarW))
			return nil
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			m.noticeErr(fmt.Sprintf("/wide: %q is not a number (26..80, 0 = default)", fields[1]))
			return nil
		}
		if n < 0 {
			n = 0
		}
		if n > sidebarMax {
			m.notice(fmt.Sprintf("/wide: %d over the %d-col cap — clamped", n, sidebarMax))
			n = sidebarMax
		} else if n != 0 && n < sidebarMin {
			m.notice(fmt.Sprintf("/wide: %d under the %d-col floor — clamped", n, sidebarMin))
			n = sidebarMin
		}
		m.cfg.UI.SidebarWidth = n
		m.applyLayout()
		shown := n
		if n == 0 {
			shown = defaultSidebarW
			if m.compact() {
				shown = compactSidebarW
			}
		}
		m.notice(fmt.Sprintf("sidebar → %d cols · %s", shown, m.persistCfg()))
	case "/zen":
		// transient focus session — intentionally NOT persisted
		m.zen = true
	case "/focus":
		if len(fields) < 2 || fields[1] != "floor" {
			m.noticeErr("/focus: usage /focus floor  (alias of /zen)")
			return nil
		}
		m.zen = true
	case "/queue":
		if len(fields) >= 2 {
			if fields[1] == "clear" {
				// dropping the items also drops their sends — the temp
				// dirs go now (no flush is coming for them).
				cleanupEntries(m.queue)
				m.queue = nil
				if m.chat != nil {
					m.chat.SetQueueLen(0)
				}
				m.notice("backlog cleared")
			} else {
				m.noticeErr("/queue: usage /queue | /queue clear")
			}
			return nil
		}
		if len(m.queue) == 0 {
			m.notice("backlog empty — type while the boss is typing to queue an item")
			return nil
		}
		var sb strings.Builder
		if len(m.queue) > 1 {
			fmt.Fprintf(&sb, "%d queued (will batch-dispatch on flush):", len(m.queue))
		} else {
			fmt.Fprintf(&sb, "1 queued (sends on flush):")
		}
		for i, e := range m.queue {
			fmt.Fprintf(&sb, "\n  %d. %s", i+1, e.text)
			if n := len(e.atts); n > 0 {
				// same machine suffix the batch prompt gets
				fmt.Fprintf(&sb, " 📎%d", n)
			}
		}
		m.notice(sb.String())
	case "/route":
		// force the backlog out NOW, bypassing the busy gate. A parked
		// question hold stays blocking — a chat Send is what deadlocks the
		// parked loop (the answer must go through AnswerQuestion first).
		if m.questionParked || m.question != nil || m.questionEscd != nil {
			m.notice("/route: boss is waiting on your answer — answer the question first (/question)")
			return nil
		}
		if len(m.queue) == 0 {
			m.notice("nothing queued — type while the boss is typing to enqueue")
			return nil
		}
		return m.dispatchQueued(true)
	case "/perm":
		if m.permEscd == nil {
			m.notice("no pending permission (/perm re-opens an esc'd prompt)")
			return nil
		}
		m.perm = m.permEscd
		m.permEscd = nil
		m.chat.SetPermission(&panels.PermissionView{
			ID:       m.perm.ID,
			ToolName: m.perm.ToolName,
			Summary:  m.perm.Summary,
		})
	case "/question":
		if m.question != nil {
			m.notice("boss question is open — answer it or esc to defer")
			return nil
		}
		if m.questionEscd == nil || len(m.questionEscd.IDs) == 0 {
			m.notice("no deferred boss question (/question re-opens a deferred question)")
			return nil
		}
		m.question = m.questionEscd
		m.questionEscd = nil
		m.chat.SetQuestion(&panels.QuestionView{
			ID:      m.question.IDs[0],
			Text:    m.question.Text,
			Options: m.question.Options,
		})
	case "/status":
		var pend, doing, done int
		for _, t := range m.st.Tasks {
			switch t.Status {
			case state.TaskPending:
				pend++
			case state.TaskInProgress:
				doing++
			case state.TaskDone:
				done++
			}
		}
		m.notice(fmt.Sprintf("mode %s · theme %s · power %s · agents %d · board %d/%d/%d\n%s",
			m.st.Mode, chrome.CurrentTheme().Name, PowerMode(m.cfg), len(m.st.Employees),
			pend, doing, done, m.st.StatusLine))
	case "/quit":
		m.closeTerminal()
		return tea.Quit
	default:
		m.noticeErr(fmt.Sprintf("/ %s: no such command (/help)", strings.TrimPrefix(cmd, "/")))
	}
	return nil
}

// applyToggle parses "on|off" for a two-state slash command.
func (m *Model) applyToggle(name string, fields []string, set func(bool)) {
	if len(fields) < 2 || (fields[1] != "on" && fields[1] != "off") {
		m.noticeErr(name + ": usage " + name + " on|off")
		return
	}
	on := fields[1] == "on"
	set(on)
	stateWord := "on"
	if !on {
		stateWord = "off (hidden)"
	}
	m.tabs.SetState(m.st)
	m.notice(name + " → " + stateWord)
}

// persistCfg — brain.json write-through after an in-app mutation (/power,
// /model), best effort: the return string is the trailing word in the
// notice ("saved to brain.json" / the failure).
func (m *Model) persistCfg() string {
	if m.cfg == nil {
		return "in-memory config — not persisted"
	}
	if err := config.Save(m.cfg); err != nil {
		return "brain.json save failed: " + err.Error()
	}
	return "saved to brain.json"
}

// notice appends a dim local notice (From "office") to the chat.
func (m *Model) notice(text string) {
	m.appendNotice(text, "")
}

// noticeErr appends a red local notice (From "office", Meta "error").
func (m *Model) noticeErr(text string) {
	m.appendNotice(text, "error")
}

func (m *Model) appendNotice(text, meta string) {
	m.st.Chat = capChat(appendChat(m.st.Chat, state.ChatMsg{
		ID:   nextMsgID(),
		From: "office",
		Text: text,
		Meta: meta,
		At:   time.Now().UnixMilli(),
	}))
	m.tabs.SetState(m.st)
}

// --- activity descriptions --------------------------------------------------

// describeEvent formats the one-line activity entry for a processed event,
// timestamped with the office clock. Ticks never reach this (filtered
// above); every other event kind leaves a trace.
func (m *Model) describeEvent(ev state.Event) string {
	stamp := chrome.OfficeClock(m.st.Tick)
	var what string
	switch ev.Kind {
	case state.EvHire:
		what = fmt.Sprintf("hire %s (%s)", ev.Employee.Name, ev.Employee.Role)
	case state.EvFire:
		what = fmt.Sprintf("fire %s", ev.EmployeeID)
	case state.EvDispatch:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("dispatch → %s «%s»", name, ev.Task.Title)
	case state.EvWorking:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("working — %s", name)
	case state.EvReturned:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("returned ← %s «%s»", name, ev.Mail.Subject)
	case state.EvIdleDrift:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("coffee — %s", name)
	case state.EvBlocked:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("BLOCKED %s — %s", name, ev.Text)
	case state.EvTask:
		what = fmt.Sprintf("task upsert «%s» (%s)", ev.Task.Title, ev.Task.Status)
	case state.EvMail:
		what = fmt.Sprintf("mail %s→%s «%s»", ev.Mail.From, ev.Mail.To, ev.Mail.Subject)
	case state.EvChatUser:
		what = "you › " + ev.Msg.Text
	case state.EvChatBoss:
		if ev.Msg.Pending {
			what = "boss › typing…"
		} else {
			what = "boss › reply"
		}
	case state.EvBubble:
		name := ev.EmployeeID
		if e := findEmployee(m.st, ev.EmployeeID); e != nil {
			name = e.Name
		}
		what = fmt.Sprintf("%s says %q", name, ev.Text)
	case state.EvThought:
		name := ev.EmployeeName
		if name == "" {
			name = ev.EmployeeID
		}
		what = "think — " + name + ": " + clipRunes(ev.Text, 60)
	case state.EvTool:
		name := ev.EmployeeName
		if name == "" {
			name = "boss"
		}
		toolState := ev.ToolState
		if toolState == "" {
			toolState = "running"
		}
		text := ev.ToolName
		if ev.ToolSummary != "" {
			text += " · " + ev.ToolSummary
		}
		what = fmt.Sprintf("tool — %s: %s (%s)", name, text, toolState)
	case state.EvPermission:
		name := ev.EmployeeName
		if name == "" {
			name = "boss"
		}
		toolState := ev.ToolState
		if toolState == "" {
			toolState = "pending"
		}
		text := ev.ToolName
		if ev.ToolSummary != "" {
			text += " · " + ev.ToolSummary
		}
		what = fmt.Sprintf("permission — %s: %s (%s)", name, text, toolState)
	case state.EvQuestion:
		name := ev.EmployeeName
		if name == "" {
			name = "boss"
		}
		what = "question — " + name + ": " + clipRunes(ev.Text, 60)
	case state.EvFileDiff:
		name := ev.EmployeeName
		if name == "" {
			name = "boss"
		}
		what = fmt.Sprintf("diff — %s: %s +%d -%d", name, ev.DiffPath, ev.DiffAdd, ev.DiffDel)
	case state.EvStatus:
		what = "status — " + ev.Text
	default:
		what = string(ev.Kind)
	}
	// keep each row single-line for the log
	what = strings.ReplaceAll(what, "\n", " ")
	return fmt.Sprintf("[%s] %s", stamp, what)
}

// clipRunes truncates machine text (activity descriptions) to n runes with
// an ellipsis — display layout, not NL.
func clipRunes(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
