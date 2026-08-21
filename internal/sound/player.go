package sound

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// throttleGap — the same sound played within this window is suppressed, so a
// chatty event burst can't overlap chimes into mush.
const throttleGap = 400 * time.Millisecond

// watchdog — how long an outlived player process may run before we kill it.
const watchdog = 2 * time.Second

// Bus routes office events to audio. Modes:
//
//	"on"   — synthesize lazy wavs and play through the platform player
//	"bell" — print the terminal bell (\a) to stdout, no files needed
//	"off"  — silence
//
// Env override: GRAFEIO_MUTE=1 forces "off" above whatever config asked for.
type Bus struct {
	mode   string
	dir    string
	player string

	mu   sync.Mutex
	last map[string]time.Time
	now  func() time.Time // test seam; defaults to time.Now
}

// ResolvePlayer finds the platform audio player, or "" when none exists.
//   - darwin: afplay
//   - linux:  paplay, then aplay
//   - other:  none
func ResolvePlayer() string {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"afplay"}
	case "linux":
		candidates = []string{"paplay", "aplay"}
	default:
		return ""
	}
	for _, c := range candidates {
		if p, err := osexec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// NewBus builds a Bus for the given config mode ("on"|"bell"|""|"off") and
// home dir ("" = GRAFEIO_HOME, then $HOME). Wav paths live at
// <home>/.grafeio/sounds/<name>.wav. The player lookup happens once here.
func NewBus(cfgSound, home string) *Bus {
	mode := cfgSound
	if mode == "" {
		mode = "on"
	}
	switch mode {
	case "on", "bell", "off":
	default:
		mode = "off"
	}
	if os.Getenv("GRAFEIO_MUTE") == "1" {
		mode = "off"
	}
	if home == "" {
		if h := os.Getenv("GRAFEIO_HOME"); h != "" {
			home = h
		} else {
			home = os.Getenv("HOME")
		}
	}
	return &Bus{
		mode:   mode,
		dir:    filepath.Join(home, ".grafeio", "sounds"),
		player: ResolvePlayer(),
		last:   make(map[string]time.Time),
		now:    time.Now,
	}
}

// Mode returns the effective mode after env overrides.
func (b *Bus) Mode() string { return b.mode }

// Dir returns the wav cache dir.
func (b *Bus) Dir() string { return b.dir }

// PlayerPath returns the resolved player command path, or "" if none.
func (b *Bus) PlayerPath() string { return b.player }

// throttled reports whether name was played within the throttle window and,
// when not, stamps it. Callers hold the mutex-free contract via b.mu.
func (b *Bus) throttled(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.last[name]; ok && b.now().Sub(t) < throttleGap {
		return true
	}
	b.last[name] = b.now()
	return false
}

// Play fires name without blocking the caller. It never panics and only
// errors on unknown names; all platform failures degrade silently (the office
// should not break because a speaker did).
func (b *Bus) Play(name string) error {
	if !IsValid(name) {
		return fmt.Errorf("sound: unknown sound %q", name)
	}
	switch b.mode {
	case "off":
		return nil
	case "bell":
		if b.throttled(name) {
			return nil
		}
		_, _ = os.Stdout.WriteString("\a")
		return nil
	case "on":
		if b.player == "" {
			return nil // no player: degrade to silence, never fatal
		}
		if b.throttled(name) {
			return nil
		}
		path, err := EnsureWav(b.dir, name)
		if err != nil {
			return nil // silent degrade: missing/readonly dir is not fatal
		}
		b.spawn(path)
		return nil
	}
	return nil
}

// spawn launches the player without waiting and arms a watchdog so a hung
// player never leaks past `watchdog`. Callers hold no locks.
func (b *Bus) spawn(path string) {
	cmd := osexec.Command(b.player, path)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(watchdog):
			_ = cmd.Process.Kill()
			<-done
		}
	}()
}
