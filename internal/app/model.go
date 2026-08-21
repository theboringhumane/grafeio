// Package app — the root Bubble Tea model for Grafeio v2: state reducer
// (exact port of node-legacy/src/app.tsx officeReducer + initialState),
// layout, key routing, and the backend event seam.
//
// Layout: topbar (1) | middle (floor left flex | right sidebar 44) | statusbar (1).
// Events arrive as state.Event tea.Msgs (backend goroutine → tea.Program.Send);
// the 180ms animation tick is a tea.Tick loop emitting state.Event{Kind: EvTick}.
package app

import (
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/office"
	"github.com/theboringhumane/grafeio/internal/panels"
	"github.com/theboringhumane/grafeio/internal/state"
)

const (
	mailCap      = 30
	chatCap      = 30
	thinkCap     = 20  // thinking blocks kept in chat
	toolCap      = 20  // tool one-liners kept in chat
	bubbleCap    = 3   // never more than 3 concurrent balloons (drop oldest)
	sidebarW     = 44  // right tab sidebar, when the terminal is wide enough
	degradeCols  = 100 // below this, the sidebar shrinks instead of the floor
	minCols      = 40
	minRows      = 12
	tickInterval = 180 * time.Millisecond
	ambientEvery = 140 // ticks between ambient bubbles

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

// queueEntry — one backlog item: the typed text plus the board row id
// QueueItemStart handed back ("" when the backend has no team seam).
type queueEntry struct {
	text    string
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

// ambientLines — pure ASCII; the floor staff typed these, not a program.
var ambientLines = []string{
	"big day. lots of meetings.",
	"shipping friday.",
	"who took the red mug?",
	"standup in 5.",
	"this diff is a crime scene.",
	"coffee machine is empty again.",
	"review queue is deep today.",
	"anyone seen the staging key?",
}

// Model is the tea.Model for the whole app.
type Model struct {
	backend state.Backend
	st      state.OfficeState

	width, height int
	middleH       int
	sidebar       int
	floorW        int
	tabs          *panels.Tabs
	chat          *panels.Chat
	activity      *panels.Activity
	keys          KeyMap

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
// text joins the model-level queue instead of reaching the backend.
type enqueueMsg struct{ text string }

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

// New builds the app around a backend. backend.Start is NOT called here —
// main owns that (goroutine → tea.Program.Send).
func New(b state.Backend) Model {
	chat := panels.NewChat(func(text string) tea.Cmd {
		return func() tea.Msg {
			// Slash commands dispatch locally, never touch the backend, and
			// never echo as chat-user.
			if strings.HasPrefix(text, "/") {
				return slashMsg{text: text}
			}
			if b != nil {
				if err := b.Send(text); err != nil {
					return sendErrMsg{err: err}
				}
			}
			return chatSentMsg{text: text}
		}
	})
	activity := panels.NewActivity()
	m := Model{
		backend:     b,
		st:          initialState(b.Mode()),
		chat:        chat,
		activity:    activity,
		activeThink: map[string]bool{},
		tabs: panels.NewTabs(
			chat,
			panels.NewAgents(),
			panels.NewBoard(),
			panels.NewMail(),
			activity,
		),
		keys: NewKeyMap(),
	}
	// Queue + permission seams: the panel owns the keys, the model owns the
	// queue/prompt state; callbacks ferry over tea.Msgs so the model value
	// copy in Update stays the single writer.
	chat.SetEnqueue(func(text string) tea.Cmd {
		return func() tea.Msg { return enqueueMsg{text: text} }
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
	m.tabs.SetState(m.st)
	return m
}

// SelectTab activates a sidebar tab by name ("chat", "agents", …).
// Used by harnesses (uishot) before the run starts.
func (m *Model) SelectTab(name string) bool {
	return m.tabs.SetActiveByTitle(name)
}

// Init starts the 180ms tick loop.
func (m Model) Init() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg {
		return state.Event{Kind: state.EvTick}
	})
}

// Update routes keys, backend events and component ticks.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
	case tea.KeyPressMsg:
		if cmd := m.handleKey(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case chatSentMsg:
		// nothing local: backend.Send owns the echo (chat-user + pending boss
		// bubble) via the event stream — applying them here duplicated the bubbles.
	case sendErrMsg:
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
				m.batchRespawned = true
				m.batchSentAt = time.Now()
				m.respawns++
				m.notice("boss went down — respawned a fresh session, resending batch")
				qdebugf("batch send failed (%v) — respawning (respawn #%d)", msg.err, m.respawns)
				cmds = append(cmds, m.resendBatchCmd(msg.items))
				break
			}
		}
		cmds = append(cmds, m.applyEvent(state.Event{
			Kind: state.EvStatus,
			Text: fmt.Sprintf("[grafeio] send failed: %v", msg.err),
		}))
	case slashMsg:
		if cmd := m.applySlash(msg.text); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case enqueueMsg:
		if len(m.queue) >= queueCap {
			m.noticeErr(fmt.Sprintf("backlog full (%d) — wait for the boss to catch up, or /queue clear", queueCap))
		} else {
			n := len(m.queue) + 1
			ent := queueEntry{text: msg.text}
			if tb, ok := m.team(); ok {
				// board row for the backlog item: title is the machine
				// first-N-chars clip of the typed text.
				ent.boardID = tb.QueueItemStart(n, clipRunes(msg.text, batchTitleClip))
			}
			m.queue = append(m.queue, ent)
			if m.chat != nil {
				m.chat.SetQueueLen(len(m.queue))
			}
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
		// spinner ticks, mouse wheel, etc. → active tab
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
// (cmd/uishot) print after the scripted run.
func (m Model) Frame() string {
	if m.width == 0 {
		return "grafeio — waiting for terminal size…"
	}
	top := chrome.TopBar(m.st, m.width)
	floor := lipgloss.NewStyle().Width(m.floorW).Height(m.middleH).
		Render(office.Styled(office.BuildRows(m.st, m.floorW, m.middleH)))
	side := lipgloss.NewStyle().Width(m.sidebar).Height(m.middleH).
		Render(m.tabs.View())
	mid := lipgloss.JoinHorizontal(lipgloss.Top, floor, side)
	bot := chrome.StatusBar(m.st, m.keys.HintLine(), len(m.queue), m.width)
	return lipgloss.JoinVertical(lipgloss.Left, top, mid, bot)
}

// handleKey implements the global keymap; unclaimed keys go to the tabs.
func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	chatActive := m.tabs.ActiveIndex() == 0
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "q":
		if !chatActive {
			return tea.Quit
		}
	case "tab":
		m.tabs.Next()
		return nil
	case "shift+tab":
		m.tabs.Prev()
		return nil
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
	default:
		if !chatActive {
			if idx := m.keys.TabJump(msg.String()); idx >= 0 {
				m.tabs.SetActive(idx)
				return nil
			}
		}
	}
	return m.tabs.Update(msg)
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

	prevPending := hasPendingBoss(m.st)
	m.st = reducer(m.st, ev)
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
	}

	if ev.Kind == state.EvTick {
		return tea.Tick(tickInterval, func(time.Time) tea.Msg {
			return state.Event{Kind: state.EvTick}
		})
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
func composeBatch(items []queueEntry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[BATCH DISPATCH — %d requests arrived while you were busy. "+
		"Treat each as an independent numbered work item: do trivial ones inline; "+
		"for non-trivial independent items DISPATCH PARALLEL SUB-AGENTS per your "+
		"manager discipline; then finalize with a one-line-per-item status table.]\n",
		len(items))
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, it.text)
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
	for i, it := range items {
		texts[i] = it.text
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
			if err := b.Send(sendText); err != nil {
				return queueSendErrMsg{err: err, items: items, batch: batch, retry: false}
			}
		}
		return chatSentMsg{text: sendText}
	}
	return send
}

