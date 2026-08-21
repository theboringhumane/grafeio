package sound

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
)

// sampleRate — one shared rate for every chime (CD-quality-ish, cheap).
const sampleRate = 22050

// msPerShape is the nominal duration of each sound, used by the tests to
// validate the written WAV data length. alert = 55ms beep + 30ms gap + 55ms
// beep.
var msPerShape = map[string]int{
	"queued":   40,
	"send":     60,
	"reply":    90,
	"done":     110,
	"dispatch": 80,
	"alert":    140,
	"error":    140,
}

// decibels converts a dB gain to a linear amplitude factor.
func decibels(db float64) float64 {
	return math.Pow(10, db/20)
}

// fadeOut applies a linear fade-out over the final pct of samples.
func fadeOut(s []int16, pct float64) {
	n := len(s)
	edge := int(float64(n) * pct)
	if edge < 1 {
		edge = 1
	}
	for i := n - edge; i < n; i++ {
		gain := float64(n-1-i) / float64(edge)
		s[i] = int16(float64(s[i]) * gain)
	}
}

// fadeEdges applies a linear fade-in/fade-out over the first/last pct.
func fadeEdges(s []int16, pct float64) {
	n := len(s)
	edge := int(float64(n) * pct)
	if edge < 1 {
		edge = 1
	}
	for i := 0; i < edge; i++ {
		gain := float64(i) / float64(edge)
		s[i] = int16(float64(s[i]) * gain)
	}
	fadeOut(s, pct)
}

// tone renders ms of a sine at freq Hz at the given amplitude into w.
// phase carries the oscillator phase forward so successive tones don't click.
func tone(w []float64, freq, amp float64, phase *float64) {
	step := 2 * math.Pi * freq / sampleRate
	for i := range w {
		w[i] = amp * math.Sin(*phase)
		*phase += step
	}
	*phase = math.Mod(*phase, 2*math.Pi)
}

// glide renders ms of a sine whose frequency sweeps f0 -> f1 linearly.
func glide(w []float64, f0, f1, amp float64, phase *float64) {
	n := len(w)
	df := (f1 - f0) / float64(n)
	f := f0
	for i := range w {
		w[i] = amp * math.Sin(*phase)
		*phase += 2 * math.Pi * f / sampleRate
		f += df
	}
	*phase = math.Mod(*phase, 2*math.Pi)
}

// toInt16 converts float64 samples ([-1,1]) to int16, clamping hard.
func toInt16(w []float64) []int16 {
	out := make([]int16, len(w))
	for i, x := range w {
		if x > 1 {
			x = 1
		} else if x < -1 {
			x = -1
		}
		out[i] = int16(x * 32767)
	}
	return out
}

// msToN converts milliseconds to a sample count.
func msToN(ms int) int {
	return ms * sampleRate / 1000
}

