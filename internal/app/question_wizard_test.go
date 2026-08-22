// question_wizard_test.go — the boss question flow is a WIZARD, not a
// single free-text modal: a question.asked request pages its structured
// question items through the chat panel's question popover one at a time
// (each page records its own answers entry), the LAST page submits the
// accumulated [][]string answer set once per batched wire request id,
// esc defers the whole request (pages + recorded answers intact), and
// /question resumes at the first unanswered page.
package app

import (
	"testing"

	"github.com/theboringhumane/theboringoffice/internal/panels"
	"github.com/theboringhumane/theboringoffice/internal/state"
)

func TestQuestionWizardTwoPageSubmit(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	// one request, two structured pages: radio, then free text
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-1",
		EmployeeName: "boss", ToolState: "pending",
		Questions: []state.QuestionItem{
			{Question: "which branch?", Options: []state.QuestionOption{{Label: "main"}, {Label: "release"}}},
			{Question: "anything else?"},
		}})

	if m.question == nil {
		t.Fatal("a pending boss question must open the wizard hold")
	}
	if m.question.Cursor != 0 || len(m.question.Items) != 2 || len(m.question.Answers) != 2 {
		t.Fatalf("fresh hold: cursor 0, 2 pages, 2 answer slots — got %+v", m.question)
	}
	if m.chat == nil {
		t.Fatal("chat panel must exist")
	}
	v := m.questionView(m.question)
	if v.Kind != panels.QuestionKindRadio || v.Index != 1 || v.Total != 2 || v.Question != "which branch?" {
		t.Fatalf("page 1 must render as the radio page 1/2: %+v", v)
	}
	if len(b.qAnswers) != 0 {
		t.Fatal("no answer set may ship before the last page")
	}

	// an EMPTY submission (no picks, no text) is a no-op: same page
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{}})
	if m.question == nil || m.question.Cursor != 0 {
		t.Fatalf("empty answer must not advance the wizard: %+v", m.question)
	}

	// page 1 answered → the wizard advances to page 2 of 2
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Picks: []string{"release"}}})
	if m.question == nil || m.question.Cursor != 1 {
		t.Fatalf("page 1 answered: the wizard must show page 2 — got %+v", m.question)
	}
	v = m.questionView(m.question)
	if v.Kind != panels.QuestionKindText || v.Index != 2 || v.Total != 2 {
		t.Fatalf("page 2 must render as the free-text page 2/2: %+v", v)
	}
	if len(m.question.Answers[0]) != 1 || m.question.Answers[0][0] != "release" {
		t.Fatalf("page 1 must record its pick: %+v", m.question.Answers)
	}
	if len(b.qAnswers) != 0 {
		t.Fatal("page 1 alone must not submit")
	}

	// page 2 answered → the accumulated set ships ONCE for the one id
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Text: "ship it tonight"}})
	if m.question != nil {
		t.Fatalf("last page answered: the hold must close — got %+v", m.question)
	}
	if len(b.qAnswers) != 1 || b.qAnswers[0].id != "que-1" {
		t.Fatalf("want ONE AnswerQuestion(que-1, …), got %+v", b.qAnswers)
	}
	got := b.qAnswers[0].answers
	if len(got) != 2 || got[0][0] != "release" || len(got[1]) != 1 || got[1][0] != "ship it tonight" {
		t.Fatalf("want [[release] [ship it tonight]], got %+v", got)
	}

	// the member's answer set joins the transcript as a user bubble
	last := m.st.Chat[len(m.st.Chat)-1]
	if last.From != "user" || last.Text != "release · ship it tonight" {
		t.Fatalf("want the joined answer bubble, got %+v", last)
	}
}

