// charter.go — bundles the oikonomos manager charter into whatever
// directory theboringoffice's live backend serves. On Start (live only) it:
//
//  1. writes the embedded charter text (internal/charter) byte-exact to
//     <dir>/.opencode/oikonomos.md, and
//  2. merges "./.opencode/oikonomos.md" into
//     <dir>/.opencode/opencode.json's "instructions" array. VERIFIED
//     against serve 1.18.19: opencode resolves relative instructions
//     entries against the PROJECT directory (globUp from the served dir
//     up to the worktree), NOT the config file's own directory — so the
//     entry must name .opencode/ explicitly. Creates the config with
//     {"instructions":["./.opencode/oikonomos.md"]} when absent and
//     surgically adds the entry when present.
//
// Hard guarantees: AGENTS.md / CLAUDE.md and every other opencode.json
// field are never touched; a second run is byte-identical (changed=false,
// "already wired"); THEBORINGOFFICE_NO_AUTOCHARTER=1 (pre-rename:
// GRAFEIO_NO_AUTOCHARTER=1) skips the whole pass. A fresh
// charter or a newly-added instructions entry reports changed=true so a
// serve started earlier can be found stale and respawned (opencode spoils
// its config at start — a running serve may not pick up a config it
// ignored on boot).
package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/theboringhumane/theboringoffice/internal/charter"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// charterRelPath is the instructions entry as it appears in
// .opencode/opencode.json. Verified against opencode serve 1.18.19: the
// consumer resolves relative entries against the PROJECT directory
// (globUp from the served dir toward the worktree root), not the config
// file's directory — hence the explicit ".opencode/" prefix.
const charterRelPath = "./.opencode/oikonomos.md"

// charterAcceptedPaths are all spellings EnsureCharter treats as "already
// wired" — a member hand-writing any of these must not get a duplicate.
var charterAcceptedPaths = []string{
	charterRelPath,
	".opencode/oikonomos.md",
	"./oikonomos.md",
	"oikonomos.md",
}

// EnsureCharter wires the bundled oikonomos manager charter into dir so a
// spawned opencode serve reads it. changed is true when any byte was
// written (callers that already spawned a serve should restart it; see the
// header comment). notes are status-line text describing what happened.
// The pass is idempotent: a follow-up run with everything already in place
// returns changed=false and writes nothing. THEBORINGOFFICE_NO_AUTOCHARTER=1
// opts out entirely (changed=false, note explaining); the pre-rename
// GRAFEIO_NO_AUTOCHARTER=1 is honored as a fallback.
func EnsureCharter(dir string) (changed bool, notes []string) {
	if envOrLegacy("THEBORINGOFFICE_NO_AUTOCHARTER", "GRAFEIO_NO_AUTOCHARTER") == "1" {
		return false, []string{"[theboringoffice] manager charter: disabled (THEBORINGOFFICE_NO_AUTOCHARTER)"}
	}

	ocDir := filepath.Join(dir, ".opencode")
	chartPath := filepath.Join(ocDir, "oikonomos.md")
	cfgPath := filepath.Join(ocDir, "opencode.json")

	// 1. The charter markdown: write byte-exact, skip when identical.
	want := []byte(charter.Text)
	if got, err := os.ReadFile(chartPath); err != nil || !bytes.Equal(got, want) {
		if err := os.MkdirAll(ocDir, 0o755); err != nil {
			return changed, append(notes, "[theboringoffice] manager charter: failed (mkdir "+ocDir+": "+err.Error()+")")
		}
		if err := os.WriteFile(chartPath, want, 0o644); err != nil {
			return changed, append(notes, "[theboringoffice] manager charter: failed (write "+chartPath+": "+err.Error()+")")
		}
		changed = true
	}

	// 2. The config merge: read opencode.json when present, ensure the
	//    instructions array contains the charter entry exactly once,
	//    preserving every other key verbatim (map[string]any round-trip,
	//    2-space indent; see mergeInstructions for field preservation).
	cfgRaw, err := os.ReadFile(cfgPath)
	if err == nil {
		merged, mergeChanged, mergeErr := mergeInstructions(cfgRaw)
		if mergeErr != nil {
			return changed, append(notes, "[theboringoffice] manager charter: failed (merge "+cfgPath+": "+mergeErr.Error()+")")
		}
		if mergeChanged {
			if err := os.WriteFile(cfgPath, merged, 0o644); err != nil {
				return changed, append(notes, "[theboringoffice] manager charter: failed (write "+cfgPath+": "+err.Error()+")")
			}
			changed = true
		}
	} else if os.IsNotExist(err) {
		fresh := []byte("{\n  \"$schema\": \"https://opencode.ai/config.json\",\n  \"instructions\": [\n    \"" + charterRelPath + "\"\n  ]\n}\n")
		if err := os.WriteFile(cfgPath, fresh, 0o644); err != nil {
			return changed, append(notes, "[theboringoffice] manager charter: failed (write "+cfgPath+": "+err.Error()+")")
		}
		changed = true
	} else {
		return changed, append(notes, "[theboringoffice] manager charter: failed (read "+cfgPath+": "+err.Error()+")")
	}

	if changed {
		return true, append(notes, "[theboringoffice] manager charter: wired (.opencode/oikonomos.md)")
	}
	return false, append(notes, "[theboringoffice] manager charter: already wired (.opencode/oikonomos.md)")
}

