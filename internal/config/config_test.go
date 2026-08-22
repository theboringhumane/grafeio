package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// useHome redirects THEBORINGOFFICE_HOME (and therefore Path) into a fresh temp dir.
func useHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("THEBORINGOFFICE_HOME", dir)
	return dir
}

// writeBrain plants a brain.json fixture under the given THEBORINGOFFICE_HOME.
func writeBrain(t *testing.T, home, content string) string {
	t.Helper()
	p := filepath.Join(home, ".theboringoffice", "configs", "brain.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func TestPath(t *testing.T) {
	t.Run("honors THEBORINGOFFICE_HOME", func(t *testing.T) {
		home := useHome(t)
		want := filepath.Join(home, ".theboringoffice", "configs", "brain.json")
		if got := Path(); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})

	t.Run("falls back to HOME", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("THEBORINGOFFICE_HOME", "")
		t.Setenv("HOME", home)
		want := filepath.Join(home, ".theboringoffice", "configs", "brain.json")
		if got := Path(); got != want {
			t.Errorf("Path() = %q, want %q", got, want)
		}
	})
}

// TestHomeOverride pins the rename-era env contract: THEBORINGOFFICE_HOME
// wins when both are exported; the pre-rename GRAFEIO_HOME still works when
// it is the only one.
func TestHomeOverride(t *testing.T) {
	t.Setenv("THEBORINGOFFICE_HOME", "/new")
	t.Setenv("GRAFEIO_HOME", "/old")
	if got := HomeOverride(); got != "/new" {
		t.Errorf("HomeOverride() = %q, want THEBORINGOFFICE_HOME \"/new\"", got)
	}
	t.Setenv("THEBORINGOFFICE_HOME", "")
	if got := HomeOverride(); got != "/old" {
		t.Errorf("HomeOverride() = %q, want GRAFEIO_HOME fallback \"/old\"", got)
	}
}

// TestLoad_LegacyPathFallback pins the rename-era read contract: with no
// file at the new path, brain.json under the pre-rename ~/.grafeio is READ
// (never written); the next Save still lands on the new path only.
func TestLoad_LegacyPathFallback(t *testing.T) {
	home := useHome(t)
	legacy := filepath.Join(home, ".grafeio", "configs", "brain.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacy, []byte(`{"ui": {"theme": "nord"}}`), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with only a legacy fixture: %v", err)
	}
	if cfg.UI.Theme != "nord" {
		t.Errorf("Load() must read the legacy brain.json: UI.Theme = %q, want \"nord\"", cfg.UI.Theme)
	}

	// The legacy file is untouched, and no new file appeared as a side
	// effect of the READ (writes happen on explicit Save / first-boot
	// default only).
	if b, err := os.ReadFile(legacy); err != nil || string(b) != `{"ui": {"theme": "nord"}}` {
		t.Errorf("legacy file must stay untouched, got %q (err=%v)", b, err)
	}
	if _, err := os.Stat(Path()); !os.IsNotExist(err) {
		t.Errorf("Load() must not conjure the new file while only reading legacy (err=%v)", err)
	}

	// Save writes the NEW path only.
	cfg.UI.Theme = "mono"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save: %v", err)
	}
	if got.UI.Theme != "mono" {
		t.Errorf("after Save the new path wins: UI.Theme = %q, want \"mono\"", got.UI.Theme)
	}
	if b, _ := os.ReadFile(legacy); string(b) != `{"ui": {"theme": "nord"}}` {
		t.Errorf("Save must not touch the legacy file, got %q", b)
	}
}

func TestLoad_MissingFile_CreatesDefaults(t *testing.T) {
	useHome(t)
	p := Path()

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("precondition: fixture should not exist yet, stat err = %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() on missing file: unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("Load() on missing file = %+v, want Default() %+v", cfg, Default())
	}

	// The file must have been created with the default skeleton.
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("brain.json was not created by Load(): %v", err)
	}
	var roundTrip Config
	if err := json.Unmarshal(b, &roundTrip); err != nil {
		t.Fatalf("created brain.json is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(&roundTrip, Default()) {
		t.Errorf("created file decodes to %+v, want Default() %+v", &roundTrip, Default())
	}

	// Second load reads the file it just wrote — same result, no error.
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("second Load(): %v", err)
	}
	if !reflect.DeepEqual(cfg2, cfg) {
		t.Errorf("second Load() = %+v, want %+v", cfg2, cfg)
	}
}

