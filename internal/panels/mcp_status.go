// mcp_status.go — the /mcp chat block: the office's view over the
// configured MCP servers and their live statuses, rendered as chat-ready
// lines (the app drops them into the chat lane as one message). Pure:
// snapshotted servers + panel width in, ANSI-styled rows out — no state,
// no backend calls.
//
// Rows: header "mcp servers", then "<glyph> <name>  <dim detail>" per
// server (glyphs: ● green connected, ◐ amber needs_auth/disabled, ✗ red
// failed, ○ dim unknown), then ONE dim reconnect hint — only while a
// failed or needs_auth entry is on the list, targeting the first FAILED
// server (a reconnect actually fixes it), else the first needs_auth (the
// serve's connect route re-attempts it). Every row folds ANSI-safely to w
// cells via foldStyledRows (chat.go): details wrap under, never overflow.
package panels

import (
	"strings"

	"github.com/theboringhumane/theboringoffice/internal/chrome"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// RenderMCPStatus renders the MCP server list as chat-ready lines.
func RenderMCPStatus(servers []state.MCPServer, w int) []string {
	if w < 1 {
		w = 1
	}
	lines := []string{chrome.Header.Render("mcp servers")}
	if len(servers) == 0 {
		lines = append(lines, chrome.DimText.Render("no mcp servers configured"))
		return lines
	}
	for _, s := range servers {
		row := mcpStatusGlyph(s.Status) + " " + s.Name
		if d := strings.TrimSpace(s.Detail); d != "" {
			row += "  " + chrome.DimText.Render(d)
		}
		lines = append(lines, foldStyledRows(row, w, w)...)
	}
	if name, ok := firstActionableMCP(servers); ok {
		lines = append(lines, foldStyledRows(chrome.DimText.Render("reconnect: /mcp reconnect "+name), w, w)...)
	}
	return lines
}

// mcpStatusGlyph paints the status dot. needs_client_registration arrives
// already folded into needs_auth backend-side (mcp.go), so four paints
// cover the contract.
func mcpStatusGlyph(status string) string {
	switch status {
	case "connected":
		return chrome.OKText.Render("●")
	case "needs_auth", "disabled":
		return chrome.WarnText.Render("◐")
	case "failed":
		return chrome.ErrText.Render("✗")
	default:
		return chrome.DimText.Render("○")
	}
}

// firstActionableMCP picks the reconnect-hint target: the first FAILED
// server wins (a reconnect actually fixes it); with none broken, the
// first needs_auth is offered (its connect attempt re-runs the handshake
// and lands back at needs_auth with the auth hint on screen). "" + false
// when nothing on the list is actionable — all-healthy renders carry no
// hint at all.
func firstActionableMCP(servers []state.MCPServer) (string, bool) {
	auth := ""
	for _, s := range servers {
		switch s.Status {
		case "failed":
			return s.Name, true
		case "needs_auth":
			if auth == "" {
				auth = s.Name
			}
		}
	}
	return auth, auth != ""
}
