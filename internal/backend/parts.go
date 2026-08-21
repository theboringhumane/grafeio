// parts.go — building the prompt_async parts array: one text part plus
// one file part per chat-input attachment. Split out of opencode.go so
// the wire shape is unit-testable without a server (parts_test.go).
//
// Wire contract (verified two ways, 2026-08-21):
//   - GET /doc on a spawned `opencode serve` 1.18.19: POST
//     /session/{sessionID}/prompt_async (operationId session.prompt_async)
//     takes parts[] of TextPartInput | FilePartInput | …; FilePartInput =
//     {"type":"file","mime":string,"filename"?:string,"url":string}
//     (required: type, mime, url; additionalProperties false).
//   - live POST against the same server: a file part with a base64 data
//     URL is accepted (HTTP 204) — see postPrompt.
package backend

import (
	"encoding/base64"
	"net/http"
	"os"

	"github.com/theboringhumane/grafeio/internal/state"
)

// payloadParts builds the parts array for prompt_async: the text part
// first (only when the user actually typed something — an attach-only
// send is legal), then one file part per attachment in draft order.
// Unreadable attachments are SKIPPED, not fatal: their names come back in
// skipped so the caller can surface one status note and still send the
// rest of the prompt.
func payloadParts(text string, atts []state.Attachment) (parts []map[string]any, skipped []string) {
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for _, att := range atts {
		data, err := os.ReadFile(att.Path)
		if err != nil {
			skipped = append(skipped, att.Name)
			continue
		}
		mime := att.Mime
		if mime == "" {
			// defensive: the panel resolves MIME at attach time; a bare
			// Attachment still gets a sane type from its head bytes.
			mime = http.DetectContentType(headBytes(data))
		}
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     mime,
			"filename": att.Name,
			"url":      "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
		})
	}
	return parts, skipped
}

// headBytes returns the first ≤512 bytes — DetectContentType's sniff
// window — of an already-read file body.
func headBytes(data []byte) []byte {
	if len(data) > 512 {
		return data[:512]
	}
	return data
}

// attachmentNames projects attachments to their display names (the user
// bubble's Meta carrier and demo ack both speak in names, never paths).
func attachmentNames(atts []state.Attachment) []string {
	if len(atts) == 0 {
		return nil
	}
	names := make([]string, len(atts))
	for i, a := range atts {
		names[i] = a.Name
	}
	return names
}