func TestDefault_Shape(t *testing.T) {
	cfg := Default()

	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Boss.Name != "boss (oikonomos)" || !cfg.Boss.Concierge || cfg.Boss.Model != "" {
		t.Errorf("Boss = %+v, want name \"boss (oikonomos)\", concierge true, empty model", cfg.Boss)
	}
	prefixes := map[string]string{
		"developer": "tekton",
		"scout":     "skopos",
		"reviewer":  "dikastes",
		"runner":    "hemerodromos",
		"hr":        "mnemosyne",
	}
	if len(cfg.Roles) != len(prefixes) {
		t.Errorf("len(Roles) = %d, want %d", len(cfg.Roles), len(prefixes))
	}
	for role, prefix := range prefixes {
		rc, ok := cfg.Roles[role]
		if !ok {
			t.Errorf("Roles missing %q", role)
			continue
		}
		if rc.NamePrefix != prefix {
			t.Errorf("Roles[%q].NamePrefix = %q, want %q", role, rc.NamePrefix, prefix)
		}
	}
	if cfg.UI.Power != PowerAuto || !cfg.UI.AmbientChatter || cfg.UI.Sounds != "on" || cfg.UI.TickMs != 0 {
		t.Errorf("UI = %+v, want power auto / ambientChatter true / sounds on / tickMs 0", cfg.UI)
	}
	if cfg.Backend.AgentmemoryURL != "http://localhost:3111" || cfg.Backend.AgentmemoryPollS != 5 {
		t.Errorf("Backend = %+v, want agentmemoryUrl http://localhost:3111 / poll 5", cfg.Backend)
	}
}

