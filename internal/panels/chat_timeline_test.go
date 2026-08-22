// chat_timeline_test.go — merge proofs for the chat timeline: subagent
// thread groups (e.g. "tekton-18 · Fix Portuguese proof") must INTERLEAVE
// with the normal conversation by timestamp, ascending (oldest top,
// newest bottom) — the old pinned-bottom workers region must not
// determine ordering. An OLDER thread rises ABOVE newer chat messages (it
// never sinks to the pinned tail), a NEWER thread sits BELOW older ones,
// equal timestamps keep stable input order (the chat slice before the
// threads slice), and the merged list holds every input unit exactly
// once: len(mergeChatTimeline(c, t)) == len(c)+len(t), no duplicates.
//
// A thread sorts by the EARLIEST At among its lines — its birth slot —
// so late tool activity never swims it down past chat turns that arrived
// after the thread was born. Timestamps here are fixed literals — no
// time.Now(), the test is deterministic.
package panels

import (
	"strings"
	"testing"

	state "github.com/theboringhumane/theboringoffice/internal/state"
)

// timelineIDs flattens a merged timeline to one label per unit —
// "msg:<id>" for a conversation entry, "grp:<agent>" for a thread — so
// the interleave order can be asserted as one joined string.
func timelineIDs(items []timelineItem, threads []workerGroup) []string {
	labels := make([]string, len(items))
	for i, it := range items {
		if it.Group >= 0 {
			labels[i] = "grp:" + threads[it.Group].name
		} else {
			labels[i] = "msg:" + it.Msg.ID
		}
	}
	return labels
}

// TestMergeChatTimeline_Chronological drives the bug fixture: three
// thread groups from one subagent (tekton-18) whose birth times
// interleave the conversation. Under the pinned-bottom rendering every
// thread sat AFTER the last chat line — here they must land strictly by
// time, every input survives, and nothing is dropped or duplicated.
func TestMergeChatTimeline_Chronological(t *testing.T) {
	chat := []state.ChatMsg{
		{ID: "chat-1", From: "user", Kind: "user", Text: "hello boss", At: 1_000},
		{ID: "chat-2", From: "boss", Kind: "boss", Text: "on it", At: 3_000},
		{ID: "chat-3", From: "boss", Kind: "boss", Text: "draft pushed", At: 5_000},
		{ID: "chat-4", From: "boss", Kind: "boss", Text: "anything else?", At: 6_000},
	}
	threads := []workerGroup{
		// born before chat-2: must rise ABOVE it, not wait at the bottom
		{name: "old", lines: []state.ChatMsg{
			{ID: "w1", From: "tekton-18", Kind: wtoolKind, Text: "read · proof.go", At: 2_000},
			{ID: "w2", From: "tekton-18", Kind: wtoolKind, Text: "edit · proof.go", At: 2_500},
		}},
		// born at the same stamp as chat-4: the chat entry wins the tie, stably
		{name: "tie", lines: []state.ChatMsg{
			{ID: "w3", From: "tekton-18", Kind: wtoolKind, Text: "read · other.go", At: 6_000},
		}},
		// born after every chat line: lands at the bottom on TIME, not by pinning
		{name: "new", lines: []state.ChatMsg{
			{ID: "w4", From: "tekton-18", Kind: wtoolKind, Text: "write · final.go", At: 7_000},
		}},
	}

	got := mergeChatTimeline(chat, threads)

	// nothing lost, nothing duplicated: len == len(chat)+len(threads),
	// each msg ID and each group index used exactly once
	if len(got) != len(chat)+len(threads) {
		t.Fatalf("merged timeline has %d rows, want len(chat)+len(threads) = %d: %v",
			len(got), len(chat)+len(threads), timelineIDs(got, threads))
	}
	msgCount := map[string]int{}
	grpCount := map[int]int{}
	for _, it := range got {
		if it.Group >= 0 {
			grpCount[it.Group]++
		} else {
			msgCount[it.Msg.ID]++
		}
	}
	for _, m := range chat {
		if msgCount[m.ID] != 1 {
			t.Fatalf("chat entry %q appears %d times in the merged timeline: %v",
				m.ID, msgCount[m.ID], timelineIDs(got, threads))
		}
	}
	for i := range threads {
		if grpCount[i] != 1 {
			t.Fatalf("thread group %d (%q) appears %d times in the merged timeline: %v",
				i, threads[i].name, grpCount[i], timelineIDs(got, threads))
		}
	}

	// the interleave: grp:old (2s) between chat-1 (1s) and chat-2 (3s);
	// grp:tie (6s) after chat-4 (6s) on the stable tie; grp:new (7s)
	// last. Pinned-bottom would read
	// [msg:chat-1 msg:chat-2 msg:chat-3 msg:chat-4 grp:old grp:tie grp:new].
	want := strings.Join([]string{
		"msg:chat-1", "grp:old", "msg:chat-2", "msg:chat-3", "msg:chat-4", "grp:tie", "grp:new",
	}, ",")
	if gotIDs := strings.Join(timelineIDs(got, threads), ","); gotIDs != want {
		t.Fatalf("chronological interleave = [%s], want [%s]", gotIDs, want)
	}
}