func TestQuestionWizardCheckboxPage(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	// one checkbox page: Multiple → every pick records in the page's array
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-7",
		EmployeeName: "boss", ToolState: "pending",
		Questions: []state.QuestionItem{{
			Question: "which files?", Multiple: true,
			Options: []state.QuestionOption{{Label: "a.go"}, {Label: "b.go"}, {Label: "c.go"}},
		}}})

	v := m.questionView(m.question)
	if v.Kind != panels.QuestionKindCheckbox {
		t.Fatalf("a multiple page must classify checkbox: %+v", v)
	}
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Picks: []string{"a.go", "c.go"}}})
	if len(b.qAnswers) != 1 || len(b.qAnswers[0].answers) != 1 {
		t.Fatalf("the one checkbox page submits ONE page-array: %+v", b.qAnswers)
	}
	set := b.qAnswers[0].answers[0]
	if len(set) != 2 || set[0] != "a.go" || set[1] != "c.go" {
		t.Fatalf("checkbox picks ship in one page-array: %+v", set)
	}
}

func TestQuestionWizardBatchedIDs(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	// a question call batching several wire ids emits one event per id —
	// the second id folds into the open hold (today's v1 semantics) and
	// its page joins the wizard as an EXTRA page.
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-1",
		EmployeeName: "boss", ToolState: "pending", Text: "first?"})
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-2",
		EmployeeName: "boss", ToolState: "pending", Text: "second?"})
	if m.question == nil || len(m.question.IDs) != 2 || len(m.question.Items) != 2 {
		t.Fatalf("the batched second id folds in: 2 ids, 2 pages — got %+v", m.question)
	}
	// a REPEAT of an already-known id folds nothing twice
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-2",
		EmployeeName: "boss", ToolState: "pending", Text: "second?"})
	if len(m.question.IDs) != 2 || len(m.question.Items) != 2 {
		t.Fatalf("a duplicate id must not re-fold: %+v", m.question)
	}

	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Text: "one"}})
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Text: "two"}})

	// the SAME accumulated answer set ships once per batched wire id
	if len(b.qAnswers) != 2 || b.qAnswers[0].id != "que-1" || b.qAnswers[1].id != "que-2" {
		t.Fatalf("want one AnswerQuestion per batched id, got %+v", b.qAnswers)
	}
	for _, call := range b.qAnswers {
		if len(call.answers) != 2 || call.answers[0][0] != "one" || call.answers[1][0] != "two" {
			t.Fatalf("batched ids share the full answer set: %+v", call)
		}
	}
}

func TestQuestionWizardEscResume(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-9",
		EmployeeName: "boss", ToolState: "pending",
		Questions: []state.QuestionItem{
			{Question: "p1?", Options: []state.QuestionOption{{Label: "x"}, {Label: "y"}}},
			{Question: "p2?"},
		}})

	// page 1 answered, then esc: the whole request parks intact
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Picks: []string{"y"}}})
	m = runMsg(t, m, questionLaterMsg{})
	if m.question != nil {
		t.Fatalf("esc must close the popover: %+v", m.question)
	}
	if m.questionEscd == nil || len(m.questionEscd.Answers[0]) != 1 || m.questionEscd.Answers[0][0] != "y" {
		t.Fatalf("esc parks the hold WITH page 1's recorded answer: %+v", m.questionEscd)
	}
	if len(b.qAnswers) != 0 {
		t.Fatal("esc must not submit")
	}

	// /question re-opens at the FIRST UNANSWERED page (2): page 1's
	// recorded answer is preserved, not re-asked.
	m = runMsg(t, m, slashMsg{text: "/question"})
	if m.question == nil || m.questionEscd != nil {
		t.Fatalf("/question must re-open the parked hold: q=%+v escd=%+v", m.question, m.questionEscd)
	}
	if m.question.Cursor != 1 {
		t.Fatalf("/question resumes at the first unanswered page (2/2) — cursor %d", m.question.Cursor)
	}
	if len(m.question.Answers[0]) != 1 || m.question.Answers[0][0] != "y" {
		t.Fatalf("page 1's answer survives the defer: %+v", m.question.Answers)
	}

	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Text: "fine"}})
	if len(b.qAnswers) != 1 {
		t.Fatalf("the resumed wizard submits on the last page: %+v", b.qAnswers)
	}
	set := b.qAnswers[0].answers
	if len(set) != 2 || set[0][0] != "y" || set[1][0] != "fine" {
		t.Fatalf("the deferred pick rides the final answer set: %+v", set)
	}
}

