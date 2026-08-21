// chat_attach.go — chat-input attachments: clipboard-image paste (ctrl+v)
// and the @ file-picker popover. Both stage pending "chips" on a dim row
// above the textarea; the chips drain into the send (or enqueue) callback
// as []state.Attachment and the backend turns each into an opencode
// prompt_async file part ({type:"file",mime,filename,url}, url a data
// URL — see internal/backend/parts.go for the verified wire contract).
//
// Two platform realities shape the design:
//
//   - On macOS cmd+v is the user's paste gesture, but the CMD key never
//     reaches a TUI in Terminal.app/iTerm2 (those terminals convert the
//     paste themselves, delivering BRACKETED PASTE = tea.PasteMsg); only
//     kitty-keyboard-protocol terminals configured to pass super report it
//     as a key ("super+v" in bubbletea v2). An image clipboard + cmd+v in
//     Terminal.app delivers NOTHING at all (no bytes to send), so the app
//     keeps its own image triggers — ctrl+v, accepted alongside super+v —
//     and the smart tea.PasteMsg arm: bracketed paste NEVER carries image
//     bytes, and the module's one clipboard dep (atotto/clipboard, ride-
//     along of bubbles/textarea) is text-only. Zero new dependencies is
//     the house rule, so an image probe shells out on darwin: pngpaste
//     when installed, else osascript writing «class PNGf» to a temp file.
//     The probe runs inside a tea.Cmd — shell outs can take tens of ms
//     and must never sit on the update goroutine.
//
//   - A Finder file copy + cmd+v pastes as the file's PATH text (Terminal
//     .app's "Paste Escaped Text": backslash-escaped spaces, sometimes
//     surrounding quotes) — the tea.PasteMsg arm classifies that via
//     pasteFilePaths and stages the files as chips instead of typing the
//     path into the draft.
//
//   - The @ picker lists the process cwd (the repo the TUI runs against).
//     The walk also runs in a tea.Cmd, capped hard (depth 6, 500 entries,
//     >8MB skipped, .git/node_modules pruned) — an attach popover is not
//     a project index.
//
// Cursor-at-tail assumption (documented v1): the picker tracks the "@" +
// fragment as the LAST word of the draft. Typing at the tail is the 99%
// case; every fragment claim re-derives from the textarea value so a
// cursor hop into the middle simply closes the picker (recheck fails),
// never types the wrong thing.
package panels

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/theboringhumane/grafeio/internal/chrome"
	"github.com/theboringhumane/grafeio/internal/state"
)

// Attachment limits — machine constants, not user preferences.
const (
	maxAttachments = 5       // chip cap; past it the OLDEST is evicted (ring)
	atMaxFiles     = 500     // walker hard cap on listed files
	atMaxDepth     = 6       // walker directory depth cap
	atMaxSize      = 8 << 20 // walker skips files over 8MB
	atVisibleRows  = 8       // popover list window
	chipMaxRows    = 2       // chips wrap budget before the "(+N)" tail fold
)

// chatAttachment — one staged chip: the display name + wire MIME plus the
// file the backend base64s at send time. temp, when non-empty, marks the
// panel-created temp dir (os.MkdirTemp "grafeio-paste-*") the app cleans
// up once the send resolves — the panel only removes it for DROPPED
// attachments (a queued send must still find the file at flush time).
type chatAttachment struct {
	name string
	mime string
	path string
	temp string
}

// clipPasteMsg reports the image-paste probe (probeClipboardImage):
// path+temp set → attach; noImage → nothing image-like on the clipboard,
// replay the textarea's own TEXT paste; err → tool failure, notice +
// text-paste fallback; unsupported → non-darwin platform (notice-once).
// The reprobe pair marks a probe started from the tea.PasteMsg arm (cmd+v
// on an image-only clipboard arrives as an EMPTY bracketed paste): a miss
// then re-feeds `reinsert` — the original paste bytes — straight into the
// textarea instead of the OSC52 clipboard replay (those bytes already
// arrived; there is nothing left to fetch), and the platform notices stay
// silent (the ctrl+v/super+v key path owns them).
type clipPasteMsg struct {
	path        string
	temp        string
	mime        string
	err         error
	noImage     bool
	unsupported bool
	reprobe     bool
	reinsert    string
}

