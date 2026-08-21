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
	bubbleCap    = 3    // never more than 3 concurrent balloons (drop oldest)
	sidebarW     = 44   // right tab sidebar, when the terminal is wide enough
	degradeCols  = 100  // below this, the sidebar shrinks instead of the floor
	minCols      = 40
	minRows      = 12
	tickInterval = 180 * time.Millisecond
	ambientEvery = 140 // ticks between ambient bubbles
)

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

	width, height       int
	middleH             int
	sidebar             int
	floorW              int
	tabs                *panels.Tabs
	chat                *panels.Chat
	activity            *panels.Activity
	keys                KeyMap
}

// chatSentMsg fires after backend.Send succeeds — the local user bubble and
// the typing placeholder are appended through the normal reducer path.
type chatSentMsg struct{ text string }

// sendErrMsg fires when the backend rejects a prompt.
type sendErrMsg struct{ err error }

// New builds the app around a backend. backend.Start is NOT called here —
// main owns that (goroutine → tea.Program.Send).
func New(b state.Backend) Model {
	chat := panels.NewChat(func(text string) tea.Cmd {
		return func() tea.Msg {
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
		backend:  b,
		st:       initialState(b.Mode()),
		chat:     chat,
		activity: activity,
		tabs: panels.NewTabs(
			chat,
			panels.NewAgents(),
			panels.NewBoard(),
			panels.NewMail(),
			activity,
		),
		keys: NewKeyMap(),
	}
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
		cmds = append(cmds, m.applyEvent(m.chatUserEvent(msg.text)))
		cmds = append(cmds, m.applyEvent(m.chatBossPendingEvent()))
	case sendErrMsg:
		cmds = append(cmds, m.applyEvent(state.Event{
			Kind: state.EvStatus,
			Text: fmt.Sprintf("[grafeio] send failed: %v", msg.err),
		}))
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
	bot := chrome.StatusBar(m.st, m.keys.HintLine(), m.width)
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
	prevPending := hasPendingBoss(m.st)
	m.st = reducer(m.st, ev)
	m.tabs.SetState(m.st)

	if ev.Kind != state.EvTick {
		m.activity.Add(m.describeEvent(ev))
	}

	if ev.Kind == state.EvTick {
		return tea.Tick(tickInterval, func(time.Time) tea.Msg {
			return state.Event{Kind: state.EvTick}
		})
	}
	if !prevPending && hasPendingBoss(m.st) && m.chat != nil {
		return m.chat.SpinnerKick()
	}
	return nil
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
	case state.EvTick: {
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

	case state.EvDispatch: {
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
		st.Chat = capList(append(append([]state.ChatMsg(nil), st.Chat...), ev.Msg), chatCap)
		return st

	case state.EvChatBoss:
		// a real answer (or a fresh pending) replaces the old typing placeholder
		var rest []state.ChatMsg
		for _, mgr := range st.Chat {
			if !(mgr.From == "boss" && mgr.Pending) {
				rest = append(rest, mgr)
			}
		}
		st.Chat = capList(append(rest, ev.Msg), chatCap)
		return st

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

func (m *Model) chatUserEvent(text string) state.Event {
	return state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
		ID:   nextMsgID(),
		From: "user",
		Text: text,
		At:   time.Now().UnixMilli(),
	}}
}

func (m *Model) chatBossPendingEvent() state.Event {
	return state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
		ID:      nextMsgID(),
		From:    "boss",
		At:      time.Now().UnixMilli(),
		Pending: true,
	}}
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
	case state.EvStatus:
		what = "status — " + ev.Text
	default:
		what = string(ev.Kind)
	}
	// keep each row single-line for the log
	what = strings.ReplaceAll(what, "\n", " ")
	return fmt.Sprintf("[%s] %s", stamp, what)
}
