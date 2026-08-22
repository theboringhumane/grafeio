// resume_test.go — the explicit session-pin (-s/--session boot flag)
// contract from the model level:
//
//	(a) the pin BEATS session.json's stored id (PrimaryOverride gets the
//	    pin) and SKIPS the 4-day freshness gate;
//	(b) the HYDRATE-SKIP GUARD: pin != stored id (or no file) skips
//	    hydrateSession — a stale, unrelated transcript must never mask the
//	    pinned server session — emitting ONE dim "resumed session …
//	    (explicit pin)" notice instead; pin == stored id hydrates as today;
//	(c) no pin: the default restore path is byte-for-byte unchanged
//	    (stored id ride, freshness gate, splash skip);
//	(d) the /session slash command prints the primary id + session.json
//	    path, and /help mentions -s|--session.
package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// pinBackend — a LIVE-mode recording backend WITH the primary seam
// (PrimaryOverride/PrimaryID): overrides records every pin in order,
// primary scripts PrimaryID for the /session slash leg.
type pinBackend struct {
	recBackend
	overrides []string
	primary   string
}

func (b *pinBackend) Mode() state.Mode          { return state.ModeLive }
func (b *pinBackend) PrimaryOverride(id string) { b.overrides = append(b.overrides, id) }
func (b *pinBackend) PrimaryID() string         { return b.primary }

