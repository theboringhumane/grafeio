// mcp_cmd_test.go — the /mcp slash contract from the model level: a bare
// /mcp fetches the backend's MCP surface and renders the panels block as
// ONE chat-office notice, a backend error becomes a red error line, /mcp
// reconnect <name> calls the reconnect seam and re-fetches the status
// block while a missing name answers with usage only (the backend is
// never touched). Also the model-level boot-gate wiring: cold start gates
// the whole View on the splash, a keypress (or the ready cascade) lifts
// it, and a RESTORED office session never sees the splash at all.
package app

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// mcpRecBackend — the app-test recBackend PLUS a scripted MCP surface:
// servers/mcpErr steer MCPServers, reconnErr steers ReconnectMCP (calls
// recorded by name), and live flips Mode for the session-restore test.
type mcpRecBackend struct {
	recBackend
	live      bool
	servers   []state.MCPServer
	mcpErr    error
	reconnErr error
	reconned  []string
	mcpCalls  int
}

func (b *mcpRecBackend) Mode() state.Mode {
	if b.live {
		return state.ModeLive
	}
	return state.ModeDemo
}

func (b *mcpRecBackend) MCPServers() ([]state.MCPServer, error) {
	b.mcpCalls++
	return b.servers, b.mcpErr
}

func (b *mcpRecBackend) ReconnectMCP(name string) error {
	b.reconned = append(b.reconned, name)
	return b.reconnErr
}

// lastChat is the newest transcript entry (slash outcomes append, never
// replace).
func lastChat(t *testing.T, m Model) state.ChatMsg {
	t.Helper()
	if len(m.st.Chat) == 0 {
		t.Fatalf("chat must hold at least the slash outcome")
	}
	return m.st.Chat[len(m.st.Chat)-1]
}

// chatTexts collects every chat text, ANSI-stripped (office notices store
// their lines styled; error lines store plain text with Meta "error").
func chatTexts(m Model) []string {
	out := make([]string, 0, len(m.st.Chat))
	for _, c := range m.st.Chat {
		out = append(out, ansi.Strip(c.Text))
	}
	return out
}

// (a) bare /mcp: one async hop, then the status block lands as ONE office
// notice carrying every server row plus the reconnect hint.
func TestMCPSlashStatus(t *testing.T) {
	b := &mcpRecBackend{servers: []state.MCPServer{
		{Name: "alpha", Status: "connected", Detail: "3 tools"},
		{Name: "beta", Status: "failed", Detail: "dial tcp timeout"},
	}}
	m := New(b, nil)
	// size first so the block renders at a realistic (unfolded) width —
	// a zero-size model folds every row at the 20-cell floor.
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runMsg(t, m, slashMsg{text: "/mcp"})

	if b.mcpCalls != 1 {
		t.Fatalf("bare /mcp must call MCPServers exactly once, got %d", b.mcpCalls)
	}
	if len(b.reconned) != 0 {
		t.Fatalf("bare /mcp must never reconnect, got %v", b.reconned)
	}
	last := lastChat(t, m)
	if last.From != "office" || last.Meta == "error" {
		t.Fatalf("status block must be a clean office notice: from=%q meta=%q", last.From, last.Meta)
	}
	plain := last.Text
	plain = ansi.Strip(plain)
	for _, want := range []string{
		"mcp servers", "alpha", "3 tools", "beta", "dial tcp timeout",
		"/mcp reconnect beta", // the hint targets the failed row
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("status block must contain %q:\n%s", want, plain)
		}
	}
}

// (b) backend error → the red error line carries the real error text
// (never a fabricated server list).
func TestMCPSlashBackendError(t *testing.T) {
	b := &mcpRecBackend{mcpErr: errors.New("opencode serve unreachable")}
	m := New(b, nil)
	m = runMsg(t, m, slashMsg{text: "/mcp"})

	last := lastChat(t, m)
	if last.Meta != "error" {
		t.Fatalf("backend failure must surface as an error notice, meta=%q", last.Meta)
	}
	if !strings.Contains(last.Text, "mcp: opencode serve unreachable") {
		t.Fatalf("error line must carry the backend error, got %q", last.Text)
	}
}

// (c) /mcp reconnect <name>: the immediate "reconnecting…" line plus —
// after the async hop — the re-fetched status block headed by the
// reconnected confirmation.
func TestMCPReconnectSuccess(t *testing.T) {
	b := &mcpRecBackend{servers: []state.MCPServer{
		{Name: "beta", Status: "connected", Detail: "5 tools"},
	}}
	m := New(b, nil)
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 120, Height: 40}) // keep rows unfolded
	m = runMsg(t, m, slashMsg{text: "/mcp reconnect beta"})

	if len(b.reconned) != 1 || b.reconned[0] != "beta" {
		t.Fatalf("reconnect must target beta exactly once, got %v", b.reconned)
	}
	if b.mcpCalls != 1 {
		t.Fatalf("a successful reconnect re-fetches the status once, got %d calls", b.mcpCalls)
	}
	foundProgress := false
	for _, txt := range chatTexts(m) {
		if strings.Contains(txt, "mcp: reconnecting beta") {
			foundProgress = true
		}
	}
	if !foundProgress {
		t.Fatalf("the in-flight reconnect must notice immediately: %v", chatTexts(m))
	}
	last := lastChat(t, m)
	if last.Meta == "error" {
		t.Fatalf("successful reconnect must not be an error notice: %q", last.Text)
	}
	plain := ansi.Strip(last.Text)
	if !strings.HasPrefix(plain, "mcp: reconnected beta") {
		t.Fatalf("status block must open with the reconnected line, got %q", plain)
	}
	if !strings.Contains(plain, "mcp servers") || !strings.Contains(plain, "beta") {
		t.Fatalf("status rows must follow the confirmation, got %q", plain)
	}
}

