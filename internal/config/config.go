// Package config — one file to run the office: ~/.grafeio/configs/brain.json
//
// The file is created with defaults on first run. Precedence:
// CLI flag > brain.json > persisted UI prefs (~/.config/grafeio/theme) > defaults.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Path returns the brain.json location, honoring GRAFEIO_HOME (tests).
func Path() string {
	home := os.Getenv("GRAFEIO_HOME")
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
}

type RoleConfig struct {
	Model      ModelRef `json:"model"`       // NOTE: honored when opencode supports per-agent model dispatch; documented in README
	NamePrefix string   `json:"namePrefix"` // roster naming seed (e.g. "tekton" -> tekton-1)
}

type UIConfig struct {
	Theme          string    `json:"theme"`          // empty = LoadPersistedTheme fallback
	Power          PowerMode `json:"power"`          // auto|performance|saver
	TickMs         int       `json:"tickMs"`         // 0 = power-mode default base (180/500)
	AmbientChatter bool      `json:"ambientChatter"` // office banter bubbles
	Sounds         string    `json:"sounds"`         // "on" | "bell" (terminal bell only) | "off"
	SidebarWidth   int       `json:"sidebarWidth"`   // right panel cols, 0 = default 68 (26..80)
	Compact        bool      `json:"compact"`        // compact layout mode
}

type BackendConfig struct {
	AgentmemoryURL   string `json:"agentmemoryUrl"`   // default localhost:3111
	Server           string `json:"server"`           // pinned opencode serve URL (else spawn)
	AgentmemoryPollS int    `json:"agentmemoryPollS"` // board sync seconds (default 5)
}

type Config struct {
	Version int           `json:"version"`
	Boss    BossConfig    `json:"boss"`
	Roles   map[string]RoleConfig `json:"roles"` // developer|scout|reviewer|runner|hr
	UI      UIConfig      `json:"ui"`
	Backend BackendConfig `json:"backend"`
}

// Default returns the stock config (also the file skeleton written on first boot).
func Default() *Config {
	return &Config{
		Version: 1,
		Boss:    BossConfig{Name: "boss (oikonomos)", Model: ""},
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
// Unknown keys are tolerated; bad JSON returns the error (caller decides).
func Load() (*Config, error) {
	p := Path()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if werr := save(p, cfg); werr != nil {
			return cfg, fmt.Errorf("config: could not write default %s: %w", p, werr)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", p, err)
	}
	cfg := Default()
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", p, err)
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
