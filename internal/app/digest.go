// digest.go — the render-skip seam: Frame() hashes everything its pixels
// depend on into one cheap digest; an unchanged digest returns the cached
// frame string verbatim, no render cost. st.Tick is always in the digest
// (sprite beats, wall clock, blink-z's, stream carets animate with it);
// panel ephemera the state can't see (textarea draft, scroll offsets,
// spinner frames) are covered by m.frameNonce, bumped on every message
// that routes keys/mouse/spinner into the tabs or mutates panels directly.
package app

import (
	"fmt"
	"hash/fnv"
	"time"
)

// governor — the power/caching bookkeeping, pointer-shared across the
// Model value copies (Init/Update hand copies around; the cache and the
// idle clock must survive them).
type governor struct {
	lastBusy    time.Time // last moment officeBusy saw life (drift clock)
	tickCount   int       // tick commands armed this run (uisot proof)
	frameKey    uint64
	frameCached string
	frameHits   uint64
	frameMisses uint64
}

// frameDigest — cheap hash of every pixel source Frame() reads.
func (m *Model) frameDigest() uint64 {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%d|%d|%d|%d|%d", m.width, m.height, m.sidebar, m.floorW, m.tabs.ActiveIndex(), m.frameNonce)
	fmt.Fprintf(h, "|%s|%s|%d|%d|%t|%t|%t|%t", m.st.Mode, m.st.StatusLine, len(m.queue), m.st.Tick, m.st.BossThinking, m.st.BossDelegating, m.perm != nil, m.question != nil)
	for _, e := range m.st.Employees {
		fmt.Fprintf(h, "|%s%s%s%s", e.ID, e.Sprite, e.Seat, e.Task)
	}
	for _, c := range m.st.Chat {
		fmt.Fprintf(h, "|%s%t%s%d%s", c.ID, c.Pending, c.Kind, len(c.Text), c.Meta)
	}
	for _, b := range m.st.Bubbles {
		fmt.Fprintf(h, "|%s%d", b.ID, b.UntilTick)
	}
	for _, t := range m.st.Tasks {
		fmt.Fprintf(h, "|%s%s%s%s", t.ID, t.Status, t.Title, t.Owner)
	}
	for _, ml := range m.st.Mails {
		fmt.Fprintf(h, "|%s%s", ml.ID, ml.Subject)
	}
	// activity lines are appended per processed event even when the event
	// leaves OfficeState untouched (child permissions/questions) — count them.
	// zen (sidebar-hidden fullscreen) and the compact layout change pixels
	// the digest's geometry terms (sidebar/floorW) only see AFTER a resize —
	// fold the mode flags in directly so the first toggled frame re-renders.
	fmt.Fprintf(h, "|%d|%s|%s|%t|%t", m.activityAdds, m.bossName, PowerMode(m.cfg), m.zen, m.compact())
	return h.Sum64()
}
