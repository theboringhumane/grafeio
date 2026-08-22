// digest.go — the render-skip seam: Frame() hashes everything its pixels
// depend on into one cheap digest; an unchanged digest returns the cached
// frame string verbatim, no render cost. st.Tick is always in the digest
// (sprite beats, wall clock, blink-z's, the typing row and think-frame /
// tool spinners animate with it); panel ephemera the state can't see
// (textarea draft, scroll offsets, spinner frames) are covered by
// m.frameNonce, bumped on every message that routes keys/mouse/spinner
// into the tabs or mutates panels directly.
package app

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/theboringhumane/theboringoffice/internal/projinfo"
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
	fmt.Fprintf(h, "|%s|%s|%d|%d|%t|%t|%t|%t", m.st.Mode, m.st.StatusLine, len(m.queue), m.st.Tick, m.st.BossThinking, m.st.BossDelegating, m.permQ.front() != nil, m.question != nil)
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
	// The permission queue is model-owned UI state too: the popover shows
	// pending[0] ("1 of N"), and answers/esc's/resolutions swap the front
	// or shrink the piles WITHOUT any state-owned term above moving — hash
	// front id + both pile depths (pending AND esc'd children included).
	frontID := ""
	if p := m.permQ.front(); p != nil {
		frontID = p.ID
	}
	fmt.Fprintf(h, "|%d|%s|%s|%t|%t|%s|%d|%d", m.activityAdds, m.bossName, PowerMode(m.cfg), m.zen, m.compact(),
		frontID, len(m.permQ.pending), len(m.permQ.escd))
	// Top bar's project+branch segment: a `git checkout` must repaint the
	// bar even when no office term above moved (TTL-bounded by projinfo).
	if pj := m.projInfo(); pj != (projinfo.Info{}) {
		fmt.Fprintf(h, "|%s|%s", pj.Project, pj.Branch)
	}
	return h.Sum64()
}
