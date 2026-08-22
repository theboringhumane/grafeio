// cto.go — the office CTO ("theboringcto"): the batch-review beat and its
// once-per-drained-board latch, shared by the scripted tour (demo.go) and
// the live backend (opencode.go). Architecture-brief routing itself lives
// in state.IsArchitectureBrief — the ONE matcher, never re-checked locally.
//
// The beat: when the LAST open brief of a batch returns and the board
// drains (zero tasks in non-done states), the CTO reviews the batch — one
// EvStatus activity note plus one EvMail (MailNotice) summarizing it. The
// latch re-arms only on a NEW dispatch: a board that stays drained never
// re-reviews, and repeated drains without new work post nothing.
package backend

import (
	"fmt"

	"github.com/theboringhumane/theboringoffice/internal/state"
)

// ctoName is the CTO's office identity (roster name + mail sender).
const ctoName = "theboringcto"

// reviewLatch — the once-per-drained-board latch. Arm on any dispatch;
// beat spends it the moment the board reads drained.
type reviewLatch struct {
	armed bool // a dispatch primed an unreviewed batch
	seq   int  // batches reviewed so far (mail-id dedupe across batches)
}

func (l *reviewLatch) arm() { l.armed = true }

// beat returns the CTO's review events exactly once per drained batch:
// nil while work is still open, while the board is empty, or after a drain
// already reviewed (no new dispatch in between).
func (l *reviewLatch) beat(open, done int) []state.Event {
	if !l.armed || open != 0 || done == 0 {
		return nil
	}
	l.armed = false
	l.seq++
	return ctoReviewEvents(done, l.seq)
}

// countBoard buckets the brief board (both backends keep a
// map[string]state.BoardTask: demo taskByID, live ctx.tasks).
func countBoard(tasks map[string]state.BoardTask) (open, done int) {
	for _, t := range tasks {
		if t.Status == state.TaskDone {
			done++
		} else {
			open++
		}
	}
	return open, done
}

// ctoReviewEvents builds the beat itself: the activity-line status note,
// then the review mail. n counts the briefs in the drained batch; seq
// keeps mail ids unique across batches.
func ctoReviewEvents(n, seq int) []state.Event {
	word := "tasks"
	if n == 1 {
		word = "task"
	}
	mail := state.MailItem{
		ID:      fmt.Sprintf("cto-review-%d", seq),
		From:    ctoName,
		To:      "manager",
		At:      nowMs(),
		Subject: fmt.Sprintf("reviewed: %d %s — architecture OK", n, word),
		Body: fmt.Sprintf("Batch drained clean: all %d %s returned, nothing left open on the board. "+
			"Returns spot-checked — structure, naming and boundaries hold. Architecture OK. — %s", n, word, ctoName),
		Kind: state.MailNotice,
	}
	return []state.Event{
		{Kind: state.EvStatus, Text: fmt.Sprintf("[office] %s reviewed the drained batch: %d %s — board clear", ctoName, n, word)},
		{Kind: state.EvMail, Mail: mail},
	}
}
