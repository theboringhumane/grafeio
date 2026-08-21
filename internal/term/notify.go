// notify.go — a tiny package-level repaint channel so the PTY reader's
// data-landing point (Grid.Write) can nudge the app to repaint the active
// TERM tab without the panel owning a tea.Program.
//
// Contract: every Grid.Write that applies bytes calls publish() once
// (non-blocking; drops when the buffer is full — a later Write re-nudges).
// The consumer drains it per tick and repaints when the TERM tab is active.
//
// APP WIRING (manager-owned, one line — the model's Update/tick):
//
//	if term.Drain() /* && term tab active */ {
//		// invalidate panel cache / repaint — e.g. trigger the state push
//	}
//
// Correctness never depends on the consumer: grid + scrollback state are
// always coherent, so any eventual tick paints a true frame — Drain is
// purely the smoothness fast path.
package term

// notifyCh carries repaint signals; deep buffer so publish never stalls
// the reader goroutine behind a slow frame.
var notifyCh = make(chan struct{}, 256)

// NotifyCh exposes the raw channel for consumers that prefer select.
func NotifyCh() <-chan struct{} { return notifyCh }

// publish nudges the app. Non-blocking: a full buffer means a repaint is
// already pending, so dropping is harmless.
func publish() {
	select {
	case notifyCh <- struct{}{}:
	default:
	}
}

// Drain empties the channel and reports whether at least one repaint was
// pending. Call from the app's Update/tick handler on the TERM tab.
func Drain() bool {
	drained := false
	for {
		select {
		case <-notifyCh:
			drained = true
		default:
			return drained
		}
	}
}