// resendBatchCmd — the ONE failure respawn: ResetPrimary(true) then resend
// the SAME composed batch on the fresh session. Errors come back with
// retry=true so the loop can never respawn twice for one flush call.
func (m *Model) resendBatchCmd(items []queueEntry) tea.Cmd {
	text := composeBatch(items)
	b := m.backend
	tb, _ := m.team()
	return func() tea.Msg {
		if tb != nil {
			if err := tb.ResetPrimary(true); err != nil {
				return queueSendErrMsg{err: fmt.Errorf("respawn: %w", err), batch: true, retry: true}
			}
		}
		if b != nil {
			if err := b.Send(text); err != nil {
				return queueSendErrMsg{err: err, items: items, batch: true, retry: true}
			}
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
	sw := sidebarW
	if w < degradeCols {
		// degrade gracefully: narrow terminals get a narrow sidebar
		sw = w / 3
		if sw < 20 {
			sw = 20
		}
		if sw > sidebarW {
			sw = sidebarW
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
			next := office.AdvanceSprites(st)

			// ambient chatter: every ~140 ticks a random working non-manager speaks
			if tick%ambientEvery == 0 {
				var working []state.Employee
				for _, e := range next.Employees {
					if e.Role != state.RoleManager && e.Sprite == state.SpriteWorking {
						working = append(working, e)
					}
				}
				if len(working) > 0 {
					next = reducer(next, state.Event{
						Kind:       state.EvBubble,
						EmployeeID: working[rand.Intn(len(working))].ID,
						Text:       ambientLines[rand.Intn(len(ambientLines))],
					})
				} else if tick%(ambientEvery*2) == 0 {
					// nobody working: occasionally an idle one breaks the silence
					var idle []state.Employee
					for _, e := range next.Employees {
						if e.Role != state.RoleManager && e.Sprite == state.SpriteAtDesk {
							idle = append(idle, e)
						}
					}
					if len(idle) > 0 {
						next = reducer(next, state.Event{
							Kind:       state.EvBubble,
							EmployeeID: idle[rand.Intn(len(idle))].ID,
							Text:       "quiet floor today.",
						})
					}
				}
			}
			return next
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
			name := ev.EmployeeName
			if name == "" {
				name = "boss"
			}
			text := ev.ToolName
			if ev.ToolSummary != "" {
				text += " · " + ev.ToolSummary
			}
			line := state.ChatMsg{
				ID:   "tool-" + ev.CallID,
				From: name,
				Kind: "tool",
				Text: strings.ReplaceAll(text, "\n", " "), // chat rows are one-liners
				Meta: ev.ToolState,
				At:   time.Now().UnixMilli(),
			}
			merged := false
			next := append([]state.ChatMsg(nil), st.Chat...)
			for i, msg := range next {
				if msg.Kind == "tool" && msg.ID == line.ID {
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
  /thinking on|off   show/hide thinking blocks
  /tools on|off      show/hide tool one-liners
  /diffs on|off      expand/collapse file diffs (ctrl+d toggles)
  /queue             show the backlog (numbered items batched on flush)
  /queue clear       drop all queued backlog items
  /route             force-dispatch the backlog now (bypasses the busy gate)
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
	case "/queue":
		if len(fields) >= 2 {
			if fields[1] == "clear" {
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
		m.notice(fmt.Sprintf("mode %s · theme %s · agents %d · board %d/%d/%d\n%s",
			m.st.Mode, chrome.CurrentTheme().Name, len(m.st.Employees),
			pend, doing, done, m.st.StatusLine))
	case "/quit":
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