// plantSession writes a session.json for dir at the NEW root with an
// EXPLICIT SavedAt (SaveSession always re-stamps to now — the stale-fresh
// legs need an old stamp).
func plantSession(t *testing.T, dir string, sf SessionFile) {
	t.Helper()
	p := SessionPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// storedTranscript — the session.json body the guard legs must NOT hydrate.
func storedTranscript() state.OfficeState {
	return state.OfficeState{
		Chat: []state.ChatMsg{{ID: "u1", From: "user", Kind: "user", Text: "hello from stored", At: 1}},
	}
}

func chatHas(m Model, sub string) bool {
	for _, txt := range chatTexts(m) {
		if strings.Contains(txt, sub) {
			return true
		}
	}
	return false
}

// (a)+(b-1) the pin beats the stored id and the guard skips hydration with
// exactly one dim notice: the pinned id rides PrimaryOverride, the stale
// stored transcript never enters the chat, and the boot splash stays (the
// guard path is NOT a warm boot).
func TestResumePinBeatsStoredSession(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := SaveSession(cwd, Snapshot(cwd, "ses-stored", storedTranscript())); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	b := &pinBackend{}
	m := New(b, nil, WithResumeSession("ses-pinned"))

	if len(b.overrides) != 1 || b.overrides[0] != "ses-pinned" {
		t.Fatalf("the pin must beat the stored id on PrimaryOverride, got %v", b.overrides)
	}
	if chatHas(m, "hello from stored") {
		t.Fatalf("pin != stored id must SKIP hydration (stale transcript masks the pin): %v", chatTexts(m))
	}
	if !chatHas(m, "resumed session ses-pinned (explicit pin) · /new for a fresh office") {
		t.Fatalf("the guard must emit the dim explicit-pin notice, got %v", chatTexts(m))
	}
	if m.bootDone {
		t.Fatalf("the guard path is not a warm boot — the splash must stay")
	}
}

// (b-2) pin == stored id hydrates as today: transcript + restore notice +
// splash skipped, and the pin still rides PrimaryOverride.
func TestResumePinMatchingStoredHydrates(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := SaveSession(cwd, Snapshot(cwd, "ses-same", storedTranscript())); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	b := &pinBackend{}
	m := New(b, nil, WithResumeSession("ses-same"))

	if len(b.overrides) != 1 || b.overrides[0] != "ses-same" {
		t.Fatalf("the pin must ride PrimaryOverride even when it matches, got %v", b.overrides)
	}
	if !chatHas(m, "hello from stored") {
		t.Fatalf("pin == stored id must hydrate the transcript: %v", chatTexts(m))
	}
	if !chatHas(m, "restored office session") {
		t.Fatalf("the restore notice must survive: %v", chatTexts(m))
	}
	if !m.bootDone {
		t.Fatalf("pin == stored id is a warm boot — the splash must be skipped")
	}
}

// (a-2) the pin SKIPS the 4-day freshness gate: a stale session.json is
// loaded anyway when the pin names its id (hydrate) — while a MISMATCHED
// pin still guards the hydration but overrides regardless of staleness.
func TestResumePinSkipsFreshnessGate(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	stale := time.Now().Add(-5 * 24 * time.Hour).UnixMilli()

	// Leg 1: pin names the stale file's id → hydrated despite staleness.
	plantSession(t, cwd, SessionFile{
		Dir: cwd, PrimaryID: "ses-old", Chat: storedTranscript().Chat, SavedAt: stale,
	})
	b1 := &pinBackend{}
	m1 := New(b1, nil, WithResumeSession("ses-old"))
	if len(b1.overrides) != 1 || b1.overrides[0] != "ses-old" {
		t.Fatalf("stale gate must not stop the pin landing, got %v", b1.overrides)
	}
	if !chatHas(m1, "hello from stored") || !m1.bootDone {
		t.Fatalf("pin == stale stored id must hydrate anyway (gate skipped): %v", chatTexts(m1))
	}

	// Leg 2: a different pin over the same stale file → guarded notice,
	// override for the PIN (never the stale stored id).
	plantSession(t, cwd, SessionFile{
		Dir: cwd, PrimaryID: "ses-old", Chat: storedTranscript().Chat, SavedAt: stale,
	})
	b2 := &pinBackend{}
	m2 := New(b2, nil, WithResumeSession("ses-other"))
	if len(b2.overrides) != 1 || b2.overrides[0] != "ses-other" {
		t.Fatalf("the pin must override the stale stored id, got %v", b2.overrides)
	}
	if chatHas(m2, "hello from stored") {
		t.Fatalf("guard: mismatched pin must skip hydration, got %v", chatTexts(m2))
	}
	if !chatHas(m2, "resumed session ses-other (explicit pin)") {
		t.Fatalf("guard notice missing: %v", chatTexts(m2))
	}
}

// (c) no pin: the default path is unchanged — a FRESH file rides its stored
// id into PrimaryOverride + hydrate + splash-skip, a STALE file is ignored
// (no override, cold splash, no restore notice).
func TestNoPinKeepsDefaultRestore(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := SaveSession(cwd, Snapshot(cwd, "ses-stored", storedTranscript())); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	b1 := &pinBackend{}
	m1 := New(b1, nil) // no WithResumeSession
	if len(b1.overrides) != 1 || b1.overrides[0] != "ses-stored" {
		t.Fatalf("the default restore must ride the stored id, got %v", b1.overrides)
	}
	if !chatHas(m1, "hello from stored") || !m1.bootDone {
		t.Fatalf("the default fresh restore must hydrate + skip the splash: %v", chatTexts(m1))
	}

	stale := time.Now().Add(-5 * 24 * time.Hour).UnixMilli()
	plantSession(t, cwd, SessionFile{
		Dir: cwd, PrimaryID: "ses-stale", Chat: storedTranscript().Chat, SavedAt: stale,
	})
	b2 := &pinBackend{}
	m2 := New(b2, nil)
	if len(b2.overrides) != 0 {
		t.Fatalf("a stale file without a pin must NOT override, got %v", b2.overrides)
	}
	if chatHas(m2, "restored office session") || m2.bootDone {
		t.Fatalf("a stale file without a pin must cold-boot (splash kept, no notice)")
	}
}

// (d) /session prints the primary id + session.json path + the resume
// command; a backend without a resolved primary reports honestly; /help
// mentions -s|--session.
func TestSessionSlashCommand(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	b := &pinBackend{primary: "ses-live-9"}
	m := New(b, nil)
	m = runMsg(t, m, slashMsg{text: "/session"})
	last := lastChat(t, m)
	if last.From != "office" || last.Meta == "error" {
		t.Fatalf("/session must be a clean office notice: from=%q meta=%q", last.From, last.Meta)
	}
	for _, want := range []string{
		"session: ses-live-9 (primary)",
		"session.json: " + SessionPath(cwd),
		"resume on the next boot: theboringoffice -s ses-live-9",
	} {
		if !strings.Contains(last.Text, want) {
			t.Fatalf("/session output must contain %q, got:\n%s", want, last.Text)
		}
	}

	// No resolved primary (demo / harness stub): honest placeholder, never
	// an invented id.
	m2 := New(&recBackend{}, nil)
	m2 = runMsg(t, m2, slashMsg{text: "/session"})
	if got := lastChat(t, m2).Text; !strings.Contains(got, "no primary resolved yet") {
		t.Fatalf("a primary-less backend must say so, got %q", got)
	}

	// /help: the /session row carries the boot-flag mention.
	m3 := New(&recBackend{}, nil)
	m3 = runMsg(t, m3, slashMsg{text: "/help"})
	got := lastChat(t, m3).Text
	if !strings.Contains(got, "/session") || !strings.Contains(got, "-s|--session") {
		t.Fatalf("/help must mention /session and -s|--session, got:\n%s", got)
	}
}
