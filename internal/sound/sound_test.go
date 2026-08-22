package sound

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// wavHeader parses the 44-byte canonical RIFF/WAVE/PCM header fields.
type wavHeader struct {
	dataSize    uint32
	chunkSize   uint32
	sampleRate  uint32
	byteRate    uint32
	bitsPer     uint16
	channels    uint16
	audioFormat uint16
	blockAlign  uint16
}

func parseHeader(t *testing.T, b []byte) wavHeader {
	t.Helper()
	if len(b) < 44 {
		t.Fatalf("wav too short: %d bytes", len(b))
	}
	if !bytes.Equal(b[0:4], []byte("RIFF")) {
		t.Fatalf("missing RIFF magic: %q", b[0:4])
	}
	if !bytes.Equal(b[8:12], []byte("WAVE")) {
		t.Fatalf("missing WAVE magic: %q", b[8:12])
	}
	if !bytes.Equal(b[12:16], []byte("fmt ")) {
		t.Fatalf("missing fmt chunk: %q", b[12:16])
	}
	if !bytes.Equal(b[36:40], []byte("data")) {
		t.Fatalf("missing data chunk: %q", b[36:40])
	}
	le := binary.LittleEndian
	return wavHeader{
		chunkSize:   le.Uint32(b[4:8]),
		audioFormat: le.Uint16(b[20:22]),
		channels:    le.Uint16(b[22:24]),
		sampleRate:  le.Uint32(b[24:28]),
		byteRate:    le.Uint32(b[28:32]),
		blockAlign:  le.Uint16(b[32:34]),
		bitsPer:     le.Uint16(b[34:36]),
		dataSize:    le.Uint32(b[40:44]),
	}
}

func TestSynthWritesValidWavs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			path, err := EnsureWav(dir, name)
			if err != nil {
				t.Fatalf("EnsureWav: %v", err)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			h := parseHeader(t, b)

			if h.audioFormat != 1 {
				t.Errorf("audioFormat = %d, want 1 (PCM)", h.audioFormat)
			}
			if h.channels != 1 {
				t.Errorf("channels = %d, want 1 (mono)", h.channels)
			}
			if h.bitsPer != 16 {
				t.Errorf("bitsPerSample = %d, want 16", h.bitsPer)
			}
			if h.sampleRate != sampleRate {
				t.Errorf("sampleRate = %d, want %d", h.sampleRate, sampleRate)
			}
			if h.chunkSize != 36+h.dataSize {
				t.Errorf("chunkSize = %d, want 36+dataSize=%d", h.chunkSize, 36+h.dataSize)
			}
			if got := len(b) - 44; uint32(got) != h.dataSize {
				t.Errorf("file data bytes = %d, header declares %d", got, h.dataSize)
			}

			// expected length vs duration spec (±1%)
			want := uint32(2 * msToN(msPerShape[name]))
			diff := math.Abs(float64(h.dataSize) - float64(want))
			if diff > float64(want)*0.01 {
				t.Errorf("dataSize = %d, want %d ±1%%", h.dataSize, want)
			}

			// no clipping
			for off := 44; off+2 <= len(b); off += 2 {
				v := int16(binary.LittleEndian.Uint16(b[off : off+2]))
				if math.Abs(float64(v)) > math.MaxInt16 {
					t.Fatalf("clipped sample at byte %d: %d", off, v)
				}
			}

			// non-silence: at least one sample is meaningfully loud
			peak := 0
			for off := 44; off+2 <= len(b); off += 2 {
				v := math.Abs(float64(int16(binary.LittleEndian.Uint16(b[off : off+2]))))
				if v > float64(peak) {
					peak = int(v)
				}
			}
			if peak < 100 {
				t.Errorf("suspiciously silent: peak = %d", peak)
			}
		})
	}
}

func TestEnsureWavIsLazyAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	p1, err := EnsureWav(dir, "done")
	if err != nil {
		t.Fatal(err)
	}
	info1, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := EnsureWav(dir, "done")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("paths differ: %q vs %q", p1, p2)
	}
	if filepath.Base(p1) != "done.wav" {
		t.Fatalf("unexpected filename: %s", filepath.Base(p1))
	}
	// second call must not rewrite the file (same hash of bytes)
	b1, _ := os.ReadFile(p1)
	p3, err := EnsureWav(dir, "done")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(p3)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("second EnsureWav changed the file")
	}
	if info1.Size() == 0 {
		t.Fatal("wav empty")
	}
}

func TestEnsureWavUnknownName(t *testing.T) {
	if _, err := EnsureWav(t.TempDir(), "bogus"); err == nil {
		t.Fatal("expected error for unknown name")
	}
}

func TestBusMuteEnvOverridesConfig(t *testing.T) {
	t.Setenv("THEBORINGOFFICE_MUTE", "1")
	b := NewBus("on", t.TempDir())
	if b.Mode() != "off" {
		t.Fatalf("THEBORINGOFFICE_MUTE=1 should force off, got %q", b.Mode())
	}
	if err := b.Play("done"); err != nil {
		t.Fatalf("off-mode Play should never error: %v", err)
	}
}

func TestBusUnknownSound(t *testing.T) {
	b := NewBus("off", t.TempDir())
	if err := b.Play("bogus"); err == nil {
		t.Fatal("expected error for unknown sound name")
	}
}

func TestBusThrottle(t *testing.T) {
	b := NewBus("bell", t.TempDir())
	// stub the clock so we control the gap
	var now time.Time
	b.now = func() time.Time { return now }

	if b.throttled("done") {
		t.Fatal("first call should not throttle")
	}
	now = now.Add(throttleGap - time.Millisecond)
	if !b.throttled("done") {
		t.Fatal("second call within gap should throttle")
	}
	now = now.Add(throttleGap + time.Millisecond)
	if b.throttled("done") {
		t.Fatal("call after gap should not throttle")
	}
	// a different name is not throttled by "done"'s clock
	if b.throttled("send") {
		t.Fatal("different sound name should not be throttled")
	}
}

func TestBusModes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"on", "on"},
		{"bell", "bell"},
		{"off", "off"},
		{"", "on"},
		{"garbage", "off"},
	} {
		b := NewBus(tc.in, t.TempDir())
		if b.Mode() != tc.want {
			t.Errorf("NewBus(%q).Mode() = %q, want %q", tc.in, b.Mode(), tc.want)
		}
	}
}

func TestBusPlayWithoutPlayerDegradesSilent(t *testing.T) {
	b := NewBus("on", t.TempDir())
	b.player = "" // simulate a headless box
	if err := b.Play("done"); err != nil {
		t.Fatalf("Play with no player should degrade silently: %v", err)
	}
}

func TestDefaultDirShape(t *testing.T) {
	home := t.TempDir()
	b := NewBus("on", home)
	want := filepath.Join(home, ".theboringoffice", "sounds")
	if b.Dir() != want {
		t.Fatalf("Dir() = %q, want %q", b.Dir(), want)
	}
}
