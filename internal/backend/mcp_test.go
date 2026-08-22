// mcp_test.go — the MCP visibility contract against httptest doubles:
// the PRIMARY route (GET /mcp) parses the verified union, sorts by name
// and carries details; the fallback chain (/mcp 404 -> /mcp/status ->
// /config) keeps older serves answerable; a serve with NONE of them
// degrades to one honest "no MCP info" error. ReconnectMCP pins its
// verified POST /mcp/{name}/connect route plus the 404 wrap. The demo
// fixture + flip are covered too.
package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// newMCPTestBackend pins a liveBackend onto the test server without Start
// (no session/SSE machinery needed — the MCP calls are plain doJSON).
func newMCPTestBackend(t *testing.T, baseURL string) *liveBackend {
	t.Helper()
	b := newLiveBackend(baseURL, t.TempDir(), nil)
	b.mu.Lock()
	b.baseURL = baseURL
	b.mu.Unlock()
	return b
}

func TestLiveMCPServersPrimaryRoute(t *testing.T) {
	var hits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/mcp" {
			w.Header().Set("Content-Type", "application/json")
			// the verified union (dev branch mcp/index.ts), plus tolerated
			// extras (tools array) and one unparseable entry.
			w.Write([]byte(`{
				"web-search": {"status": "connected"},
				"github": {"status": "needs_auth"},
				"postgres": {"status": "failed", "error": "connection refused"},
				"local-memory": {"status": "connected", "tools": [{}, {}, {}]},
				"legacy": {"status": "needs_client_registration", "error": "provide clientId in config"},
				"weird": {"status": "pending"},
				"broken": 42
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	b := newMCPTestBackend(t, ts.URL)
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(hits) != 1 || hits[0] != "GET /mcp" {
		t.Fatalf("the verified primary route must be the ONLY hop, got %v", hits)
	}
	wantNames := []string{"broken", "github", "legacy", "local-memory", "postgres", "web-search", "weird"}
	if len(servers) != len(wantNames) {
		t.Fatalf("want %d servers, got %d: %+v", len(wantNames), len(servers), servers)
	}
	for i, name := range wantNames {
		if servers[i].Name != name {
			t.Fatalf("servers must sort by name: slot %d want %q got %+v", i, name, servers)
		}
	}
	byName := map[string]state.MCPServer{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	if s := byName["github"]; s.Status != "needs_auth" || s.Detail != "run: opencode mcp auth github" {
		t.Fatalf("needs_auth must carry the auth hint, got %+v", s)
	}
	if s := byName["legacy"]; s.Status != "needs_auth" || s.Detail != "provide clientId in config" {
		t.Fatalf("needs_client_registration must fold into needs_auth with its error, got %+v", s)
	}
	if s := byName["postgres"]; s.Status != "failed" || s.Detail != "connection refused" {
		t.Fatalf("failed must carry the wire error, got %+v", s)
	}
	if s := byName["local-memory"]; s.Status != "connected" || s.Detail != "3 tools" {
		t.Fatalf("tolerated tools array must become '3 tools', got %+v", s)
	}
	if s := byName["web-search"]; s.Status != "connected" || s.Detail != "" {
		t.Fatalf("plain connected carries no detail on the verified wire, got %+v", s)
	}
	if s := byName["weird"]; s.Status != "unknown" || !strings.Contains(s.Detail, "pending") {
		t.Fatalf("an unrecognized wire status must degrade to unknown, got %+v", s)
	}
	if s := byName["broken"]; s.Status != "unknown" {
		t.Fatalf("an unparseable entry must degrade alone, got %+v", s)
	}
}

func TestLiveMCPServersEmptyMapIsRealInfo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	b := newMCPTestBackend(t, ts.URL)
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("an empty-but-valid map is REAL info, not an error: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("want zero servers, got %+v", servers)
	}
}

func TestLiveMCPServersFallbackChain(t *testing.T) {
	var hits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/mcp/status" {
			w.Write([]byte(`{"a": {"status": "connected"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	b := newMCPTestBackend(t, ts.URL)
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(hits) != 2 || hits[0] != "GET /mcp" || hits[1] != "GET /mcp/status" {
		t.Fatalf("the 404 on the primary must walk the chain in order, got %v", hits)
	}
	if len(servers) != 1 || servers[0].Name != "a" || servers[0].Status != "connected" {
		t.Fatalf("fallback payload must parse like the primary, got %+v", servers)
	}
}

func TestLiveMCPServersConfigFallback(t *testing.T) {
	var hits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet && r.URL.Path == "/config" {
			w.Write([]byte(`{"mcp": {
				"z-remote": {"type": "remote", "url": "https://mcp.example.com"},
				"a-local": {"type": "local", "command": ["u", "x"], "enabled": false}
			}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	b := newMCPTestBackend(t, ts.URL)
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("MCPServers: %v", err)
	}
	if len(hits) != 3 || hits[2] != "GET /config" {
		t.Fatalf("config is the THIRD hop, got %v", hits)
	}
	if len(servers) != 2 {
		t.Fatalf("config mcp keys must surface as servers, got %+v", servers)
	}
	if servers[0].Name != "a-local" || servers[0].Status != "disabled" {
		t.Fatalf("enabled:false maps to disabled, got %+v", servers[0])
	}
	if servers[1].Name != "z-remote" || servers[1].Status != "unknown" ||
		!strings.Contains(servers[1].Detail, "remote") {
		t.Fatalf("config carries no live state: enabled entries land as unknown, got %+v", servers[1])
	}
}

func TestLiveMCPServersDegradesToNoInfo(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ts.Close()
	b := newMCPTestBackend(t, ts.URL)
	servers, err := b.MCPServers()
	if err == nil {
		t.Fatalf("a serve with none of the routes must error, got servers %+v", servers)
	}
	if !strings.Contains(err.Error(), "no MCP info") {
		t.Fatalf("the degrade must NAME the no-info outcome, got %v", err)
	}
	if !strings.Contains(err.Error(), "GET /mcp") || !strings.Contains(err.Error(), "GET /config") {
		t.Fatalf("the error must join every hop's failure, got %v", err)
	}
}

func TestLiveReconnectMCP(t *testing.T) {
	var hits []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost && r.URL.Path == "/mcp/postgres/connect" {
			w.Write([]byte(`true`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	b := newMCPTestBackend(t, ts.URL)

	if err := b.ReconnectMCP("postgres"); err != nil {
		t.Fatalf("reconnect on the verified route must succeed, got %v", err)
	}
	if len(hits) != 1 || hits[0] != "POST /mcp/postgres/connect" {
		t.Fatalf("reconnect must POST the verified connect route, got %v", hits)
	}
	if err := b.ReconnectMCP("ghost"); err == nil || !strings.Contains(err.Error(), "no reconnect route on this server version") {
		t.Fatalf("a 404 must wrap as 'no reconnect route on this server version', got %v", err)
	}
	if err := b.ReconnectMCP("  "); err == nil {
		t.Fatal("an empty name must error before any HTTP")
	}
}

func TestDemoMCPServersFixture(t *testing.T) {
	b := newDemoBackend(nil)
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("demo MCPServers: %v", err)
	}
	want := []state.MCPServer{
		{Name: "github", Status: "needs_auth", Detail: "run: opencode mcp auth github"},
		{Name: "local-memory", Status: "connected", Detail: "12 tools"},
		{Name: "postgres", Status: "failed", Detail: "connection refused"},
		{Name: "web-search", Status: "connected", Detail: "3 tools"},
	}
	if len(servers) != len(want) {
		t.Fatalf("the fixture is four servers, got %+v", servers)
	}
	for i, s := range want {
		if servers[i] != s {
			t.Fatalf("fixture slot %d want %+v got %+v", i, s, servers[i])
		}
	}
	if err := b.ReconnectMCP("nope"); err == nil {
		t.Fatal("an unknown server name must error")
	}
}

func TestDemoReconnectMCPFlipsFailed(t *testing.T) {
	b := newDemoBackend(nil)
	var notes []string
	if err := b.Start(func(e state.Event) {
		if e.Kind == state.EvStatus {
			notes = append(notes, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()

	if err := b.ReconnectMCP("postgres"); err != nil {
		t.Fatalf("reconnecting the scripted failure must succeed: %v", err)
	}
	servers, err := b.MCPServers()
	if err != nil {
		t.Fatalf("demo MCPServers: %v", err)
	}
	if servers[2].Name != "postgres" || servers[2].Status != "connected" || servers[2].Detail != "" {
		t.Fatalf("postgres must flip to connected (detail cleared), got %+v", servers[2])
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "[demo] mcp reconnect postgres: failed -> connected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the flip must ride the demo feed as EvStatus, got %v", notes)
	}

	// needs_auth is not reconnect-fixable: nil error, status unchanged.
	if err := b.ReconnectMCP("github"); err != nil {
		t.Fatalf("needs_auth reconnect must be a note-only nil, got %v", err)
	}
	servers, _ = b.MCPServers()
	if servers[0].Name != "github" || servers[0].Status != "needs_auth" {
		t.Fatalf("github must stay needs_auth (OAuth is the remedy), got %+v", servers[0])
	}
}
