// opencode.go — the LIVE backend for theboringoffice. Port of
// node-legacy/src/backend/opencode.ts.
//
// Responsibilities:
//   - resolve an opencode server: baseURL -> env OPENCODE_SERVER ->
//     spawn `opencode serve --port 0 --hostname 127.0.0.1` (URL parsed
//     from stdout, 10s timeout; the spawned child is ours to kill on Stop).
//   - run plain net/http against it (the TS used the @opencode-ai/sdk; the
//     directory rides in x-opencode-directory, mirrored into ?directory=
//     for GETs exactly like the SDK's rewrite) and find-or-create the
//     primary ("boss") session for this directory.
//   - subscribe to the SSE event stream (GET /event) and normalize via
//     events.go (pure), plus the mapping branches that need I/O:
//     child-idle -> returned+mail+task-done (+best-effort child delete 10s
//     later), primary-assistant-completed -> chat-boss pinned to the
//     completing message's own text.
//   - sync board + mail from agentmemory every 5s when the probe found it.
//
// Chat path: Send drives the same emit callback Start received, so the
// user message and the pending boss bubble hit state immediately; the
// prompt POST returns at once and the SSE stream drives the completion.
// Pending bubbles are queued (FIFO id boss-N); a completion drains one and
// emits a "bossmsg-"+<messageID> bubble — the reducer strips the pending
// placeholder on any EvChatBoss, so the first completed bubble after a
// Send replaces it and later completions (multi-message turns) append.
//
// Note: unlike the demo backend, this backend never emits EvTick — the app
// owns the animation timer for live mode.
//
// Thought streaming: message.part.delta frames make EvThought events arrive
// at token rate (tens per second). emitThought coalesces per CallID: at
// most one emit every thoughtMinGapMs, keeping the LAST update in flight
// and dropping intermediates (coalesce, never reorder). A Done=true always
// flushes immediately so the block collapses on completion, not 150ms late.
//
// Chat streaming: TEXT parts of the primary session delta on the same
// channel. events.go registers streaming text parts and accumulates their
// deltas; emitChatStream coalesces the growing EvChatBoss updates per
// bubble ID ("bossmsg-"+messageID, Pending:true) exactly like the thought
// gate. The message.updated completion pin emits the pinned full text on
// the SAME ID with Pending:false (it stops the stream first). Stop() and
// session.error flush any still-open stream as Pending:false with a
// "[theboringoffice] stream interrupted" note.
//
// Office concierge: cfg.Boss.Concierge (default on) adds a second,
// lightweight root session ("theboringoffice concierge") so the member never talks
// to a dark boss while the primary turn is busy. It is created lazily on
// the FIRST SendConcierge, registered in normCtx as the pseudo-desk
// "concierge" (its own session's text stream rides EvChatOffice
// "office-"+messageID bubbles — one lane per message, never EvChatBoss —
// its tool parts show as inline "concierge" lines, its reasoning is
// suppressed, and ITS children (its own task-tool dispatches) hire/work/
// return exactly like the primary's). AbortSessions covers it;
// ResetPrimary/NewOffice un-seat it (never deleted server-side); the next
// busy-boss message recreates it lazily.
package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/netwatch"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

type liveBackend struct {
	directory string
	optURL    string

	// cfg is the brain.json this backend runs under; NewLive substitutes
	// config.Default() for nil, so every read below is non-nil.
	cfg *config.Config

	fl *flow

	mu            sync.Mutex
	baseURL       string // empty until resolved/start
	primaryID     string
	ctx           *normCtx
	client        *http.Client // bounded, for control calls
	sseClient     *http.Client // no timeout; SSE ctx drives lifetime
	sseCancel     context.CancelFunc
	proc          *exec.Cmd // spawned server, if we spawned it
	chatSeq       int
	pendingBoss   []string
	bossCompleted map[string]bool
	thoughtSlots  map[string]*thoughtSlot // CallID -> coalescing slot (thought stream gate)
	chatSlots     map[string]*thoughtSlot // boss bubble Msg.ID -> coalescing slot (text delta stream gate)
	am            *amHandle
	amTasks       map[string]string // id -> dedupe key
	amMails       map[string]bool
	lastUserText  string // belt-and-braces echo dedupe (see Send)
	lastUserMeta  string // attachment carrier of the last echo (same gate)
	lastUserAt    int64
	respawnFresh  bool   // ResetPrimary(true) latched: next Send respawns a fresh session once
	respawnOldID  string // primary id ResetPrimary dropped, so Send can un-seat it
	// primaryOverride — the office-session resume pin (internal/app
	// sessions.go) set BEFORE Start when a saved session.json for this
	// directory exists. Start prefers it over find-or-create; a server-side
	// 404/fetch failure degrades to the normal ensurePrimary path (degrade
	// open — a stale file must never hard-fail a boot).
	primaryOverride string
	// promptModelRejected latches when a serve rejects the per-prompt model
	// override with a 400 (an older/foreign server without the /doc model
	// field). From then on prompts go out without the override — degrade
	// open, never fake success.
	promptModelRejected bool
	// sseNoteSig is the SSE status-note dedupe latch (D1): the last failure
	// class reported by pump, "" when the stream is healthy/recovered. See
	// sseNote/sseRecovered.
	sseNoteSig string
	// Network watch (OFFLINE mode): net probes the internet touchstones on
	// a fixed cadence and flips the gate after two straight misses (flap
	// guard — one stray loss must not park the office). netOnline is a chan
	// that stays CLOSED while online (gate open: pump/pollLoop pass freely)
	// and is replaced with an open chan on offline (gate shut: both loops
	// park in waitOnline instead of churning reconnects) — netShut records
	// which side the gate currently is so a flip never closes twice.
	// netGen bumps per flip so the pump fresh-starts its SSE ladder on
	// recovery; netCancel kills the watcher goroutine (Stop).
	net       *netwatch.Watcher
	netCancel context.CancelFunc
	netOnline chan struct{}
	netShut   bool
	netGen    int
	// lastAbortAt stamps the most recent AbortSessions round (ms): an
	// empty completion landing inside the quiet window after it is the
	// aborted turn's own death rattle and is swallowed instead of surfacing
	// as a "could not read reply" line (see maybeBossCompleted).
	lastAbortAt int64
	// review — the CTO's once-per-drained-board latch over the CHILD-
	// SESSION brief board (ctx.tasks): any child EvDispatch re-arms it, and
	// the return that drains the board makes the CTO post his ONE review
	// beat (EvStatus + EvMail notice — see cto.go). The agentmemory mirror
	// (syncBoard/amTasks) deliberately never arms it: the CTO reviews the
	// office's own dispatched work, not a foreign board.
	review reviewLatch
	// Office concierge ("theboringoffice concierge" side session; see SendConcierge).
	// conciergeID is "" until the first SendConcierge creates the session
	// lazily (a quiet boss NEVER spins one up); conciergeBooted latches
	// once the preamble has ridden the first prompt (subsequent prompts go
	// out raw); pendingOffice is the FIFO of placeholder bubble ids
	// ("office-pend-"+N) mirroring pendingBoss; officeCompleted dedupes
	// completion pins like bossCompleted.
	conciergeID     string
	conciergeBooted bool
	pendingOffice   []string
	officeCompleted map[string]bool
}

func newLiveBackend(baseURL, directory string, cfg *config.Config) *liveBackend {
	b := &liveBackend{
		directory:       directory,
		optURL:          baseURL,
		cfg:             cfg,
		fl:              newFlow(),
		ctx:             newNormCtx(cfg),
		bossCompleted:   make(map[string]bool),
		officeCompleted: make(map[string]bool),
		thoughtSlots:    make(map[string]*thoughtSlot),
		chatSlots:       make(map[string]*thoughtSlot),
		amTasks:         make(map[string]string),
		amMails:         make(map[string]bool),
		client:          &http.Client{Timeout: 15 * time.Second},
		sseClient:       &http.Client{},
		net:             netwatch.New(nil, 0),
		netOnline:       make(chan struct{}),
	}
	// The gate bootstraps OPEN (closed chan): the office assumes
	// connectivity until the watcher's first confirmed round refutes it —
	// degrade open, same as every other probe in this file.
	close(b.netOnline)
	return b
}

func (b *liveBackend) Mode() state.Mode { return state.ModeLive }

// ---------------------------------------------------------------- start

