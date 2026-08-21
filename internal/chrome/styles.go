// Package chrome — the central lipgloss style registry for the Grafeio v2
// UI: theme colors, bar/border/tab styles, and the per-role color/glyph maps.
//
// Ports nameColor + ROLE_GLYPH from node-legacy/src/office/{roster,sprites}.ts
// (dup of the office package maps is deliberate here: chrome must not import
// internal/office, which is owned elsewhere and feeds only the floor).
//
// THEME REGISTRY: every color the UI uses lives in a Theme. SetTheme(name)
// swaps the active theme and re-points ALL exported style vars (Bar, PanelBox,
// TabActive, DimText, …) — chrome/panels/topbar/statusbar read those vars at
// render time, so a switch is live within a frame. Theme names persist to
// $XDG_CONFIG_HOME/grafeio/theme (best effort); LoadPersistedTheme is read by
// main at startup.
package chrome

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"

	glan "charm.land/glamour/v2/ansi"
	glst "charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/state"
)

// Theme — every color slot the UI reads from.
type Theme struct {
	Name string

	Accent  color.Color // active tab, boss, pending, count-pending
	Err     color.Color // blocked/failed/offline
	OK      color.Color // live, done, returned
	Info    color.Color // counts, developer, brief
	Magenta color.Color // reviewer
	Blue    color.Color // runner
	White   color.Color // default ink
	Black   color.Color // fg on accent bg
	Dim     color.Color // neutral chrome, hints, notices

	BarBg     color.Color // inverted bar background (topbar + statusbar)
	Border    color.Color // panel rounded border
	ToolColor color.Color // chat tool one-liner ink (noir: dim cyan)

	// Deep-work stream accents.
	Warn     color.Color // permission modal amber, queue badge (statusbar)
	Question color.Color // question-tool yellow ("boss asks ›")

	// per-role chat/floor identity colors
	RoleBoss     color.Color
	RoleHR       color.Color
	RoleDev      color.Color
	RoleScout    color.Color
	RoleReviewer color.Color
	RoleRunner   color.Color

	// Expanded chat diffs (opencode-style): full-row tints + inks.
	// DiffAddBg/DiffDelBg nil → the row tint is suppressed and mono-style
	// emphasis steps in (bold additions / underlined deletions).
	DiffAddBg    color.Color // tint behind addition rows
	DiffDelBg    color.Color // tint behind deletion rows
	DiffAddFg    color.Color // addition text ink on the tint
	DiffDelFg    color.Color // deletion text ink on the tint
	DiffCtxFg    color.Color // context line ink (untinted rows)
	DiffGutterFg color.Color // line-number gutter ink

	// Chroma — the chroma styles.Get name used for inline syntax highlighting
	// inside expanded diff rows (per theme).
	ChromaStyle string

	// Glamour — the glamour standard style for boss markdown (dark/light/notty/dracula).
	Glamour string
}

