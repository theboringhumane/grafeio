package charter

import (
	"strings"
	"testing"
)

func TestString_ReturnsEmbeddedCharter(t *testing.T) {
	got := String()

	// Doc contract: identical bytes to the embedded Text var.
	if got != Text {
		t.Errorf("String() differs from Text: len(String)=%d, len(Text)=%d", len(got), len(Text))
	}
	if got == "" {
		t.Fatal("String() is empty; embed should have pulled charter.md in")
	}

	// Shape of the bundled asset: markdown with the charter H1 and its
	// section headings, anchored on the current charter.md content.
	if !strings.HasPrefix(got, "# Office Charter") {
		t.Errorf("charter should start with \"# Office Charter\", starts with %.40q", got)
	}
	for _, heading := range []string{
		"## Dispatch ladder (MANDATORY)",
		"## Briefing discipline (every dispatch)",
		"## Proof-of-work (every return, no exceptions)",
		"## Critical thinking before dispatch",
		"## After the turn",
	} {
		if !strings.Contains(got, heading) {
			t.Errorf("charter missing section heading %q", heading)
		}
	}
}

func TestContainsPhrases(t *testing.T) {
	cases := []struct {
		name    string
		phrases []string
		want    bool
	}{
		{
			name:    "positive: phrases verbatim from charter.md",
			phrases: []string{"Office Charter", "oikonomos", "Dispatch ladder"},
			want:    true,
		},
		{
			name:    "positive: match is case-insensitive",
			phrases: []string{"OFFICE CHARTER", "OiKoNoMoS", "dIsPaTcH"},
			want:    true,
		},
		{
			// Real contract: substring matching, not word matching — a
			// partial slice of a heading matches.
			name:    "positive: partial phrase is a substring",
			phrases: []string{"patch ladd", "ork (every ret"},
			want:    true,
		},
		{
			name:    "positive: multi-word phrase across a sentence",
			phrases: []string{"Proof-of-work (every return, no exceptions)"},
			want:    true,
		},
		{
			name:    "negative: absent phrase",
			phrases: []string{"quantum fermentation"},
			want:    false,
		},
		{
			name:    "negative: one present, one absent",
			phrases: []string{"oikonomos", "ferrari"},
			want:    false,
		},
		{
			name:    "negative: casing of absent phrase is irrelevant",
			phrases: []string{"FERRARI"},
			want:    false,
		},
		{
			// Real contract: empty input vacuously reports true — the
			// loop never finds a missing phrase.
			name:    "vacuous: nil phrases",
			phrases: nil,
			want:    true,
		},
		{
			name:    "vacuous: empty slice",
			phrases: []string{},
			want:    true,
		},
		{
			// Real contract: strings.Contains(x, "") is always true, so
			// an empty phrase matches everything.
			name:    "vacuous: empty-string phrase",
			phrases: []string{""},
			want:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainsPhrases(tc.phrases); got != tc.want {
				t.Errorf("ContainsPhrases(%q) = %v, want %v", tc.phrases, got, tc.want)
			}
		})
	}
}
