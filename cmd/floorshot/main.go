// floorshot — freeze-frame renderer for the office floor: prints styled +
// plain frames for a scripted seed state at the standard shell sizes.
//
//	floorshot --size 120x26   (default; also 84x22, 140x30, 150x28)
//
// Shots: A = steady frame (one dev working, lit monitor), B = blink frame
// (floating sleep-z's), C = meeting at the boss desk (nameplate [meetin]),
// D = exec frame (A's steady seed + the CTO seated in his suite).
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/theboringhumane/theboringoffice/internal/office"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

func parseSize(s string) (int, int, error) {
	w, h, ok := strings.Cut(s, "x")
	if !ok {
		return 0, 0, fmt.Errorf("bad size %q (want WxH)", s)
	}
	wi, err1 := strconv.Atoi(w)
	hi, err2 := strconv.Atoi(h)
	if err1 != nil || err2 != nil || wi < 1 || hi < 1 {
		return 0, 0, fmt.Errorf("bad size %q (want WxH)", s)
	}
	return wi, hi, nil
}

// seed — the scripted office: boss + hr + 4 devs + 1 scout + 1 reviewer,
// 2 dispatches (in-progress tasks), one returned mail, one ambient bubble.
func seed(tick int) state.OfficeState {
	return state.OfficeState{
		Mode: state.ModeDemo,
		Tick: tick,
		Employees: []state.Employee{
			{ID: "boss", Name: "boss", Role: state.RoleManager, Seat: "manager", Sprite: state.SpriteAtDesk},
			{ID: "hr", Name: "hr", Role: state.RoleHR, Seat: "hr", Sprite: state.SpriteAtDesk},
			{ID: "tekton-1", Name: "tekton-1", Role: state.RoleDeveloper, Seat: "dev-1", Sprite: state.SpriteWorking, Task: "dispatch-1"},
			{ID: "tekton-2", Name: "tekton-2", Role: state.RoleDeveloper, Seat: "dev-2", Sprite: state.SpriteWorking, Task: "dispatch-2"},
			{ID: "tekton-3", Name: "tekton-3", Role: state.RoleDeveloper, Seat: "dev-3", Sprite: state.SpriteAtDesk},
			{ID: "tekton-4", Name: "tekton-4", Role: state.RoleDeveloper, Seat: "dev-4", Sprite: state.SpriteAtDesk},
			{ID: "skopos", Name: "skopos", Role: state.RoleScout, Seat: "scout-1", Sprite: state.SpriteAtDesk},
			{ID: "dikastes", Name: "dikastes", Role: state.RoleReviewer, Seat: "cabin-2", Sprite: state.SpriteAtDesk},
		},
		Tasks: []state.BoardTask{
			{ID: "dispatch-1", Title: "ship floorshot", Status: state.TaskInProgress, Owner: "tekton-1", At: 100},
			{ID: "dispatch-2", Title: "port floor grid", Status: state.TaskInProgress, Owner: "tekton-2", At: 120},
		},
		Mails: []state.MailItem{
			{ID: "mail-1", From: "tekton-1", To: "boss", At: 140, Subject: "dispatch-1 back", Kind: state.MailReturn},
		},
		Bubbles: []state.SpeechBubble{
			{ID: "bub-1", EmployeeID: "tekton-1", Text: "big day. lots", UntilTick: 100},
		},
		StatusLine: "demo floor",
	}
}

func main() {
	size := flag.String("size", "120x26", "grid size: 120x26 | 84x22 | 140x30 | 150x28")
	flag.Parse()
	W, H, err := parseSize(*size)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	shots := []struct {
		name  string
		state state.OfficeState
	}{
		{"A", seed(2)},  // steady frame: typing arms mid-beat, lit monitors
		{"B", seed(16)}, // blink frame: sleep-z's float above the idle sprites
		{"C", func() state.OfficeState { // reviewer at the boss desk: [meetin]
			st := seed(4)
			for i := range st.Employees {
				if st.Employees[i].ID == "dikastes" {
					st.Employees[i].Sprite = state.SpriteMeeting
				}
			}
			return st
		}()},
		{"D", func() state.OfficeState { // exec frame: the CTO seated in his suite
			st := seed(2) // A's steady tick; append-only, seed() stays CTO-free
			st.Employees = append(st.Employees, state.Employee{
				ID: "theboringcto", Name: "theboringcto", Role: state.RoleCTO, Seat: "cto", Sprite: state.SpriteAtDesk,
			})
			return st
		}()},
	}

	for _, shot := range shots {
		rows := office.BuildRows(shot.state, W, H)
		fmt.Printf("===== SHOT %s =====\n", shot.name)
		fmt.Println("[STYLED]")
		fmt.Println(office.Styled(rows))
		fmt.Println("[PLAIN]")
		fmt.Println(office.Styleless(rows))
	}
}