func (b *liveBackend) Start(emit func(state.Event)) error {
	b.fl.setEmit(emit)

	// Manager charter (oikonomos) runs FIRST, before server resolution:
	// any directory theboringoffice serves gets .opencode/oikonomos.md + the
	// opencode.json instructions entry wired ahead of a spawned serve
	// reading its project config. A degradation never blocks the boot —
	// failures surface on the status line only.
	charterNotes := emitCharterNotes(emit, b.directory)

	u := b.optURL
	if u == "" {
		u = os.Getenv("OPENCODE_SERVER")
	}
	if u == "" {
		spawnedURL, proc, err := spawnServe(b.directory)
		if err == nil && charterNotes.changed {
			// opencode spoils its config at start: a serve whose boot
			// raced/missed the freshly-written instructions entry keeps
			// running charter-blind. When THIS pass just wired the charter
			// (first-run wiring, refreshed chart bytes, a newly-merged
			// entry), restart the spawn once so the serve reads the config
			// with the merge already on disk. Behind an explicit URL (cfg/OPENCODE_SERVER)
			// the server is not ours to restart — the note stands and the
			// charter applies from the server's next boot.
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] manager charter: restarting serve so it picks up the config"})
			_ = proc.Process.Kill()
			_ = proc.Wait()
			spawnedURL, proc, err = spawnServe(b.directory)
		}
		if err != nil {
			return err
		}
		b.mu.Lock()
		b.proc = proc
		b.mu.Unlock()
		u = spawnedURL
	}
	b.mu.Lock()
	b.baseURL = u
	b.mu.Unlock()

	primary, err := b.resolvePrimary()
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.primaryID = primary.ID
	b.mu.Unlock()

	// Connectivity watcher (OFFLINE mode): once the server stands, start
	// probing the internet. Offline parks pump/pollLoop at the gate (one
	// EvOffline pair), online reopens it with a fresh SSE ladder (one
	// EvOnline pair) — see onNetTransition. The goroutine dies via ctx in
	// Stop; a boot stranded before this point never spawns it (no leak).
	netCtx, netCancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.netCancel = netCancel
	b.mu.Unlock()
	go b.net.Start(netCtx, b.onNetTransition)

	// Fixed seats: the boss and Mnemosyne (hr) are always on the floor.
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: primary.ID, Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk,
	}})
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr", Sprite: state.SpriteAtDesk,
	}})

	// The exec suite opens too: the idle pseudo-CTO ("theboringcto",
	// seat "cto") holds the office CTO's chair from boot — demo parity
	// (the scripted tour hires him at t0). He is a floor ghost: EvHire'd
	// here but NEVER keyed into ctx.employees, so the session-id mappers
	// stay blind to him. The first architecture child session swaps in
	// for him, the last one's removal re-seats him — see
	// normCtx.seatPseudoCTO/dropPseudoCTO in events.go.
	for _, e := range b.ctx.seatPseudoCTO() {
		b.fl.emit(e)
	}

	// Agentmemory base: the focused env override wins, else brain.json
	// (which itself defaults to localhost:3111 — identical when absent).
	amURL := os.Getenv("AGENTMEMORY_URL")
	if amURL == "" {
		amURL = b.cfg.Backend.AgentmemoryURL
	}
	b.am = probeAgentmemory(amURL)
	board := "in-memory | agentmemory: offline (in-memory board)"
	if b.am.kind == "actions" {
		board = "agentmemory (" + b.am.winner + ")"
	}
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] live - " + u + " | board: " + board})

	if m := b.bossModelRef(); m != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] boss model override: " + m})
	}
	if b.cfg.Backend.CTOModel != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] cto model override: " + b.cfg.Backend.CTOModel})
	}

	if !b.cfg.Boss.Concierge {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] office concierge: off (boss.concierge=false) — busy-boss chat routes to the boss queue"})
	}

	if b.am.kind == "actions" {
		b.syncBoard()
		go b.pollLoop(amPollBase(b.cfg.Backend.AgentmemoryPollS))
	}

	go b.pump()
	return nil
}

// ---------------------------------------------------------------- send

// Send pushes user chat to the boss. It is the plain-text state.Backend
// contract; chat-input attachments ride the optional SendWith seam below
// (the app type-asserts it — see attachmentBackend in internal/app).
//
// NO IDLE GATE (D2): Send NEVER holds the message waiting for the boss to
// be free, and never checks the session's busy state. The prompt POSTs to
// the serve immediately, every time; opencode serve itself queues a prompt
// that lands mid-turn behind the running turn and drains it when the turn
// settles (prompt_async semantics — proven wave-6+: a prompt during a
// parked question persists server-side). Any client-side "wait for idle"
// behavior lives in the APP layer (internal/app model.go's queue), NOT
// here — this backend's Send is always the immediate-send path.
func (b *liveBackend) Send(text string) error {
	return b.SendWith(text, nil)
}

// SendWith is Send + chat-input attachments: the user-bubble echo carries
// their names in ChatMsg.Meta (state.AttachMeta — "att ␟ name ␟ name…",
// the chat panel renders the dim " · 📎 N" suffix from it) and the prompt
// posts one file part per readable attachment (parts.go). Semantics of
// the plain Send otherwise: echo chat-user, stage ONE pending boss bubble
// (FIFO id boss-N), POST the prompt async (immediately — there is no
// busy/idle gate in this backend by design; see Send). Completed assistant
// messages arrive over SSE and emit their own pinned
// "bossmsg-"+<messageID> bubbles; the FIRST of them strips the pending
// placeholder (the reducer drops pending bubbles on any EvChatBoss), later
// ones append. A prompt error re-emits the SAME pending id with the
// failure note instead.
func (b *liveBackend) SendWith(text string, atts []state.Attachment) error {
	trimmed := strings.TrimSpace(text)
	if (trimmed == "" && len(atts) == 0) || b.fl.isStopped() {
		return nil
	}
	meta := state.AttachMeta(attachmentNames(atts))

	// Belt-and-braces echo dedupe: the chat-user echo fires exactly once
	// per prompt. This backend never maps SSE message.updated (user role)
	// to chat again, but if the same text (with the same attachments)
	// would fire twice within 2s (double Send, retry path, app-side echo
	// raced back in), swallow the second echo — the prompt POST below
	// still always runs.
	b.mu.Lock()
	now := nowMs()
	duplicate := trimmed == b.lastUserText && meta == b.lastUserMeta &&
		b.lastUserText != "" && now-b.lastUserAt < 2000
	if !duplicate {
		b.lastUserText = trimmed
		b.lastUserMeta = meta
		b.lastUserAt = now
	}
	b.chatSeq++
	userID := "user-" + itoa(b.chatSeq)
	b.mu.Unlock()
	if !duplicate {
		b.fl.emit(state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
			ID: userID, From: "user", Text: trimmed, At: now, Kind: "user", Meta: meta,
		}})
	}

	b.mu.Lock()
	ready := b.baseURL != "" && !b.fl.isStopped()
	primaryID := b.primaryID
	forceFresh := b.respawnFresh
	b.mu.Unlock()

	// Respawn path (ResetPrimary cleared the hold): establish a primary
	// session on demand — forced-fresh ("theboringoffice office · respawn") when
	// ResetPrimary(true) latched it, otherwise the normal reuse pass.
	oldID := b.respawnOldID
	if ready && primaryID == "" {
		var (
			primary ocSession
			perr    error
		)
		if forceFresh {
			primary, perr = b.createPrimary(b.bossNameShort() + " · respawn")
			if perr == nil {
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] primary session respawned fresh (" + primary.ID + ")"})
			}
		} else {
			primary, perr = b.ensurePrimary()
		}
		if perr != nil {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] primary respawn failed: " + shortTitle(perr.Error(), 100)})
		} else {
			b.mu.Lock()
			b.primaryID = primary.ID
			b.respawnFresh = false // consume the one-shot
			b.respawnOldID = ""
			b.mu.Unlock()
			primaryID = primary.ID
			// Re-seat the boss so the floor follows the new session.
			if oldID != "" && oldID != primary.ID {
				b.fl.emit(state.Event{Kind: state.EvFire, EmployeeID: oldID})
			}
			b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
				ID: primary.ID, Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk,
			}})
		}
	}
	ready = ready && primaryID != ""
	if !ready {
		b.mu.Lock()
		b.chatSeq++
		deadID := "boss-" + itoa(b.chatSeq)
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID: deadID, From: "boss", Text: "[theboringoffice] backend not started", At: nowMs(), Pending: false,
		}})
		return nil
	}
	b.mu.Lock()
	b.chatSeq++
	pendingID := "boss-" + itoa(b.chatSeq)
	b.pendingBoss = append(b.pendingBoss, pendingID)
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
		ID: pendingID, From: "boss", Text: "", At: nowMs(), Pending: true,
	}})

	err := b.postPrompt(primaryID, trimmed, atts)
	if err != nil {
		b.mu.Lock()
		for i, id := range b.pendingBoss {
			if id == pendingID {
				b.pendingBoss = append(b.pendingBoss[:i], b.pendingBoss[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID:      pendingID,
			From:    "boss",
			Text:    "[theboringoffice] prompt failed: " + shortTitle(err.Error(), 120),
			At:      nowMs(),
			Pending: false,
		}})
	}
	return nil
}

// ------------------------------------------------------- office concierge

// conciergePreamble rides the concierge session's FIRST prompt exactly
// once (fetch-side one-shot; subsequent prompts go out raw). It pins the
// concierge's contract: answer instantly and short; real work gets a
// sub-agent dispatch NOW (the task tool), never a queue and never a
// promise to get back later.
const conciergePreamble = "You are the office concierge. The boss is busy right now.\n" +
	"Answer the user in <=3 sentences.\n" +
	"If the message IS work (create/change/run/research), DO NOT queue: immediately\n" +
	"dispatch a sub-agent via the task tool for it and reply acknowledging\n" +
	"the delegation in one line. Never ask the user to wait; never say you'll\n" +
	"get back later."

