// mail_test.go — boot-order regression: hydrateSession calls SetState
// with persisted mails BEFORE the first SetSize, so the panel renders
// at width 0. styleMailRow must survive the fully-clipped row instead
// of panicking on slice bounds (live-boot crash: [3:0]).
package panels

import (
	"strings"
	"testing"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

func mailFixture() []state.MailItem {
	return []state.MailItem{
		{Kind: state.MailBrief, From: "boss", To: "dev", Subject: "ship it", At: 100},
		{Kind: state.MailReturn, From: "dev", To: "boss", Subject: "shipped", At: 200},
	}
}

// SetState before SetSize (w == 0) must not panic, and the deferred
// re-render at a real width must produce styled, complete rows.
func TestMailSetStateBeforeSetSize(t *testing.T) {
	m := NewMail()
	m.SetState(state.OfficeState{Mails: mailFixture()}) // boot order: no size yet

	m.SetSize(60, 20)
	out := m.View()
	if !strings.Contains(out, "shipped") {
		t.Fatalf("expected newest mail subject in view, got:\n%s", out)
	}
	if !strings.Contains(out, "boss") {
		t.Fatalf("expected sender name in view, got:\n%s", out)
	}
}

// Newest-first ordering (the demo's At field is a tick, higher = newer).
func TestMailRenderNewestFirst(t *testing.T) {
	m := NewMail()
	m.SetSize(60, 20)
	m.SetState(state.OfficeState{Mails: mailFixture()})
	out := m.View()
	if strings.Index(out, "shipped") > strings.Index(out, "ship it") {
		t.Fatalf("expected newest first, got:\n%s", out)
	}
}