// themeList keeps the /themes order stable (map iteration is random).
var themeList = []Theme{
	{ // noir — the original dark look (ANSI palette)
		Name:   "noir",
		Accent: lipgloss.Color("3"), Err: lipgloss.Color("1"),
		OK: lipgloss.Color("2"), Info: lipgloss.Color("6"),
		Magenta: lipgloss.Color("5"), Blue: lipgloss.Color("4"),
		White: lipgloss.Color("7"), Black: lipgloss.Color("0"),
		Dim:   lipgloss.Color("8"),
		BarBg: lipgloss.Color("8"), Border: lipgloss.Color("7"),
		ToolColor:    lipgloss.Color("6"),
		Warn:         lipgloss.Color("3"),
		Question:     lipgloss.Color("11"),
		RoleBoss:     lipgloss.Color("3"),
		RoleHR:       lipgloss.Color("1"),
		RoleDev:      lipgloss.Color("6"),
		RoleScout:    lipgloss.Color("2"),
		RoleReviewer: lipgloss.Color("5"),
		RoleRunner:   lipgloss.Color("4"),
		DiffAddBg:    lipgloss.Color("#16301d"),
		DiffDelBg:    lipgloss.Color("#33191c"),
		DiffAddFg:    lipgloss.Color("#56d364"),
		DiffDelFg:    lipgloss.Color("#ff7b72"),
		DiffCtxFg:    lipgloss.Color("8"),
		DiffGutterFg: lipgloss.Color("8"),
		ChromaStyle:  "github-dark",
		Glamour:      "dark",
	},
	{ // paper — light bg, dark fg, the same accents re-darkened
		Name:   "paper",
		Accent: lipgloss.Color("#9a6700"), Err: lipgloss.Color("#d1242f"),
		OK: lipgloss.Color("#1a7f37"), Info: lipgloss.Color("#0891b2"),
		Magenta: lipgloss.Color("#a626a4"), Blue: lipgloss.Color("#0969da"),
		White: lipgloss.Color("#201f1e"), Black: lipgloss.Color("#ffffff"),
		Dim:   lipgloss.Color("#57606a"),
		BarBg: lipgloss.Color("#d8dce1"), Border: lipgloss.Color("#57606a"),
		ToolColor:    lipgloss.Color("#0891b2"),
		Warn:         lipgloss.Color("#b35900"),
		Question:     lipgloss.Color("#9a6700"),
		RoleBoss:     lipgloss.Color("#9a6700"),
		RoleHR:       lipgloss.Color("#d1242f"),
		RoleDev:      lipgloss.Color("#0891b2"),
		RoleScout:    lipgloss.Color("#1a7f37"),
		RoleReviewer: lipgloss.Color("#a626a4"),
		RoleRunner:   lipgloss.Color("#0969da"),
		DiffAddBg:    lipgloss.Color("#d2ecd7"),
		DiffDelBg:    lipgloss.Color("#f5d8d8"),
		DiffAddFg:    lipgloss.Color("#1a7f37"),
		DiffDelFg:    lipgloss.Color("#d1242f"),
		DiffCtxFg:    lipgloss.Color("#57606a"),
		DiffGutterFg: lipgloss.Color("#8d98a3"),
		ChromaStyle:  "github",
		Glamour:      "light",
	},
	{ // mono — grayscale; emphasis comes from bold/dim, not hue
		Name:   "mono",
		Accent: lipgloss.Color("#e4e4e4"), Err: lipgloss.Color("#d0d0d0"),
		OK: lipgloss.Color("#c6c6c6"), Info: lipgloss.Color("#bcbcbc"),
		Magenta: lipgloss.Color("#a8a8a8"), Blue: lipgloss.Color("#949494"),
		White: lipgloss.Color("#e4e4e4"), Black: lipgloss.Color("#111111"),
		Dim:   lipgloss.Color("#6f6f6f"),
		BarBg: lipgloss.Color("#3a3a3a"), Border: lipgloss.Color("#8a8a8a"),
		ToolColor:    lipgloss.Color("#a8a8a8"),
		Warn:         lipgloss.Color("#efefef"),
		Question:     lipgloss.Color("#c6c6c6"),
		RoleBoss:     lipgloss.Color("#ffffff"),
		RoleHR:       lipgloss.Color("#d0d0d0"),
		RoleDev:      lipgloss.Color("#bcbcbc"),
		RoleScout:    lipgloss.Color("#a8a8a8"),
		RoleReviewer: lipgloss.Color("#949494"),
		RoleRunner:   lipgloss.Color("#808080"),
		// mono: NO row tints (nil bg) — additions render bold, deletions
		// underlined; hue carries no meaning in a grayscale theme.
		DiffAddBg:    nil,
		DiffDelBg:    nil,
		DiffAddFg:    lipgloss.Color("#e4e4e4"),
		DiffDelFg:    lipgloss.Color("#d0d0d0"),
		DiffCtxFg:    lipgloss.Color("#6f6f6f"),
		DiffGutterFg: lipgloss.Color("#6f6f6f"),
		ChromaStyle:  "bw",
		Glamour:      "notty",
	},
	{ // dracula — canonical palette (draculatheme.com)
		Name:   "dracula",
		Accent: lipgloss.Color("#f1fa8c"), Err: lipgloss.Color("#ff5555"),
		OK: lipgloss.Color("#50fa7b"), Info: lipgloss.Color("#8be9fd"),
		Magenta: lipgloss.Color("#bd93f9"), Blue: lipgloss.Color("#ffb86c"),
		White: lipgloss.Color("#f8f8f2"), Black: lipgloss.Color("#282a36"),
		Dim:   lipgloss.Color("#6272a4"),
		BarBg: lipgloss.Color("#44475a"), Border: lipgloss.Color("#6272a4"),
		ToolColor:    lipgloss.Color("#8be9fd"),
		Warn:         lipgloss.Color("#ffb86c"),
		Question:     lipgloss.Color("#f1fa8c"),
		RoleBoss:     lipgloss.Color("#f1fa8c"),
		RoleHR:       lipgloss.Color("#ff5555"),
		RoleDev:      lipgloss.Color("#8be9fd"),
		RoleScout:    lipgloss.Color("#50fa7b"),
		RoleReviewer: lipgloss.Color("#bd93f9"),
		RoleRunner:   lipgloss.Color("#ffb86c"),
		DiffAddBg:    lipgloss.Color("#1f3a2a"),
		DiffDelBg:    lipgloss.Color("#442a30"),
		DiffAddFg:    lipgloss.Color("#50fa7b"),
		DiffDelFg:    lipgloss.Color("#ff5555"),
		DiffCtxFg:    lipgloss.Color("#8a97c4"),
		DiffGutterFg: lipgloss.Color("#6272a4"),
		ChromaStyle:  "dracula",
		Glamour:      "dracula",
	},
	{ // solarized — canonical Solarized Dark palette (ethanschoonover.com/solarized)
		Name:   "solarized",
		Accent: lipgloss.Color("#b58900"), Err: lipgloss.Color("#dc322f"),
		OK: lipgloss.Color("#859900"), Info: lipgloss.Color("#2aa198"),
		Magenta: lipgloss.Color("#6c71c4"), Blue: lipgloss.Color("#268bd2"),
		White: lipgloss.Color("#839496"), Black: lipgloss.Color("#002b36"),
		Dim:   lipgloss.Color("#586e75"),
		BarBg: lipgloss.Color("#073642"), Border: lipgloss.Color("#586e75"),
		ToolColor:    lipgloss.Color("#2aa198"),
		Warn:         lipgloss.Color("#cb4b16"),
		Question:     lipgloss.Color("#b58900"),
		RoleBoss:     lipgloss.Color("#b58900"),
		RoleHR:       lipgloss.Color("#dc322f"),
		RoleDev:      lipgloss.Color("#2aa198"),
		RoleScout:    lipgloss.Color("#859900"),
		RoleReviewer: lipgloss.Color("#6c71c4"),
		RoleRunner:   lipgloss.Color("#268bd2"),
		// tints over base03 (#002b36): dark green / dark red washes
		DiffAddBg:    lipgloss.Color("#14362a"),
		DiffDelBg:    lipgloss.Color("#3c2429"),
		DiffAddFg:    lipgloss.Color("#859900"),
		DiffDelFg:    lipgloss.Color("#dc322f"),
		DiffCtxFg:    lipgloss.Color("#586e75"),
		DiffGutterFg: lipgloss.Color("#586e75"),
		ChromaStyle:  "solarized-dark",
		Glamour:      "light",
	},
}

