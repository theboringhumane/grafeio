// models_test.go — the brain.json model knobs (backend.bossModel /
// backend.ctoModel, legacy boss.model) on the LIVE wire: a configured
// "provider/model" ref rides prompt_async as
// {"model":{"providerID","modelID"}} and NOTHING ships a "model" key when
// every knob is empty (additive only — a default brain.json makes the same
// payload as yesterday). The routing rule: boss/concierge prompts take the
// boss override (backend.bossModel wins over legacy boss.model), a session
// hired as the CTO (state.IsArchitectureBrief is the ONE matcher) takes
// backend.ctoModel.
package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// modelStub is a stub opencode serve that answers the control calls a
// live backend makes here and records every POST body — the session-create
// and prompt_async payloads under test.
type modelStub struct {
	mu    sync.Mutex
	posts []modelPost
}

type modelPost struct {
	path string
	body string
}

// serve answers POST /session (create), POST .../prompt_async (204) and a
// bare /session listing; every POST lands in the log.
func (s *modelStub) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("stub read body: %v", err)
			}
			s.mu.Lock()
			s.posts = append(s.posts, modelPost{r.Method + " " + r.URL.Path, string(b)})
			s.mu.Unlock()
			if r.URL.Path == "/session" {
				w.Write([]byte(`{"id":"ses-made","title":""}`))
				return
			}
			w.WriteHeader(http.StatusNoContent) // prompt_async contract
			return
		}
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// lastPost returns the most recent POST body to the given path suffix,
// and whether one was seen.
func (s *modelStub) lastPost(suffix string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.posts) - 1; i >= 0; i-- {
		if len(s.posts[i].path) >= len(suffix) && s.posts[i].path[len(s.posts[i].path)-len(suffix):] == suffix {
			return s.posts[i].body, true
		}
	}
	return "", false
}

// payloadModel decodes the prompt body's model object, ""s when absent.
func payloadModel(t *testing.T, body string, wantKey bool) (provider, model string, ok bool) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("prompt body is not JSON: %v — %s", err, body)
	}
	mo, present := m["model"]
	if present != wantKey {
		t.Fatalf("payload model key: want present=%v, got %v — body %s", wantKey, present, body)
	}
	if !present {
		return "", "", false
	}
	obj, ok := mo.(map[string]any)
	if !ok {
		t.Fatalf("model is not an object: %v", mo)
	}
	p, _ := obj["providerID"].(string)
	mid, _ := obj["modelID"].(string)
	return p, mid, true
}

// liveStubBackend pins a live backend on the stub serve (baseURL + a boss
// session) WITHOUT Start — no SSE pump, no network watcher; the wire
// calls under test are the POSTs themselves.
func liveStubBackend(stub *modelStub, srv *httptest.Server, cfg *config.Config) *liveBackend {
	b := newLiveBackend("", "/tmp", cfg)
	b.mu.Lock()
	b.baseURL = srv.URL
	b.primaryID = "ses-boss"
	b.mu.Unlock()
	return b
}

// TestModelOverrideAdditiveByDefault: an unconfigured brain.json changes
// NOTHING on the wire — neither the session create nor the prompt carries
// a "model" key.
func TestModelOverrideAdditiveByDefault(t *testing.T) {
	stub := &modelStub{}
	srv := stub.serve(t)
	b := liveStubBackend(stub, srv, config.Default())
	log := &eventLog{}
	b.fl.setEmit(log.emit)

	if _, err := b.createPrimary("theboringoffice office"); err != nil {
		t.Fatal(err)
	}
	body, ok := stub.lastPost("POST /session")
	if !ok {
		t.Fatal("no session create recorded")
	}
	payloadModel(t, body, false)

	if err := b.postPrompt("ses-boss", "hello boss", nil); err != nil {
		t.Fatal(err)
	}
	body, ok = stub.lastPost("POST /session/ses-boss/prompt_async")
	if !ok {
		t.Fatal("no prompt recorded")
	}
	payloadModel(t, body, false)
	t.Logf("default prompt payload (no model key): %s", body)
}

// TestBossPromptModelOverride: backend.bossModel resolves to the prompt's
// model object; it wins over the legacy boss.model, which itself still
// works alone.
func TestBossPromptModelOverride(t *testing.T) {
	stub := &modelStub{}
	srv := stub.serve(t)

	cfg := config.Default()
	cfg.Backend.BossModel = "anthropic/claude-sonnet-4"
	b := liveStubBackend(stub, srv, cfg)
	log := &eventLog{}
	b.fl.setEmit(log.emit)

	if err := b.postPrompt("ses-boss", "hello boss", nil); err != nil {
		t.Fatal(err)
	}
	body, _ := stub.lastPost("POST /session/ses-boss/prompt_async")
	p, m, _ := payloadModel(t, body, true)
	if p != "anthropic" || m != "claude-sonnet-4" {
		t.Fatalf("want anthropic/claude-sonnet-4, got %s/%s", p, m)
	}
	t.Logf("resolved boss prompt payload: %s", body)

	// Precedence: backend.bossModel wins over the legacy boss.model.
	cfg.Boss.Model = "anthropic/claude-opus-4-1"
	if err := b.postPrompt("ses-boss", "again", nil); err != nil {
		t.Fatal(err)
	}
	// NOTE: the 2s echo dedupe doesn't apply — postPrompt is the wire leg.
	body, _ = stub.lastPost("POST /session/ses-boss/prompt_async")
	p, m, _ = payloadModel(t, body, true)
	if m != "claude-sonnet-4" {
		t.Fatalf("backend.bossModel must win over boss.model: got model %q", m)
	}
	// Legacy alone still works (existing brain.json keeps functioning).
	cfg.Backend.BossModel = ""
	if err := b.postPrompt("ses-boss", "legacy", nil); err != nil {
		t.Fatal(err)
	}
	body, _ = stub.lastPost("POST /session/ses-boss/prompt_async")
	p, m, _ = payloadModel(t, body, true)
	if p != "anthropic" || m != "claude-opus-4-1" {
		t.Fatalf("legacy boss.model alone: want anthropic/claude-opus-4-1, got %s/%s", p, m)
	}

	// And the session CREATE stays model-free even with the knob set (the
	// serve documents model only on prompt_async — see postPrompt's note).
	cfg.Backend.BossModel = "anthropic/claude-sonnet-4"
	if _, err := b.createPrimary("theboringoffice office"); err != nil {
		t.Fatal(err)
	}
	body, ok := stub.lastPost("POST /session")
	if !ok {
		t.Fatal("no session create recorded")
	}
	payloadModel(t, body, false)
}

