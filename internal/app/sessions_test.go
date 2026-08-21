package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/theboringhumane/grafeio/internal/state"
)

// scratchHome points GRAFEIO_HOME at a t.TempDir for the sessions tests.
func scratchHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GRAFEIO_HOME", home)
	return home
}

func TestSessionRoundTrip(t *testing.T) {
	scratchHome(t)
	dir := t.TempDir()
	st := state.OfficeState{
		Employees: []state.Employee{
			{ID: "dev-1", Name: "tekton-1", Role: state.RoleDeveloper, Seat: "desk-1", Sprite: state.SpriteAtDesk},
		},
		Tasks: []state.BoardTask{{ID: "t1", Title: "wire the SSE stream", Status: state.TaskDone, At: 1}},
		Mails: []state.MailItem{{ID: "m1", From: "tekton-1", To: "boss", At: 2, Subject: "return", Kind: state.MailReturn}},
		Chat: []state.ChatMsg{
			{ID: "u1", From: "user", Kind: "user", Text: "say pineapple", At: 3},
			{ID: "b1", From: "boss", Kind: "boss", Text: "Pineapple.", At: 4},
		},
	}
	sf := Snapshot(dir, "ses-123", st)
	if err := SaveSession(dir, sf); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, ok := LoadSession(dir)
	if !ok {
		t.Fatal("LoadSession: no session found after save")
	}
	if !got.Fresh() {
		t.Fatal("freshly written session reports stale")
	}
	if got.Dir != dir || got.PrimaryID != "ses-123" {
		t.Fatalf("round trip mismatch: dir=%q primary=%q", got.Dir, got.PrimaryID)
	}
	if len(got.Chat) != 2 || len(got.Tasks) != 1 || len(got.Mails) != 1 || len(got.Agents) != 1 {
		t.Fatalf("round trip surface counts: chat=%d tasks=%d mails=%d agents=%d",
			len(got.Chat), len(got.Tasks), len(got.Mails), len(got.Agents))
	}
}

func TestSessionMalformedFallsSilent(t *testing.T) {
	scratchHome(t)
	dir := t.TempDir()
	if err := os.MkdirAll(SessionPath(dir)[:len(SessionPath(dir))-len("/session.json")], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SessionPath(dir), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sf, ok := LoadSession(dir); ok || sf != nil {
		t.Fatalf("malformed session must degrade silently (ok=false), got %+v", sf)
	}
	if _, ok := LoadSession(dir + "-missing"); ok {
		t.Fatal("missing session must report ok=false")
	}
}

func TestSessionFreshWindow(t *testing.T) {
	fresh := &SessionFile{Dir: "/x", SavedAt: time.Now().Add(-3 * 24 * time.Hour).UnixMilli()}
	if !fresh.Fresh() {
		t.Fatal("3-day-old session must be fresh")
	}
	stale := &SessionFile{Dir: "/x", SavedAt: time.Now().Add(-5 * 24 * time.Hour).UnixMilli()}
	if stale.Fresh() {
		t.Fatal("5-day-old session must be stale")
	}
}

func TestSnapshotCaps(t *testing.T) {
	st := state.OfficeState{}
	for i := 0; i < 205; i++ {
		st.Chat = append(st.Chat, state.ChatMsg{ID: "c", From: "user", Text: "x"})
	}
	for i := 0; i < 60; i++ {
		st.Tasks = append(st.Tasks, state.BoardTask{ID: "t"})
		st.Mails = append(st.Mails, state.MailItem{ID: "m"})
	}
	sf := Snapshot("/d", "p", st)
	if len(sf.Chat) != sessionChatCap {
		t.Fatalf("chat not trimmed to %d (got %d)", sessionChatCap, len(sf.Chat))
	}
	if len(sf.Tasks) != sessionListCap || len(sf.Mails) != sessionListCap {
		t.Fatalf("tasks/mails not trimmed to %d (got %d/%d)", sessionListCap, len(sf.Tasks), len(sf.Mails))
	}
}

func TestSessionDirHashStable(t *testing.T) {
	a, b := SessionDirHash("/tmp/foo"), SessionDirHash("/tmp/foo")
	if a != b || len(a) != 40 {
		t.Fatalf("hash unstable or not sha1 hex: %q vs %q", a, b)
	}
	if SessionDirHash("/tmp/foo") == SessionDirHash("/tmp/bar") {
		t.Fatal("distinct dirs share a hash")
	}
}

func TestSaveLatestWins(t *testing.T) {
	scratchHome(t)
	dir := t.TempDir()
	if err := SaveSession(dir, Snapshot(dir, "ses-old", state.OfficeState{})); err != nil {
		t.Fatal(err)
	}
	first, _ := LoadSession(dir)
	if err := SaveSession(dir, Snapshot(dir, "ses-new", state.OfficeState{})); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadSession(dir)
	if !ok {
		t.Fatal("session disappeared after overwrite")
	}
	if got.PrimaryID != "ses-new" {
		t.Fatalf("latest write did not win (primary=%q)", got.PrimaryID)
	}
	if !strings.HasSuffix(SessionPath(dir), "session.json") {
		t.Fatalf("session path shape drifted: %q", SessionPath(dir))
	}
	_ = first
}