// SendConcierge is the live office-concierge seam (state.ConciergeCapable —
// the app type-asserts it when the boss's turn is occupied; deliberately NOT
// on state.Backend, mirrors SessionAborter/SendWith).
//
// Lifecycle: the concierge session ("theboringoffice concierge", same serve, same
// cwd) is created LAZILY on this first call — a quiet boss never pays for
// one (CONCIERGE-proof: ConciergeID() stays "" until first use). Once made,
// it registers inside normCtx as the pseudo-desk "concierge"
// (events.go:registerConcierge): no hire/manager event (the floor keeps one
// boss), but its tool parts surface as inline "concierge" office lines and
// its OWN children (task-tool dispatches) hire via the normal parentID
// chain exactly like the primary's children.
//
// Echo + placeholder mirror Send exactly: the chat-user echo fires HERE
// (backend-owned, same ownership as Send — the app never echoes), then one
// pending office placeholder ("office-pend-"+N, FIFO) stages the typing
// bubble. The concierge's assistant replies ride EvChatOffice ONLY —
// "office-"+messageID bubbles, From/Kind "office", streaming growth at
// ~7fps + completion pin — never EvChatBoss: exactly one lane fires per
// message (maybeOfficeCompleted guards SessionID, maybeBossCompleted
// guards primaryID).
//
// Degrade-open rules: boss.concierge=false treats the message as a normal
// boss Send (the turn queues server-side — nothing is ever dropped); a
// concierge create/prompt failure lands as an office bubble, not a stuck
// lane.
func (b *liveBackend) SendConcierge(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || b.fl.isStopped() {
		return nil
	}
	if !b.cfg.Boss.Concierge {
		// Feature off: degrade open to the boss lane (prompt_async queues
		// behind the busy turn server-side, exactly like any Send).
		return b.Send(trimmed)
	}
	b.mu.Lock()
	now := nowMs()
	b.chatSeq++
	userID := "user-" + itoa(b.chatSeq)
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
		ID: userID, From: "user", Text: trimmed, At: now, Kind: "user",
	}})

	b.mu.Lock()
	ready := b.baseURL != "" && !b.fl.isStopped()
	conciergeID := b.conciergeID
	b.mu.Unlock()
	if !ready {
		b.mu.Lock()
		b.chatSeq++
		deadID := "office-pend-" + itoa(b.chatSeq)
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
			ID: deadID, From: "office", Kind: "office", Text: "[theboringoffice] backend not started", At: nowMs(), Pending: false,
		}})
		return nil
	}
	if conciergeID == "" {
		sesh, err := b.createPrimary("theboringoffice concierge")
		if err != nil {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] concierge session create failed: " + shortTitle(err.Error(), 100)})
			b.mu.Lock()
			b.chatSeq++
			deadID := "office-pend-" + itoa(b.chatSeq)
			b.mu.Unlock()
			b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
				ID: deadID, From: "office", Kind: "office",
				Text: "[theboringoffice] office concierge unavailable: " + shortTitle(err.Error(), 100), At: nowMs(), Pending: false,
			}})
			return nil // degrade: do not hard-fail the message
		}
		b.mu.Lock()
		b.conciergeID = sesh.ID
		b.ctx.registerConcierge(sesh.ID)
		conciergeID = sesh.ID
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] office concierge session ready (" + sesh.ID + ")"})
	}
	b.mu.Lock()
	b.chatSeq++
	pendingID := "office-pend-" + itoa(b.chatSeq)
	b.pendingOffice = append(b.pendingOffice, pendingID)
	booted := b.conciergeBooted
	if !booted {
		b.conciergeBooted = true
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
		ID: pendingID, From: "office", Kind: "office", Text: "", At: nowMs(), Pending: true,
	}})

	prompt := trimmed
	if !booted {
		prompt = conciergePreamble + "\n\n" + trimmed
	}
	err := b.postPrompt(conciergeID, prompt, nil)
	if err != nil {
		b.mu.Lock()
		for i, id := range b.pendingOffice {
			if id == pendingID {
				b.pendingOffice = append(b.pendingOffice[:i], b.pendingOffice[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
			ID:      pendingID,
			From:    "office",
			Kind:    "office",
			Text:    "[theboringoffice] concierge prompt failed: " + shortTitle(err.Error(), 120),
			At:      nowMs(),
			Pending: false,
		}})
	}
	return nil
}

// ConciergeID returns the concierge session id, "" until first use — the
// laziness/quiet-boss proof (headless --concierge-probe reads it via the
// same type-assert pattern as PrimaryID).
func (b *liveBackend) ConciergeID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conciergeID
}

// forgetConciergeLocked un-seats the concierge (ResetPrimary/NewOffice
// respawn) and returns the old id, "" when there was none. The server-side
// session is NEVER deleted — it simply stops being the office's concierge
// (mirrors the primary's respawn semantics); the next SendConcierge lazily
// creates and preambles a fresh one. Caller holds b.mu; emission of the
// status note is the caller's job AFTER unlocking.
func (b *liveBackend) forgetConciergeLocked() string {
	concID := b.conciergeID
	b.conciergeID = ""
	b.conciergeBooted = false
	b.pendingOffice = nil
	b.officeCompleted = make(map[string]bool)
	b.ctx.dismissConcierge()
	return concID
}

// ---------------------------------------------------------------- stop

func (b *liveBackend) Stop() error {
	if b.fl.isStopped() {
		return nil
	}
	// Graceful stream shutdown: a boss answer still mid-delta never gets
	// its completion pin, so flush whatever accumulated as a Pending=false
	// bubble (update-in-place on the same ID) with an interruption note.
	// Must run BEFORE fl.stop() seals the emit callback.
	b.mu.Lock()
	streamEvs := interruptedStreamEvents(b.ctx, "[theboringoffice] stream interrupted")
	for id := range b.chatSlots {
		delete(b.chatSlots, id)
	}
	b.mu.Unlock()
	for _, e := range streamEvs {
		b.fl.emit(e)
	}

	b.fl.stop() // seals emit, kills timers + pollers

	b.mu.Lock()
	cancel := b.sseCancel
	b.sseCancel = nil
	netCancel := b.netCancel
	b.netCancel = nil
	proc := b.proc
	b.proc = nil
	b.mu.Unlock()

	if netCancel != nil {
		netCancel() // kills the connectivity watcher goroutine (no leak)
	}
	if cancel != nil {
		cancel()
	}
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait() // reap
	}
	return nil
}

// ---------------------------------------------------------------- spawn

var urlRe = regexp.MustCompile(`https?://\S+`)
var urlTrimRe = regexp.MustCompile(`[.,;)\]]+$`)

// debugSSE toggles the raw SSE trace in streamOnce
// (THEBORINGOFFICE_DEBUG_SSE=1; pre-rename GRAFEIO_DEBUG_SSE=1 works too).
var debugSSE = envOrLegacy("THEBORINGOFFICE_DEBUG_SSE", "GRAFEIO_DEBUG_SSE") != ""

// spawnServe runs `opencode serve --port 0 --hostname 127.0.0.1` and
// resolves with the listening URL scanned from stdout, or dies after 10s.
func spawnServe(directory string) (string, *exec.Cmd, error) {
	cmd := exec.Command("opencode", "serve", "--port", "0", "--hostname", "127.0.0.1")
	if directory != "" {
		cmd.Dir = directory
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("opencode serve spawn failed: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, fmt.Errorf("opencode serve spawn failed: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("opencode serve spawn failed: %w", err)
	}

	var outMu sync.Mutex
	var output strings.Builder // last bits of stdout+stderr, for error text

	type result struct {
		url string
		err error
	}
	urlCh := make(chan result, 1)

	// Stdout is scanned line by line; stderr just feeds the error buffer.
	scan := func(r io.Reader, watch bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			outMu.Lock()
			output.WriteString(line)
			output.WriteByte('\n')
			outMu.Unlock()
			if watch {
				if m := urlRe.FindString(line); m != "" {
					m = urlTrimRe.ReplaceAllString(m, "")
					select {
					case urlCh <- result{url: m}:
					default:
					}
					watch = false
				}
			}
		}
	}
	go scan(stdout, true)
	go scan(stderr, false)

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	select {
	case r := <-urlCh:
		return r.url, cmd, nil
	case err := <-exitCh:
		outMu.Lock()
		snap := output.String()
		outMu.Unlock()
		return "", nil, fmt.Errorf("opencode serve exited before printing a URL: %v: %s", err, trimTo(snap, 200))
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-exitCh
		return "", nil, errors.New("opencode serve: no listening URL within 10s")
	}
}

func trimTo(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

// ---------------------------------------------------------------- http

// doJSON issues an opencode control call. The directory rides the
// x-opencode-directory header on every request and, exactly like the SDK's
// GET rewrite, a ?directory= query param on GET/HEAD.
func (b *liveBackend) doJSON(method, path string, body []byte, out any) error {
	b.mu.Lock()
	base := b.baseURL
	b.mu.Unlock()
	if base == "" {
		return errors.New("backend not started")
	}
	qs := ""
	if method == http.MethodGet || method == http.MethodHead {
		qs = "?directory=" + url.QueryEscape(b.directory)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path+qs, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-opencode-directory", url.QueryEscape(b.directory))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New(httpErrorText(res.StatusCode, data))
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	return nil
}

// httpErrorText pulls message text out of the SDK error shapes.
func httpErrorText(status int, body []byte) string {
	var shape struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &shape) == nil {
		if shape.Error.Message != "" {
			return fmt.Sprintf("status %d: %s", status, shape.Error.Message)
		}
		if shape.Message != "" {
			return fmt.Sprintf("status %d: %s", status, shape.Message)
		}
	}
	return fmt.Sprintf("status %d: %s", status, trimTo(string(body), 200))
}

// STALE_SESSION_MSG_LIMIT: a reused root session carrying more history
// than this is treated as a stale giant context (the class that timed out
// turns earlier) and a fresh "theboringoffice office" session is created anyway.
const STALE_SESSION_MSG_LIMIT = 50

// ensurePrimary reuses the newest root session for this directory, else
// creates one titled "theboringoffice office". Reuse passes the stale check first:
// > STALE_SESSION_MSG_LIMIT messages -> create fresh anyway. The choice is
// logged on the status line.
func (b *liveBackend) ensurePrimary() (ocSession, error) {
	var sessions []ocSession
	if err := b.doJSON(http.MethodGet, "/session", nil, &sessions); err == nil {
		var newest *ocSession
		for i := range sessions {
			s := &sessions[i]
			if s.ParentID != "" {
				continue
			}
			if newest == nil || s.Time.Created > newest.Time.Created {
				newest = s
			}
		}
		if newest != nil {
			count := b.sessionMessageCount(newest.ID)
			if count > STALE_SESSION_MSG_LIMIT {
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
					"[theboringoffice] primary session %s has %d msgs (> %d, stale) — creating fresh", newest.ID, count, STALE_SESSION_MSG_LIMIT)})
				return b.createPrimary(b.bossName())
			}
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
				"[theboringoffice] primary session: reuse %s (%d msgs)", newest.ID, count)})
			return *newest, nil
		}
	}
	return b.createPrimary(b.bossName())
}

// resolvePrimary is Start's boss-session choice: when a PrimaryOverride is
// latched (the app restored an office session for this directory) the saved
// session wins — BUT ONLY if the server still has it (a 404/fetch failure,
// e.g. the member hand-deleted it server-side, falls back to the normal
// find-or-create: degrade open, never hard fail the boot on a stale file).
// Without an override this IS ensurePrimary.
func (b *liveBackend) resolvePrimary() (ocSession, error) {
	b.mu.Lock()
	override := b.primaryOverride
	b.mu.Unlock()
	if override != "" {
		var s ocSession
		if err := b.doJSON(http.MethodGet, "/session/"+override, nil, &s); err == nil && s.ID != "" {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
				"[theboringoffice] primary session: restored %s (office session on disk)", s.ID)})
			return s, nil
		}
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] saved office session's primary " + override + " is gone server-side — normal find-or-create instead"})
	}
	return b.ensurePrimary()
}

// createPrimary makes a brand-new root session with the given title.
func (b *liveBackend) createPrimary(title string) (ocSession, error) {
	var created ocSession
	body, _ := json.Marshal(map[string]any{"title": title})
	if err := b.doJSON(http.MethodPost, "/session", body, &created); err != nil {
		return ocSession{}, fmt.Errorf("session.create failed: %w", err)
	}
	return created, nil
}