// attachWalkMsg delivers the @ picker's repo file list (relative slash
// paths) — built by walkAttachFiles inside a tea.Cmd.
type attachWalkMsg struct{ files []string }

// SetNoticeHandler wires the app's office-notice seam: attachment events
// (cap eviction, backspace removal, platform gaps) surface as chat
// notices exactly like slash-command outcomes.
func (c *Chat) SetNoticeHandler(fn func(text string) tea.Cmd) { c.onNotice = fn }

// noticef routes a panel-originated notice to the app (nil-safe — bare
// NewChat constructions in harnesses have no seam wired).
func (c *Chat) noticef(text string) tea.Cmd {
	if c.onNotice != nil {
		return c.onNotice(text)
	}
	return nil
}

// ------------------------------------------------- image paste: triggers + probe

// startImagePaste kicks the clipboard image probe (the ctrl+v / super+v
// key trigger). An image result attaches (chips); text/empty clipboards
// replay the textarea's own paste path (see onClipPaste), keeping
// today's plain-text behavior exactly.
func (c *Chat) startImagePaste() tea.Cmd {
	if c.atOpen {
		// pasting is not fragment typing: close the picker (the fragment
		// text stays) so a paste can't wedge a stale filter.
		c.closeAttachPicker()
	}
	return readClipboardImage()
}

// startImagePasteReprobe runs the SAME probe for the smart tea.PasteMsg
// arm: on macOS an image-only clipboard delivers cmd+v as an EMPTY
// bracketed paste (the terminal has no text to send), so the paste
// itself is the trigger. orig carries the original paste bytes — on a
// miss onClipPaste re-feeds THEM to the textarea instead of the OSC52
// clipboard replay.
func (c *Chat) startImagePasteReprobe(orig string) tea.Cmd {
	if c.atOpen {
		c.closeAttachPicker()
	}
	return probeClipboardImage(orig, true)
}

// readClipboardImage probes the OS clipboard for IMAGE bytes. darwin:
// pngpaste when $PATH has it, else osascript (writes «class PNGf» to a
// temp file — both exit non-zero on a text/empty clipboard, which is the
// "no image" signal, not an error). Non-darwin: the unsupported arm.
func readClipboardImage() tea.Cmd {
	return probeClipboardImage("", false)
}

// probeClipboardImage is readClipboardImage's body parameterized for the
// two trigger paths: reprobe=false is the key trigger (misses replay the
// textarea text paste); reprobe=true is the bracketed-paste trigger
// (misses re-insert orig — startImagePasteReprobe). The shell-out
// mechanics are identical either way.
func probeClipboardImage(orig string, reprobe bool) tea.Cmd {
	return func() tea.Msg {
		if runtime.GOOS != "darwin" {
			return clipPasteMsg{unsupported: true, reprobe: reprobe, reinsert: orig}
		}
		dir, err := os.MkdirTemp("", "grafeio-paste-*")
		if err != nil {
			return clipPasteMsg{err: err, reprobe: reprobe, reinsert: orig}
		}
		path := filepath.Join(dir, "paste.png")
		var runErr error
		if _, lookErr := exec.LookPath("pngpaste"); lookErr == nil {
			runErr = exec.Command("pngpaste", path).Run()
		} else {
			runErr = exec.Command("osascript",
				"-e", `set f to POSIX file "`+path+`"`,
				"-e", `set d to the clipboard as «class PNGf»`,
				"-e", `set fp to open for access f with write permission`,
				"-e", `write d to fp`,
				"-e", `close access fp`,
			).Run()
		}
		if runErr != nil {
			_ = os.RemoveAll(dir)
			return clipPasteMsg{noImage: true, reprobe: reprobe, reinsert: orig}
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			_ = os.RemoveAll(dir)
			return clipPasteMsg{noImage: true, reprobe: reprobe, reinsert: orig}
		}
		m := http.DetectContentType(headSniff(data))
		if !strings.HasPrefix(m, "image/") {
			// pngpaste wrote something non-image-shaped: treat as noise,
			// not an attachment (and don't leak the temp dir).
			_ = os.RemoveAll(dir)
			return clipPasteMsg{noImage: true, reprobe: reprobe, reinsert: orig}
		}
		return clipPasteMsg{path: path, temp: dir, mime: m, reprobe: reprobe, reinsert: orig}
	}
}

