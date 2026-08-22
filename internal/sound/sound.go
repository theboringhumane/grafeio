// Package sound — terminal-native office audio, zero deps.
//
// Eight short PCM chimes are synthesized in pure Go (16-bit mono 22050 Hz
// WAV) once into ~/.theboringoffice/sounds/ and played through the platform player
// (afplay on darwin, paplay/aplay on linux) — fire-and-forget, so the office
// loop never blocks on audio. Config mode "on" plays waves, "bell" emits the
// terminal bell, "off" is silent; THEBORINGOFFICE_MUTE=1 (pre-rename:
// GRAFEIO_MUTE=1) overrides to silent.
package sound

// Names returns the known sound names in stable UI order.
func Names() []string {
	return []string{"queued", "send", "reply", "done", "dispatch", "alert", "error"}
}

// IsValid reports whether name is a known sound.
func IsValid(name string) bool {
	for _, n := range Names() {
		if n == name {
			return true
		}
	}
	return false
}