func TestLoad_FixtureFieldsMatch(t *testing.T) {
	home := useHome(t)
	writeBrain(t, home, `{
  "version": 7,
  "boss": {"name": "chief", "model": "anthropic/claude-sonnet-4-5", "concierge": false},
  "roles": {"developer": {"model": "openai/gpt-5", "namePrefix": "mason"}},
  "ui": {"theme": "gruvbox", "power": "performance", "tickMs": 42, "ambientChatter": false, "sounds": "bell", "sidebarWidth": 60, "compact": true},
  "backend": {"agentmemoryUrl": "http://am:9999", "server": "http://oc:7777", "agentmemoryPollS": 9}
}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.Version != 7 {
		t.Errorf("Version = %d, want 7", cfg.Version)
	}
	if cfg.Boss.Name != "chief" || cfg.Boss.Model != "anthropic/claude-sonnet-4-5" || cfg.Boss.Concierge {
		t.Errorf("Boss = %+v, want custom fixture values", cfg.Boss)
	}
	dev := cfg.Roles["developer"]
	if dev.Model != "openai/gpt-5" || dev.NamePrefix != "mason" {
		t.Errorf("Roles[developer] = %+v, want model openai/gpt-5 prefix mason", dev)
	}

	// Real contract: Unmarshal merges into Default()'s non-nil map, so role
	// keys absent from the file keep their default name prefixes.
	scout, ok := cfg.Roles["scout"]
	if !ok || scout.NamePrefix != "skopos" {
		t.Errorf("Roles[scout] = %+v (ok=%v), want default prefix skopos (map merge)", scout, ok)
	}
	if len(cfg.Roles) != 5 {
		t.Errorf("len(Roles) = %d, want 5 (1 overwritten + 4 defaults merged)", len(cfg.Roles))
	}

	if cfg.UI.Theme != "gruvbox" || cfg.UI.Power != PowerPerformance || cfg.UI.TickMs != 42 ||
		cfg.UI.AmbientChatter || cfg.UI.Sounds != "bell" || cfg.UI.SidebarWidth != 60 || !cfg.UI.Compact {
		t.Errorf("UI = %+v, want custom fixture values", cfg.UI)
	}
	if cfg.Backend.AgentmemoryURL != "http://am:9999" || cfg.Backend.Server != "http://oc:7777" || cfg.Backend.AgentmemoryPollS != 9 {
		t.Errorf("Backend = %+v, want custom fixture values", cfg.Backend)
	}
}

func TestLoad_PartialFixture_KeepsDefaults(t *testing.T) {
	home := useHome(t)
	// Only one leaf field is set; everything else must fall back to Default()
	// because Load unmarshals on top of Default().
	writeBrain(t, home, `{"ui": {"theme": "nord"}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	if cfg.UI.Theme != "nord" {
		t.Errorf("UI.Theme = %q, want \"nord\"", cfg.UI.Theme)
	}
	if cfg.UI.Power != PowerAuto || !cfg.UI.AmbientChatter || cfg.UI.Sounds != "on" {
		t.Errorf("UI = %+v, untouched fields should keep defaults", cfg.UI)
	}
	if cfg.Boss.Name != "boss (oikonomos)" || !cfg.Boss.Concierge {
		t.Errorf("Boss = %+v, untouched fields should keep defaults", cfg.Boss)
	}
	if cfg.Backend.AgentmemoryURL != "http://localhost:3111" || cfg.Backend.AgentmemoryPollS != 5 {
		t.Errorf("Backend = %+v, untouched fields should keep defaults", cfg.Backend)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want default 1", cfg.Version)
	}
}

func TestLoad_NormalizesEmptyFields(t *testing.T) {
	home := useHome(t)
	// Explicitly empty strings cannot survive Load: it re-fills them.
	writeBrain(t, home, `{"boss": {"name": ""}, "backend": {"agentmemoryUrl": ""}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Boss.Name != "boss (oikonomos)" {
		t.Errorf("Boss.Name = %q, want refilled \"boss (oikonomos)\"", cfg.Boss.Name)
	}
	if cfg.Backend.AgentmemoryURL != "http://localhost:3111" {
		t.Errorf("Backend.AgentmemoryURL = %q, want refilled \"http://localhost:3111\"", cfg.Backend.AgentmemoryURL)
	}
}

func TestLoad_MalformedJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"truncated object", `{"version": 1, "boss":`},
		{"not json at all", `hello world`},
		{"wrong type for field", `{"version": "one"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := useHome(t)
			p := writeBrain(t, home, tc.content)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load() with malformed JSON: want error, got cfg=%+v", cfg)
			}
			if cfg != nil {
				t.Errorf("Load() with malformed JSON returned cfg=%+v, want nil", cfg)
			}
			if !strings.Contains(err.Error(), "config: parse") {
				t.Errorf("error = %q, want prefix \"config: parse\"", err.Error())
			}
			if !strings.Contains(err.Error(), p) {
				t.Errorf("error = %q, want it to mention the path %q", err.Error(), p)
			}
		})
	}
}

func TestSave_RoundTrip(t *testing.T) {
	useHome(t)

	cfg := Default()
	cfg.UI.Theme = "dracula"
	cfg.UI.SidebarWidth = 64
	cfg.Boss.Name = "el jefe"
	rc := cfg.Roles["hr"]
	rc.Model = "anthropic/claude-opus-4-1"
	cfg.Roles["hr"] = rc

	if err := Save(cfg); err != nil {
		t.Fatalf("Save(): %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save(): %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("Save→Load round trip = %+v, want %+v", got, cfg)
	}

	// save() serializes with two-space indent and a trailing newline.
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Errorf("saved file does not end with newline")
	}
	if !strings.Contains(string(b), "\n  \"version\": 1") {
		t.Errorf("saved file is not two-space-indented JSON; head: %.80q", string(b))
	}
}

func TestLoadSave_DropsUnknownFields(t *testing.T) {
	home := useHome(t)
	p := writeBrain(t, home, `{
  "ui": {"theme": "nord"},
  "mysteryField": {"x": 1},
  "boss": {"name": "chief", "mysteryBoss": true}
}`)

	// Unknown keys are tolerated on load (no error, ignored).
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with unknown fields: %v", err)
	}
	if cfg.UI.Theme != "nord" || cfg.Boss.Name != "chief" {
		t.Errorf("Load() = %+v, known fields should survive alongside unknown ones", cfg)
	}

	// Real contract: they are NOT preserved by a writeback — Save marshals
	// only the typed struct, so unknown keys vanish from the file.
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	for _, gone := range []string{"mysteryField", "mysteryBoss"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("saved file still contains unknown key %q", gone)
		}
	}
	if !strings.Contains(string(b), `"theme": "nord"`) {
		t.Errorf("saved file lost known field theme=nord")
	}
}

