// mcp.go — MCP server visibility + management for both backends.
//
// WIRE VERIFIED 2026-08-22 against anomalyco/opencode dev branch:
//
//	GET  /mcp                  -> { "<name>": MCPStatus, ... }
//	  docs:   https://opencode.ai/docs/server/ ("LSP, Formatters & MCP")
//	  route:  packages/opencode/src/server/routes/instance/httpapi/groups/mcp.ts
//	          (McpPaths.status = "/mcp", success Record(String, MCP.Status))
//	  shape:  packages/opencode/src/mcp/index.ts (Status union) —
//	          {status:"connected"} | {status:"disabled"} |
//	          {status:"failed", error} | {status:"needs_auth"} |
//	          {status:"needs_client_registration", error}
//	POST /mcp/{name}/connect   -> boolean   (groups/mcp.ts McpPaths.connect)
//	POST /mcp/{name}/auth      -> {authorizationUrl, oauthState} (OAuth start;
//	  not wired here — ReconnectMCP only re-establishes the connection)
//
// Two deliberate wrinkles:
//   - the connected variant carries NO tool list/count on the wire, so
//     Detail gets "N tools" only when a (future/older) payload happens to
//     expose tools/toolCount — both parse-tolerated below.
//   - needs_client_registration folds into needs_auth for the UI (its
//     error string IS the actionable detail: provide a clientId).
//
// Older/foreign serves keep answering through the fallback chain (same
// degrade-open tradition as AnswerPermission's modern+legacy routes):
// GET /mcp -> GET /mcp/status -> GET /config (mcp keys). Every hop parses
// tolerantly and alone (a 404 on one hop never sinks the next); when all
// three fail the caller gets ONE honest "no MCP info" error joining the
// per-hop failures — never a fabricated list.
package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// ---------------------------------------------------------------- live: list

// mcpWireStatus is the tolerant per-server payload: status/error are the
// documented union; tools/toolCount ride only on serves that expose them
// (the verified dev-branch payload carries neither).
type mcpWireStatus struct {
	Status    string          `json:"status"`
	Error     string          `json:"error"`
	Tools     json.RawMessage `json:"tools"`
	ToolCount int             `json:"toolCount"`
}

// toolCount best-efforts a tool count out of the tolerated extras; 0 when
// the payload says nothing (the common case on the verified wire).
func (w mcpWireStatus) toolCount() int {
	if w.ToolCount > 0 {
		return w.ToolCount
	}
	var arr []json.RawMessage
	if len(w.Tools) > 0 && json.Unmarshal(w.Tools, &arr) == nil {
		return len(arr)
	}
	return 0
}

// wireToMCPServer maps one raw status entry to the UI contract. Detail
// rules: failed -> the wire error; needs_auth -> the serve's own remedy
// hint ("run: opencode mcp auth <name>", mirroring its toast copy) unless
// the wire carries a better error; connected -> "N tools" when exposed;
// unknown wire statuses land as unknown with the raw status kept visible.
func wireToMCPServer(name string, w mcpWireStatus) state.MCPServer {
	s := state.MCPServer{Name: name, Status: w.Status}
	switch w.Status {
	case "connected":
		if n := w.toolCount(); n > 0 {
			s.Detail = itoa(n) + " tools"
		}
	case "disabled":
		// the amber glyph says it all; no detail
	case "failed":
		s.Detail = w.Error
	case "needs_auth":
		if w.Error != "" {
			s.Detail = w.Error
		} else {
			s.Detail = "run: opencode mcp auth " + name
		}
	case "needs_client_registration":
		// same lane as needs_auth for the UI; the wire error explains the
		// clientId pre-registration the member has to do.
		s.Status = "needs_auth"
		if w.Error != "" {
			s.Detail = w.Error
		} else {
			s.Detail = "run: opencode mcp auth " + name
		}
	default:
		s.Status = "unknown"
		s.Detail = "status: " + w.Status
	}
	if w.Error != "" && s.Status != "unknown" && s.Detail == "" {
		s.Detail = w.Error
	}
	return s
}

