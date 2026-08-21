// opencode.go — the LIVE backend for grafeio. Port of
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
// "[grafeio] stream interrupted" note.
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

	"github.com/theboringhumane/grafeio/internal/config"
	"github.com/theboringhumane/grafeio/internal/state"
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
}

func newLiveBackend(baseURL, directory string, cfg *config.Config) *liveBackend {
	return &liveBackend{
		directory:     directory,
		optURL:        baseURL,
		cfg:           cfg,
		fl:            newFlow(),
		ctx:           newNormCtx(cfg),
		bossCompleted: make(map[string]bool),
		thoughtSlots:  make(map[string]*thoughtSlot),
		chatSlots:     make(map[string]*thoughtSlot),
		amTasks:       make(map[string]string),
		amMails:       make(map[string]bool),
		client:        &http.Client{Timeout: 15 * time.Second},
		sseClient:     &http.Client{},
	}
}

func (b *liveBackend) Mode() state.Mode { return state.ModeLive }

// ---------------------------------------------------------------- start

func (b *liveBackend) Start(emit func(state.Event)) error {
	b.fl.setEmit(emit)

	// Manager charter (oikonomos) runs FIRST, before server resolution:
	// any directory grafeio serves gets .opencode/oikonomos.md + the
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
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] manager charter: restarting serve so it picks up the config"})
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

	// Fixed seats: the boss and Mnemosyne (hr) are always on the floor.
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: primary.ID, Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk,
	}})
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr", Sprite: state.SpriteAtDesk,
	}})

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
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] live - " + u + " | board: " + board})

	if b.cfg.Boss.Model != "" {
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] boss model override: " + string(b.cfg.Boss.Model)})
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
func (b *liveBackend) Send(text string) error {
	return b.SendWith(text, nil)
}

// SendWith is Send + chat-input attachments: the user-bubble echo carries
// their names in ChatMsg.Meta (state.AttachMeta — "att ␟ name ␟ name…",
// the chat panel renders the dim " · 📎 N" suffix from it) and the prompt
// posts one file part per readable attachment (parts.go). Semantics of
// the plain Send otherwise: echo chat-user, stage ONE pending boss bubble
// (FIFO id boss-N), POST the prompt async. Completed assistant messages
// arrive over SSE and emit their own pinned "bossmsg-"+<messageID>
// bubbles; the FIRST of them strips the pending placeholder (the reducer
// drops pending bubbles on any EvChatBoss), later ones append. A prompt
// error re-emits the SAME pending id with the failure note instead.
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
	// session on demand — forced-fresh ("grafeio office · respawn") when
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
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] primary session respawned fresh (" + primary.ID + ")"})
			}
		} else {
			primary, perr = b.ensurePrimary()
		}
		if perr != nil {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] primary respawn failed: " + shortTitle(perr.Error(), 100)})
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
			ID: deadID, From: "boss", Text: "[grafeio] backend not started", At: nowMs(), Pending: false,
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
			Text:    "[grafeio] prompt failed: " + shortTitle(err.Error(), 120),
			At:      nowMs(),
			Pending: false,
		}})
	}
	return nil
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
	streamEvs := interruptedStreamEvents(b.ctx, "[grafeio] stream interrupted")
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
	proc := b.proc
	b.proc = nil
	b.mu.Unlock()

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

// debugSSE toggles the raw SSE trace in streamOnce (GRAFEIO_DEBUG_SSE=1).
var debugSSE = os.Getenv("GRAFEIO_DEBUG_SSE") != ""

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
// turns earlier) and a fresh "grafeio office" session is created anyway.
const STALE_SESSION_MSG_LIMIT = 50

// ensurePrimary reuses the newest root session for this directory, else
// creates one titled "grafeio office". Reuse passes the stale check first:
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
					"[grafeio] primary session %s has %d msgs (> %d, stale) — creating fresh", newest.ID, count, STALE_SESSION_MSG_LIMIT)})
				return b.createPrimary(b.bossName())
			}
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
				"[grafeio] primary session: reuse %s (%d msgs)", newest.ID, count)})
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
				"[grafeio] primary session: restored %s (office session on disk)", s.ID)})
			return s, nil
		}
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] saved office session's primary " + override + " is gone server-side — normal find-or-create instead"})
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
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] board action create failed: " + shortTitle(err.Error(), 100)})
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
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] board action mark done failed: " + shortTitle(err.Error(), 100)})
	}
}

