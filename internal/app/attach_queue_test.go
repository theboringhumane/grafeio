// attach_queue_test.go — the message backlog CARRIES chat-input
// attachments: enqueued items keep their files through the busy wait, the
// composed [BATCH DISPATCH] prompt marks each item with the machine
// " 📎N" suffix, and the flush pushes every attachment through the
// backend's attachment seam (the teamBackend twin: type-asserted, not in
// state.Backend).
package app

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/theboringhumane/grafeio/internal/state"
)

// recBackend — a recording state.Backend WITH the attachment seam (the
// app's type-assert must pick SendWith over the plain-text Send).
type recBackend struct {
	sentTexts []string
	sentAtts  [][]state.Attachment
}

func (r *recBackend) Mode() state.Mode                      { return state.ModeDemo }
func (r *recBackend) Start(func(state.Event)) error         { return nil }
func (r *recBackend) Stop() error                           { return nil }
func (r *recBackend) Send(text string) error                { return nil } // untouched: the seam wins
func (r *recBackend) AnswerPermission(string, string) error { return nil }
func (r *recBackend) AnswerQuestion(string, []string) error { return nil }
func (r *recBackend) RejectQuestion(string) error           { return nil }
func (r *recBackend) SendWith(text string, atts []state.Attachment) error {
	r.sentTexts = append(r.sentTexts, text)
	r.sentAtts = append(r.sentAtts, atts)
	return nil
}

// runMsg feeds one msg through Update (the model value copies — keep the
// returned one) and executes the returned cmd tree breadth-first (the
// flush's send closure hides inside a tea.Batch). Spinner ticks are
// DROPPED: they sleep ~80ms and re-arm forever — not send results.
func runMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	nm, cmd := m.Update(msg)
	m = nm.(Model)
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		out := c()
		if out == nil {
			continue
		}
		switch out := out.(type) {
		case tea.BatchMsg:
			queue = append(queue, out...)
		case spinner.TickMsg:
			// the blinking caret's self-re-arm — ignore
		default:
			nm, next := m.Update(out)
			m = nm.(Model)
			if next != nil {
				queue = append(queue, next)
			}
		}
	}
	return m
}

func TestQueueCarriesAttachments(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	att1 := state.Attachment{Name: "paste.png", Mime: "image/png", Path: "/tmp/x/paste.png"}
	att2 := state.Attachment{Name: "internal/app/model.go", Mime: "text/x-go", Path: "internal/app/model.go"}
	att3 := state.Attachment{Name: "README.md", Mime: "text/markdown", Path: "README.md"}

	// busy boss → both Enters enqueue
	m = runMsg(t, m, state.Event{Kind: state.EvChatBoss,
		Msg: state.ChatMsg{ID: "boss-1", From: "boss", Pending: true}})
	m = runMsg(t, m, enqueueMsg{text: "first item", atts: []state.Attachment{att1}})
	m = runMsg(t, m, enqueueMsg{text: "second item", atts: []state.Attachment{att2, att3}})
	if len(m.queue) != 2 {
		t.Fatalf("want 2 queued items, got %d", len(m.queue))
	}
	if len(m.queue[0].atts) != 1 || len(m.queue[1].atts) != 2 {
		t.Fatalf("attachments must survive the enqueue: %+v", m.queue)
	}

	// boss reply completes → the backlog flushes as ONE composed batch
	m = runMsg(t, m, state.Event{Kind: state.EvChatBoss,
		Msg: state.ChatMsg{ID: "bossmsg-m1", From: "boss", Kind: "boss", Text: "done", Pending: false}})

	if len(b.sentTexts) != 1 {
		t.Fatalf("want ONE batch send, got %d sends", len(b.sentTexts))
	}
	got := b.sentTexts[0]
	for _, want := range []string{"[BATCH DISPATCH — 2 requests", "1. first item 📎1", "2. second item 📎2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("batch prompt missing %q:\n%s", want, got)
		}
	}
	atts := b.sentAtts[0]
	if len(atts) != 3 {
		t.Fatalf("the flush must send ALL 3 attachments, got %d", len(atts))
	}
	if atts[0].Name != "paste.png" || atts[1].Name != "internal/app/model.go" || atts[2].Name != "README.md" {
		t.Fatalf("attachment order must follow the items: %+v", atts)
	}
}
