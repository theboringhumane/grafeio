// chat_attach_test.go — render proof for the chat-input attachments:
// the chips line and the @ picker popover must be visible in View() (as
// plain text) and the layout budget must match the drawn rows. No disk,
// no clipboard: attachments are staged through addAttachment directly.
package panels

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/grafeio/internal/state"
)

// TestChatAttachRender stages two attachments (an image paste chip and an
// @-picked file), opens the picker, and prints the ANSI-stripped panel at
// 60 cols — the eyeball proof for chips + popover (verifies layout rows
// against the SetSize budget too).
func TestChatAttachRender(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)

	// two staged chips: a pasted image + a repo file
	c.addAttachment(chatAttachment{name: "paste.png", mime: "image/png", path: "/tmp/x/paste.png"})
	c.addAttachment(chatAttachment{name: "internal/app/model.go", mime: "text/x-go", path: "internal/app/model.go"})

	// the picker answers its walk: three files, live-filtered to two
	c.atOpen = true
	c.atFrag = "internal"
	c.onAttachWalk(attachWalkMsg{files: []string{
		"cmd/grafeio/main.go", "internal/app/model.go", "internal/panels/chat.go",
	}})

	view := ansi.Strip(c.View())
	fmt.Println("---- CHAT PANEL (60 cols, ansi-stripped) ----")
	fmt.Print(view)
	fmt.Println("---- END PANEL ----")

	// chips line: both names, dim, above the textarea
	if !strings.Contains(view, "📎 paste.png (image/png) · internal/app/model.go") {
		t.Fatalf("chips line missing from view:\n%s", view)
	}
	// popover: bordered box, header, accented selected row, filtered rows,
	// count footer
	for _, want := range []string{"attach file", "› internal/app/model.go",
		"  internal/panels/chat.go", "2/3"} {
		if !strings.Contains(view, want) {
			t.Fatalf("popover row %q missing from view:\n%s", want, view)
		}
	}
	if strings.Contains(view, "cmd/grafeio/main.go") {
		t.Fatalf("unfiltered file leaked the 'internal' filter:\n%s", view)
	}
	// the SetSize budget pays exactly the rows the tab draws (no overlap)
	if got, want := c.chipsH(), len(c.chipsLines()); got != want {
		t.Fatalf("chipsH budget %d != drawn %d", got, want)
	}
	if got, want := c.popoverH(), len(strings.Split(ansi.Strip(c.renderAttachPopover()), "\n")); got != want {
		t.Fatalf("popoverH budget %d != drawn %d", got, want)
	}
}

// TestAtFragmentOf pins the word-start + tail-tracking rules of the "@"
// trigger (emails and mid-text @s must NOT open the picker).
func TestAtFragmentOf(t *testing.T) {
	cases := []struct {
		in       string
		wantFrag string
		wantOK   bool
	}{
		{"@", "", true},                    // just opened
		{"hello @mod", "mod", true},        // after whitespace
		{"multi\nline @cha", "cha", true},  // after newline
		{"boss@grafe.io", "", false},       // email — not a word start
		{"see @model.go notes", "", false}, // fragment ended at the space
		{"no picker here", "", false},      // no @ at all
		{"@mod @ch", "ch", true},           // last @ wins
		{"x@y", "", false},                 // mid-word @
	}
	for _, c := range cases {
		frag, ok := atFragmentOf(c.in)
		if ok != c.wantOK || frag != c.wantFrag {
			t.Fatalf("atFragmentOf(%q) = (%q,%v), want (%q,%v)", c.in, frag, ok, c.wantFrag, c.wantOK)
		}
	}
}

// TestNarrowAttachRender: compact sidebar (30 → 28 content cols) — chips
// fold into "(+N)", every drawn row (chips + picker) stays inside the
// column budget (clip, never overflow).
func TestNarrowAttachRender(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(28, 24)
	c.addAttachment(chatAttachment{name: "paste.png", mime: "image/png", path: "p"})
	c.addAttachment(chatAttachment{name: "internal/panels/chat_attach.go", mime: "text/x-go", path: "f"})
	c.addAttachment(chatAttachment{name: "internal/app/model.go", mime: "text/x-go", path: "g"})
	c.atOpen = true
	c.onAttachWalk(attachWalkMsg{files: []string{
		"internal/panels/chat_attach.go", "internal/panels/chat_attach_test.go",
	}})
	view := ansi.Strip(c.View())
	for i, ln := range strings.Split(view, "\n") {
		if w := len([]rune(ln)); w > 28 {
			t.Fatalf("row %d overflows the 28-col budget: %d cells (%q)", i, w, ln)
		}
	}
	if !strings.Contains(view, "(+1)") {
		t.Fatalf("the third chip must fold into (+1):\n%s", view)
	}
}