// TestLoad_BackendModels — the backend.bossModel / backend.ctoModel knobs:
// they load from a fixture, backfill empty when absent, keep a malformed
// (slash-less) value verbatim (the serve owns the error), and stay
// additive — Save(Default()) must not introduce either key.
func TestLoad_BackendModels(t *testing.T) {
	home := useHome(t)
	writeBrain(t, home, `{"version": 1, "backend": {"bossModel": "anthropic/claude-sonnet-4", "ctoModel": "anthropic/claude-haiku-4-5"}}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.Backend.BossModel != "anthropic/claude-sonnet-4" || cfg.Backend.CTOModel != "anthropic/claude-haiku-4-5" {
		t.Errorf("Backend = %+v, want bossModel anthropic/claude-sonnet-4 / ctoModel anthropic/claude-haiku-4-5", cfg.Backend)
	}

	// Backfill: a brain.json without the keys keeps the empty defaults
	// (the server-side default model decides — zero wire diff).
	home = useHome(t)
	writeBrain(t, home, `{"version": 1, "backend": {"agentmemoryUrl": "http://am:9"}}`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() backfill fixture: %v", err)
	}
	if cfg.Backend.BossModel != "" || cfg.Backend.CTOModel != "" {
		t.Errorf("Backend = %+v, want empty model knobs when the file lacks them", cfg.Backend)
	}

	// Validation-lite: a slash-less (malformed) value is KEPT verbatim.
	home = useHome(t)
	writeBrain(t, home, `{"version": 1, "backend": {"bossModel": "claude-sonnet-4"}}`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load() malformed model: %v", err)
	}
	if cfg.Backend.BossModel != "claude-sonnet-4" {
		t.Errorf("BossModel = %q, want the malformed value kept verbatim", cfg.Backend.BossModel)
	}

	// Zero diff: Save(Default()) must not introduce either key (omitempty).
	useHome(t)
	if err := Save(Default()); err != nil {
		t.Fatalf("Save(default): %v", err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("read back default save: %v", err)
	}
	for _, key := range []string{"bossModel", "ctoModel"} {
		if strings.Contains(string(b), key) {
			t.Errorf("default brain.json must not mention %q (omitempty zero diff)", key)
		}
	}

	// And a configured pair survives the Save round trip.
	cfg = Default()
	cfg.Backend.BossModel = "anthropic/claude-sonnet-4"
	cfg.Backend.CTOModel = "anthropic/claude-haiku-4-5"
	if err := Save(cfg); err != nil {
		t.Fatalf("Save(configured): %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load() saved pair: %v", err)
	}
	if got.Backend.BossModel != cfg.Backend.BossModel || got.Backend.CTOModel != cfg.Backend.CTOModel {
		t.Errorf("round trip = %+v, want bossModel+ctoModel preserved", got.Backend)
	}
}

func TestLoad_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
	home := useHome(t)
	p := writeBrain(t, home, `{"version": 1}`)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load() on unreadable file: want error, got cfg=%+v", cfg)
	}
	if !strings.Contains(err.Error(), "config: read") {
		t.Errorf("error = %q, want prefix \"config: read\"", err.Error())
	}
}
