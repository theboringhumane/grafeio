// cfg_test.go — unit proof for brain.json-driven backend wiring: boss
// session titles and roster name prefixes follow cfg.Roles/cfg.Boss.
package backend

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/config"
)

// TestBossSessionTitles: fresh-create title is cfg.Boss.Name exactly;
// the respawn title strips a trailing "(…)" descriptor ("boss · respawn"
// reads like a title; "boss (oikonomos) · respawn" does not). A blank
// Name keeps the historic "theboringoffice office" so hand-rolled configs cannot
// break the floor.
func TestBossSessionTitles(t *testing.T) {
	b := newLiveBackend("", "/tmp", config.Default())
	if got := b.bossName(); got != "boss (oikonomos)" {
		t.Fatalf("default bossName: want %q, got %q", "boss (oikonomos)", got)
	}
	if got := b.bossNameShort(); got != "boss" {
		t.Fatalf("default bossNameShort: want %q, got %q", "boss", got)
	}

	cfg := config.Default()
	cfg.Boss.Name = "kostas"
	b = newLiveBackend("", "/tmp", cfg)
	if got := b.bossName(); got != "kostas" {
		t.Fatalf("custom bossName: want %q, got %q", "kostas", got)
	}
	if got := b.bossNameShort(); got != "kostas" {
		t.Fatalf("custom bossNameShort (no parenthetical): want %q, got %q", "kostas", got)
	}

	cfg = config.Default()
	cfg.Boss.Name = ""
	b = newLiveBackend("", "/tmp", cfg)
	if got := b.bossName(); got != "theboringoffice office" {
		t.Fatalf("blank-name fallback: want %q, got %q", "theboringoffice office", got)
	}
}

// TestRosterNamingOverride: cfg.Roles[role].NamePrefix seeds the numbered
// roster names; config.Default() reproduces the historic tekton/skopos/
// dikastes/hemerodromos roster — and routes architecture briefs
// ("architect"/"design"/"review" in the title) to the CTO's desk
// (state.IsArchitectureBrief is the ONE matcher).
func TestRosterNamingOverride(t *testing.T) {
	stock := newNormCtx(config.Default())
	cases := []struct {
		title string
		want  string
	}{
		{"write the reducer", "tekton-1"},
		{"explore the repo", "skopos-1"},
		{"review the diff", "theboringcto-1"},  // architecture briefs route to the CTO
		{"dikastes on the gate", "dikastes-1"}, // explicit reviewer title keeps the cabin
		{"runner: ship it", "hemerodromos-1"},
	}
	for _, c := range cases {
		emp := stock.issueEmployee(ocSession{ID: "s-" + c.title, Title: c.title})
		if emp.Name != c.want {
			t.Fatalf("default prefix for %q: want %q, got %q", c.title, c.want, emp.Name)
		}
	}
	// Numbering continues per role.
	emp := stock.issueEmployee(ocSession{ID: "s-2", Title: "write more tests"})
	if emp.Name != "tekton-2" {
		t.Fatalf("numbering: want tekton-2, got %q", emp.Name)
	}

	cfg := config.Default()
	rc := cfg.Roles["developer"]
	rc.NamePrefix = "forge"
	cfg.Roles["developer"] = rc
	custom := newNormCtx(cfg)
	emp = custom.issueEmployee(ocSession{ID: "s-3", Title: "write the reducer"})
	if emp.Name != "forge-1" {
		t.Fatalf("override prefix: want forge-1, got %q", emp.Name)
	}
	// Unnamed roles keep their stock bases.
	emp = custom.issueEmployee(ocSession{ID: "s-4", Title: "explore the repo"})
	if emp.Name != "skopos-1" {
		t.Fatalf("stock scout under partial override: want skopos-1, got %q", emp.Name)
	}
}