// sessionMessageCount counts rows in GET /session/{id}/message; -1 on
// error (reuse proceeds — a counting failure must not churn sessions).
func (b *liveBackend) sessionMessageCount(sessionID string) int {
	var rows []json.RawMessage
	if err := b.doJSON(http.MethodGet, "/session/"+sessionID+"/message", nil, &rows); err != nil {
		return -1
	}
	return len(rows)
}

// ---------------------------------------------------------------- queue board + respawn

// QueueItemStart mirrors a queued office item onto the agentmemory board
// as a pending action the office can watch. Best-effort: "" when the
// agentmemory probe was "none" (offline) — QueueItemDone("") is a no-op.
// Errors are dropped to the status line only when the server was
// reachable and still failed. NOT part of state.Backend: the app side
// type-asserts this seam.
func (b *liveBackend) QueueItemStart(index int, title string) string {
	if b.am == nil || b.am.kind != "actions" {
		return ""
	}
	boardID, err := b.am.CreateAction(fmt.Sprintf("QUE-%d: %s", index, title), fmt.Sprintf("que-%d", index))
	if err != nil {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] board action create failed: " + shortTitle(err.Error(), 100)})
		return ""
	}
	return boardID
}

// QueueItemDone marks a queue item's board action done. Empty id / offline
// probe -> silent no-op; a failed round-trip is status-line only.
func (b *liveBackend) QueueItemDone(boardID string) {
	if boardID == "" || b.am == nil || b.am.kind != "actions" {
		return
	}
	if err := b.am.MarkAction(boardID, "done"); err != nil {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] board action mark done failed: " + shortTitle(err.Error(), 100)})
	}
}

// ResetPrimary clears the hold on the primary session so the NEXT Send
// lazily establishes a replacement (nothing is archived/deleted — the old
// session simply stops being the boss). With forceNew=true the
// replacement is a BRAND-NEW session titled "theboringoffice office · respawn",
// consumed one-shot; false runs the normal reuse pass (which still creates
// fresh when the newest root session is stale). Live backend only; the
// demo twin is a no-op. Used by the queue-flush resilience path: a failed
// flush respawns a fresh primary and retries once.
func (b *liveBackend) ResetPrimary(forceNew bool) error {
	b.mu.Lock()
	old := b.primaryID
	b.primaryID = ""
	b.respawnFresh = forceNew
	b.respawnOldID = old
	concID := b.forgetConciergeLocked() // respawn semantics: concierge goes with the office
	b.mu.Unlock()
	if concID != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] office concierge dismissed with the respawn (" + concID + ") — next busy-boss message recreates it lazily"})
	}
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[theboringoffice] primary session reset (forceNew=%v) — next send respawns", forceNew)})
	return nil
}

// ---------------------------------------------------------------- office session seams (ADDITIVE)

// PrimaryOverride pins the boss-session id Start should resume (the app
// calls it BEFORE Start, after restoring a saved office session for this
// directory — see internal/app/sessions.go). resolvePrimary verifies the
// session still exists server-side; anything else falls back to
// find-or-create.
//
// NOT part of state.Backend: the app type-asserts this seam (same pattern
// as teamBackend/attachmentBackend); harness stubs never implement it.
func (b *liveBackend) PrimaryOverride(id string) {
	b.mu.Lock()
	b.primaryOverride = id
	b.mu.Unlock()
}

// PrimaryID returns the current primary ("boss") session id, "" until
// Start resolves one. The office-session persist loop snapshots it. The
// id moves when the session is respawned (ResetPrimary/next-send) or
// replaced (/new → NewOffice) — reader-snapshot on demand, no caching.
func (b *liveBackend) PrimaryID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.primaryID
}

// NewOffice — the /new command's backend leg: ResetPrimary(true) semantics
// (the old primary is un-seated, seconds-old respawn latch consumed, the
// server-side session itself NEVER deleted), then create a BRAND-NEW
// primary titled "theboringoffice office" (bossName()) NOW — not lazily on the
// next send — and re-seat the floor boss on it (fire the old hire row,
// hire the new one). Returns the new session id so the persist loop
// threads it into the next snapshot. Requires a started backend.
func (b *liveBackend) NewOffice() (string, error) {
	primary, err := b.createPrimary(b.bossName())
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	old := b.respawnOldID
	if old == "" {
		old = b.primaryID // no latch pending (e.g. direct call) — un-seat the live one
	}
	b.primaryID = primary.ID
	b.respawnFresh = false // the fresh-create latch is consumed eagerly here
	b.respawnOldID = ""
	concID := b.forgetConciergeLocked() // a fresh office starts fresh-concierge too
	b.mu.Unlock()
	if concID != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] office concierge dismissed with the new office (" + concID + ") — next busy-boss message recreates it lazily"})
	}
	if old != "" && old != primary.ID {
		b.fl.emit(state.Event{Kind: state.EvFire, EmployeeID: old})
	}
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: primary.ID, Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk,
	}})
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] new office session fresh (" + primary.ID + ")"})
	return primary.ID, nil
}

// postPrompt is promptAsync: POST /session/{id}/prompt_async (204 on ok).
//
// The parts array is one text part (when text is non-empty) plus one file
// part per attachment — {"type":"file","mime","filename","url"} with the
// url a base64 data URL. Wire shape verified 2026-08-21 against serve
// 1.18.19: GET /doc documents FilePartInput for session.prompt_async
// (required type/mime/url), and a live POST with a data-URL file part is
// accepted (HTTP 204). Attachments that fail to read are skipped with a
// status note rather than sinking the prompt (parts.go).
//
// The configured model override rides as {"model":{"providerID","modelID"}}
// — the exact shape serve 1.18.19 documents in GET /doc for prompt_async
// (verified 2026-08-21 against the spawned server). The attached model is
// routed by target session (see promptModelOverride): boss/concierge
// prompts take the boss override (cfg.Backend.BossModel, falling back to
// the legacy cfg.Boss.Model), a CTO-seated session takes
// cfg.Backend.CTOModel. A ModelRef without a "provider/model" slash is
// ignored with a status note. If a serve ever rejects the model field with
// 400 (an older/foreign server), the override latches off and the prompt
// retries bare — degrade open, never fake it.
func (b *liveBackend) postPrompt(sessionID, text string, atts []state.Attachment) error {
	parts, skipped := payloadParts(text, atts)
	if len(skipped) > 0 {
		// The prompt still goes out with whatever parts survived — the
		// member sees exactly which attachment didn't make it.
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] could not attach " +
			strings.Join(skipped, ", ") + " (file unreadable) — sent without it"})
	}
	payload := map[string]any{"parts": parts}
	provider, model := splitModelRef(b.promptModelOverride(sessionID))
	b.mu.Lock()
	rejected := b.promptModelRejected
	b.mu.Unlock()
	withModel := provider != "" && model != "" && !rejected
	if withModel {
		payload["model"] = map[string]any{"providerID": provider, "modelID": model}
	}
	body, _ := json.Marshal(payload)
	err := b.doJSON(http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil)
	if err != nil && withModel && strings.Contains(strings.ToLower(err.Error()), "model") {
		b.mu.Lock()
		b.promptModelRejected = true
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] boss model override unavailable on this serve (400 rejected the model field) — continuing without it"})
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] boss model override unavailable in serve (see /doc session.prompt_async): retrying bare prompt"})
		// Retry bare exactly once: the member-visible cost of the failed
		// POST was zero (rejected before the turn started).
		payload = map[string]any{"parts": parts}
		body, _ = json.Marshal(payload)
		err = b.doJSON(http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil)
	}
	return err
}

// bossModelRef is the effective boss (primary-session) model override:
// cfg.Backend.BossModel wins when set; the legacy cfg.Boss.Model stands
// otherwise (an existing brain.json keeps working). "" = server default.
func (b *liveBackend) bossModelRef() string {
	if s := strings.TrimSpace(b.cfg.Backend.BossModel); s != "" {
		return s
	}
	return string(b.cfg.Boss.Model)
}

// promptModelOverride routes the model override for the prompt's target
// session: a session the floor hired as the CTO (events.go
// roleFromSession — state.IsArchitectureBrief is the ONE matcher) takes
// cfg.Backend.CTOModel; everything else (the primary, the concierge, and
// any unhired id) takes the boss override. "" = no override, the payload
// ships without a "model" key at all (additive only).
//
// Note on reachability: CTO children are created by the serve itself on
// the boss's task-tool calls — theboringoffice never POSTs their sessions or
// prompts (see normCtx: per-sub-agent model dispatch is opencode's, not
// ours) — so today a CTO-seated override surfaces only where theboringoffice
// itself prompts one. The routing rule lives here EXACTLY ONCE so any
// future per-child prompt path carries it without re-learning the rule.
func (b *liveBackend) promptModelOverride(sessionID string) string {
	b.mu.Lock()
	emp, ok := b.ctx.employees[sessionID]
	b.mu.Unlock()
	if ok && emp.Role == state.RoleCTO {
		return b.cfg.Backend.CTOModel
	}
	return b.bossModelRef()
}

