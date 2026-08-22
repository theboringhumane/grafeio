// Package config — one file to run the office: ~/.theboringoffice/configs/brain.json
//
// The file is created with defaults on first run. Precedence:
// CLI flag > brain.json > persisted UI prefs (~/.config/theboringoffice/theme) > defaults.
//
// Rename-era compatibility: the product was "grafeio" (dirs ~/.grafeio and
// env vars GRAFEIO_*). Reads fall back to the old locations; writes ONLY
// land on the new paths — a user's brain.json/session/theme is never
// silently lost, and the old dir is never mutated.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HomeOverride returns the test/harness scratch-root override:
// THEBORINGOFFICE_HOME, falling back to the pre-rename GRAFEIO_HOME so
// existing scripts and CI keep working. "" means "use $HOME".
func HomeOverride() string {
	if home := os.Getenv("THEBORINGOFFICE_HOME"); home != "" {
		return home
	}
	return os.Getenv("GRAFEIO_HOME")
}

// Path returns the brain.json location, honoring HomeOverride() (tests).
func Path() string {
	home := HomeOverride()
	if home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".theboringoffice", "configs", "brain.json")
}

// legacyPath is the pre-rename brain.json location (~/.grafeio/...). Read
// fallback only — Load consults it when the new path is absent; writes
// always go to Path().
func legacyPath() string {
	home := HomeOverride()
	if home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".grafeio", "configs", "brain.json")
}

// PowerMode — battery posture of the whole app.
type PowerMode string

const (
	PowerAuto        PowerMode = "auto"        // adaptive tick: busy = fast, idle = slow
	PowerPerformance PowerMode = "performance" // always fast
	PowerSaver       PowerMode = "saver"       // slow tick, coalesced renders, quieter office
)

// ModelRef — a provider/model string opencode understands ("anthropic/claude-sonnet-4-5").
type ModelRef string

type BossConfig struct {
	Name  string   `json:"name"`  // display name: "boss (oikonomos)"
	Model ModelRef `json:"model"` // orch/boss model override (prompt-level); empty = server default
	// Concierge answers instantly when the boss's turn is occupied: its own
	// lightweight session that answers directly or dispatches its own developers.
	Concierge bool `json:"concierge"` // default true
}

type RoleConfig struct {
	Model      ModelRef `json:"model"`      // NOTE: honored when opencode supports per-agent model dispatch; documented in README
	NamePrefix string   `json:"namePrefix"` // roster naming seed (e.g. "tekton" -> tekton-1)
}

type UIConfig struct {
	Theme          string    `json:"theme"`          // empty = LoadPersistedTheme fallback
	Power          PowerMode `json:"power"`          // auto|performance|saver
	TickMs         int       `json:"tickMs"`         // 0 = power-mode default base (180/500)
	AmbientChatter bool      `json:"ambientChatter"` // office banter bubbles
	Sounds         string    `json:"sounds"`         // "on" | "bell" (terminal bell only) | "off"
	SidebarWidth   int       `json:"sidebarWidth"`   // right panel cols, 0 = default 80 (26..100)
	Compact        bool      `json:"compact"`        // compact layout mode
}

type BackendConfig struct {
	AgentmemoryURL   string `json:"agentmemoryUrl"`   // default localhost:3111
	Server           string `json:"server"`           // pinned opencode serve URL (else spawn)
	AgentmemoryPollS int    `json:"agentmemoryPollS"` // board sync seconds (default 5)
	// BossModel is the "provider/model" override ("anthropic/claude-sonnet-4")
	// the primary (boss) session's prompts carry. "" = server default.
	// Validation is deliberately lite: a value WITHOUT a "/" is kept as-is —
	// the wire layer skips it (opencode needs providerID AND modelID), and
	// a well-formed-but-wrong one errors visibly at the serve (the backend's
	// status line surfaces it and retries bare).
	BossModel string `json:"bossModel,omitempty"`
	// CTOModel is the comparable override for the office CTO
	// ("theboringcto"): the routing rule is title-based
	// (state.IsArchitectureBrief is the ONE matcher; the floor hires those
	// child sessions as the CTO). "" = server default. Same validation-lite
	// rule as BossModel.
	CTOModel string `json:"ctoModel,omitempty"`
}

type Config struct {
	Version int                   `json:"version"`
	Boss    BossConfig            `json:"boss"`
	Roles   map[string]RoleConfig `json:"roles"` // developer|scout|reviewer|runner|hr
	UI      UIConfig              `json:"ui"`
	Backend BackendConfig         `json:"backend"`
}

// Default returns the stock config (also the file skeleton written on first boot).
func Default() *Config {
	return &Config{
		Version: 1,
		Boss:    BossConfig{Name: "boss (oikonomos)", Model: "", Concierge: true},
		Roles: map[string]RoleConfig{
			"developer": {NamePrefix: "tekton"},
			"scout":     {NamePrefix: "skopos"},
			"reviewer":  {NamePrefix: "dikastes"},
			"runner":    {NamePrefix: "hemerodromos"},
			"hr":        {NamePrefix: "mnemosyne"},
		},
		UI: UIConfig{
			Theme:          "",
			Power:          PowerAuto,
			TickMs:         0,
			AmbientChatter: true,
			Sounds:         "on",
			SidebarWidth:   0,
			Compact:        false,
		},
		Backend: BackendConfig{
			AgentmemoryURL:   "http://localhost:3111",
			Server:           "",
			AgentmemoryPollS: 5,
		},
	}
}

// Load reads brain.json; creates it (parents + defaults) when absent.
// A pre-rename file under ~/.grafeio is READ as a fallback (never moved,
// never written — the next Save lands on the new path only), so an upgrade
// never silently loses the user's brain.json.
// Unknown keys are tolerated; bad JSON returns the error (caller decides).
func Load() (*Config, error) {
	p := Path()
	readFrom := p
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		if lb, lerr := os.ReadFile(legacyPath()); lerr == nil {
			b, err, readFrom = lb, nil, legacyPath()
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if werr := save(p, cfg); werr != nil {
			return cfg, fmt.Errorf("config: could not write default %s: %w", p, werr)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", readFrom, err)
	}
	cfg := Default()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", readFrom, err)
	}
	if cfg.Boss.Name == "" {
		cfg.Boss.Name = "boss (oikonomos)"
	}
	if cfg.Backend.AgentmemoryURL == "" {
		cfg.Backend.AgentmemoryURL = "http://localhost:3111"
	}
	return cfg, nil
}

// Save writes the current config back (used by in-app mutation commands).
func Save(cfg *Config) error {
	return save(Path(), cfg)
}

func save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
