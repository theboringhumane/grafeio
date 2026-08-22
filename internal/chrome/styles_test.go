// styles_test.go — the device light/dark theme contract:
//   - every registry theme's Dark flag agrees with the luminance of its
//     background (BarBg — the registry's only explicit surface slot);
//   - SetThemeAuto follows the terminal background while unpinned and never
//     overrides — or persists over — an explicit pin.
package chrome

import (
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// bgLuminance computes WCAG relative luminance of a registry color. ANSI
// index colors (noir's entry) resolve through lipgloss/x-ansi's xterm
// palette inside RGBA(); hex colors carry their own RGB.
func bgLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	lin := func(v uint32) float64 {
		x := float64(v) / 65535
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// TestThemeClassificationMatchesBackground asserts the Dark flag of every
// registry theme matches the luminance of its BarBg background slot.
func TestThemeClassificationMatchesBackground(t *testing.T) {
	for _, th := range themeList {
		lum := bgLuminance(th.BarBg)
		if got, want := th.Dark, lum < 0.5; got != want {
			t.Errorf("theme %q: Dark=%v but BarBg luminance %.3f says dark=%v", th.Name, got, lum, want)
		} else {
			t.Logf("theme %-10s BarBg luminance %.3f → dark=%v", th.Name, lum, th.Dark)
		}
	}
}

// TestAutoDefaultsMatchClassification pins the auto defaults to real
// registry entries with the right classification — a bad constant would
// silently invert the device light/dark mapping.
func TestAutoDefaultsMatchClassification(t *testing.T) {
	if !themes[DefaultDarkTheme].Dark {
		t.Errorf("DefaultDarkTheme %q is not classified dark", DefaultDarkTheme)
	}
	if themes[DefaultLightTheme].Dark {
		t.Errorf("DefaultLightTheme %q is not classified light", DefaultLightTheme)
	}
}

// restoreTheme resets the global theme/pin latch so other tests in the
// package (statusbar/topbar render against the active theme) see the boot
// state regardless of run order.
func restoreTheme() {
	current = themes[DefaultDarkTheme]
	applyTheme(current)
	pinned = false
}

// TestSetThemeAutoUnpinnedFollowsBackground: with no pin, a dark terminal
// background selects noir and a light one paper.
func TestSetThemeAutoUnpinnedFollowsBackground(t *testing.T) {
	pinned = false
	defer restoreTheme()

	if got := SetThemeAuto(true); got != DefaultDarkTheme {
		t.Fatalf("SetThemeAuto(dark) = %q, want %q", got, DefaultDarkTheme)
	}
	if CurrentTheme().Name != DefaultDarkTheme || !CurrentTheme().Dark {
		t.Errorf("after dark bg: current = %q (Dark=%v)", CurrentTheme().Name, CurrentTheme().Dark)
	}
	if got := SetThemeAuto(false); got != DefaultLightTheme {
		t.Fatalf("SetThemeAuto(light) = %q, want %q", got, DefaultLightTheme)
	}
	if CurrentTheme().Name != DefaultLightTheme || CurrentTheme().Dark {
		t.Errorf("after light bg: current = %q (Dark=%v)", CurrentTheme().Name, CurrentTheme().Dark)
	}
}

// TestPinnedThemeSurvivesSetThemeAuto: an explicit SetTheme latches the pin
// and background-driven switches become no-ops in both directions.
func TestPinnedThemeSurvivesSetThemeAuto(t *testing.T) {
	pinned = false
	defer restoreTheme()

	if !SetTheme("dracula") {
		t.Fatal("SetTheme(dracula) returned false")
	}
	if !ThemePinned() {
		t.Fatal("explicit SetTheme did not latch the pin")
	}
	for _, dark := range []bool{true, false} {
		if got := SetThemeAuto(dark); got != "" {
			t.Errorf("SetThemeAuto(%v) while pinned = %q, want no-op \"\"", dark, got)
		}
		if CurrentTheme().Name != "dracula" {
			t.Errorf("pinned theme moved by SetThemeAuto(%v): %q", dark, CurrentTheme().Name)
		}
	}
}

// TestSetThemeAutoNeverPersists: auto flips must not write the persisted
// theme file — tomorrow's boot re-detects from the device.
func TestSetThemeAutoNeverPersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pinned = false
	defer restoreTheme()

	SetThemeAuto(true)
	SetThemeAuto(false)
	if _, err := os.Stat(ThemeConfigPath()); !os.IsNotExist(err) {
		t.Errorf("auto picks wrote %s (err=%v) — auto must never persist", ThemeConfigPath(), err)
	}
	if got := LoadPersistedTheme(); got != "" {
		t.Errorf("LoadPersistedTheme = %q after auto picks, want \"\"", got)
	}
}

// TestLoadPersistedTheme_LegacyFallback pins the rename-era contract: with
// nothing at the new theme path, a pre-rename ~/.config/grafeio/theme is
// READ (never written); once a pin lands the new path wins.
func TestLoadPersistedTheme_LegacyFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	pinned = false
	defer restoreTheme()

	if got := LoadPersistedTheme(); got != "" {
		t.Fatalf("no files anywhere: want \"\", got %q", got)
	}

	legacy := legacyThemeConfigPath()
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(legacy, []byte("dracula\n"), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if got := LoadPersistedTheme(); got != "dracula" {
		t.Fatalf("legacy fallback: want dracula, got %q", got)
	}

	// A persist lands ONLY on the new path; the legacy file stays as found.
	if !SetTheme("paper") {
		t.Fatal("SetTheme(paper) returned false")
	}
	if err := PersistTheme(); err != nil {
		t.Fatalf("PersistTheme: %v", err)
	}
	b, err := os.ReadFile(legacy)
	if err != nil || string(b) != "dracula\n" {
		t.Errorf("legacy file must stay untouched after PersistTheme, got %q (err=%v)", b, err)
	}
	if got := LoadPersistedTheme(); got != "paper" {
		t.Fatalf("new path wins once present: want paper, got %q", got)
	}
}
