// chat_attach_test.go — render proof for the chat-input attachments:
// the chips line and the @ picker popover must be visible in View() (as
// plain text) and the layout budget must match the drawn rows. No disk,
// no clipboard: attachments are staged through addAttachment directly.
package panels

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	state "github.com/theboringhumane/theboringoffice/internal/state"
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
		"cmd/theboringoffice/main.go", "internal/app/model.go", "internal/panels/chat.go",
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
	if strings.Contains(view, "cmd/theboringoffice/main.go") {
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
	c.Update(attachWalkMsg{files: []string{"cmd/theboringoffice/main.go", "internal/app/model.go", "internal/panels/chat.go"}})
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
// default arm as a plain string) lands too. ("bracketed paste" is plain
// prose — it must survive the smart-paste classifier as TEXT.)
func TestPasteMsgReachesTextarea(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)
	c.Update(tea.PasteMsg{Content: "bracketed paste"})
	if got := c.ta.Value(); got != "bracketed paste" {
		t.Fatalf("tea.PasteMsg must insert into the textarea, got %q", got)
	}
}

// TestPasteFilePaths pins the Finder-copy classifier: ok ONLY when every
// token unquotes/unescapes to an existing REGULAR file — prose, missing
// paths, mixed hits, directories and empty pastes all classify as TEXT.
func TestPasteFilePaths(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "shot.png")
	f2 := filepath.Join(dir, "notes.txt")
	fSpace := filepath.Join(dir, "my file.png")
	for _, p := range []string{f1, f2, fSpace} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	escape := func(p string) string { return strings.ReplaceAll(p, " ", `\ `) }

	cases := []struct {
		name    string
		content string
		want    []string
		wantOK  bool
	}{
		{"one existing file", f1, []string{f1}, true},
		{"two files space-separated", f1 + " " + f2, []string{f1, f2}, true},
		{"escaped space (Paste Escaped Text)", escape(fSpace), []string{fSpace}, true},
		{"surrounding double quotes", `"` + fSpace + `"`, []string{fSpace}, true},
		{"surrounding single quotes", `'` + fSpace + `'`, []string{fSpace}, true},
		{"leading/trailing whitespace", "  " + f1 + "\n", []string{f1}, true},
		{"prose is not a path", "hello world", nil, false},
		{"nonexistent path", "/definitely/not/here.png", nil, false},
		{"one real file + one ghost", f1 + " /definitely/not/here.png", nil, false},
		{"a directory is not a chip", dir, nil, false},
		{"empty paste", "", nil, false},
		{"whitespace-only paste", "   ", nil, false},
	}
	for _, tc := range cases {
		got, ok := pasteFilePaths(tc.content)
		if ok != tc.wantOK {
			t.Errorf("%s: pasteFilePaths(%q) ok=%v, want %v", tc.name, tc.content, ok, tc.wantOK)
			continue
		}
		if ok && strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: pasteFilePaths(%q) = %v, want %v", tc.name, tc.content, got, tc.want)
		}
	}
}

// TestPasteMsgAttachesFinderCopy drives the smart arm end-to-end: a
// Finder-copied file arrives as tea.PasteMsg path text and must stage a
// chip (basename + resolved MIME) while the textarea stays EMPTY.
func TestPasteMsgAttachesFinderCopy(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, []byte("not-really-a-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewChat(nil)
	c.SetSize(60, 30)
	c.Update(tea.PasteMsg{Content: png})
	if len(c.atts) != 1 {
		t.Fatalf("a Finder-copy paste must stage exactly one chip, got %d", len(c.atts))
	}
	if c.atts[0].name != filepath.Base(png) || c.atts[0].path != png {
		t.Fatalf("the chip names the basename and keeps the path, got %+v", c.atts[0])
	}
	if got := c.ta.Value(); got != "" {
		t.Fatalf("a Finder-copy paste must NOT type into the textarea, got %q", got)
	}
	fmt.Println("---- FINDER-COPY PASTE (60 cols, ansi-stripped) ----")
	fmt.Print(ansi.Strip(c.View()))
	fmt.Println("---- END PANEL ----")
	if view := ansi.Strip(c.View()); !strings.Contains(view, "📎 shot.png (image/png)") {
		t.Fatalf("the staged chip must render on the chips row:\n%s", view)
	}
}

// TestImagePasteKeyTriggers: ctrl+v AND super+v (cmd+v as kitty-protocol
// terminals report it) fire the same image probe and close an open @
// picker. The returned cmds are NEVER executed — they shell out.
func TestImagePasteKeyTriggers(t *testing.T) {
	superV := tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModSuper})
	if got := superV.String(); got != "super+v" {
		t.Fatalf("the cmd modifier must render as \"super+v\", got %q", got)
	}
	for _, msg := range []tea.KeyPressMsg{
		tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}),
		superV,
	} {
		c := NewChat(nil)
		c.SetSize(60, 30)
		c.atOpen = true
		cmd := c.Update(msg)
		if cmd == nil {
			t.Fatalf("%s must fire the image probe (non-nil cmd)", msg.String())
		}
		if c.atOpen {
			t.Fatalf("%s must close an open @ picker", msg.String())
		}
	}
}

// TestPasteMsgEmptyImageProbe: an empty/whitespace bracketed paste is
// darwin's cmd+v-with-an-image-clipboard signal (the terminal has no
// bytes to send) — it must fire the probe and NOT touch the textarea.
// Elsewhere it inserts harmlessly like any plain paste.
func TestPasteMsgEmptyImageProbe(t *testing.T) {
	for _, content := range []string{"", "  "} {
		c := NewChat(nil)
		c.SetSize(60, 30)
		cmd := c.Update(tea.PasteMsg{Content: content})
		if runtime.GOOS == "darwin" {
			if cmd == nil {
				t.Fatalf("an empty/whitespace paste (%q) on darwin must fire the image probe", content)
			}
			if got := c.ta.Value(); got != "" {
				t.Fatalf("the probe trigger must NOT touch the textarea, got %q", got)
			}
			// DO NOT execute cmd — it shells out (pngpaste/osascript).
		} else {
			if got := c.ta.Value(); got != content {
				t.Fatalf("a whitespace-only paste (%q) just inserts on %s, got %q", content, runtime.GOOS, got)
			}
		}
	}
}

// TestClipPasteReprobeRefeeds: a reprobe MISS re-feeds the original
// bracketed-paste bytes straight into the textarea (NOT the OSC52
// clipboard replay) — and the platform notices stay silent on the
// paste-triggered path. Pure: clipPasteMsg is constructed directly.
func TestClipPasteReprobeRefeeds(t *testing.T) {
	c := NewChat(nil)
	c.SetSize(60, 30)
	c.Update(clipPasteMsg{noImage: true, reprobe: true, reinsert: "  "})
	if got := c.ta.Value(); got != "  " {
		t.Fatalf("a reprobe miss must re-feed the original paste bytes, got %q", got)
	}

	c2 := NewChat(nil)
	c2.SetSize(60, 30)
	c2.Update(clipPasteMsg{unsupported: true, reprobe: true, reinsert: "x"})
	if got := c2.ta.Value(); got != "x" {
		t.Fatalf("an unsupported reprobe must still land the bytes, got %q", got)
	}
	if c2.pasteUnsupported {
		t.Fatal("bracketed-paste probes never fire the unsupported notice")
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