// charterOutcome is the two things Start needs out of a charter pass.
type charterOutcome struct {
	changed bool
	notes   []string
}

// emitCharterNotes runs EnsureCharter for dir and surfaces every note as a
// status event on emit. Called exactly once from liveBackend.Start, BEFORE
// server resolution: a spawned serve then reads a config that already has
// the charter entry. The demo backend never calls this.
func emitCharterNotes(emit func(state.Event), dir string) charterOutcome {
	changed, notes := EnsureCharter(dir)
	out := charterOutcome{changed: changed, notes: notes}
	for _, n := range notes {
		emit(state.Event{Kind: state.EvStatus, Text: n})
	}
	return out
}

// mergeInstructions is the pure half of the config merge: cfg is an: cfg is an
// existing opencode.json. It returns cfg unchanged (changed=false) when the
// instructions array already names the charter by any accepted spelling.
// Otherwise it appends the canonical charterRelPath entry and re-marshals
// with 2-space indent. Every foreign field survives (map[string]any round-trip); a
// non-object instruction entry (a number, say) is left alone and the
// charter is appended alongside it. A non-object top level, an explicit
// JSON null instructions, or an instructions value that is not an array all
// fail closed: the member's hand-rolled shape is never clobbered.
func mergeInstructions(cfg []byte) (merged []byte, changed bool, err error) {
	var doc map[string]any
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return nil, false, fmt.Errorf("unparseable json: %w", err)
	}

	rawInstr, has := doc["instructions"]
	if has {
		if rawInstr == nil {
			return nil, false, fmt.Errorf("instructions is null — refusing to rewrite a hand-shaped config")
		}
		arr, ok := rawInstr.([]any)
		if !ok {
			return nil, false, fmt.Errorf("instructions is %T, not an array — refusing to rewrite a hand-shaped config", rawInstr)
		}
		for _, item := range arr {
			s, ok := item.(string)
			if !ok {
				continue
			}
			for _, accepted := range charterAcceptedPaths {
				if s == accepted {
					return cfg, false, nil // already wired
				}
			}
		}
		arr = append(arr, charterRelPath)
		doc["instructions"] = arr
	} else {
		doc["instructions"] = []any{charterRelPath}
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(out, '\n'), true, nil
}

// envOrLegacy reads the THEBORINGOFFICE_* env var, falling back to the
// pre-rename GRAFEIO_* name (whole-product rename: grafeio ->
// theboringoffice; old dotfiles and CI exports keep working).
func envOrLegacy(key, legacyKey string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return os.Getenv(legacyKey)
}