// splitModelRef parses "provider/model" (ModelRef). Both halves must be
// non-empty for the override to be honored.
func splitModelRef(s string) (provider, model string) {
	parts := strings.SplitN(strings.TrimSpace(s), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// ---------------------------------------------------------------- brain.json boss naming

// bossName is the fresh-create session title (cfg.Boss.Name); the cfg
// contract guarantees non-empty, but a belt-and-braces fallback keeps the
// historic title so a hand-rolled blank config cannot break the floor.
func (b *liveBackend) bossName() string {
	if b.cfg.Boss.Name != "" {
		return b.cfg.Boss.Name
	}
	return "theboringoffice office"
}

// bossNameShort strips a trailing "(…)" parenthetical for the respawn
// title: "boss · respawn" reads like a title; "boss (oikonomos) · respawn"
// does not.
func (b *liveBackend) bossNameShort() string {
	name := b.bossName()
	if i := strings.LastIndex(name, " ("); i > 0 && strings.HasSuffix(name, ")") {
		return strings.TrimSpace(name[:i])
	}
	return name
}

// ---------------------------------------------------------------- permission replies

// AnswerPermission replies to a pending permission prompt. Primary route is
// the modern global one (POST /permission/{requestID}/reply, opencode >=1.18);
// if the server rejects it, fall back to the legacy session-scoped route
// (POST /session/{id}/permissions/{permissionID}) using the session the
// request was seen on.
func (b *liveBackend) AnswerPermission(permissionID, response string) error {
	switch response {
	case "once", "always", "reject":
	default:
		return fmt.Errorf("invalid permission response %q (want once|always|reject)", response)
	}
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	body, _ := json.Marshal(map[string]any{"reply": response})
	if err := b.doJSON(http.MethodPost, "/permission/"+permissionID+"/reply", body, nil); err == nil {
		return nil
	}
	b.mu.Lock()
	hold, ok := b.ctx.pendingPerms[permissionID]
	b.mu.Unlock()
	if !ok || hold.SessionID == "" {
		return errors.New("permission.reply failed and the request's session is unknown")
	}
	legacy, _ := json.Marshal(map[string]any{"response": response})
	return b.doJSON(http.MethodPost, "/session/"+hold.SessionID+"/permissions/"+permissionID, legacy, nil)
}

// ---------------------------------------------------------------- question replies

// AnswerQuestion replies to a pending question request. This is THE fix for
// the question-loop deadlock: the opencode agent loop PARKS at
// question.asked and resumes only when the question API gets a reply — a
// normal chat prompt does NOT answer it, so chat used to sit queued forever.
//
// Primary route is the modern global one (POST /question/{requestID}/reply,
// opencode 1.18.19 /doc: body {"answers": [["label"], ...]} — one
// string-array per asked question; -> 200 boolean). answers arrives already
// in THAT wire shape (string[][]): a multiple-select (checkbox) page puts
// every picked label in its own slot, radio/free-text pages carry one — the
// body ships verbatim, no per-question single-wrap. Fallback is the
// session-scoped v2 route
// (POST /api/session/{sessionID}/question/{requestID}/reply, same body
// shape) keyed by the session the request was seen on via the normCtx hold.
// NOTE: /doc 1.18.19 exposes NO /session/{id}/questions/... legacy shim for
// questions the way permissions had — the v2 route is the only fallback.
func (b *liveBackend) AnswerQuestion(requestID string, answers [][]string) error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	body, _ := json.Marshal(map[string]any{"answers": answers})
	if err := b.doJSON(http.MethodPost, "/question/"+requestID+"/reply", body, nil); err == nil {
		return nil
	}
	b.mu.Lock()
	hold, ok := b.ctx.pendingQuestions[requestID]
	b.mu.Unlock()
	if !ok || hold.SessionID == "" {
		return errors.New("question.reply failed and the request's session is unknown")
	}
	return b.doJSON(http.MethodPost, "/api/session/"+hold.SessionID+"/question/"+requestID+"/reply", body, nil)
}

// RejectQuestion declines a pending question request outright (opencode
// serve DOES expose a true reject — /doc 1.18.19: POST
// /question/{requestID}/reject, no request body, -> 200 boolean). Fallback
// is the session-scoped v2 reject on the request's captured session.
func (b *liveBackend) RejectQuestion(requestID string) error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	if err := b.doJSON(http.MethodPost, "/question/"+requestID+"/reject", nil, nil); err == nil {
		return nil
	}
	b.mu.Lock()
	hold, ok := b.ctx.pendingQuestions[requestID]
	b.mu.Unlock()
	if !ok || hold.SessionID == "" {
		return errors.New("question.reject failed and the request's session is unknown")
	}
	return b.doJSON(http.MethodPost, "/api/session/"+hold.SessionID+"/question/"+requestID+"/reject", nil, nil)
}

// ---------------------------------------------------------------- abort (/stop)

// AbortSessions is the live /stop contract (state.SessionAborter — the app
// type-asserts it; deliberately NOT in state.Backend so harness stubs are
// untouched). It POSTs /session/{id}/abort against the primary session AND
// every live child session: an opencode abort ends only its own session's
// run ("stop any ongoing AI processing or command execution", /doc
// 1.18.19), so sub-agent work the boss fanned out must be called out
// session by session. The OFFICE CONCIERGE (when one is registered — it
// rides ctx.employees as the pseudo-desk "concierge") is included in the
// same loop, so a busy boss + busy concierge + fan-out all stop together. Per-session failures are NON-fatal: each failure is
// noted on the status line, the sessions that DID abort still stop, and
// the collected errors.Join is the return value.
//
// Post-abort tidy: any boss text stream still mid-delta is flushed as a
// final "[theboringoffice] stream interrupted" bubble (same shape as Stop's
// graceful shutdown), and the OLDEST outstanding pending placeholder (the
// FIFO head — the RUNNING turn's bubble) closes with a "[theboringoffice] stopped"
// note so the UI never shows a frozen typing bubble for a dead turn when
// the serve drops the aborted turn without a completion pin. Placeholders
// BEHIND the head belong to queued prompts the serve still owns and will
// run next; they stay pending and complete when the server-side queue
// drains. lastAbortAt opens a short quiet window so the aborted turn's
// own empty completion (if the serve emits one) is swallowed instead of
// printing a "could not read reply" error for a turn the user killed on
// purpose.
func (b *liveBackend) AbortSessions() error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	b.mu.Lock()
	base := b.baseURL
	primaryID := b.primaryID
	var ids []string
	if primaryID != "" {
		ids = append(ids, primaryID)
	}
	for id := range b.ctx.employees {
		if !b.ctx.fired[id] && !b.ctx.returned[id] {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	if base == "" {
		return errors.New("backend not started")
	}
	if len(ids) == 0 {
		return nil // nothing running; not an error
	}
	// Open the quiet window BEFORE the first POST: the serve reports the
	// abort as session.error "Aborted" on SSE the instant it processes the
	// request, which races our return path — the latch must already be armed
	// when that echo lands (see onEvent / maybeBossCompleted).
	b.mu.Lock()
	b.lastAbortAt = nowMs()
	b.mu.Unlock()
	var errs []error
	aborted := 0
	for _, id := range ids {
		if err := b.abortSession(id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] abort failed for session " + id + ": " + shortTitle(err.Error(), 100)})
			continue
		}
		aborted++
	}
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[theboringoffice] turn aborted (%d/%d session(s))", aborted, len(ids))})

	// Flush what the aborted turn leaves behind, then close the running
	// turn's placeholder: text streams flush interrupted (boss lane and
	// office lane alike — per-stream session routing), the FIFO heads get
	// the stopped marker.
	b.mu.Lock()
	streamEvs := interruptedStreamEvents(b.ctx, "[theboringoffice] stream interrupted")
	for id := range b.chatSlots {
		delete(b.chatSlots, id)
	}
	var headID string
	if aborted > 0 && len(b.pendingBoss) > 0 {
		headID = b.pendingBoss[0]
		b.pendingBoss = b.pendingBoss[1:]
	}
	var officeHeadID string
	if aborted > 0 && len(b.pendingOffice) > 0 {
		officeHeadID = b.pendingOffice[0]
		b.pendingOffice = b.pendingOffice[1:]
	}
	b.mu.Unlock()
	for _, e := range streamEvs {
		b.fl.emit(e)
	}
	if headID != "" {
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID:      headID,
			From:    "boss",
			Text:    "[theboringoffice] stopped (turn aborted)",
			At:      nowMs(),
			Pending: false,
		}})
	}
	if officeHeadID != "" {
		b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
			ID:      officeHeadID,
			From:    "office",
			Kind:    "office",
			Text:    "[theboringoffice] stopped (turn aborted)",
			At:      nowMs(),
			Pending: false,
		}})
	}
	return errors.Join(errs...)
}

// abortQuietMs is how long after AbortSessions an EMPTY primary completion
// is treated as the aborted turn's death rattle and swallowed (one
// completion only — the latch is single-shot in maybeBossCompleted).
const abortQuietMs = 15000

// abortSession cancels one session's in-flight turn: POST
// /session/{sessionID}/abort (opencode serve /doc, operationId
// session.abort, -> 200 "Aborted session"). Errors surface to the caller
// so the batch can note them per-id without sinking the whole round.
func (b *liveBackend) abortSession(sessionID string) error {
	return b.doJSON(http.MethodPost, "/session/"+sessionID+"/abort", nil, nil)
}

// ---------------------------------------------------------------- diffs

// fetchDiffAndEmit pulls GET /session/{id}/diff on completion paths that may
// have missed the inline session.diff event; paths already surfaced (by the
// SSE event) are skipped via ctx.diffSeen. Failures are silent — a session
// without snapshot support returns an error and there is nothing to show.
func (b *liveBackend) fetchDiffAndEmit(sessionID string) {
	b.mu.Lock()
	started := b.baseURL != "" && !b.fl.isStopped()
	primaryID := b.primaryID
	b.mu.Unlock()
	if !started || sessionID == "" {
		return
	}
	var diffs []ocSnapshotFileDiff
	if err := b.doJSON(http.MethodGet, "/session/"+sessionID+"/diff", nil, &diffs); err != nil {
		return
	}
	if len(diffs) == 0 {
		return
	}
	b.mu.Lock()
	empID, empName, _ := actorFor(sessionID, b.ctx, primaryID)
	var evs []state.Event
	for _, d := range diffs {
		if ev, ok := diffEvent(sessionID, empID, empName, d, b.ctx); ok {
			evs = append(evs, ev)
		}
	}
	b.mu.Unlock()
	for _, e := range evs {
		b.fl.emit(e)
	}
}

// latestAssistantText returns the newest non-empty assistant text part in
// a session; "" on any failure (abort, rename, network — not a return).
func (b *liveBackend) latestAssistantText(sessionID string) string {
	var rows []struct {
		Info  ocMessage `json:"info"`
		Parts []ocPart  `json:"parts"`
	}
	if err := b.doJSON(http.MethodGet, "/session/"+sessionID+"/message", nil, &rows); err != nil {
		return ""
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Info.Role != "assistant" {
			continue
		}
		for _, part := range rows[i].Parts {
			if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
				return strings.TrimSpace(part.Text)
			}
		}
	}
	return ""
}

