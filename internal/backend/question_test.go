// question_test.go — unit proofs for the structured question path: the
// question.asked normalizer keeps the legacy flattened Text/ToolSummary
// AND populates the new Questions pages (radio/checkbox/textarea), and the
// live reply ships the per-question answer arrays verbatim (string[][],
// multi-select labels riding together in one page's slot).
package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/config"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

// wireQuestion builds one ocQuestionInfo the way the SSE JSON decoder would.
func wireQuestion(question, header string, multiple bool, opts ...[2]string) ocQuestionInfo {
	var q ocQuestionInfo
	q.Question = question
	q.Header = header
	q.Multiple = multiple
	for _, o := range opts {
		q.Options = append(q.Options, struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		}{Label: o[0], Description: o[1]})
	}
	return q
}

// TestMapQuestionAskedStructured: the event carries BOTH the untouched
// flattened one-liner and the structured pages (options, multiple, header);
// whitespace-only questions drop and never become blank pages.
func TestMapQuestionAskedStructured(t *testing.T) {
	ctx := newNormCtx(nil)
	p := ocQuestionReq{
		ID:        "que-1",
		SessionID: "sess-1",
		Questions: []ocQuestionInfo{
			wireQuestion(" pick one ", " scoping ", true, [2]string{"x", "x-desc"}, [2]string{"y", ""}),
			wireQuestion("tell me", "", false),
			wireQuestion("   ", "", false, [2]string{"ghost", ""}), // dropped: empty question
		},
	}
	evs := mapQuestionAsked(p, ctx, "sess-1")
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	ev := evs[0]

	// The legacy flattening is unchanged.
	if ev.Text != "pick one tell me" {
		t.Fatalf("flattened Text changed: %q", ev.Text)
	}
	// ...including the dropped page's option: the legacy flattening always
	// collected options even from blank questions, and that stays as-is.
	if ev.ToolSummary != "x | y | ghost" {
		t.Fatalf("flattened ToolSummary changed: %q", ev.ToolSummary)
	}
	if ev.ToolState != "pending" || ev.QuestionID != "que-1" {
		t.Fatalf("hold fields broken: state=%q id=%q", ev.ToolState, ev.QuestionID)
	}

	// The structured pages.
	if len(ev.Questions) != 2 {
		t.Fatalf("want 2 pages (ghost dropped), got %d", len(ev.Questions))
	}
	radio := ev.Questions[0]
	if radio.Question != "pick one" || radio.Header != "scoping" || !radio.Multiple {
		t.Fatalf("page 0 wrong: %+v", radio)
	}
	if len(radio.Options) != 2 || radio.Options[0].Label != "x" ||
		radio.Options[0].Description != "x-desc" || radio.Options[1].Label != "y" {
		t.Fatalf("page 0 options wrong: %+v", radio.Options)
	}
	free := ev.Questions[1]
	if free.Question != "tell me" || free.Header != "" || free.Multiple || len(free.Options) != 0 {
		t.Fatalf("free-text page wrong: %+v", free)
	}
}

// TestMapQuestionAskedAllEmpty: a request whose questions are all blank
// keeps the flattened fallback text and leaves Questions nil.
func TestMapQuestionAskedAllEmpty(t *testing.T) {
	ctx := newNormCtx(nil)
	p := ocQuestionReq{
		ID: "que-2", SessionID: "sess-1",
		Questions: []ocQuestionInfo{wireQuestion("  ", "", false)},
	}
	evs := mapQuestionAsked(p, ctx, "sess-1")
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Text != "question from the floor" {
		t.Fatalf("fallback text changed: %q", evs[0].Text)
	}
	if evs[0].Questions != nil {
		t.Fatalf("Questions must stay nil when every page drops: %+v", evs[0].Questions)
	}
}

// TestLiveBackendAnswerQuestionBody: the reply body IS the wire shape —
// {"answers":[["x","y"],["free"]]} for a 2-page answer whose first
// (multiple-select) page has two picks; no per-question single-wrap.
func TestLiveBackendAnswerQuestionBody(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("true"))
	}))
	defer srv.Close()

	b := newLiveBackend(srv.URL, t.TempDir(), config.Default())
	b.mu.Lock()
	b.baseURL = srv.URL // doJSON reads baseURL under mu; Start would spin SSE
	b.mu.Unlock()

	err := b.AnswerQuestion("que-1", [][]string{{"x", "y"}, {"free"}})
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if gotPath != "/question/que-1/reply" {
		t.Fatalf("wrong route: %q", gotPath)
	}
	want := `{"answers":[["x","y"],["free"]]}`
	var gotJSON, wantJSON any
	if err := json.Unmarshal([]byte(gotBody), &gotJSON); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatal(err)
	}
	gotCmp, _ := json.Marshal(gotJSON)
	wantCmp, _ := json.Marshal(wantJSON)
	if string(gotCmp) != string(wantCmp) {
		t.Fatalf("body mismatch:\n got: %s\nwant: %s", gotBody, want)
	}
}

// TestDemoBackendAnswerQuestionLog: the demo twin logs per-question picks
// "; "-separated with ", " inside a page ("a, b; c"), and resolves the hold.
func TestDemoBackendAnswerQuestionLog(t *testing.T) {
	b := newDemoBackend(config.Default())
	var events []state.Event
	b.fl.setEmit(func(e state.Event) { events = append(events, e) })

	b.mu.Lock()
	b.pendingQuestion["que-1"] = permHold{SessionID: "boss", EmployeeID: "boss", EmployeeName: "boss"}
	b.mu.Unlock()

	if err := b.AnswerQuestion("que-1", [][]string{{"a", "b"}, {"c"}}); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	var status string
	var resolved bool
	for _, e := range events {
		if e.Kind == state.EvStatus {
			status = e.Text
		}
		if e.Kind == state.EvQuestion && e.QuestionID == "que-1" && e.ToolState == "resolved" {
			resolved = true
		}
	}
	if status != "[demo] answered question que-1: a, b; c" {
		t.Fatalf("status line wrong: %q", status)
	}
	if !resolved {
		t.Fatal("missing resolved EvQuestion on que-1")
	}
}