// onClipPaste consumes the probe result: attach on an image hit. On a
// miss the fallback depends on the trigger — the key path replays the
// textarea's own text paste (textarea.Paste — the ctrl+v clipboard path,
// whose internal msgs now arrive through Update's default arm) with the
// platform/tool notices alongside; the reprobe path (a tea.PasteMsg-arms
// probe) re-feeds the paste's original bytes straight into the textarea
// with NO notices (the user pasted, their text must simply land).
func (c *Chat) onClipPaste(msg clipPasteMsg) tea.Cmd {
	if msg.reprobe && (msg.unsupported || msg.err != nil || msg.noImage || msg.path == "") {
		// The probe answered "no image" for a bracketed paste: feed the
		// original bytes DIRECTLY to the textarea here — returning a
		// tea.PasteMsg would re-enter the same Update arm and probe-loop
		// forever. (This can only fire on darwin — the arm gates it — so
		// no unsupported notice; a tool error is still worth surfacing.)
		var cmd tea.Cmd
		c.ta, cmd = c.ta.Update(tea.PasteMsg{Content: msg.reinsert})
		if msg.err != nil {
			return tea.Batch(c.noticef("image paste failed: "+msg.err.Error()), cmd)
		}
		return cmd
	}
	switch {
	case msg.unsupported:
		var note tea.Cmd
		if !c.pasteUnsupported {
			c.pasteUnsupported = true // notice-once per session
			note = c.noticef("image paste not supported on this platform (yet)")
		}
		return tea.Batch(note, textarea.Paste)
	case msg.err != nil:
		return tea.Batch(c.noticef("image paste failed: "+msg.err.Error()), textarea.Paste)
	case msg.noImage || msg.path == "":
		return textarea.Paste
	}
	c.pasteSeq++
	name := "paste.png"
	if c.pasteSeq > 1 {
		name = "paste-" + itoa(c.pasteSeq) + ".png"
	}
	return c.addAttachment(chatAttachment{name: name, mime: msg.mime, path: msg.path, temp: msg.temp})
}

// ------------------------------------------------- smart paste: Finder file copies

// pasteFilePaths classifies a bracketed-paste body as a Finder-copy
// paste: on macOS, pasting a copied file delivers its PATH as text
// (one token per file, backslash-escaped spaces, sometimes surrounding
// quotes). The body splits into whitespace-separated tokens honoring
// "\x" → literal "x" unescaping and single/double surrounding quotes;
// ok=true ONLY when every token resolves to an EXISTING REGULAR file
// (a directory or a missing path classifies the whole paste as plain
// text). No ~ expansion — the shell does that, the paste buffer
// doesn't.
func pasteFilePaths(content string) (paths []string, ok bool) {
	toks := splitPasteTokens(strings.TrimSpace(content))
	if len(toks) == 0 {
		return nil, false
	}
	paths = make([]string, 0, len(toks))
	for _, tok := range toks {
		info, err := os.Stat(tok)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		paths = append(paths, tok)
	}
	return paths, true
}