// samplesFor renders one sound into 16-bit mono samples at sampleRate.
// Unknown names return nil.
func samplesFor(name string) []int16 {
	switch name {
	case "queued": // 40ms sine 660Hz, -12dB, quick linear fade-out
		s := toInt16(toneFirst(660, decibels(-12), msToN(40)))
		fadeOut(s, 0.5)
		return s

	case "send": // 60ms sine 520->640Hz glide, gentle edges
		w := make([]float64, msToN(60))
		var ph float64
		glide(w, 520, 640, decibels(-14), &ph)
		s := toInt16(w)
		fadeEdges(s, 0.15)
		return s

	case "reply": // 90ms two-tone chime C5 -> G5
		half := msToN(90) / 2
		w1 := make([]float64, half)
		w2 := make([]float64, msToN(90)-half)
		var ph float64
		tone(w1, 523.25, decibels(-11), &ph)
		tone(w2, 783.99, decibels(-11), &ph)
		s := toInt16(append(w1, w2...))
		fadeEdges(s, 0.1)
		return s

	case "done": // 110ms rising triad blip C5 E5 G5, 3 steps
		steps := []float64{523.25, 659.25, 783.99}
		total := msToN(110)
		step := total / 3
		w := make([]float64, 0, total)
		var ph float64
		for _, f := range steps {
			part := make([]float64, step)
			tone(part, f, decibels(-11), &ph)
			w = append(w, part...)
		}
		// pad rounding remainder with the last step's tail
		for len(w) < total {
			w = append(w, 0)
		}
		s := toInt16(w)
		fadeEdges(s, 0.08)
		return s

	case "dispatch": // 80ms brown-noise soft whoosh, lowpassed, fade in/out
		n := msToN(80)
		rng := rand.New(rand.NewPCG(7, 7)) // deterministic so the wav is stable
		raw := make([]float64, n)
		var b float64
		for i := range raw {
			b += 0.04 * (rng.Float64()*2 - 1) // brownian walk
			raw[i] = b
		}
		// one-pole lowpass ~700 Hz
		rc := 1.0 / (2 * math.Pi * 700)
		a := (1.0 / sampleRate) / (rc + 1.0/sampleRate)
		var y float64
		for i, x := range raw {
			y += a * (x - y)
			raw[i] = y
		}
		// normalize to -10 dB peak
		peak := 0.0
		for _, x := range raw {
			if ax := math.Abs(x); ax > peak {
				peak = ax
			}
		}
		if peak > 0 {
			gain := decibels(-10) / peak
			for i := range raw {
				raw[i] *= gain
			}
		}
		s := toInt16(raw)
		fadeEdges(s, 0.45)
		return s

	case "alert": // two 55ms square-ish 880Hz beeps with a 30ms gap
		beep := make([]float64, msToN(55))
		// square approximation: odd harmonics 1, 3, 5
		for i := range beep {
			t := float64(i) / sampleRate
			v := math.Sin(2*math.Pi*880*t) + math.Sin(6*math.Pi*880*t)/3 + math.Sin(10*math.Pi*880*t)/5
			beep[i] = v / (1 + 1.0/3 + 1.0/5) * decibels(-8)
		}
		s := toInt16(beep)
		fadeEdges(s, 0.06)
		gap := make([]int16, msToN(30))
		out := make([]int16, 0, 2*len(s)+len(gap))
		out = append(out, s...)
		out = append(out, gap...)
		out = append(out, s...)
		return out

	case "error": // 140ms descending 440 -> 220Hz glide
		w := make([]float64, msToN(140))
		var ph float64
		glide(w, 440, 220, decibels(-10), &ph)
		s := toInt16(w)
		fadeEdges(s, 0.12)
		return s
	}
	return nil
}

// toneFirst short-hands building a single tone buffer.
func toneFirst(freq, amp float64, n int) []float64 {
	w := make([]float64, n)
	var ph float64
	tone(w, freq, amp, &ph)
	return w
}

// writeWav writes samples as a little-endian 16-bit mono PCM RIFF WAV.
func writeWav(path string, samples []int16) error {
	dataSize := uint32(len(samples) * 2)
	buf := new(bytes.Buffer)
	put := func(v any) error { return binary.Write(buf, binary.LittleEndian, v) }

	buf.WriteString("RIFF")
	if err := put(uint32(36 + dataSize)); err != nil {
		return err
	}
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	for _, v := range []any{
		uint32(16),             // subchunk1 size (PCM)
		uint16(1),              // audio format: PCM
		uint16(1),              // mono
		uint32(sampleRate),     // sample rate
		uint32(sampleRate * 2), // byte rate
		uint16(2),              // block align (16-bit mono)
		uint16(16),             // bits per sample
	} {
		if err := put(v); err != nil {
			return err
		}
	}
	buf.WriteString("data")
	if err := put(dataSize); err != nil {
		return err
	}
	for _, s := range samples {
		if err := put(s); err != nil {
			return err
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// EnsureWav returns the path of the wav for name, synthesizing it lazily on
// first call. dir is created as needed. Unknown names are an error.
func EnsureWav(dir, name string) (string, error) {
	if !IsValid(name) {
		return "", fmt.Errorf("sound: unknown name %q", name)
	}
	path := filepath.Join(dir, name+".wav")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	// Build first via samplesFor (nil-safe: IsValid above guarantees non-nil).
	samples := samplesFor(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeWav(path, samples); err != nil {
		return "", err
	}
	return path, nil
}