// TestMalformedModelSkippedOnWire: a slash-less bossModel is kept in the
// config (validation-lite) but never reaches the wire — the serve needs
// providerID AND modelID.
func TestMalformedModelSkippedOnWire(t *testing.T) {
	stub := &modelStub{}
	srv := stub.serve(t)

	cfg := config.Default()
	cfg.Backend.BossModel = "claude-sonnet-4" // missing the provider half
	b := liveStubBackend(stub, srv, cfg)
	log := &eventLog{}
	b.fl.setEmit(log.emit)

	if err := b.postPrompt("ses-boss", "hello", nil); err != nil {
		t.Fatal(err)
	}
	body, _ := stub.lastPost("POST /session/ses-boss/prompt_async")
	payloadModel(t, body, false)
}

// TestCTORoutedDispatchUsesCTOModel: a session the floor hired as the CTO
// (architecture-brief title — state.IsArchitectureBrief is the ONE
// matcher) carries backend.ctoModel on its prompt, while a plain dev
// dispatch and the boss take the boss override (here: unset → no model
// key; and both-set → each gets its own).
func TestCTORoutedDispatchUsesCTOModel(t *testing.T) {
	stub := &modelStub{}
	srv := stub.serve(t)

	cfg := config.Default()
	cfg.Backend.CTOModel = "anthropic/claude-haiku-4-5"
	b := liveStubBackend(stub, srv, cfg)
	log := &eventLog{}
	b.fl.setEmit(log.emit)

	created := func(id, title string) ocSSEEvent {
		return ocSSEEvent{Type: "session.created", Properties: json.RawMessage(
			`{"info":{"id":"` + id + `","parentID":"ses-boss","title":"` + title + `","time":{"created":1,"updated":1}}}`)}
	}
	// An architecture brief hires the CTO; a plain brief a developer.
	if err := b.onEvent(created("ses-cto", "design the board sync protocol")); err != nil {
		t.Fatal(err)
	}
	if err := b.onEvent(created("ses-dev", "write the reducer")); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	ctoEmp, devEmp := b.ctx.employees["ses-cto"], b.ctx.employees["ses-dev"]
	b.mu.Unlock()
	if ctoEmp.Role != state.RoleCTO || devEmp.Role != state.RoleDeveloper {
		t.Fatalf("routing preconditions broken: arch=%q dev=%q", ctoEmp.Role, devEmp.Role)
	}

	// The CTO dispatch carries ctoModel…
	if err := b.postPrompt("ses-cto", "review the batch", nil); err != nil {
		t.Fatal(err)
	}
	body, _ := stub.lastPost("POST /session/ses-cto/prompt_async")
	p, m, _ := payloadModel(t, body, true)
	if p != "anthropic" || m != "claude-haiku-4-5" {
		t.Fatalf("CTO prompt: want anthropic/claude-haiku-4-5, got %s/%s", p, m)
	}
	t.Logf("resolved cto-routed prompt payload: %s", body)

	// …while the plain dev dispatch and the boss carry nothing (boss
	// override unset — additive).
	if err := b.postPrompt("ses-dev", "write it", nil); err != nil {
		t.Fatal(err)
	}
	body, _ = stub.lastPost("POST /session/ses-dev/prompt_async")
	payloadModel(t, body, false)
	if err := b.postPrompt("ses-boss", "boss turn", nil); err != nil {
		t.Fatal(err)
	}
	body, _ = stub.lastPost("POST /session/ses-boss/prompt_async")
	payloadModel(t, body, false)

	// Both knobs set: each session gets its OWN model.
	cfg.Backend.BossModel = "anthropic/claude-sonnet-4"
	if err := b.postPrompt("ses-cto", "review again", nil); err != nil {
		t.Fatal(err)
	}
	body, _ = stub.lastPost("POST /session/ses-cto/prompt_async")
	_, m, _ = payloadModel(t, body, true)
	if m != "claude-haiku-4-5" {
		t.Fatalf("boss knob set must not leak into a CTO dispatch: got %q", m)
	}
	if err := b.postPrompt("ses-boss", "boss again", nil); err != nil {
		t.Fatal(err)
	}
	body, _ = stub.lastPost("POST /session/ses-boss/prompt_async")
	_, m, _ = payloadModel(t, body, true)
	if m != "claude-sonnet-4" {
		t.Fatalf("boss prompt must take the boss knob: got %q", m)
	}
}