var themes = func() map[string]Theme {
	m := make(map[string]Theme, len(themeList))
	for _, t := range themeList {
		m[t.Name] = t
	}
	return m
}()

// ThemeNames is the stable, display-ordered list of theme names (/themes).
func ThemeNames() []string {
	names := make([]string, 0, len(themeList))
	for _, t := range themeList {
		names = append(names, t.Name)
	}
	return names
}

var current Theme

// CurrentTheme returns the active theme.
func CurrentTheme() Theme { return current }

// SetTheme makes `name` the active theme, re-pointing every exported style
// var. Returns false (and changes nothing) for an unknown name.
func SetTheme(name string) bool {
	t, ok := themes[name]
	if !ok {
		return false
	}
	current = t
	applyTheme(t)
	return true
}

// ThemeConfigPath is the persisted theme file
// ($XDG_CONFIG_HOME/grafeio/theme, falling back to ~/.config/grafeio/theme).
func ThemeConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "grafeio", "theme")
}

// LoadPersistedTheme returns the persisted theme name, or "" when absent
// or unreadable. Callers (main) decide to SetTheme on it.
func LoadPersistedTheme() string {
	b, err := os.ReadFile(ThemeConfigPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// PersistTheme writes the active theme name to ThemeConfigPath, mkdir -p'ing
// first. Best effort: callers should ignore the error.
func PersistTheme() error {
	p := ThemeConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(current.Name+"\n"), 0o644)
}

// MarkdownStyle is the per-theme glamour style for boss chat markdown:
// the named standard style minus the outer document margin, so a boss reply
// fills the sidebar instead of wasting cells on margins.
func MarkdownStyle() glan.StyleConfig {
	var s glan.StyleConfig
	switch current.Glamour {
	case "light":
		s = glst.LightStyleConfig
	case "notty":
		s = glst.NoTTYStyleConfig
	case "dracula":
		s = glst.DraculaStyleConfig
	default: // "dark"
		s = glst.DarkStyleConfig
	}
	zero := uint(0)
	s.Document.Margin = &zero
	if current.Name == "noir" {
		// keep the original noir look: explicit document ink
		v := "252"
		s.Document.Color = &v
	}
	return s
}

// Theme constants — re-pointed by SetTheme. ink slots of the active theme:
// Accent/Err/OK/Info stay semantic for consumers; panels read these vars.
var (
	Accent   color.Color
	Err      color.Color
	OK       color.Color
	Info     color.Color
	Magenta  color.Color
	Blue     color.Color
	White    color.Color
	Black    color.Color
	Dim      color.Color
	Warn     color.Color
	Question color.Color
)

// BarBgColor is the inverted bar background of the active theme.
var BarBgColor color.Color

// Expanded diff slots of the active theme (re-pointed by SetTheme).
// DiffAddBg/DiffDelBg are nil in themes with suppressed tints (mono).
var (
	DiffAddBg    color.Color
	DiffDelBg    color.Color
	DiffAddFg    color.Color
	DiffDelFg    color.Color
	DiffCtxFg    color.Color
	DiffGutterFg color.Color

	// DiffChromaStyle is the chroma styles.Get name for inline syntax in
	// expanded diff rows (nil-safe: styles.Get falls back when unknown).
	DiffChromaStyle string
)

// Bar / panel styles — re-derived by SetTheme.
var (
	// Bar is the base style for topbar + statusbar rows (inverted bar).
	Bar lipgloss.Style

	// PanelBox draws a rounded border panel (TS used borderStyle="round").
	PanelBox lipgloss.Style

	// TabActive — accent bg, black fg (the selected tab label). Labels carry
	// their own padding spaces, so no extra Padding here.
	TabActive lipgloss.Style
	// TabInactive — gray.
	TabInactive lipgloss.Style

	// Header is a panel title (bold ink).
	Header lipgloss.Style

	// Common text styles.
	DimText    lipgloss.Style
	AccentText lipgloss.Style
	ErrText    lipgloss.Style
	OKText     lipgloss.Style
	InfoText   lipgloss.Style

	// ToolStyle is the chat tool one-liner style (noir: dim cyan).
	ToolStyle lipgloss.Style

	// Deep-work stream styles.
	WarnText     lipgloss.Style // permission amber
	WarnBold     lipgloss.Style // permission amber, bold (modal header)
	QuestionText lipgloss.Style // question-tool yellow
)

// applyTheme re-points every exported style var for the given theme.
func applyTheme(t Theme) {
	Accent, Err, OK, Info = t.Accent, t.Err, t.OK, t.Info
	Magenta, Blue, White, Black, Dim = t.Magenta, t.Blue, t.White, t.Black, t.Dim
	Warn, Question = t.Warn, t.Question
	BarBgColor = t.BarBg

	Bar = lipgloss.NewStyle().Background(BarBgColor).Foreground(White)
	PanelBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Border)
	TabActive = lipgloss.NewStyle().Background(Accent).Foreground(Black).Bold(true)
	TabInactive = lipgloss.NewStyle().Foreground(Dim)
	Header = lipgloss.NewStyle().Bold(true).Foreground(White)

	DimText = lipgloss.NewStyle().Foreground(Dim)
	AccentText = lipgloss.NewStyle().Foreground(Accent)
	ErrText = lipgloss.NewStyle().Foreground(Err)
	OKText = lipgloss.NewStyle().Foreground(OK)
	InfoText = lipgloss.NewStyle().Foreground(Info)
	ToolStyle = lipgloss.NewStyle().Foreground(t.ToolColor).Faint(true)
	WarnText = lipgloss.NewStyle().Foreground(t.Warn)
	WarnBold = lipgloss.NewStyle().Foreground(t.Warn).Bold(true)
	QuestionText = lipgloss.NewStyle().Foreground(t.Question)

	DiffAddBg, DiffDelBg = t.DiffAddBg, t.DiffDelBg
	DiffAddFg, DiffDelFg = t.DiffAddFg, t.DiffDelFg
	DiffCtxFg, DiffGutterFg = t.DiffCtxFg, t.DiffGutterFg
	DiffChromaStyle = t.ChromaStyle
}

// RoleColor — port of node-legacy roster.nameColor: per-theme role ink;
// boss, hr, dev, scout, reviewer, runner; default is the theme's ink.
func RoleColor(name string) color.Color {
	t := current
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "boss"), strings.HasPrefix(n, "manager"):
		return t.RoleBoss
	case strings.HasPrefix(n, "hr"):
		return t.RoleHR
	case strings.HasPrefix(n, "tekton"), strings.HasPrefix(n, "dev"):
		return t.RoleDev
	case strings.HasPrefix(n, "skopos"), strings.HasPrefix(n, "scout"):
		return t.RoleScout
	case strings.HasPrefix(n, "dikastes"), strings.HasPrefix(n, "review"):
		return t.RoleReviewer
	case strings.HasPrefix(n, "hemero"), strings.HasPrefix(n, "run"):
		return t.RoleRunner
	default:
		return t.White
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

// ModeColor — accent in demo, ok when live (matches the TS bars).
func ModeColor(mode state.Mode) color.Color {
	if mode == state.ModeDemo {
		return Accent
	}
	return OK
}

func init() {
	SetTheme("noir")
}
