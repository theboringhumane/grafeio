package version

import "testing"

// String renders the dev default and every stamped shape a release can hand us.
func TestString(t *testing.T) {
	defer func() { Version, Commit, Date = "dev", "unknown", "unknown" }() // restore for other tests
	tests := []struct {
		name                  string
		version, commit, date string
		want                  string
	}{
		{"dev default", "dev", "unknown", "unknown", "theboringoffice dev"},
		{"bare semver", "0.1.0", "unknown", "unknown", "theboringoffice v0.1.0"},
		{"v-prefixed semver", "v0.1.0", "unknown", "unknown", "theboringoffice v0.1.0"},
		{"stamped with metadata", "0.1.0", "abc1234", "2026-08-22", "theboringoffice v0.1.0 (abc1234, 2026-08-22)"},
		{"partial stamp: commit only", "v0.1.0", "abc1234", "unknown", "theboringoffice v0.1.0 (abc1234)"},
		{"partial stamp: date only", "v0.1.0", "unknown", "2026-08-22", "theboringoffice v0.1.0 (2026-08-22)"},
	}
	for _, tc := range tests {
		Version, Commit, Date = tc.version, tc.commit, tc.date
		if got := String(); got != tc.want {
			t.Errorf("%s: String() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