// messageText fetches ONLY the completing message's own parts
// (GET /session/{id}/message/{messageID} — /doc operationId
// "session.message") and joins its text parts. The completion is pinned to
// the message ID that fired message.updated — no session-latest fallback:
// on a reused session the newest assistant text can be a PREVIOUS turn's
// (or previous day's) reply, which is exactly the stale-bubble bug. The
// returned finish stamp ("stop", "tool-calls", ...) lets the caller tell a
// mid-turn tool-call message (legitimately no text) from a real empty end.
func (b *liveBackend) messageText(sessionID, messageID string) (text string, finish string, err error) {
	var row struct {
		Info  ocMessage `json:"info"`
		Parts []ocPart  `json:"parts"`
	}
	if err := b.doJSON(http.MethodGet, "/session/"+sessionID+"/message/"+messageID, nil, &row); err != nil {
		return "", "", err
	}
	var parts []string
	for _, part := range row.Parts {
		if part.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(part.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n"), row.Info.Finish, nil
}

// deleteChild best-effort deletes a returned child session; success fires
// the employee unless session.deleted already did. When the departed was
// the LAST real CTO on the floor, the idle pseudo-CTO is re-seated in the
// exec suite right after the fire (mirror of the session.deleted mapper in
// events.go — both fire paths share the seatPseudoCTO latch + the
// liveCTOs guard, so overlapping architecture children re-seat exactly
// once, on the final departure).
func (b *liveBackend) deleteChild(sessionID string) {
	b.mu.Lock()
	if b.ctx.fired[sessionID] {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	err := b.doJSON(http.MethodDelete, "/session/"+sessionID, nil, nil)
	if err != nil {
		return
	}
	b.mu.Lock()
	if b.ctx.fired[sessionID] {
		b.mu.Unlock()
		return
	}
	b.ctx.fired[sessionID] = true
	emp, ok := b.ctx.employees[sessionID]
	delete(b.ctx.employees, sessionID)
	var reseat []state.Event
	if ok && emp.Role == state.RoleCTO && !b.ctx.pseudoCTO && b.ctx.liveCTOs() == 0 {
		reseat = b.ctx.seatPseudoCTO()
	}
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvFire, EmployeeID: sessionID})
	for _, e := range reseat {
		b.fl.emit(e)
	}
}

// ---------------------------------------------------------------- SSE

// waitOnline parks the caller while the office is OFFLINE: it returns
// (gen, true) once the gate is open (netOnline closed) or (0, false) on
// Stop. While online it returns immediately — callers pay one mutex + one
// chan read per loop pass. gen is re-read at wake time, so a loop that
// slept through an offline→online round sees the LATEST generation and can
// fresh-start its backoff ladder.
func (b *liveBackend) waitOnline() (int, bool) {
	for {
		if b.fl.isStopped() {
			return 0, false
		}
		b.mu.Lock()
		gate := b.netOnline
		gen := b.netGen
		b.mu.Unlock()
		select {
		case <-gate:
			b.mu.Lock()
			gen = b.netGen
			b.mu.Unlock()
			return gen, true
		case <-b.fl.done:
			return 0, false
		}
	}
}

// netGateFlip applies a connectivity flip to the gate plumbing — shut on
// offline, reopen on online, generation bumped on every flip — and hands
// back the in-flight SSE cancel (nil-safe for the caller). netShut guards
// the close so the boot-confirm online flip (gate already open from the
// constructor) never closes a closed channel.
func (b *liveBackend) netGateFlip(online bool) context.CancelFunc {
	b.mu.Lock()
	defer b.mu.Unlock()
	if online && b.netShut {
		close(b.netOnline)
		b.netShut = false
	} else if !online && !b.netShut {
		b.netOnline = make(chan struct{})
		b.netShut = true
	}
	b.netGen++
	return b.sseCancel
}

// onNetTransition is the watcher's emit callback — ONE event pair per
// connectivity flip, in the same anti-spam spirit as sseNote (the watcher
// itself fires only on transitions). OFFLINE: shut the gate (pump/pollLoop
// park) and announce the wait. ONLINE: reopen the gate, bump the
// generation (the pump fresh-starts its ladder), announce the resume, and
// soft-cancel the in-flight SSE attempt — a no-op on an already-dead
// stream, an instant abort on a live one hung on a workless read.
func (b *liveBackend) onNetTransition(online bool) {
	cancel := b.netGateFlip(online)
	if !online {
		b.fl.emit(state.Event{Kind: state.EvOffline})
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] offline — office waiting for internet…"})
		return
	}
	b.fl.emit(state.Event{Kind: state.EvOnline})
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] back online — resumed"})
	if cancel != nil {
		cancel()
	}
}

// sseBackoffSteps is the reconnect ladder after a stream pass ends without
// delivering a single frame: 1s, then 2s, 5s, 10s, then 30s forever
// (capped). The FIRST frame read off a stream resets the ladder to 1s —
// a connection that was healthy earns the fast retry again. This replaces
// the fixed 1s reconnect that flooded the status line every second while a
// serve was down (D1).
var sseBackoffSteps = []time.Duration{
	1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second,
}

// pump owns the SSE connection for the backend's lifetime: connect, scan
// `data:` frames, dispatch; reconnect on EOF/error until Stop cancels the
// SSE context, waiting out the sseBackoffSteps ladder between passes.
//
// Status notes are deduped per failure class (sseNote): an outage reports
// ONCE when it starts, once more only if the failure CLASS changes (e.g.
// clean close -> HTTP 500), stays silent through every ladder retry, and
// reports recovery with exactly one "[theboringoffice] event stream: reconnected"
// line (sseRecovered, fired by streamOnce's first frame). Never one status
// line per second while the server is down.
func (b *liveBackend) pump() {
	fails := 0   // consecutive passes that delivered no frame
	lastGen := 0 // connectivity gate generation last seen (a flip fresh-starts the ladder)
	for {
		// Park while OFFLINE: the office is waiting for the internet, so
		// nothing here reconnects, notes, or climbs the ladder.
		gen, up := b.waitOnline()
		if !up {
			return
		}
		if gen != lastGen {
			fails = 0 // back online: re-attach immediately on the fast step
			lastGen = gen
		}
		if b.fl.isStopped() {
			return
		}
		progressed, err := b.streamOnce()
		if b.fl.isStopped() {
			return
		}
		if progressed {
			fails = 0
		} else {
			fails++
		}
		step := fails - 1 // the pass that just ended healthy retries fast
		if step < 0 {
			step = 0
		}
		if step >= len(sseBackoffSteps) {
			step = len(sseBackoffSteps) - 1
		}
		wait := sseBackoffSteps[step]
		if err == nil {
			b.sseNote("closed", "[theboringoffice] event stream closed (board/mail continue; re-attaching in "+shortDur(wait)+")")
		} else {
			b.sseNote(sseErrClass(err), "[theboringoffice] event stream error: "+shortTitle(err.Error(), 100)+
				" (re-attaching in "+shortDur(wait)+")")
		}
		select {
		case <-b.fl.done:
			return
		case <-time.After(wait):
		}
	}
}

// sseNote emits an SSE status note at most ONCE per failure-class change:
// the same outage retrying through the backoff ladder is silent after its
// first report. The latch clears on recovery (sseRecovered), so the next
// outage reports fresh.
func (b *liveBackend) sseNote(sig, text string) {
	b.mu.Lock()
	fresh := sig != b.sseNoteSig
	if fresh {
		b.sseNoteSig = sig
	}
	b.mu.Unlock()
	if fresh {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: text})
	}
}

// sseRecovered is streamOnce's first-frame hook: when the stream yields
// data after a reported outage or close, the ONE recovery line goes out
// and the dedupe latch clears so a later outage reports fresh.
func (b *liveBackend) sseRecovered() {
	b.mu.Lock()
	had := b.sseNoteSig
	b.sseNoteSig = ""
	b.mu.Unlock()
	if had != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] event stream: reconnected"})
	}
}

// sseErrClass buckets an SSE failure for the dedupe latch: the same outage
// (a dead serve refusing dials over and over) reads as ONE class no matter
// how many times the pump retries. Transport trouble collapses to the
// request op ("request Get") so a changing host:port in the error text
// can't defeat the latch; HTTP-status and other failures key on their
// flattened message, which is already class-shaped ("event subscribe: HTTP
// 500").
func sseErrClass(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return "request " + uerr.Op
	}
	return shortTitle(err.Error(), 80)
}

// streamOnce runs one SSE connection to its EOF or error. progressed
// reports whether at least one data frame was read off the stream — the
// pump uses it to reset the reconnect ladder (a stream that read frames
// was healthy and earns the fast 1s retry; a pass that failed before its
// first frame climbs the ladder). The FIRST frame after a reported outage
// also fires the one-shot recovery note (sseRecovered).
func (b *liveBackend) streamOnce() (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.sseCancel = cancel
	base := b.baseURL
	b.mu.Unlock()
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/event?directory="+url.QueryEscape(b.directory), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-opencode-directory", url.QueryEscape(b.directory))
	res, err := b.sseClient.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("event subscribe: HTTP %d", res.StatusCode)
	}

	progressed := false
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if b.fl.isStopped() {
			return progressed, nil
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if !progressed {
			progressed = true
			b.sseRecovered()
		}
		var raw ocSSEEvent
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			continue
		}
		if debugSSE {
			// THEBORINGOFFICE_DEBUG_SSE=1: raw stream trace — event type plus, for
			// part traffic, the part id/type/text length so reasoning
			// streaming behaviour can be verified without a proxy.
			note := raw.Type
			var pp struct {
				Part    ocPart `json:"part"`
				PartID  string `json:"partID"`
				Field   string `json:"field"`
				Delta   string `json:"delta"`
				Session string `json:"sessionID"`
			}
			if json.Unmarshal(raw.Properties, &pp) == nil {
				switch {
				case pp.Part.ID != "":
					note += " part.id=" + pp.Part.ID + " part.type=" + pp.Part.Type +
						" part.text.len=" + itoa(len([]rune(pp.Part.Text))) +
						" part.time.end=" + itoa64(pp.Part.Time.End)
				case pp.PartID != "":
					note += " partID=" + pp.PartID + " field=" + pp.Field +
						" delta.len=" + itoa(len([]rune(pp.Delta)))
				}
			}
			fmt.Fprintf(os.Stderr, "[sse-raw] %s | %s\n", note, trimTo(payload, 400))
		}
		if err := b.onEvent(raw); err != nil {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[theboringoffice] event handling failed (" + raw.Type + "): " + shortTitle(err.Error(), 100)})
		}
	}
	return progressed, sc.Err()
}

