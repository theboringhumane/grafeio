// Package charter bundles the "oikonomos" office-manager protocol inside
// grafeio: the charter markdown ships as an embedded asset so a spawned
// opencode serve can be wired to the manager-orchestration intelligence
// without the user installing anything else.
package charter

import (
	_ "embed"
	"strings"
)

// Text is the bundled office charter, byte-exact from charter.md. Written
// by the live backend into <workingdir>/.opencode/oikonomos.md and
// referenced from .opencode/opencode.json's instructions array.
//
//go:embed charter.md
var Text string

// String returns the bundled charter text (convenience for callers that
// prefer a func). Identical bytes to Text.
func String() string { return Text }

// ContainsPhrases is a cheap subset probe: reports whether the charter
// contains every given phrase (case-insensitive substring match). Used by
// the headless charter-probe to spot-check the embedded asset and the live
// boss's grounding in it — a full SHA is overkill, a vibe check is the
// contract.
func ContainsPhrases(phrases []string) bool {
	lower := strings.ToLower(Text)
	for _, p := range phrases {
		if !strings.Contains(lower, strings.ToLower(p)) {
			return false
		}
	}
	return true
}