// splitPasteTokens tokenizes a trimmed paste body on whitespace,
// unescaping "\x" → "x" (Terminal.app escapes spaces as "\ ") and
// stripping ONE pair of surrounding single/double quotes per token.
// A trailing lone backslash survives literally.
func splitPasteTokens(s string) []string {
	var toks []string
	r := []rune(s)
	for i := 0; i < len(r); {
		for i < len(r) && unicode.IsSpace(r[i]) {
			i++
		}
		if i >= len(r) {
			break
		}
		var b strings.Builder
		for i < len(r) && !unicode.IsSpace(r[i]) {
			if r[i] == '\\' && i+1 < len(r) {
				b.WriteRune(r[i+1])
				i += 2
				continue
			}
			if (r[i] == '\'' || r[i] == '"') && b.Len() == 0 {
				q := r[i]
				i++
				for i < len(r) && r[i] != q {
					b.WriteRune(r[i])
					i++
				}
				if i < len(r) {
					i++ // the closing quote
				}
				continue
			}
			b.WriteRune(r[i])
			i++
		}
		if b.Len() > 0 {
			toks = append(toks, b.String())
		}
	}
	return toks
}

// attachPastedFiles stages one chip per Finder-copied path (the smart
// tea.PasteMsg arm): display name + sniffed MIME, no temp dir (the file
// is the user's own, not a panel-created paste). The @ picker closes
// like startImagePaste — a paste is not fragment typing — and NOTHING
// is typed into the textarea.
func (c *Chat) attachPastedFiles(paths []string) tea.Cmd {
	if c.atOpen {
		c.closeAttachPicker()
	}
	var cmds []tea.Cmd
	for _, p := range paths {
		if cmd := c.addAttachment(chatAttachment{name: filepath.Base(p), mime: attachMime(p), path: p}); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// headSniff returns DetectContentType's ≤512-byte sniff window (the
// backend has the same helper, unexported — deliberate dup, packages
// don't share internals).
func headSniff(data []byte) []byte {
	if len(data) > 512 {
		return data[:512]
	}
	return data
}

// ---------------------------------------------------------------- @ picker: walk + filter

// walkAttachFiles lists attachable files under root (the process cwd):
// recursive, pruning .git + node_modules, skipping binary-ish extensions
// and >8MB files, capped at depth 6 / 500 entries — sized for a popover,
// not a project index. Unreadable entries are skipped, never fatal.
func walkAttachFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip the entry, don't sink the walk
		}
		if path == root {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(root)+"/")
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return fs.SkipDir
			}
			if strings.Count(rel, "/") >= atMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if binaryishExt[strings.ToLower(filepath.Ext(d.Name()))] {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > atMaxSize {
			return nil
		}
		out = append(out, rel)
		if len(out) >= atMaxFiles {
			return fs.SkipAll
		}
		return nil
	})
	return out
}

// binaryishExt — the picker skips files that are prompt-context noise
// (executables, media, archives, fonts, bytecode, lockfiles). A picker
// heuristic, not a security gate — ctrl+v's image paste covers real
// screenshots, and anything textual with an odd extension still lists.
var binaryishExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".icns": true, ".bmp": true, ".tiff": true, ".avif": true,
	".pdf": true,
	".zip": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true,
	".7z": true, ".rar": true, ".tar": true, ".zst": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true,
	".o": true, ".bin": true, ".wasm": true, ".class": true, ".jar": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
	".wav": true, ".flac": true, ".ogg": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".ds_store": true,
}

// attachMime resolves an @-picked file's wire MIME: extension mapping
// first (mime.TypeByExtension), content sniffing of the head bytes as the
// fallback (http.DetectContentType; octet-stream when even that fails).
func attachMime(path string) string {
	if m := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); m != "" {
		return m
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return http.DetectContentType(head[:n])
}