// ---------------------------------------------------------------- thought gate

// thoughtMinGapMs is the per-CallID EvThought emission floor: the UI gets
// ~7 fps of transcript growth instead of every token.
const thoughtMinGapMs = 150

// thoughtSlot is the coalescing state for one thought's stream.
type thoughtSlot struct {
	pending *state.Event // latest coalesced update waiting for the gap
	lastAt  int64        // ms of the last emitted update for this CallID
	ticking bool         // a flush timer is already in flight
}

// emitThought gates EvThought bursts per CallID: emit now if the gap has
// passed, otherwise stash the event as the slot's pending update (any older
// pending is dropped — the LAST update always wins) and arm one trailing
// flush. Done=true flushes immediately and retires the slot; order is never
// violated because SSE frames for a part are strictly ordered and a pending
// update is cleared when its successor ships.
func (b *liveBackend) emitThought(e state.Event) {
	if e.CallID == "" {
		b.fl.emit(e)
		return
	}
	now := nowMs()
	b.mu.Lock()
	slot := b.thoughtSlots[e.CallID]
	if slot == nil {
		slot = &thoughtSlot{}
		b.thoughtSlots[e.CallID] = slot
	}
	if e.Done {
		slot.pending = nil
		slot.lastAt = now
		delete(b.thoughtSlots, e.CallID)
		b.mu.Unlock()
		b.fl.emit(e)
		return
	}
	if now-slot.lastAt >= thoughtMinGapMs {
		slot.lastAt = now
		b.mu.Unlock()
		b.fl.emit(e)
		return
	}
	pending := e
	slot.pending = &pending
	if slot.ticking {
		b.mu.Unlock()
		return
	}
	slot.ticking = true
	wait := time.Duration(thoughtMinGapMs-(now-slot.lastAt)) * time.Millisecond
	b.mu.Unlock()
	b.fl.at(wait, func() { b.flushThought(e.CallID) })
}

// flushThought ships the coalesced trailing update for a CallID, if any.
func (b *liveBackend) flushThought(callID string) {
	b.mu.Lock()
	slot := b.thoughtSlots[callID]
	if slot == nil {
		b.mu.Unlock()
		return
	}
	slot.ticking = false
	pending := slot.pending
	slot.pending = nil
	if pending != nil {
		slot.lastAt = nowMs()
	}
	b.mu.Unlock()
	if pending != nil {
		b.fl.emit(*pending)
	}
}

// ---------------------------------------------------------------- chat stream gate

// emitChatStream gates the boss text-delta bursts per bubble ID, identical
// to the thought gate: at most one emit every thoughtMinGapMs, trailing
// flush keeps the LAST update. The completion pin (maybeBossCompleted) owns
// the final emit and deletes the slot, so no stale trailing update can land
// after the pinned text.
func (b *liveBackend) emitChatStream(e state.Event) {
	if e.Msg.ID == "" {
		b.fl.emit(e)
		return
	}
	now := nowMs()
	b.mu.Lock()
	slot := b.chatSlots[e.Msg.ID]
	if slot == nil {
		slot = &thoughtSlot{}
		b.chatSlots[e.Msg.ID] = slot
	}
	if now-slot.lastAt >= thoughtMinGapMs {
		slot.lastAt = now
		b.mu.Unlock()
		b.fl.emit(e)
		return
	}
	pending := e
	slot.pending = &pending
	if slot.ticking {
		b.mu.Unlock()
		return
	}
	slot.ticking = true
	wait := time.Duration(thoughtMinGapMs-(now-slot.lastAt)) * time.Millisecond
	b.mu.Unlock()
	b.fl.at(wait, func() { b.flushChatStream(e.Msg.ID) })
}

// flushChatStream ships the coalesced trailing chat update for a bubble ID.
// A deleted slot (completion pin or Stop) makes this a no-op.
func (b *liveBackend) flushChatStream(id string) {
	b.mu.Lock()
	slot := b.chatSlots[id]
	if slot == nil {
		b.mu.Unlock()
		return
	}
	slot.ticking = false
	pending := slot.pending
	slot.pending = nil
	if pending != nil {
		slot.lastAt = nowMs()
	}
	b.mu.Unlock()
	if pending != nil {
		b.fl.emit(*pending)
	}
}

// onEvent normalizes via events.go, then runs the I/O-needing branches.
func (b *liveBackend) onEvent(raw ocSSEEvent) error {
	b.mu.Lock()
	primaryID := b.primaryID
	conciergeID := b.conciergeID
	// Abort window: the serve reports a member-initiated /stop as
	// session.error "Aborted" on the primary (and concierge — AbortSessions
	// aborts the whole family) — protocol noise, since AbortSessions already
	// emitted the intentional "[theboringoffice] stopped" marker. Swallow those
	// while the quiet window is open; every other error (or any error
	// outside the window) maps normally.
	if raw.Type == "session.error" && b.lastAbortAt != 0 && nowMs()-b.lastAbortAt < abortQuietMs {
		var p ocSessionErrorProps
		if json.Unmarshal(raw.Properties, &p) == nil && (p.SessionID == primaryID || p.SessionID == conciergeID) {
			msg := "aborted"
			if p.Error != nil && p.Error.Data.Message != "" {
				msg = p.Error.Data.Message
			}
			b.mu.Unlock()
			if strings.Contains(strings.ToLower(msg), "abort") {
				return nil
			}
			// A REAL error in the window still surfaces: feed it through the
			// normal mapping below.
			b.mu.Lock()
		}
	}
	events := mapOCEvent(raw, b.ctx, primaryID, nowMs())
	// A fresh child dispatch re-arms the CTO review latch: the batch that
	// just opened owes him exactly one review when it drains.
	for _, e := range events {
		if e.Kind == state.EvDispatch {
			b.review.arm()
		}
	}
	b.mu.Unlock()
	for _, e := range events {
		if e.Kind == state.EvThought {
			b.emitThought(e)
			continue
		}
		// Streaming boss chat updates (Pending:true, "bossmsg-"+messageID)
		// coalesce at ~7fps like thoughts; placeholders and finals pass.
		if e.Kind == state.EvChatBoss && e.Msg.Pending && strings.HasPrefix(e.Msg.ID, "bossmsg-") {
			b.emitChatStream(e)
			continue
		}
		// The concierge's streaming office bubbles ride the same gate,
		// keyed by the disjoint "office-"+messageID prefix (one lane per
		// message, no identity collision with bossmsg-).
		if e.Kind == state.EvChatOffice && e.Msg.Pending && strings.HasPrefix(e.Msg.ID, "office-") {
			b.emitChatStream(e)
			continue
		}
		b.fl.emit(e)
	}

	switch raw.Type {
	case "session.idle":
		var p struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(raw.Properties, &p) == nil {
			b.maybeChildReturned(p.SessionID)
		}
	case "session.status":
		var p ocSessionStatusProps
		if json.Unmarshal(raw.Properties, &p) == nil && p.Status.Type == "idle" {
			b.maybeChildReturned(p.SessionID)
		}
	case "message.updated":
		var p struct {
			Info ocMessage `json:"info"`
		}
		if json.Unmarshal(raw.Properties, &p) == nil {
			b.maybeBossCompleted(p.Info)
			b.maybeOfficeCompleted(p.Info)
		}
	}
	return nil
}

// maybeChildReturned: child went idle — a real return only if an assistant
// text part exists. Emits task-done + returned+mail, then schedules the
// best-effort child delete 10s out (-> fire).
func (b *liveBackend) maybeChildReturned(sessionID string) {
	b.mu.Lock()
	_, known := b.ctx.employees[sessionID]
	already := b.ctx.returned[sessionID]
	started := b.baseURL != ""
	isConcierge := sessionID != "" && sessionID == b.conciergeID
	b.mu.Unlock()
	if !known || already || !started || isConcierge {
		// isConcierge: the concierge lives in ctx.employees as a pseudo-desk
		// (So its children hire + its tools attribute) but it is NOT a
		// brief — idle there is answer-done, never a return/mail/fire.
		return
	}

	text := b.latestAssistantText(sessionID)
	if text == "" {
		return // no assistant output — not a return
	}
	// The child's edits surface as diffs next to its return (completion-time
	// fetch; the session.diff event wins when the server emits one).
	b.fetchDiffAndEmit(sessionID)

	b.mu.Lock()
	if b.ctx.returned[sessionID] {
		b.mu.Unlock()
		return
	}
	b.ctx.returned[sessionID] = true
	emp := b.ctx.employees[sessionID]
	prev, ok := b.ctx.tasks[sessionID]
	if !ok {
		title := emp.Task
		if title == "" {
			title = "untitled brief"
		}
		prev = state.BoardTask{
			ID:     "task-" + sessionID,
			Title:  title,
			Status: state.TaskInProgress,
			Owner:  emp.Name,
			At:     nowMs(),
		}
	}
	done := prev
	done.Status = state.TaskDone
	b.ctx.tasks[sessionID] = done
	mail := state.MailItem{
		ID:      "mail-" + sessionID,
		From:    emp.Name,
		To:      "manager",
		At:      nowMs(),
		Subject: "return: " + prev.Title,
		Body:    sliceMax(text, 240),
		Kind:    state.MailReturn,
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvTask, Task: done})
	b.fl.emit(state.Event{Kind: state.EvReturned, EmployeeID: sessionID, TaskID: done.ID, Mail: mail})

	// Board-drain review: when THIS return was the last open brief of the
	// batch (zero child-session tasks left in non-done states), the CTO
	// posts his ONE review beat — the latch spends it, only a fresh child
	// dispatch re-arms (see onEvent). Locking mirrors the block above.
	b.mu.Lock()
	review := b.review.beat(countBoard(b.ctx.tasks))
	b.mu.Unlock()
	for _, e := range review {
		b.fl.emit(e)
	}

	// Tidy the org chart: delete the child 10s later (best effort).
	b.fl.at(10*time.Second, func() { b.deleteChild(sessionID) })
}