// (d) reconnect backend error → error line, no status re-fetch.
func TestMCPReconnectError(t *testing.T) {
	b := &mcpRecBackend{reconnErr: errors.New(`POST /mcp/zeta/connect: 404`)}
	m := New(b, nil)
	m = runMsg(t, m, slashMsg{text: "/mcp reconnect zeta"})

	if len(b.reconned) != 1 || b.reconned[0] != "zeta" {
		t.Fatalf("reconnect must still be attempted for zeta, got %v", b.reconned)
	}
	if b.mcpCalls != 0 {
		t.Fatalf("a failed reconnect must not re-fetch the status, got %d calls", b.mcpCalls)
	}
	last := lastChat(t, m)
	if last.Meta != "error" {
		t.Fatalf("failed reconnect must be the red error line, meta=%q", last.Meta)
	}
	if !strings.Contains(last.Text, "mcp: POST /mcp/zeta/connect: 404") {
		t.Fatalf("error line must carry the backend error, got %q", last.Text)
	}
}

// (e) /mcp reconnect with no name → usage line, backend never touched.
func TestMCPReconnectMissingArg(t *testing.T) {
	b := &mcpRecBackend{servers: []state.MCPServer{{Name: "beta", Status: "connected"}}}
	m := New(b, nil)
	m = runMsg(t, m, slashMsg{text: "/mcp reconnect"})

	last := lastChat(t, m)
	if last.Meta != "error" || !strings.Contains(last.Text, "/mcp: usage /mcp reconnect <name>") {
		t.Fatalf("missing name must answer with the usage error line, got meta=%q text=%q",
			last.Meta, last.Text)
	}
	if len(b.reconned) != 0 || b.mcpCalls != 0 {
		t.Fatalf("usage must never touch the backend: reconned=%v mcpCalls=%d",
			b.reconned, b.mcpCalls)
	}
}

// (f) model-level boot gate: a cold start renders the splash as the whole
// View; a keypress lifts it (skip path); a ready backend finishing its
// cascade lifts it too (done path).
func TestBootGateColdStartSkipAndDone(t *testing.T) {
	const subtitle = "γραφείο · a startup office in your terminal"

	// skip path: cold start → splash owns the View → any key lifts it.
	m := New(&mcpRecBackend{}, nil)
	if m.bootDone {
		t.Fatalf("a cold start must gate on the boot splash")
	}
	m = runMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, subtitle) {
		t.Fatalf("before any key the View must be the boot splash")
	}
	m = runMsg(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})
	if !m.bootDone {
		t.Fatalf("a keypress must lift the splash (skip)")
	}
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, subtitle) {
		t.Fatalf("after the skip the View must render the office frame")
	}

	// done path: a backend event marks the uplink ready; synthetic ticks
	// (fed past runMsg — the re-arming cmd sleeps 80ms a pop) run the
	// compressed cascade to Done, which lifts the gate on its own.
	m2 := New(&mcpRecBackend{}, nil)
	m2 = runMsg(t, m2, tea.WindowSizeMsg{Width: 100, Height: 30})
	m2 = runMsg(t, m2, state.Event{Kind: state.EvStatus, Text: "up"})
	for i := 0; i < 30 && !m2.bootDone; i++ {
		nm, _ := m2.Update(bootTickMsg{})
		m2 = nm.(Model)
	}
	if !m2.bootDone {
		t.Fatalf("the ready cascade must finish the splash inside 30 ticks")
	}
	if plain := ansi.Strip(m2.View().Content); strings.Contains(plain, subtitle) {
		t.Fatalf("after Done the View must render the office frame")
	}
}

// (g) a RESTORED office session (live mode, fresh session.json) is
// already warm: New() skips the splash entirely. A cold live start (no
// session.json on disk) keeps it.
func TestBootSkippedOnSessionRestore(t *testing.T) {
	scratchHome(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	st := state.OfficeState{
		Chat: []state.ChatMsg{{ID: "u1", From: "user", Kind: "user", Text: "hello", At: 1}},
	}
	if err := SaveSession(cwd, Snapshot(cwd, "ses-restored", st)); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	m := New(&mcpRecBackend{live: true}, nil)
	if !m.bootDone {
		t.Fatalf("a restored office session must SKIP the boot splash")
	}
	restored := false
	for _, txt := range chatTexts(m) {
		if strings.Contains(txt, "restored office session") {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("the restore notice must survive (hydration still runs)")
	}

	// cold live start: THEBORINGOFFICE_HOME is the same scratch dir but nothing
	// was saved for a DIFFERENT working directory… so swap to an
	// unsaved one via a fresh scratch home.
	scratchHome(t)
	m2 := New(&mcpRecBackend{live: true}, nil)
	if m2.bootDone {
		t.Fatalf("a cold live start (no session.json) must keep the boot splash")
	}
}
