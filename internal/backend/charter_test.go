package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theboringhumane/grafeio/internal/charter"
)

// charterProbePhrases is the live-probe subset contract: the charter must
// name the manager role and the 3-dispatch minimum.
func charterProbePhrases() []string {
	return []string{"manager", "oikonomos", "MINIMUM 3", "Proof-of-work"}
}

func TestCharterAssetSelfCheck(t *testing.T) {
	if !charter.ContainsPhrases(charterProbePhrases()) {
		t.Fatalf("embedded charter failed its own subset probe: missing one of %v", charterProbePhrases())
	}
	want := "MINIMUM 3 sub-agents"
	if !strings.Contains(charter.String(), want) {
		t.Fatalf("embedded charter does not contain %q", want)
	}
	if strings.Contains(charter.String(), "\r") {
		t.Fatal("embedded charter contains CR bytes — expected LF-only")
	}
}

func TestEnsureCharterCreatesFresh(t *testing.T) {
	dir := t.TempDir()
	changed, notes := EnsureCharter(dir)
	if !changed {
		t.Fatal("fresh dir: changed=false, want true")
	}
	if !containsNote(notes, "manager charter: wired") {
		t.Fatalf("notes missing the wired line: %v", notes)
	}
	chartRaw, err := os.ReadFile(filepath.Join(dir, ".opencode", "oikonomos.md"))
	if err != nil {
		t.Fatalf("chart file: %v", err)
	}
	if string(chartRaw) != charter.Text {
		t.Fatal("chart file is not byte-exact the embedded charter")
	}
	cfgRaw, err := os.ReadFile(filepath.Join(dir, ".opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("config file: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("config is not json: %v", err)
	}
	if cfg["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("fresh config's $schema: %v", cfg["$schema"])
	}
	assertHasCharterInstructions(t, cfg)
}

func TestEnsureCharterIdempotentByteIdentical(t *testing.T) {
	dir := t.TempDir()
	if _, notes := EnsureCharter(dir); !containsNote(notes, "manager charter: wired") {
		t.Fatalf("first run: want wired note, got %v", notes)
	}
	chart1 := mustRead(t, filepath.Join(dir, ".opencode", "oikonomos.md"))
	cfg1 := mustRead(t, filepath.Join(dir, ".opencode", "opencode.json"))

	changed, notes := EnsureCharter(dir)
	if changed {
		t.Fatal("second run: changed=true, want false (byte-idempotency)")
	}
	if !containsNote(notes, "already") {
		t.Fatalf("second run notes missing an already-wired line: %v", notes)
	}
	if got := mustRead(t, filepath.Join(dir, ".opencode", "oikonomos.md")); got != chart1 {
		t.Fatal("second run rewrote oikonomos.md")
	}
	if got := mustRead(t, filepath.Join(dir, ".opencode", "opencode.json")); got != cfg1 {
		t.Fatal("second run rewrote opencode.json")
	}
}

func TestEnsureCharterMergePreservesForeignFields(t *testing.T) {
	dir := t.TempDir()
	ocDir := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prior := `{
    "theme": "nord",
    "model": "anthropic/claude-haiku-4-5",
    "instructions": ["docs/CODE_STYLE.md"],
    "nested": {"deep": {"list": [1, 2, 3]}, "flag": true}
}
`
	cfgPath := filepath.Join(ocDir, "opencode.json")
	if err := os.WriteFile(cfgPath, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, notes := EnsureCharter(dir)
	if !changed {
		t.Fatal("existing config without the entry: changed=false, want true")
	}
	if !containsNote(notes, "manager charter: wired") {
		t.Fatalf("notes: %v", notes)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, cfgPath)), &cfg); err != nil {
		t.Fatalf("merged config is not json: %v", err)
	}
	// Foreign fields survive, values intact.
	if cfg["theme"] != "nord" {
		t.Fatalf("theme dropped/changed: %v", cfg["theme"])
	}
	if cfg["model"] != "anthropic/claude-haiku-4-5" {
		t.Fatalf("model dropped/changed: %v", cfg["model"])
	}
	nested, ok := cfg["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested dropped: %v", cfg["nested"])
	}
	if nested["deep"].(map[string]any)["list"].([]any)[2].(float64) != 3 {
		t.Fatalf("nested.deep.list mutated: %v", nested)
	}
	if nested["flag"] != true {
		t.Fatalf("nested.flag mutated: %v", nested)
	}
	// The pre-existing instruction entry survives AND the charter lands once.
	instr := cfg["instructions"].([]any)
	if len(instr) != 2 || instr[0] != "docs/CODE_STYLE.md" || instr[1] != charterRelPath {
		t.Fatalf("instructions: %v", instr)
	}

	// A second run must not duplicate the entry.
	if changed, _ := EnsureCharter(dir); changed {
		t.Fatal("second run re-wrote the merged config")
	}
	var cfg2 map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, cfgPath)), &cfg2); err != nil {
		t.Fatal(err)
	}
	if len(cfg2["instructions"].([]any)) != 2 {
		t.Fatalf("second run duplicated the charter entry: %v", cfg2["instructions"])
	}
}

