// backoff_test.go — unit proof for the agentmemory poll backoff (cfg-driven
// efficiency knob). Pure: no server, no timers — the same BackoffInterval
// helper pollLoop calls is driven directly.
package backend

import (
	"testing"
	"time"
)

// TestBackoffIntervalGrowth: base 5s doubles after every 5 consecutive
// no-change syncs (5s -> 10s -> 20s) and never exceeds 4x base; a change
// (the caller resetting to base) snaps the cadence straight back.
func TestBackoffIntervalGrowth(t *testing.T) {
	base := 5 * time.Second
	max := 20 * time.Second // 4x base
	interval := base

	// Syncs 1-4 unchanged: cadence holds at base.
	for n := 1; n <= 4; n++ {
		if got := BackoffInterval(base, interval, n); got != interval {
			t.Fatalf("noChange=%d: interval grew early: %s -> %s", n, interval, got)
		}
		interval = BackoffInterval(base, interval, n)
	}
	// Sync 5 unchanged: first doubling.
	if got := BackoffInterval(base, interval, 5); got != 10*time.Second {
		t.Fatalf("noChange=5: want 10s, got %s", got)
	}
	interval = BackoffInterval(base, interval, 5)

	// Syncs 6-9 hold at 10s.
	for n := 6; n <= 9; n++ {
		if got := BackoffInterval(base, interval, n); got != 10*time.Second {
			t.Fatalf("noChange=%d: want steady 10s, got %s", n, got)
		}
	}
	// Sync 10: second doubling, lands exactly on the 4x cap.
	if got := BackoffInterval(base, interval, 10); got != max {
		t.Fatalf("noChange=10: want %s (cap), got %s", max, got)
	}
	interval = BackoffInterval(base, interval, 10)

	// Syncs 15/20: never past the cap.
	for n := 11; n <= 20; n++ {
		if got := BackoffInterval(base, interval, n); got != max {
			t.Fatalf("noChange=%d: cap breached: %s", n, got)
		}
		interval = max
	}

	// Observed change is the caller's base-reset; the helper itself must not
	// shorten (documented contract — pollLoop owns the reset branch).
	if got := BackoffInterval(base, max, 21); got != max {
		t.Fatalf("no-change helper shortened interval: %s -> %s", max, got)
	}
}

// TestBackoffIntervalEdge: zero/negative bases fall back to the historic 5s;
// sub-loop counters (1-4) never grow the interval.
func TestBackoffIntervalEdge(t *testing.T) {
	got := BackoffInterval(0, 5*time.Second, 5)
	if got != 10*time.Second {
		t.Fatalf("base=0: want fallback 5s -> 10s growth, got %s", got)
	}
	got = BackoffInterval(-time.Second, 5*time.Second, 5)
	if got != 10*time.Second {
		t.Fatalf("base=-1s: want fallback 5s -> 10s growth, got %s", got)
	}
	if got := BackoffInterval(5*time.Second, 5*time.Second, 0); got != 5*time.Second {
		t.Fatalf("noChange=0 must hold at base, got %s", got)
	}
}

// TestAMPollBase: cfg.Backend.AgentmemoryPollS clamps — 0/negative -> 5s
// (historic default), under a second clamps to 1s.
func TestAMPollBase(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{0, 5 * time.Second},
		{-3, 5 * time.Second},
		{5, 5 * time.Second},
		{1, 1 * time.Second},
		{30, 30 * time.Second},
	}
	for _, c := range cases {
		if got := amPollBase(c.in); got != c.want {
			t.Fatalf("amPollBase(%d): want %s, got %s", c.in, c.want, got)
		}
	}
}

// TestSplitModelRef: "provider/model" parses for the prompt_async override;
// anything else is ignored (override skipped).
func TestSplitModelRef(t *testing.T) {
	p, m := splitModelRef("anthropic/claude-sonnet-4-5")
	if p != "anthropic" || m != "claude-sonnet-4-5" {
		t.Fatalf("want anthropic/claude-sonnet-4-5, got %s/%s", p, m)
	}
	p, m = splitModelRef("openai/gpt-5/mini")
	if p != "openai" || m != "gpt-5/mini" {
		t.Fatalf("want openai/gpt-5/mini, got %s/%s", p, m)
	}
	for _, bad := range []string{"", "nogid", "/onlymodel", "onlyprovider/", "  "} {
		if p, m := splitModelRef(bad); p != "" || m != "" {
			t.Fatalf("splitModelRef(%q) must parse empty, got %q/%q", bad, p, m)
		}
	}
}
