// parts_test.go — unit proof for the prompt_async payload builder
// (parts.go). Pure: no server — real temp files feed payloadParts and the
// marshaled payload is compared byte-for-byte against the exact JSON the
// wire expects (serve 1.18.19 /doc FilePartInput shape).
package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// marshalPayload mirrors postPrompt's body assembly (parts is the only
// field that varies with attachments).
func marshalPayload(t *testing.T, parts []map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"parts": parts})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// TestPayloadTextOnly: the plain send stays exactly today's shape — one
// text part, nothing else.
func TestPayloadTextOnly(t *testing.T) {
	parts, skipped := payloadParts("ship it", nil)
	if len(skipped) != 0 {
		t.Fatalf("no attachments but skipped = %v", skipped)
	}
	got := marshalPayload(t, parts)
	want := `{"parts":[{"text":"ship it","type":"text"}]}`
	if got != want {
		t.Fatalf("text-only payload mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestPayloadOneImage: a pasted PNG (tiny real file) becomes one file
// part with a base64 data URL after the text part.
func TestPayloadOneImage(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "paste.png")
	if err := os.WriteFile(img, []byte("not-really-a-png-but-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	atts := []state.Attachment{{Name: "paste.png", Mime: "image/png", Path: img}}
	parts, skipped := payloadParts("what is this", atts)
	if len(skipped) != 0 {
		t.Fatalf("readable attach must not skip: %v", skipped)
	}
	got := marshalPayload(t, parts)
	want := `{"parts":[{"text":"what is this","type":"text"},` +
		`{"filename":"paste.png","mime":"image/png","type":"file",` +
		`"url":"data:image/png;base64,bm90LXJlYWxseS1hLXBuZy1idXQtYnl0ZXM="}]}`
	if got != want {
		t.Fatalf("one-image payload mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestPayloadMultiAttach: two attachments keep draft order and an
// unreadable one is SKIPPED (named in skipped) without sinking the send.
func TestPayloadMultiAttach(t *testing.T) {
	dir := t.TempDir()
	go_ := filepath.Join(dir, "model.go")
	if err := os.WriteFile(go_, []byte("package app"), 0o644); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(dir, "paste-2.png")
	if err := os.WriteFile(png, []byte("pngbytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	ghost := filepath.Join(dir, "deleted-before-send.txt")
	atts := []state.Attachment{
		{Name: "internal/app/model.go", Mime: "text/x-go", Path: go_},
		{Name: "paste-2.png", Mime: "image/png", Path: png},
		{Name: "deleted-before-send.txt", Mime: "text/plain", Path: ghost},
	}
	parts, skipped := payloadParts("review these", atts)
	if len(skipped) != 1 || skipped[0] != "deleted-before-send.txt" {
		t.Fatalf("want the missing file skipped by name, got %v", skipped)
	}
	got := marshalPayload(t, parts)
	want := `{"parts":[{"text":"review these","type":"text"},` +
		`{"filename":"internal/app/model.go","mime":"text/x-go","type":"file",` +
		`"url":"data:text/x-go;base64,cGFja2FnZSBhcHA="},` +
		`{"filename":"paste-2.png","mime":"image/png","type":"file",` +
		`"url":"data:image/png;base64,cG5nYnl0ZXM="}]}`
	if got != want {
		t.Fatalf("multi-attach payload mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestPayloadAttachOnly: text empty + attachments still produces a legal
// prompt (file parts only — the enter gate allows a bare-attachment send).
func TestPayloadAttachOnly(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "paste.png")
	if err := os.WriteFile(img, []byte("pngbytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	parts, skipped := payloadParts("", []state.Attachment{{Name: "paste.png", Mime: "image/png", Path: img}})
	if len(skipped) != 0 {
		t.Fatalf("readable attach must not skip: %v", skipped)
	}
	got := marshalPayload(t, parts)
	want := `{"parts":[{"filename":"paste.png","mime":"image/png","type":"file",` +
		`"url":"data:image/png;base64,cG5nYnl0ZXM="}]}`
	if got != want {
		t.Fatalf("attach-only payload mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestPayloadMimeFallback: an Attachment with no pre-resolved MIME gets
// one sniffed from its head bytes (http.DetectContentType).
func TestPayloadMimeFallback(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "notes")
	if err := os.WriteFile(txt, []byte("plain words in a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	parts, skipped := payloadParts("", []state.Attachment{{Name: "notes", Path: txt}})
	if len(skipped) != 0 {
		t.Fatalf("readable attach must not skip: %v", skipped)
	}
	if len(parts) != 1 {
		t.Fatalf("want 1 part, got %d", len(parts))
	}
	if mime, _ := parts[0]["mime"].(string); mime != "text/plain; charset=utf-8" {
		t.Fatalf("want sniffed text/plain, got %q", mime)
	}
}