// ResetPrimary clears the hold on the primary session so the NEXT Send
// lazily establishes a replacement (nothing is archived/deleted — the old
// session simply stops being the boss). With forceNew=true the
// replacement is a BRAND-NEW session titled "grafeio office · respawn",
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
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
		"[grafeio] primary session reset (forceNew=%v) — next send respawns", forceNew)})
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
// primary titled "grafeio office" (bossName()) NOW — not lazily on the
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
	b.mu.Unlock()
	if old != "" && old != primary.ID {
		b.fl.emit(state.Event{Kind: state.EvFire, EmployeeID: old})
	}
	b.fl.emit(state.Event{Kind: state.EvHire, Employee: state.Employee{
		ID: primary.ID, Name: "manager", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk,
	}})
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] new office session fresh (" + primary.ID + ")"})
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
// cfg.Boss.Model rides as {"model":{"providerID","modelID"}} — the exact
// shape serve 1.18.19 documents in GET /doc for prompt_async (verified
// 2026-08-21 against the spawned server). A ModelRef without a
// "provider/model" slash is ignored with a status note. If a serve ever
// rejects the model field with 400 (an older/foreign server), the override
// latches off and the prompt retries bare — degrade open, never fake it.
func (b *liveBackend) postPrompt(sessionID, text string, atts []state.Attachment) error {
	parts, skipped := payloadParts(text, atts)
	if len(skipped) > 0 {
		// The prompt still goes out with whatever parts survived — the
		// member sees exactly which attachment didn't make it.
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] could not attach " +
			strings.Join(skipped, ", ") + " (file unreadable) — sent without it"})
	}
	payload := map[string]any{"parts": parts}
	provider, model := splitModelRef(string(b.cfg.Boss.Model))
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
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] boss model override unavailable on this serve (400 rejected the model field) — continuing without it"})
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] boss model override unavailable in serve (see /doc session.prompt_async): retrying bare prompt"})
		// Retry bare exactly once: the member-visible cost of the failed
		// POST was zero (rejected before the turn started).
		payload = map[string]any{"parts": parts}
		body, _ = json.Marshal(payload)
		err = b.doJSON(http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil)
	}
	return err
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
	return "grafeio office"
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
// string-array per asked question; -> 200 boolean). Fallback is the
// session-scoped v2 route
// (POST /api/session/{sessionID}/question/{requestID}/reply, same body
// shape) keyed by the session the request was seen on via the normCtx hold.
// NOTE: /doc 1.18.19 exposes NO /session/{id}/questions/... legacy shim for
// questions the way permissions had — the v2 route is the only fallback.
func (b *liveBackend) AnswerQuestion(requestID string, answers []string) error {
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	wrapped := make([][]string, len(answers))
	for i, a := range answers {
		wrapped[i] = []string{a}
	}
	body, _ := json.Marshal(map[string]any{"answers": wrapped})
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
// the employee unless session.deleted already did.
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
	delete(b.ctx.employees, sessionID)
	b.mu.Unlock()
	b.fl.emit(state.Event{Kind: state.EvFire, EmployeeID: sessionID})
}

// ---------------------------------------------------------------- SSE

// pump owns the SSE connection for the backend's lifetime: connect, scan
// `data:` frames, dispatch; reconnect with a 1s backoff on EOF/error until
// Stop cancels the SSE context.
func (b *liveBackend) pump() {
	for {
		if b.fl.isStopped() {
			return
		}
		err := b.streamOnce()
		if b.fl.isStopped() {
			return
		}
		if err == nil {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] event stream closed (board/mail continue; re-attaching)"})
		} else {
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] event stream error: " + shortTitle(err.Error(), 100)})
		}
		select {
		case <-b.fl.done:
			return
		case <-time.After(1 * time.Second):
		}
	}
}

// streamOnce runs one SSE connection to its EOF or error.
func (b *liveBackend) streamOnce() error {
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.sseCancel = cancel
	base := b.baseURL
	b.mu.Unlock()
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/event?directory="+url.QueryEscape(b.directory), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-opencode-directory", url.QueryEscape(b.directory))
	res, err := b.sseClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("event subscribe: HTTP %d", res.StatusCode)
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if b.fl.isStopped() {
			return nil
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var raw ocSSEEvent
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			continue
		}
		if debugSSE {
			// GRAFEIO_DEBUG_SSE=1: raw stream trace — event type plus, for
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
			b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] event handling failed (" + raw.Type + "): " + shortTitle(err.Error(), 100)})
		}
	}
	return sc.Err()
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
	events := mapOCEvent(raw, b.ctx, primaryID, nowMs())
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
	b.mu.Unlock()
	if !known || already || !started {
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
		text = "[grafeio] could not read reply (msg " + info.ID + ")"
	} else if text == "" {
		if finish == "tool-calls" {
			return // mid-turn message; the text rides the continuation message
		}
		text = "[grafeio] could not read reply (msg " + info.ID + ")"
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
					"[grafeio] board poll: change observed — cadence back to %s", shortDur(base))})
			}
			noChange = 0
			interval = base
		} else {
			noChange++
			if next := BackoffInterval(base, interval, noChange); next != interval {
				interval = next
				b.fl.emit(state.Event{Kind: state.EvStatus, Text: fmt.Sprintf(
					"[grafeio] board poll backoff: %s after %d unchanged syncs (cap %s)",
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
