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
//     later), primary-assistant-completed -> chat-boss on the pending id.
//   - sync board + mail from agentmemory every 5s when the probe found it.
//
// Chat path: Send drives the same emit callback Start received, so the
// user message and the pending boss bubble hit state immediately; the
// prompt POST returns at once and the SSE stream drives the completion.
// Pending bubbles are a FIFO of ids (boss-N); a completion shifts the
// oldest id and re-emits it with the real text (same-id swap protocol —
// the reducer replaces the bubble with the matching id).
//
// Note: unlike the demo backend, this backend never emits EvTick — the app
// owns the animation timer for live mode.
//
// Thought streaming: message.part.delta frames make EvThought events arrive
// at token rate (tens per second). emitThought coalesces per CallID: at
// most one emit every thoughtMinGapMs, keeping the LAST update in flight
// and dropping intermediates (coalesce, never reorder). A Done=true always
// flushes immediately so the block collapses on completion, not 150ms late.
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

	"github.com/theboringhumane/grafeio/internal/state"
)

type liveBackend struct {
	directory string
	optURL    string

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
	am            *amHandle
	amTasks       map[string]string // id -> dedupe key
	amMails       map[string]bool
	lastUserText  string // belt-and-braces echo dedupe (see Send)
	lastUserAt    int64
}

func newLiveBackend(baseURL, directory string) *liveBackend {
	return &liveBackend{
		directory:     directory,
		optURL:        baseURL,
		fl:            newFlow(),
		ctx:           newNormCtx(),
		bossCompleted: make(map[string]bool),
		thoughtSlots:  make(map[string]*thoughtSlot),
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

	u := b.optURL
	if u == "" {
		u = os.Getenv("OPENCODE_SERVER")
	}
	if u == "" {
		spawnedURL, proc, err := spawnServe(b.directory)
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

	primary, err := b.ensurePrimary()
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

	b.am = probeAgentmemory(os.Getenv("AGENTMEMORY_URL"))
	board := "in-memory | agentmemory: offline (in-memory board)"
	if b.am.kind == "actions" {
		board = "agentmemory (" + b.am.winner + ")"
	}
	b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[grafeio] live - " + u + " | board: " + board})

	if b.am.kind == "actions" {
		b.syncBoard()
		b.fl.every(5*time.Second, b.syncBoard)
	}

	go b.pump()
	return nil
}

// ---------------------------------------------------------------- send

// Send pushes user chat to the boss: echo chat-user, stage ONE pending
// boss bubble (FIFO id boss-N), POST the prompt async. The completion
// arrives over SSE and re-emits the SAME bubble id with the real text —
// the swap protocol the reducer relies on. A prompt error also re-emits
// the same id, with the failure note instead.
func (b *liveBackend) Send(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || b.fl.isStopped() {
		return nil
	}

	// Belt-and-braces echo dedupe: the chat-user echo fires exactly once
	// per prompt. This backend never maps SSE message.updated (user role)
	// to chat again, but if the same text would fire twice within 2s
	// (double Send, retry path, app-side echo raced back in), swallow the
	// second echo — the prompt POST below still always runs.
	b.mu.Lock()
	now := nowMs()
	duplicate := trimmed == b.lastUserText && b.lastUserText != "" && now-b.lastUserAt < 2000
	if !duplicate {
		b.lastUserText = trimmed
		b.lastUserAt = now
	}
	b.chatSeq++
	userID := "user-" + itoa(b.chatSeq)
	b.mu.Unlock()
	if !duplicate {
		b.fl.emit(state.Event{Kind: state.EvChatUser, Msg: state.ChatMsg{
			ID: userID, From: "user", Text: trimmed, At: now, Kind: "user",
		}})
	}

	b.mu.Lock()
	ready := b.baseURL != "" && b.primaryID != "" && !b.fl.isStopped()
	if !ready {
		b.chatSeq++
		deadID := "boss-" + itoa(b.chatSeq)
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
			ID: deadID, From: "boss", Text: "[grafeio] backend not started", At: nowMs(), Pending: false,
		}})
		return nil
	}
	b.chatSeq++
	pendingID := "boss-" + itoa(b.chatSeq)
	b.pendingBoss = append(b.pendingBoss, pendingID)
	primaryID := b.primaryID
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
		ID: pendingID, From: "boss", Text: "", At: nowMs(), Pending: true,
	}})

	err := b.postPrompt(primaryID, trimmed)
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

// ensurePrimary reuses the newest root session for this directory, else
// creates one titled "grafeio office".
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
			return *newest, nil
		}
	}
	var created ocSession
	body, _ := json.Marshal(map[string]any{"title": "grafeio office"})
	if err := b.doJSON(http.MethodPost, "/session", body, &created); err != nil {
		return ocSession{}, fmt.Errorf("session.create failed: %w", err)
	}
	return created, nil
}

// postPrompt is promptAsync: POST /session/{id}/prompt_async (204 on ok).
func (b *liveBackend) postPrompt(sessionID, text string) error {
	body, _ := json.Marshal(map[string]any{
		"parts": []map[string]any{{"type": "text", "text": text}},
	})
	return b.doJSON(http.MethodPost, "/session/"+sessionID+"/prompt_async", body, nil)
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

// maybeBossCompleted: boss replied — swap the oldest pending bubble for
// the real reply text on the SAME id.
func (b *liveBackend) maybeBossCompleted(info ocMessage) {
	b.mu.Lock()
	if info.SessionID != b.primaryID || info.Role != "assistant" || info.Time.Completed == 0 || b.bossCompleted[info.ID] {
		b.mu.Unlock()
		return
	}
	b.bossCompleted[info.ID] = true
	primaryID := b.primaryID
	b.mu.Unlock()

	text := b.latestAssistantText(primaryID)
	if text == "" {
		text = "(the boss sent an empty reply)"
	}
	// Boss edits surface as diff events on message completion.
	b.fetchDiffAndEmit(primaryID)

	b.mu.Lock()
	var id string
	if len(b.pendingBoss) > 0 {
		id = b.pendingBoss[0]
		b.pendingBoss = b.pendingBoss[1:]
	} else {
		b.chatSeq++
		id = "boss-" + itoa(b.chatSeq)
	}
	b.mu.Unlock()

	b.fl.emit(state.Event{Kind: state.EvChatBoss, Msg: state.ChatMsg{
		ID: id, From: "boss", Text: text, At: nowMs(), Pending: false,
	}})
}

// ---------------------------------------------------------------- agentmemory sync

// syncBoard polls agentmemory -> board/mail (5s cadence, only on change).
func (b *liveBackend) syncBoard() {
	if b.fl.isStopped() || b.am == nil || b.am.kind != "actions" {
		return
	}
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
			b.fl.emit(state.Event{Kind: state.EvTask, Task: task})
		}
	}
	for _, mail := range mails {
		b.mu.Lock()
		seen := b.amMails[mail.ID]
		b.amMails[mail.ID] = true
		b.mu.Unlock()
		if !seen {
			b.fl.emit(state.Event{Kind: state.EvMail, Mail: mail})
		}
	}
}