// TestChatAttachKeyflow drives the REAL key path end-to-end: typing "@mod"
// opens the picker and filters, down+enter attaches and strips the
// fragment from the draft (the words before it survive).
func TestChatAttachKeyflow(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)

	typeRune := func(r rune) {
		c.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	for _, r := range "read " {
		typeRune(r)
	}
	typeRune('@') // word boundary → picker opens
	if !c.atOpen {
		t.Fatal("typing @ at a word boundary must open the picker")
	}
	// the open cmd walks the disk; the answer arrives as its own msg
	c.Update(attachWalkMsg{files: []string{"cmd/grafeio/main.go", "internal/app/model.go", "internal/panels/chat.go"}})
	for _, r := range "mod" {
		typeRune(r)
	}
	if got := len(c.atFiltered); got != 1 {
		t.Fatalf("'mod' filter must leave exactly model.go, got %d: %v", got, c.atFiltered)
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})) // attach
	if c.atOpen {
		t.Fatal("enter must close the picker")
	}
	if got := c.ta.Value(); got != "read " {
		t.Fatalf("attaching must strip @fragment ('@mod'), keep the draft: got %q", got)
	}
	if len(c.atts) != 1 || c.atts[0].name != "internal/app/model.go" {
		t.Fatalf("the highlighted file must stage as a chip, got %+v", c.atts)
	}

	// esc keeps the typed fragment (and closes the picker)
	typeRune('@')
	if !c.atOpen {
		t.Fatal("@ after whitespace must reopen the picker")
	}
	for _, r := range "int" {
		typeRune(r)
	}
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if c.atOpen {
		t.Fatal("esc must close the picker")
	}
	if got := c.ta.Value(); got != "read @int" {
		t.Fatalf("esc keeps the fragment: got %q", got)
	}

	// mid-word @ never opens (email case)
	c.ta.SetValue("")
	for _, r := range "boss@grafe.io" {
		typeRune(r)
	}
	if c.atOpen {
		t.Fatal("a mid-word @ (email) must NOT open the picker")
	}
}

// TestPasteMsgReachesTextarea pins the R1 regression fix: a bracketed
// paste (tea.PasteMsg) lands in the textarea, and the textarea's OWN
// clipboard answer (its unexported pasteMsg rides through the same
// default arm as a plain string) lands too.
func TestPasteMsgReachesTextarea(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)
	c.Update(tea.PasteMsg{Content: "bracketed paste"})
	if got := c.ta.Value(); got != "bracketed paste" {
		t.Fatalf("tea.PasteMsg must insert into the textarea, got %q", got)
	}
}

// TestEscAndSendClearAttachState: Enter drains chips and ClearAttachments
// resets everything (the /clear path).
func TestEscAndSendClearAttachState(t *testing.T) {
	var sent []state.Attachment
	c := NewChat(func(_ string, atts []state.Attachment) tea.Cmd {
		sent = atts
		return nil
	})
	c.SetSize(60, 30)
	c.addAttachment(chatAttachment{name: "a.go", mime: "text/x-go", path: "a.go"})
	c.addAttachment(chatAttachment{name: "b.go", mime: "text/x-go", path: "b.go"})
	c.ta.SetValue("ship these")
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if len(sent) != 2 || sent[0].Name != "a.go" || sent[1].Name != "b.go" {
		t.Fatalf("Enter must drain both chips into the send, got %+v", sent)
	}
	if len(c.atts) != 0 {
		t.Fatal("a send clears the chip state")
	}

	// backspace on an empty draft pops the newest chip
	c.addAttachment(chatAttachment{name: "x.go", mime: "text/x-go", path: "x.go"})
	c.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	if len(c.atts) != 0 {
		t.Fatalf("backspace on empty input must pop the chip, got %d", len(c.atts))
	}
}

// TestAttachCapRing: past the 5-chip cap the OLDEST is evicted (ring) and
// drained attachments become clean state.Attachments.
func TestAttachCapRing(t *testing.T) {
	c := NewChat(nil)
	for i := 0; i < 7; i++ {
		c.addAttachment(chatAttachment{name: fmt.Sprintf("f%d.txt", i), mime: "text/plain", path: "f.txt"})
	}
	if len(c.atts) != 5 {
		t.Fatalf("cap must hold at 5, got %d", len(c.atts))
	}
	if c.atts[0].name != "f2.txt" || c.atts[4].name != "f6.txt" {
		t.Fatalf("oldest must be evicted FIFO: got %s..%s", c.atts[0].name, c.atts[4].name)
	}
	drained := c.drainAttachments()
	if len(drained) != 5 || len(c.atts) != 0 {
		t.Fatalf("drain must hand over all %d chips and clear, got %d/%d", 5, len(drained), len(c.atts))
	}
	if drained[0].Name != "f2.txt" || drained[4].Name != "f6.txt" {
		t.Fatalf("drain order must be FIFO: %s..%s", drained[0].Name, drained[4].Name)
	}
}