func TestEnsureCharterAcceptedSpellings(t *testing.T) {
	for _, spelling := range charterAcceptedPaths {
		dir := t.TempDir()
		ocDir := filepath.Join(dir, ".opencode")
		if err := os.MkdirAll(ocDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(ocDir, "opencode.json")
		body := `{"instructions":["` + spelling + `"],"x":1}`
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		// First run may rewrite the chart markdown (EnsureCharter creates it
		// if missing); pre-create it so the spelling check is isolated to
		// the config merge.
		if err := os.WriteFile(filepath.Join(ocDir, "oikonomos.md"), []byte(charter.Text), 0o644); err != nil {
			t.Fatal(err)
		}
		changed, _ := EnsureCharter(dir)
		if changed {
			t.Fatalf("spelling %q: changed=true, want false (accepted as wired)", spelling)
		}
		if got := mustRead(t, cfgPath); got != body {
			t.Fatalf("spelling %q: config rewritten: %s", spelling, got)
		}
	}
}

func TestEnsureCharterEnvOptOut(t *testing.T) {
	t.Setenv("GRAFEIO_NO_AUTOCHARTER", "1")
	dir := t.TempDir()
	changed, notes := EnsureCharter(dir)
	if changed {
		t.Fatal("opt-out: changed=true, want false")
	}
	if !containsNote(notes, "disabled (GRAFEIO_NO_AUTOCHARTER)") {
		t.Fatalf("opt-out notes: %v", notes)
	}
	// Nothing written at all.
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("opt-out wrote files: %v %v", entries, err)
	}
}

func TestEnsureCharterRefusesNullOrNonArrayInstructions(t *testing.T) {
	for _, body := range []string{
		`{"instructions":null,"x":1}`,
		`{"instructions":"docs/*.md","x":1}`,
	} {
		dir := t.TempDir()
		ocDir := filepath.Join(dir, ".opencode")
		if err := os.MkdirAll(ocDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfgPath := filepath.Join(ocDir, "opencode.json")
		if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, notes := EnsureCharter(dir)
		if !containsNote(notes, "manager charter: failed") {
			t.Fatalf("%s: want a failed note, got %v", body, notes)
		}
		if got := mustRead(t, cfgPath); got != body {
			t.Fatalf("%s: config clobbered: %s", body, got)
		}
	}
}

func TestEnsureCharterRefusesBrokenJson(t *testing.T) {
	dir := t.TempDir()
	ocDir := filepath.Join(dir, ".opencode")
	if err := os.MkdirAll(ocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(ocDir, "opencode.json")
	broken := `{"instructions": [`
	if err := os.WriteFile(cfgPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	_, notes := EnsureCharter(dir)
	if !containsNote(notes, "manager charter: failed") {
		t.Fatalf("broken json: want a failed note, got %v", notes)
	}
	if got := mustRead(t, cfgPath); got != broken {
		t.Fatalf("broken json clobbered: %s", got)
	}
}

func TestEnsureCharterNeverTouchesAgentsMd(t *testing.T) {
	dir := t.TempDir()
	agents := "# member repo rules\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, notes := EnsureCharter(dir); !containsNote(notes, "manager charter: wired") {
		t.Fatalf("notes: %v", notes)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md")); err != nil || string(got) != agents {
		t.Fatalf("AGENTS.md touched: %q %v", got, err)
	}
}

func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func assertHasCharterInstructions(t *testing.T, cfg map[string]any) {
	t.Helper()
	instr, ok := cfg["instructions"].([]any)
	if !ok {
		t.Fatalf("instructions missing or not an array: %v", cfg["instructions"])
	}
	found := 0
	for _, x := range instr {
		if x == charterRelPath {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("instructions %v: want exactly one %q, got %d", instr, charterRelPath, found)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