// mapWireStatuses normalizes + sorts the keyed status map. One unreadable
// entry degrades alone (unknown + note) instead of sinking the whole list.
func mapWireStatuses(raw map[string]json.RawMessage) []state.MCPServer {
	servers := make([]state.MCPServer, 0, len(raw))
	for name, blob := range raw {
		var w mcpWireStatus
		if err := json.Unmarshal(blob, &w); err != nil || w.Status == "" {
			servers = append(servers, state.MCPServer{
				Name: name, Status: "unknown", Detail: "unreadable status payload",
			})
			continue
		}
		servers = append(servers, wireToMCPServer(name, w))
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers
}

// MCPServers lists the configured MCP servers with live status (sorted by
// name). Chain: GET /mcp (verified primary route) -> GET /mcp/status (a
// documented-by-convention sibling some servers expose) -> GET /config
// (config-only degrade: the mcp keys prove WHAT is configured but the
// live state is unknowable there, so non-disabled entries surface as
// "unknown"). An empty-but-valid map is REAL info (nothing configured),
// distinct from the all-hops-failed error.
func (b *liveBackend) MCPServers() ([]state.MCPServer, error) {
	if b.fl.isStopped() {
		return nil, errors.New("backend stopped")
	}
	var errs []error
	for _, path := range []string{"/mcp", "/mcp/status"} {
		var raw map[string]json.RawMessage
		if err := b.doJSON(http.MethodGet, path, nil, &raw); err != nil {
			errs = append(errs, fmt.Errorf("GET %s: %w", path, err))
			continue
		}
		return mapWireStatuses(raw), nil
	}
	// Last resort: /config — an older serve that never heard of /mcp
	// usually still exposes config. Its mcp entries carry type/enabled
	// only, never the live connection state.
	var cfg struct {
		MCP map[string]struct {
			Type    string `json:"type"`
			Enabled *bool  `json:"enabled"`
		} `json:"mcp"`
	}
	if err := b.doJSON(http.MethodGet, "/config", nil, &cfg); err != nil {
		errs = append(errs, fmt.Errorf("GET /config: %w", err))
		return nil, fmt.Errorf("no MCP info available from this server: %w", errors.Join(errs...))
	}
	servers := make([]state.MCPServer, 0, len(cfg.MCP))
	for name, entry := range cfg.MCP {
		s := state.MCPServer{Name: name, Status: "unknown"}
		switch {
		case entry.Enabled != nil && !*entry.Enabled:
			s.Status = "disabled"
		case entry.Type != "":
			s.Detail = "configured (" + entry.Type + ") — live status unknown on this serve"
		default:
			s.Detail = "configured — live status unknown on this serve"
		}
		servers = append(servers, s)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}

// ---------------------------------------------------------------- live: reconnect

// ReconnectMCP asks the serve to re-establish one MCP connection: POST
// /mcp/{name}/connect (verified above; the serve re-runs its connect with
// enabled:true, which is exactly a reconnect for a failed/disabled entry —
// and a harmless re-create for a healthy one). A serve predating the
// route 404s the hop; that 404 surfaces as a plain "no reconnect route on
// this server version" instead of a mystery status line.
func (b *liveBackend) ReconnectMCP(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("mcp server name is empty")
	}
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	if err := b.doJSON(http.MethodPost, "/mcp/"+url.PathEscape(name)+"/connect", nil, nil); err != nil {
		if strings.Contains(err.Error(), "status 404") {
			return fmt.Errorf("no reconnect route on this server version (POST /mcp/%s/connect): %w", name, err)
		}
		return fmt.Errorf("mcp reconnect %q failed: %w", name, err)
	}
	return nil
}

// ---------------------------------------------------------------- demo fixture

// demoMCPFixture is the demo's fixed cast of four MCP servers — one per
// status glyph the panel can paint, sorted by name (the same contract the
// live backend ships). The live wire exposes NO tool count; the "N tools"
// details here exist to exercise the panel's detail lane.
func demoMCPFixture() []state.MCPServer {
	return []state.MCPServer{
		{Name: "github", Status: "needs_auth", Detail: "run: opencode mcp auth github"},
		{Name: "local-memory", Status: "connected", Detail: "12 tools"},
		{Name: "postgres", Status: "failed", Detail: "connection refused"},
		{Name: "web-search", Status: "connected", Detail: "3 tools"},
	}
}

// MCPServers is the demo twin of the live list: the scripted four, with
// any server the member already reconnected reporting connected (the flip
// lives in mcpReconnected so repeated /mcp calls stay honest).
func (b *demoBackend) MCPServers() ([]state.MCPServer, error) {
	if b.fl.isStopped() {
		return nil, errors.New("backend stopped")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	servers := demoMCPFixture()
	for i, s := range servers {
		if b.mcpReconnected[s.Name] {
			servers[i] = state.MCPServer{Name: s.Name, Status: "connected"}
		}
	}
	return servers, nil
}

// ReconnectMCP is the demo twin of the live connect call. A FAILED server
// flips to connected (state visible on the next MCPServers call) and the
// note rides the demo feed as EvStatus — the exact shape the
// permission/question twins already use ("[demo] ..."; no new event kind
// is warranted for a status flip). needs_auth is NOT reconnect-fixable
// (OAuth is the remedy there) and connected is a no-op; both return nil
// after a status note — only an unknown name is an error.
func (b *demoBackend) ReconnectMCP(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("mcp server name is empty")
	}
	if b.fl.isStopped() {
		return errors.New("backend stopped")
	}
	b.mu.Lock()
	if b.mcpReconnected == nil {
		b.mcpReconnected = make(map[string]bool)
	}
	var found state.MCPServer
	for _, s := range demoMCPFixture() {
		if s.Name == name {
			found = s
			break
		}
	}
	b.mu.Unlock()
	if found.Name == "" {
		return fmt.Errorf("no such mcp server %q", name)
	}
	switch found.Status {
	case "failed":
		b.mu.Lock()
		b.mcpReconnected[name] = true
		b.mu.Unlock()
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[demo] mcp reconnect " + name + ": failed -> connected"})
	case "needs_auth":
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[demo] mcp reconnect " + name + ": needs OAuth (opencode mcp auth " + name + ") — unchanged"})
	default:
		b.fl.emit(state.Event{Kind: state.EvStatus, Text: "[demo] mcp reconnect " + name + ": already " + found.Status + " — nothing to do"})
	}
	return nil
}