// TestMergeChatTimeline_StableOnEqualTimestamps isolates the tie rule:
// at equal timestamps the output keeps input order — every chat entry
// (in its slice order) before every thread (in its slice order), never
// an unstable shuffle across the boundary.
func TestMergeChatTimeline_StableOnEqualTimestamps(t *testing.T) {
	chat := []state.ChatMsg{
		{ID: "chat-first", From: "user", Kind: "user", At: 100},
		{ID: "chat-mid", From: "boss", Kind: "boss", At: 200},
		{ID: "chat-last", From: "boss", Kind: "boss", At: 200},
	}
	threads := []workerGroup{
		{name: "a", lines: []state.ChatMsg{{ID: "wa", From: "tekton-1", Kind: wtoolKind, At: 200}}},
		{name: "b", lines: []state.ChatMsg{{ID: "wb", From: "tekton-2", Kind: wtoolKind, At: 200}}},
		{name: "c", lines: []state.ChatMsg{{ID: "wc", From: "tekton-3", Kind: wtoolKind, At: 300}}},
	}

	got := mergeChatTimeline(chat, threads)
	want := strings.Join([]string{
		"msg:chat-first", "msg:chat-mid", "msg:chat-last", "grp:a", "grp:b", "grp:c",
	}, ",")
	if gotIDs := strings.Join(timelineIDs(got, threads), ","); gotIDs != want {
		t.Fatalf("equal timestamps must keep stable input order = [%s], want [%s]", gotIDs, want)
	}
}

// TestMergeChatTimeline_ThreadKeepsBirthSlot pins the thread sort key: a
// thread interleaves by its EARLIEST line's At (its birth), not its
// latest activity — otherwise a long-running thread would glide down the
// conversation with every tool call, recreating the stuck-at-the-bottom
// feel of the pinned region. Here the thread was born BEFORE chat-1 but
// its last tool call is newer than chat-2: it must still lead.
func TestMergeChatTimeline_ThreadKeepsBirthSlot(t *testing.T) {
	chat := []state.ChatMsg{
		{ID: "chat-1", From: "user", Kind: "user", At: 1_000},
		{ID: "chat-2", From: "boss", Kind: "boss", At: 9_000},
	}
	threads := []workerGroup{
		{name: "born-early", lines: []state.ChatMsg{
			{ID: "w1", From: "tekton-18", Kind: wtoolKind, Text: "read · big.go", At: 500},
			{ID: "w2", From: "tekton-18", Kind: wtoolKind, Text: "edit · big.go", At: 4_000},
			{ID: "w3", From: "tekton-18", Kind: wtoolKind, Text: "bash · go test", At: 9_500},
		}},
	}

	got := mergeChatTimeline(chat, threads)
	want := strings.Join([]string{"grp:born-early", "msg:chat-1", "msg:chat-2"}, ",")
	if gotIDs := strings.Join(timelineIDs(got, threads), ","); gotIDs != want {
		t.Fatalf("a thread sorts by its birth slot = [%s], want [%s]", gotIDs, want)
	}
}