func TestQuestionWizardLegacyFlattened(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	// a legacy/flattened event (Text only, no structured Questions)
	// degrades to a single free-text page.
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-old",
		EmployeeName: "boss", ToolState: "pending", Text: "ship it?"})
	if m.question == nil || len(m.question.Items) != 1 {
		t.Fatalf("a flattened event must open one page: %+v", m.question)
	}
	v := m.questionView(m.question)
	if v.Kind != panels.QuestionKindText || v.Total != 1 || v.Question != "ship it?" {
		t.Fatalf("Text-only flattens to a single free-text page: %+v", v)
	}
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Text: "yes"}})
	if len(b.qAnswers) != 1 || b.qAnswers[0].answers[0][0] != "yes" {
		t.Fatalf("the legacy page submits like any other: %+v", b.qAnswers)
	}

	// a flat "a | b | c" ToolSummary migrates to RADIO options (the
	// "free-form answer" sentinel stays a text page)
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-opts",
		EmployeeName: "boss", ToolState: "pending", Text: "which?",
		ToolSummary: "alpha | beta | gamma"})
	v = m.questionView(m.question)
	if v.Kind != panels.QuestionKindRadio || len(v.Options) != 3 || v.Options[1].Label != "beta" {
		t.Fatalf("flat options split on \" | \" become radio options: %+v", v)
	}
	m = runMsg(t, m, questionAnswerMsg{ans: panels.QuestionAnswer{Picks: []string{"beta"}}})

	// the "free-form answer" sentinel is NOT an options list
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-free",
		EmployeeName: "boss", ToolState: "pending", Text: "why?",
		ToolSummary: "free-form answer"})
	v = m.questionView(m.question)
	if v.Kind != panels.QuestionKindText || len(v.Options) != 0 {
		t.Fatalf("\"free-form answer\" is not an options list: %+v", v)
	}
}

func TestQuestionWizardResolvedClosesHold(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-5",
		EmployeeName: "boss", ToolState: "pending", Text: "open?"})
	if m.question == nil {
		t.Fatal("hold must open")
	}
	// a resolved event for ANY batched id closes the open hold
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-5",
		EmployeeName: "boss", ToolSummary: "answered", ToolState: "resolved"})
	if m.question != nil {
		t.Fatalf("resolved must close the open hold: %+v", m.question)
	}
	if len(b.qAnswers) != 0 {
		t.Fatal("the backend's own resolution never ships an app answer")
	}

	// …and an ESC'D hold clears the same way (open + defer, resolve out)
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-6",
		EmployeeName: "boss", ToolState: "pending", Text: "later?"})
	m = runMsg(t, m, questionLaterMsg{})
	if m.questionEscd == nil {
		t.Fatal("esc'd hold must park")
	}
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-6",
		EmployeeName: "boss", ToolSummary: "answered", ToolState: "resolved"})
	if m.questionEscd != nil {
		t.Fatalf("resolved must clear the esc'd hold too: %+v", m.questionEscd)
	}
}

func TestQuestionWizardChildQuestionNoPopover(t *testing.T) {
	b := &recBackend{}
	m := New(b, nil)

	// a CHILD (employee) question never opens the popover — activity
	// line only, exactly like employee thoughts/permissions.
	m = runMsg(t, m, state.Event{Kind: state.EvQuestion, QuestionID: "que-child",
		EmployeeName: "tekton-1", ToolState: "pending", Text: "child asks?"})
	if m.question != nil {
		t.Fatalf("a child question must stay popover-less: %+v", m.question)
	}
	if len(b.qAnswers) != 0 {
		t.Fatal("a child question never submits")
	}
}