// atFragmentOf extracts the live "@fragment" from a draft value: the LAST
// "@" that starts a word (input start or after whitespace) with only
// non-whitespace after it. ok=false when no such fragment — the picker
// closes (emails like boss@grafe.io never open it either).
func atFragmentOf(v string) (frag string, ok bool) {
	r := []rune(v)
	at := -1
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == '@' {
			at = i
			break
		}
		if unicode.IsSpace(r[i]) {
			// a word boundary BEFORE hitting "@" — the tail word isn't an
			// @-fragment
			return "", false
		}
	}
	if at < 0 || (at > 0 && !unicode.IsSpace(r[at-1])) {
		return "", false
	}
	return string(r[at+1:]), true
}

// atWordBoundary reports whether an "@" typed right now would start a
// word (empty input or after whitespace) and so open the picker.
func (c *Chat) atWordBoundary() bool {
	r := []rune(c.ta.Value())
	if len(r) == 0 {
		return true
	}
	return unicode.IsSpace(r[len(r)-1])
}

// openAttachPicker opens the popover at the just-typed "@": empty
// fragment, selection at the top, file list from cache — a tea.Cmd walk
// on first open (disk I/O never blocks the update goroutine).
func (c *Chat) openAttachPicker() tea.Cmd {
	c.atOpen = true
	c.atFrag = ""
	c.atSel = 0
	c.refilterAttach() // also splits the layout (picker consumes rows)
	if c.atFiles != nil {
		return nil
	}
	return func() tea.Msg {
		return attachWalkMsg{files: walkAttachFiles(".")}
	}
}

// closeAttachPicker closes the popover, keeping the typed fragment in the
// draft (esc semantics), and splits the layout back.
func (c *Chat) closeAttachPicker() {
	if !c.atOpen {
		return
	}
	c.atOpen = false
	c.SetSize(c.w, c.h)
}

// onAttachWalk consumes the walk result and refilters (the list may have
// been typed-filtered while the walk was in flight).
func (c *Chat) onAttachWalk(msg attachWalkMsg) {
	c.atFiles = msg.files
	c.refilterAttach()
}

// refilterAttach recomputes the visible list (substring, case-insensitive
// — path containment is deliberate over fuzzy scoring: predictable while
// you type) and clamps the selection. The popover height follows the
// filtered count, so the layout re-splits.
func (c *Chat) refilterAttach() {
	frag := strings.ToLower(c.atFrag)
	c.atFiltered = c.atFiltered[:0]
	for _, f := range c.atFiles {
		if frag == "" || strings.Contains(strings.ToLower(f), frag) {
			c.atFiltered = append(c.atFiltered, f)
		}
	}
	if n := len(c.atFiltered); c.atSel >= n {
		c.atSel = n - 1
	}
	if c.atSel < 0 {
		c.atSel = 0
	}
	c.SetSize(c.w, c.h)
}

// afterDraftEdit re-derives the @ fragment from the draft tail after the
// textarea consumed a typed/backspace key — the picker lives only while
// the tail still reads "@fragment".
func (c *Chat) afterDraftEdit() {
	frag, ok := atFragmentOf(c.ta.Value())
	if !ok {
		c.closeAttachPicker()
		return
	}
	if frag != c.atFrag {
		c.atFrag = frag
		c.refilterAttach()
	}
}

// atMove walks the selection up/down the filtered window (wraps).
func (c *Chat) atMove(d int) {
	if n := len(c.atFiltered); n > 0 {
		c.atSel = (c.atSel + d + n) % n
	}
}

// attachPicked attaches the highlighted file and removes the "@fragment"
// from the draft tail (the @ plus fragment ARE the last runes —
// cursor-at-tail). Enter and tab land here, so quick double-attaches need
// no re-navigation.
func (c *Chat) attachPicked() tea.Cmd {
	if len(c.atFiltered) == 0 {
		c.closeAttachPicker()
		return nil
	}
	path := c.atFiltered[c.atSel]
	r := []rune(c.ta.Value())
	drop := len([]rune(c.atFrag)) + 1 // fragment + the "@" itself
	if drop <= len(r) {
		c.ta.SetValue(string(r[:len(r)-drop]))
	}
	c.closeAttachPicker()
	return c.addAttachment(chatAttachment{name: path, mime: attachMime(path), path: path})
}

