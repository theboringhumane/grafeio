// Package chrome — the central lipgloss style registry for the Grafeio v2
// UI: theme colors, bar/border/tab styles, and the per-role color/glyph maps.
//
// Ports nameColor + ROLE_GLYPH from node-legacy/src/office/{roster,sprites}.ts
// (dup of the office package maps is deliberate here: chrome must not import
// internal/office, which is owned elsewhere and feeds only the floor).
package chrome

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/state"
)

// Theme constants. ink color names from the TS port map to ANSI codes:
// yellow=3, red=1, green=2, cyan=6, magenta=5, blue=4, white=7,
// blackBright (neutral chrome) = 8.
var (
	Accent  = lipgloss.Color("3") // yellow — active tab, boss, pending, demo
	Err     = lipgloss.Color("1") // red — blocked/failed/offline, hr
	OK      = lipgloss.Color("2") // green — live, done, returned, scout
	Info    = lipgloss.Color("6") // cyan — counts, developer, brief
	Magenta = lipgloss.Color("5") // reviewer
	Blue    = lipgloss.Color("4") // runner
	White   = lipgloss.Color("7") // default ink
	Black   = lipgloss.Color("0") // fg on accent bg
	Dim     = lipgloss.Color("8") // brightBlack — neutral chrome, at-desk, notice
)

// BarBgColor is the inverted bar background (ink blackBright).
var BarBgColor = Dim

// Bar / panel styles.
var (
	// Bar is the base style for topbar + statusbar rows (inverted bar).
	Bar = lipgloss.NewStyle().Background(BarBgColor).Foreground(White)

	// PanelBox draws a rounded border panel (TS used borderStyle="round").
	PanelBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(White)

	// TabActive — accent bg, black fg (the selected tab label). Labels carry
	// their own padding spaces, so no extra Padding here.
	TabActive = lipgloss.NewStyle().Background(Accent).Foreground(Black).Bold(true)
	// TabInactive — gray.
	TabInactive = lipgloss.NewStyle().Foreground(Dim)

	// Header is a panel title (bold white).
	Header = lipgloss.NewStyle().Bold(true).Foreground(White)

	// Common text styles.
	DimText    = lipgloss.NewStyle().Foreground(Dim)
	AccentText = lipgloss.NewStyle().Foreground(Accent)
	ErrText    = lipgloss.NewStyle().Foreground(Err)
	OKText     = lipgloss.NewStyle().Foreground(OK)
	InfoText   = lipgloss.NewStyle().Foreground(Info)
)

// RoleColor — port of node-legacy roster.nameColor: boss yellow, hr red,
// dev cyan, scout green, reviewer magenta, runner blue; default white.
func RoleColor(name string) color.Color {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "boss"), strings.HasPrefix(n, "manager"):
		return Accent
	case strings.HasPrefix(n, "hr"):
		return Err
	case strings.HasPrefix(n, "tekton"), strings.HasPrefix(n, "dev"):
		return Info
	case strings.HasPrefix(n, "skopos"), strings.HasPrefix(n, "scout"):
		return OK
	case strings.HasPrefix(n, "dikastes"), strings.HasPrefix(n, "review"):
		return Magenta
	case strings.HasPrefix(n, "hemero"), strings.HasPrefix(n, "run"):
		return Blue
	default:
		return White
	}
}

// RoleGlyph — port of node-legacy sprites.ROLE_GLYPH. Dup of the office
// package copy is intentional (chrome never imports internal/office).
func RoleGlyph(role state.EmployeeRole) string {
	switch role {
	case state.RoleManager:
		return "M"
	case state.RoleHR:
		return "H"
	case state.RoleDeveloper:
		return "T"
	case state.RoleScout:
		return "S"
	case state.RoleReviewer:
		return "D"
	case state.RoleRunner:
		return "R"
	default:
		return "?"
	}
}

// Fg renders s in the given color (foreground only).
func Fg(c color.Color, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

// OnBar renders s colored against the shared bar background.
func OnBar(c color.Color, s string) string {
	return lipgloss.NewStyle().Background(BarBgColor).Foreground(c).Render(s)
}

// OnBarBold renders s bold-and-colored against the shared bar background.
func OnBarBold(c color.Color, s string) string {
	return lipgloss.NewStyle().Background(BarBgColor).Foreground(c).Bold(true).Render(s)
}

// ModeColor — yellow in demo, green when live (matches the TS bars).
func ModeColor(mode state.Mode) color.Color {
	if mode == state.ModeDemo {
		return Accent
	}
	return OK
}
