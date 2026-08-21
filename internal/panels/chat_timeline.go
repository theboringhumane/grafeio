// chat_timeline.go — the unified conversation timeline.
//
// The chat panel used to pin the per-agent subagent threads as a docked
// region AFTER the whole conversation, so every new message slid in above
// them and the threads never scrolled. mergeChatTimeline interleaves the
// visible chat entries and the thread groups into ONE ordered list:
// timestamp ascending (oldest top, newest bottom), stable for ties, so a
// thread renders as a normal conversation entry at its birth slot and the
// pinned dock is gone. The rendering itself (a thread's collapsed one-row
// summary or its live expanded block) is unchanged — only the position
// moves.
package panels

import (
	"sort"

	"github.com/theboringhumane/grafeio/internal/state"
)

// timelineItem is one unit of the merged chat timeline: either a regular
// conversation entry (Group < 0, Msg set) or a whole subagent thread
// (Group >= 0, indexing the threads slice mergeChatTimeline was called
// with — the thread renders as its ONE block at that slot).
type timelineItem struct {
	Msg   state.ChatMsg // valid when Group < 0
	Group int           // index into the caller's threads slice, -1 for a message
}

// mergeChatTimeline interleaves the visible conversation entries and the
// per-agent worker threads into one timeline, sorted by timestamp
// ascending (oldest first), stable for equal timestamps. Sort keys:
//
//   - a chat entry sorts by its At;
//   - a thread sorts by the EARLIEST At found among its entries — its
//     creation time — so a live thread keeps its birth slot instead of
//     swimming down the conversation with every new tool call (a merged
//     update rewrites the entry's At; the earliest one still available is
//     the closest thing to the thread's enqueue time). A thread with no
//     usable timestamp keys to 0 — it lands before stamped entries but
//     keeps input order among the stamp-less default-rendered history.
//
// Pure: the inputs are never mutated (the sort runs over an index array).
func mergeChatTimeline(chat []state.ChatMsg, threads []workerGroup) []timelineItem {
	items := make([]timelineItem, 0, len(chat)+len(threads))
	keys := make([]int64, 0, len(chat)+len(threads))
	for _, m := range chat {
		items = append(items, timelineItem{Msg: m, Group: -1})
		keys = append(keys, m.At)
	}
	for i, g := range threads {
		items = append(items, timelineItem{Group: i})
		at := int64(0)
		for j, m := range g.lines {
			if j == 0 || m.At < at {
				at = m.At
			}
		}
		keys = append(keys, at)
	}
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	// stable: equal timestamps keep input order (chat first, threads
	// second), so tie-stamped history renders exactly as the slice order.
	sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	out := make([]timelineItem, len(items))
	for i, j := range idx {
		out[i] = items[j]
	}
	return out
}