// maybeBossCompleted: boss replied — emit a chat-boss bubble pinned to the
// COMPLETING message's own text.
//
// Identity + dedupe: bubble ID is "bossmsg-"+<messageID> (deterministic) —
// the SAME ID the text-delta stream grew under, so one bubble identity
// spans stream + completion and the UI replaces the growing bubble with
// this pinned text. bossCompleted remembers every completion seen, so a
// repeated message.updated for the same ID is swallowed before any re-emit
// (the reducer would otherwise append a second copy). Pending placeholders
// keep their swap semantics: any EvChatBoss strips the pending bubble, so
// the first completed bubble after a Send replaces it and later
// completions of the same turn append as their own distinct bubbles.
//
// Stream handoff: completion STOPS the delta stream for this message
// (parts unregistered, accumulator freed, any coalesced trailing update
// dropped) — the pinned fetch text supersedes the accumulated deltas.
//
// Text selection: ONLY the completing message's own text parts
// (messageText), NEVER the session-latest assistant text — on a reused
// session the newest text-bearing assistant message can be an older turn's
// reply, which was the stale-repeat bug. A fetch failure, or an empty
// final message, emits the dim error line instead; a message that finished
// with "tool-calls" is mid-turn protocol (its text rides the NEXT
// assistant message) and legitimately emits nothing.
func (b *liveBackend) maybeBossCompleted(info ocMessage) {
	b.mu.Lock()
	if info.SessionID != b.primaryID || info.Role != "assistant" || info.Time.Completed == 0 || b.bossCompleted[info.ID] {
		b.mu.Unlock()
		return
	}
	b.bossCompleted[info.ID] = true
	// Stop the delta stream for this message: unregister its text parts,
	// free the accumulator, and drop any coalesced trailing update still
	// in flight — the pinned text below replaces the growing bubble.
	unregisterTextStream(b.ctx, info.ID)
	delete(b.chatSlots, "bossmsg-"+info.ID)
	primaryID := b.primaryID
	b.mu.Unlock()

	text, finish, err := b.messageText(primaryID, info.ID)
	if err != nil {
		text = "[theboringoffice] could not read reply (msg " + info.ID + ")"
	} else if text == "" {
		if finish == "tool-calls" {
			return // mid-turn message; the text rides the continuation message
		}
		// Abort window: an EMPTY completion right after AbortSessions is the
		// aborted turn's death rattle — the user already got the "[theboringoffice]
		// stopped" marker, so swallow it (one completion only; the aborted
		// placeholder was already popped by AbortSessions, hence the extra
		// pendingBoss pop below keeps the FIFO balanced). A completion
		// carrying actual partial text pins normally — what the model wrote
		// before the stop is worth showing.
		b.mu.Lock()
		aborted := b.lastAbortAt != 0 && nowMs()-b.lastAbortAt < abortQuietMs
		b.lastAbortAt = 0
		if aborted && len(b.pendingBoss) > 0 {
			b.pendingBoss = b.pendingBoss[1:]
		}
		b.mu.Unlock()
		if aborted {
			return
		}
		text = "[theboringoffice] could not read reply (msg " + info.ID + ")"
	}
	// Boss edits surface as diff events on message completion.
	b.fetchDiffAndEmit(primaryID)

	b.mu.Lock()
	if len(b.pendingBoss) > 0 {
		b.pendingBoss = b.pendingBoss[1:]
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
		ID: "bossmsg-" + info.ID, From: "boss", Kind: "boss", Text: text, At: nowMs(), Pending: false,
	}})
}

// maybeOfficeCompleted: the concierge replied — emit an EvChatOffice bubble
// pinned to the COMPLETING message's own text. This is the exact mirror of
// maybeBossCompleted on the office lane: identity "office-"+messageID,
// From/Kind "office", Pending:false; same one-completion dedupe; same
// stream handoff (deltas stop, pinned fetch text supersedes); same
// mid-turn "tool-calls" swallow (its text rides the continuation message);
// same abort-window quiet for the aborted turn's empty death rattle.
// Sessions are disjoint — primary completions never reach here and
// concierge completions never reach maybeBossCompleted — so exactly ONE of
// EvChatOffice/EvChatBoss fires per assistant message.
func (b *liveBackend) maybeOfficeCompleted(info ocMessage) {
	b.mu.Lock()
	if conciergeID := b.conciergeID; conciergeID == "" ||
		info.SessionID != conciergeID || info.Role != "assistant" ||
		info.Time.Completed == 0 || b.officeCompleted[info.ID] {
		b.mu.Unlock()
		return
	}
	b.officeCompleted[info.ID] = true
	// Stop the delta stream for this message (same handoff as the boss:
	// unregister parts, drop any coalesced trailing update still in flight).
	unregisterTextStream(b.ctx, info.ID)
	delete(b.chatSlots, "office-"+info.ID)
	conciergeID := b.conciergeID
	b.mu.Unlock()

	text, finish, err := b.messageText(conciergeID, info.ID)
	if err != nil {
		text = "[theboringoffice] could not read reply (msg " + info.ID + ")"
	} else if text == "" {
		if finish == "tool-calls" {
			return // mid-turn message; the text rides the continuation message
		}
		// Abort window: an EMPTY concierge completion right after
		// AbortSessions (which aborts the concierge too) is the aborted
		// turn's death rattle — the member already has the stopped marker.
		b.mu.Lock()
		aborted := b.lastAbortAt != 0 && nowMs()-b.lastAbortAt < abortQuietMs
		b.lastAbortAt = 0
		if aborted && len(b.pendingOffice) > 0 {
			b.pendingOffice = b.pendingOffice[1:]
		}
		b.mu.Unlock()
		if aborted {
			return
		}
		text = "[theboringoffice] could not read reply (msg " + info.ID + ")"
	}
	// Concierge edits (it can dispatch AND touch files directly) surface as
	// diff events on completion, same as the boss lane.
	b.fetchDiffAndEmit(conciergeID)

	b.mu.Lock()
	if len(b.pendingOffice) > 0 {
		b.pendingOffice = b.pendingOffice[1:]
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatOffice, Msg: state.ChatMsg{
		ID: "office-" + info.ID, From: "office", Kind: "office", Text: text, At: nowMs(), Pending: false,
	}})
}

// ---------------------------------------------------------------- agentmemory sync

// amPollBase derives the board poll cadence from cfg.Backend.AgentmemoryPollS:
// 0 or negative -> the historic 5s; less than 1s is clamped to 1s.
func amPollBase(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 5
	}
	if seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

// Backoff tuning: after backoffStep consecutive syncs that observed no board
// change, the poll interval doubles, capped at backoffMaxFactor x the base.
// The FIRST observed change resets cadence to base immediately.
const backoffStep = 5
const backoffMaxFactor = 4

// BackoffInterval computes the next agentmemory poll wait given the current
// wait and the running no-change count for the sync that just finished.
// Pure — exported so cmd/headless --efficiency can simulate the cadence
// without a live server. It never shortens the interval here (the change
// path is the caller's base-reset) and never exceeds backoffMaxFactor x base.
func BackoffInterval(base, current time.Duration, noChange int) time.Duration {
	if base <= 0 {
		base = 5 * time.Second
	}
	if noChange > 0 && noChange%backoffStep == 0 {
		max := backoffMaxFactor * base
		if dbl := 2 * current; dbl <= max {
			return dbl
		}
		return max
	}
	return current
}

// pollLoop replaces the fixed ticker: syncBoard, then wait the current
// cadence. The backend cannot see the office's pending queue, so battery
// savings come from exponential backoff instead of activity indicators —
// an uneventful board drifts from the base cadence to 4x base; any observed
// change snaps back. Timing: Stop closes fl.done, which wakes the select.
func (b *liveBackend) pollLoop(base time.Duration) {
	interval := base
	noChange := 0
	for {
		// Park while OFFLINE: a dead board lane must not hammer agentmemory
		// (or spam its backoff notes) while the office waits for internet.
		if _, up := b.waitOnline(); !up {
			return
		}
		// Wait FIRST: Start already ran one warming sync before this goroutine
		// began, mirroring the old fixed-ticker cadence exactly.
		select {
		case <-b.fl.done:
			return
		case <-time.After(interval):
		}
		if b.fl.isStopped() {
			return
		}
		changed := b.syncBoard()
		if changed {
			if noChange > 0 && interval != base {
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
					"[theboringoffice] board poll: change observed — cadence back to %s", shortDur(base))})
			}
			noChange = 0
			interval = base
		} else {
			noChange++
			if next := BackoffInterval(base, interval, noChange); next != interval {
				interval = next
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
					"[theboringoffice] board poll backoff: %s after %d unchanged syncs (cap %s)",
					shortDur(interval), noChange, shortDur(backoffMaxFactor*base))})
			}
		}
	}
}

// shortDur renders a Duration the way the status line likes it.
func shortDur(d time.Duration) string {
	return d.Round(time.Second).String()
}

// syncBoard polls agentmemory -> board/mail and reports whether anything
// changed (backs the poll backoff; task rows dedupe on title|status|owner,
// mail rows on first sight only).
func (b *liveBackend) syncBoard() bool {
	if b.fl.isStopped() || b.am == nil || b.am.kind != "actions" {
		return false
	}
	changed := false
	tasks := b.am.listActions()
	mails := b.am.listMails()
	for _, task := range tasks {
		key := task.Title + "|" + string(task.Status) + "|" + task.Owner
		b.mu.Lock()
		stale := b.amTasks[task.ID] != key
		if stale {
			b.amTasks[task.ID] = key
		}
		b.mu.Unlock()
		if stale {
			changed = true
			b.fl.emit(state.Event{Kind: state.EvTask, Task: task})
		}
	}
	for _, mail := range mails {
		b.mu.Lock()
		seen := b.amMails[mail.ID]
		b.amMails[mail.ID] = true
		b.mu.Unlock()
		if !seen {
			changed = true
			b.fl.emit(state.Event{Kind: state.EvMail, Mail: mail})
		}
	}
	return changed
}