// ---------------------------------------------------------------- chip staging

// addAttachment stages one chip (notice + oldest-eviction past the cap —
// the newest paste always wins, like a clipboard history ring) and splits
// the layout for the chips row(s).
func (c *Chat) addAttachment(a chatAttachment) tea.Cmd {
	var cmds []tea.Cmd
	for len(c.atts) >= maxAttachments {
		old := c.atts[0]
		c.atts = c.atts[1:]
		c.cleanupAttachment(old) // dropped before any send: remove its temp dir NOW
		if cmd := c.noticef("attachment cap " + itoa(maxAttachments) + " — dropped oldest: " + old.name); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	c.atts = append(c.atts, a)
	c.SetSize(c.w, c.h)
	return tea.Batch(cmds...)
}

// popAttachment removes the LAST chip (backspace on an empty draft) with
// a notice — the quiet undo for a mis-paste.
func (c *Chat) popAttachment() tea.Cmd {
	if len(c.atts) == 0 {
		return nil
	}
	last := c.atts[len(c.atts)-1]
	c.atts = c.atts[:len(c.atts)-1]
	c.cleanupAttachment(last)
	c.SetSize(c.w, c.h)
	return c.noticef("removed attachment " + last.name)
}

// drainAttachments hands the staged chips to the caller (send or enqueue)
// as state.Attachments and clears the row. Temp paste dirs are NOT
// removed here: the owning send closure cleans them up after the backend
// call resolves (a queued flush must still find the file).
func (c *Chat) drainAttachments() []state.Attachment {
	if len(c.atts) == 0 {
		return nil
	}
	out := make([]state.Attachment, len(c.atts))
	for i, a := range c.atts {
		out[i] = state.Attachment{Name: a.name, Mime: a.mime, Path: a.path, Temp: a.temp}
	}
	c.atts = nil
	c.SetSize(c.w, c.h)
	return out
}

// ClearAttachments drops every staged chip (/clear) — their temp dirs go
// NOW because no send will ever come for them.
func (c *Chat) ClearAttachments() {
	if len(c.atts) == 0 && !c.atOpen {
		return
	}
	for _, a := range c.atts {
		c.cleanupAttachment(a)
	}
	c.atts = nil
	c.atOpen = false
	c.SetSize(c.w, c.h)
}

// cleanupAttachment removes a chip's temp dir, best effort (no-op for
// @-picked files, which are the repo's own).
func (c *Chat) cleanupAttachment(a chatAttachment) {
	if a.temp != "" {
		_ = os.RemoveAll(a.temp)
	}
}

// ---------------------------------------------------------------- render: chips + popover

// chipsLines lays the staged attachments out as one "📎 a · b · c" row
// wrapped to the panel width, folding overflow into a trailing "(+N)"
// past chipMaxRows. Empty slice → nothing staged. The wrapped row count
// is the SetSize layout budget (chipsH) — render and budget share this.
func (c *Chat) chipsLines() []string {
	if len(c.atts) == 0 {
		return nil
	}
	items := make([]string, len(c.atts))
	for i, a := range c.atts {
		label := a.name
		if strings.HasPrefix(a.mime, "image/") {
			label += " (" + a.mime + ")"
		}
		items[i] = label
	}
	// greedy fit: keep as many leading chips as the row budget allows.
	// NB wrapChips — NOT wrapPlain: the (+N) accounting assumes the old
	// greedy wrap where ONE over-wide chip is a single (later-clipped)
	// row, not split into many
	shown := len(items)
	for shown > 1 && len(strings.Split(wrapChips("📎 "+strings.Join(items[:shown], " · "), c.w), "\n")) > chipMaxRows {
		shown--
	}
	text := "📎 " + strings.Join(items[:shown], " · ")
	if rest := len(items) - shown; rest > 0 {
		text += "  ·  (+" + itoa(rest) + ")"
	}
	lines := strings.Split(strings.TrimRight(wrapChips(text, c.w), "\n"), "\n")
	for i, ln := range lines {
		// a single chip wider than the panel wraps whole on wrapChips —
		// clip it (the narrow-sidebar degrade rule: never overflow)
		lines[i] = clipPlain(ln, c.w)
	}
	return lines
}

// wrapChips greedy word-wraps plain text to w cells WITHOUT the
// hard-split foldWrap applies: a chip wider than the row stays whole on
// its own row so chipsLines can CLIP it (chips degrade to "(+N)", not to
// a stack of file-path fragments).
func wrapChips(s string, w int) string {
	if w < 4 {
		w = 4
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		var cur strings.Builder
		curW := 0
		for _, word := range strings.Fields(para) {
			ww := lipgloss.Width(word)
			switch {
			case curW == 0:
				cur.WriteString(word)
				curW = ww
			case curW+1+ww <= w:
				cur.WriteString(" " + word)
				curW += 1 + ww
			default:
				out = append(out, cur.String())
				cur.Reset()
				cur.WriteString(word)
				curW = ww
			}
		}
		out = append(out, cur.String())
	}
	return strings.Join(out, "\n")
}

// chipsH — rows the chips line consumes (SetSize budget): 0 when nothing
// is staged, the wrapped count otherwise.
func (c *Chat) chipsH() int { return len(c.chipsLines()) }

// popoverH — rows the open picker consumes (SetSize budget): header +
// list window (1 "no matches" row when empty) + count footer + the
// PanelBox border pair.
func (c *Chat) popoverH() int {
	if !c.atOpen {
		return 0
	}
	rows := len(c.atFiltered)
	if rows > atVisibleRows {
		rows = atVisibleRows
	}
	if rows == 0 {
		rows = 1
	}
	return rows + 4
}

// renderAttachChips draws the chips rows (dim, plain-wrapped).
func (c *Chat) renderAttachChips() string {
	lines := c.chipsLines()
	for i := range lines {
		lines[i] = chrome.DimText.Render(lines[i])
	}
	return strings.Join(lines, "\n")
}

// atWindow returns the visible slice of the filtered list plus its start
// index — the selection always stays inside the atVisibleRows window.
func (c *Chat) atWindow() ([]string, int) {
	start := 0
	if c.atSel >= atVisibleRows {
		start = c.atSel - atVisibleRows + 1
	}
	end := start + atVisibleRows
	if end > len(c.atFiltered) {
		end = len(c.atFiltered)
	}
	if start > end {
		start = end
	}
	return c.atFiltered[start:end], start
}

// renderAttachPopover draws the picker as a bordered box (the textarea
// region's own row budget — popoverH): bold "attach file" header, up to
// atVisibleRows entries with the selected one accented under a "›"
// marker, and a dim "filtered/total" count footer.
func (c *Chat) renderAttachPopover() string {
	inner := c.w - 2 // PanelBox border columns
	if inner < 1 {
		inner = 1
	}
	lines := make([]string, 0, c.popoverH()-2)
	lines = append(lines, chrome.Header.Render(fitPlain("attach file", inner)))
	vis, start := c.atWindow()
	if len(c.atFiltered) == 0 {
		lines = append(lines, chrome.DimText.Render(fitPlain("(no matches)", inner)))
	}
	for i, p := range vis {
		idx := start + i
		if idx == c.atSel {
			lines = append(lines, chrome.AccentText.Render(fitPlain("› "+p, inner)))
		} else {
			lines = append(lines, fitPlain("  "+p, inner))
		}
	}
	footer := itoa(len(c.atFiltered)) + "/" + itoa(len(c.atFiles))
	lines = append(lines, chrome.DimText.Render(fitPlain(footer, inner)))
	return chrome.PanelBox.Width(c.w).Render(strings.Join(lines, "\n"))
}
