package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"qcode/internal/control"
	"qcode/internal/lineedit"
	"qcode/internal/question"
	"qcode/internal/redaction"
	"qcode/internal/session"
)

func TestRestoredQuestionerUsesLiveUIAndInteractionBroker(t *testing.T) {
	for _, surface := range []string{"terminal", "browser"} {
		t.Run(surface, func(t *testing.T) {
			host := control.NewHost(context.Background(), 1)
			defer host.Shutdown()
			live := New(nil, nil, nil, "test", "model", ".")
			live.terminal = lineedit.NewTerminal(readWriter{Reader: live.input, Writer: io.Discard}, inputPrompt)
			live.activeAgent = "main"
			live.SetDetachedAgentManager(host)
			staged := New(nil, nil, nil, "test", "model", ".")
			staged.activeAgent = "main"
			// The factory installs the callback before the detached manager is
			// attached. Resume redirects this UI only after staging succeeds.
			questioner := staged.AgentQuestioner("main")
			staged.SetDetachedAgentManager(host)
			staged.sessionHost = live
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan questionResult, 1)
			go func() {
				answers, err := questioner(ctx, []question.Question{{Text: "Database?", Options: []string{"SQLite", "Postgres"}}})
				result <- questionResult{answers: answers, err: err}
			}()
			deadline := time.Now().Add(time.Second)
			for !live.hasPendingQuestion("main") || len(host.PendingInteractions()) == 0 {
				if staged.hasPendingQuestion("main") {
					t.Fatal("restored question was queued on the detached UI")
				}
				if time.Now().After(deadline) {
					t.Fatal("restored question did not reach the live UI and broker")
				}
				time.Sleep(time.Millisecond)
			}
			want := "SQLite"
			if surface == "terminal" {
				var wake [2]byte
				if _, err := io.ReadFull(live.input, wake[:]); err != nil {
					t.Fatal(err)
				}
				live.input.route([]byte("1\r"))
				live.handlePendingQuestions(ctx)
			} else {
				want = "Postgres"
				interaction := host.PendingInteractions()[0]
				if err := live.ResolveRemoteInteraction("browser", interaction.ID, []byte(`["Postgres"]`)); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-result:
				if got.err != nil || len(got.answers) != 1 || got.answers[0] != want {
					t.Fatalf("restored question result = %+v, want %s", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("restored questioner did not receive the answer")
			}
		})
	}
}

func TestQuestionnaireDefersForTabSwitch(t *testing.T) {
	input := newInterruptReader(nil)
	u := &UI{input: input, display: newHistoryWriter(io.Discard), drafts: map[string]string{"main": "unfinished prompt"}, activeAgent: "main", width: 80}
	u.terminal = lineedit.NewTerminal(readWriter{Reader: input, Writer: io.Discard}, "> ")
	input.setTabHandler(u.requestTabSwitch)
	result := make(chan error, 1)
	go func() {
		_, _, err := u.runQuestionnaireProgress(context.Background(), []question.Question{{Text: "Which flow?", AllowCustom: true}}, 0, nil, "Cancel", true)
		result <- err
	}()
	input.route([]byte(ctrlPageDownSequence))
	select {
	case err := <-result:
		if !errors.Is(err, errQuestionDeferred) {
			t.Fatalf("question was not deferred: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("tab switch did not release questionnaire")
	}
	if u.pendingTab != 1 || u.drafts["main"] != "unfinished prompt" {
		t.Fatalf("pending tab or draft lost: tab=%d drafts=%v", u.pendingTab, u.drafts)
	}
}

func TestQuestionnaireRestoresInterruptedDraft(t *testing.T) {
	input := newInterruptReader(nil)
	u := &UI{input: input, display: newHistoryWriter(io.Discard), drafts: map[string]string{}, activeAgent: "main", width: 80}
	u.terminal = lineedit.NewTerminal(readWriter{Reader: input, Writer: io.Discard}, "> ")
	request := &questionRequest{agentID: "main", questions: []question.Question{{Text: "Which flow?", AllowCustom: true}}, draft: "unfinished prompt", ctx: context.Background(), result: make(chan questionResult, 1)}
	u.questions = []*questionRequest{request}
	done := make(chan struct{})
	go func() {
		u.handlePendingQuestions(context.Background())
		close(done)
	}()
	input.route([]byte("OAuth\r"))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("question was not answered")
	}
	result := <-request.result
	if result.err != nil || len(result.answers) != 1 || result.answers[0] != "OAuth" {
		t.Fatalf("answer = %+v", result)
	}
	if u.drafts["main"] != "unfinished prompt" {
		t.Fatalf("draft = %q", u.drafts["main"])
	}
	restored := make([]byte, len(request.draft))
	if _, err := io.ReadFull(input, restored); err != nil || string(restored) != request.draft {
		t.Fatalf("restored input = %q, %v", restored, err)
	}
}

func TestNormalizeQuestionAnswer(t *testing.T) {
	options := []string{"SQLite", "Postgres"}
	for _, test := range []struct {
		answer string
		want   string
		valid  bool
	}{
		{answer: "1", want: "SQLite", valid: true},
		{answer: "postgres", want: "Postgres", valid: true},
		{answer: "Something else", want: "Something else", valid: true},
	} {
		got, valid := normalizeQuestionAnswer(test.answer, options)
		if got != test.want || valid != test.valid {
			t.Errorf("normalizeQuestionAnswer(%q) = %q, %v; want %q, %v", test.answer, got, valid, test.want, test.valid)
		}
	}
}

func TestNormalizeQuestionAnswerCanRequireOption(t *testing.T) {
	if got, valid := normalizeQuestionAnswerWithCustom("Something else", []string{"SQLite", "Postgres"}, false); valid || got != "" {
		t.Fatalf("strict answer = %q, %v; want empty, false", got, valid)
	}
}

func TestFormatQuestion(t *testing.T) {
	got := formatQuestion(question.Question{Text: "Which store?", Options: []string{"SQLite", "Postgres"}, AllowCustom: true}, 0, 2, 80)
	for _, want := range []string{"Question 1/2", "Which store?", "1) SQLite", "2) Postgres", "type your own answer", "Ctrl+C cancels these questions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("questionnaire %q does not contain %q", got, want)
		}
	}
}

func TestFormatQuestionWithOptionDescription(t *testing.T) {
	item := question.Question{Text: "What fails?", Options: []string{"Colored lines are missed", "Cursor codes appear"}, OptionDescriptions: []string{"The gag count stays zero", ""}, AllowCustom: true}
	got := formatQuestion(item, 0, 1, 80)
	if !strings.Contains(got, "Colored lines are missed — The gag count stays zero") {
		t.Fatalf("option description missing: %q", got)
	}
	if answer, valid := normalizeQuestionAnswer("1", item.Options); !valid || answer != "Colored lines are missed" {
		t.Fatalf("selected answer = %q, %v", answer, valid)
	}
}

func TestEscapeDefersQuestionWithoutAnswering(t *testing.T) {
	input := newInterruptReader(nil)
	u := &UI{input: input, display: newHistoryWriter(io.Discard), drafts: map[string]string{"main": "unfinished draft"}, deferredInteractions: make(map[string]bool), activeAgent: "main", width: 80}
	u.terminal = lineedit.NewTerminal(readWriter{Reader: input, Writer: io.Discard}, "> ")
	u.terminal.SubmitOnEscape = func() bool { return true }
	request := &questionRequest{agentID: "main", questions: []question.Question{{Text: "Which flow?", AllowCustom: true}}, draft: "unfinished draft", ctx: context.Background(), result: make(chan questionResult, 1)}
	u.questions = []*questionRequest{request}
	done := make(chan bool, 1)
	go func() { done <- u.handlePendingQuestions(context.Background()) }()
	input.route([]byte{29}) // interruptReader's decoded standalone Escape.
	select {
	case handled := <-done:
		if handled {
			t.Fatal("deferred question blocked composer")
		}
	case <-time.After(time.Second):
		t.Fatal("Escape did not open composer")
	}
	if !u.deferredInteractions["main"] || len(u.questions) != 1 || u.drafts["main"] != "unfinished draft" {
		t.Fatal("deferred interaction/draft lost")
	}
	select {
	case <-request.result:
		t.Fatal("Escape answered question")
	default:
	}
}

func startQuestionnaire(t *testing.T, u *UI, questions []question.Question) (<-chan questionResult, <-chan string) {
	t.Helper()
	u.input = newInterruptReader(nil)
	u.input.setPageHandler(u.showPage)
	u.input.setHistoryBoundaryHandler(u.showHistoryBoundary)
	u.input.setTabHandler(u.requestTabSwitch)
	u.terminal = lineedit.NewTerminal(readWriter{Reader: u.input, Writer: io.Discard}, inputPrompt)
	u.terminal.SubmitOnEscape = func() bool { return true }
	u.terminal.KeyHandler = u.handlePendingPanelKey
	frames := make(chan string, 16)
	u.terminal.RenderInput = func(prompt, line string, pos int) {
		u.renderInput(prompt, line, pos)
		if strings.HasPrefix(prompt, "Answer ") {
			select {
			case frames <- questionnaireFrame(u):
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan questionResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		answers, _, err := u.runQuestionnaireProgress(ctx, questions, 0, nil, "Ctrl+C cancels this request.", true)
		result <- questionResult{answers: answers, err: err}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("questionnaire did not stop")
		}
	})
	return result, frames
}

func questionnaireFrame(u *UI) string {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return strings.Join(u.inputScreenRows, "\n")
}

func waitQuestionnaireFrame(t *testing.T, frames <-chan string, text string) string {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case frame := <-frames:
			if strings.Contains(frame, text) {
				return frame
			}
		case <-timer.C:
			t.Fatalf("questionnaire did not show %q", text)
		}
	}
}

func TestQuestionnaireVisibleWhileHistoryPaused(t *testing.T) {
	u, _ := layoutFixture(t)
	manager := u.manager.(*layoutManager)
	manager.states["main"] = session.StatusWaitingForApproval
	manager.latest = map[string]string{"main": strings.Repeat("a long submitted prompt ", 20)}
	manager.queued = map[string][]session.QueuedPrompt{"main": {{RequestID: "queued", Prompt: "queued task"}}}
	for i := 0; i < 60; i++ {
		u.display.AddLine("earlier conversation")
	}
	u.renderInput(inputPrompt, "", 0)
	u.showPage(1)
	u.toggleQueuePanel()
	before := *u.activeViewportLocked()
	result, frames := startQuestionnaire(t, u, []question.Question{{Text: "Which database?", Options: []string{"SQLite", "Postgres"}, AllowCustom: true}})
	frame := waitQuestionnaireFrame(t, frames, "Answer 1/1>")
	for _, text := range []string{"Question 1/1", "Which database?", "1) SQLite", "2) Postgres"} {
		if !strings.Contains(frame, text) {
			t.Fatalf("missing %q while history is paused:\n%s", text, frame)
		}
	}
	_, _ = u.display.Write([]byte("\rAsking the user (|)"))
	u.drawTaskIndicator()
	if !strings.Contains(questionnaireFrame(u), "Which database?") {
		t.Fatal("activity repaint hid the question")
	}
	u.input.route([]byte("2\r"))
	select {
	case got := <-result:
		if got.err != nil || len(got.answers) != 1 || got.answers[0] != "Postgres" {
			t.Fatalf("answer = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("questionnaire did not accept answer")
	}
	if got := *u.activeViewportLocked(); got.browsing != before.browsing || got.anchor != before.anchor {
		t.Fatalf("question changed history position: %+v, want %+v", got, before)
	}
	if strings.Contains(questionnaireFrame(u), "Answer 1/1>") {
		t.Fatal("answer prompt was not cleared")
	}
	if !strings.Contains(strings.Join(u.display.Lines(), "\n"), "Which database?") {
		t.Fatal("question was not recorded in history")
	}
}

func TestQuestionnaireScrollsLongChoicesOnShortTerminal(t *testing.T) {
	u, _ := layoutFixture(t)
	u.height, u.width = 12, 40
	item := question.Question{Text: "Which database?", Options: []string{"SQLite", "Postgres", "MySQL", "MongoDB", "Redis", "DynamoDB"}, AllowCustom: true}
	for range item.Options {
		item.OptionDescriptions = append(item.OptionDescriptions, strings.Repeat("A longer description of this database. ", 3))
	}
	result, frames := startQuestionnaire(t, u, []question.Question{item})
	frame := waitQuestionnaireFrame(t, frames, "Answer 1/1>")
	if !strings.Contains(frame, "Which database?") || !strings.Contains(frame, "PgUp/PgDn") {
		t.Fatalf("short terminal did not show question and scroll hint:\n%s", frame)
	}
	u.input.route([]byte("6"))
	waitQuestionnaireFrame(t, frames, "Answer 1/1> 6")
	seen := frame
	for i := 0; i < 40; i++ {
		u.input.route([]byte(pageDownSequence))
		next := questionnaireFrame(u)
		if next == frame {
			break
		}
		frame = next
		if !strings.Contains(frame, "Question 1/1") || !strings.Contains(frame, "Answer 1/1> 6") {
			t.Fatalf("paging lost the question heading or answer draft:\n%s", frame)
		}
		seen += "\n" + frame
	}
	for _, option := range item.Options {
		if !strings.Contains(seen, option) {
			t.Fatalf("option %q could not be reached by paging", option)
		}
	}
	for i := 0; i < 40; i++ {
		u.input.route([]byte(pageUpSequence))
		next := questionnaireFrame(u)
		if next == frame {
			break
		}
		frame = next
	}
	if !strings.Contains(questionnaireFrame(u), "Which database?") {
		t.Fatal("Page Up did not return to question text")
	}
	u.screenMu.Lock()
	u.width, u.height = 20, 18
	u.paintFixedLocked(0)
	for _, row := range u.inputScreenRows {
		if visibleWidth(row) > u.width {
			t.Errorf("resized panel row is too wide: %q", row)
		}
	}
	u.screenMu.Unlock()
	if !strings.Contains(questionnaireFrame(u), "Which database?") {
		t.Fatal("resize hid the question text")
	}
	u.input.route([]byte("\r"))
	select {
	case got := <-result:
		if got.err != nil || len(got.answers) != 1 || got.answers[0] != "DynamoDB" {
			t.Fatalf("answer = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("paging prevented answering")
	}
}

func TestQuestionnairePanelShowsValidationFeedback(t *testing.T) {
	u, _ := layoutFixture(t)
	result, frames := startQuestionnaire(t, u, []question.Question{{Text: "Which database?", Options: []string{"SQLite", "Postgres"}}})
	waitQuestionnaireFrame(t, frames, "Answer 1/1>")
	for _, attempt := range []struct{ input, feedback string }{
		{"\r", "Please enter an answer"},
		{"unlisted\r", "Choose one of the listed options"},
	} {
		u.input.route([]byte(attempt.input))
		frame := waitQuestionnaireFrame(t, frames, attempt.feedback)
		if !strings.Contains(frame, "Which database?") {
			t.Fatal("validation feedback replaced the question")
		}
	}
	u.input.route([]byte("2\r"))
	select {
	case got := <-result:
		if got.err != nil || len(got.answers) != 1 || got.answers[0] != "Postgres" {
			t.Fatalf("answer = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("questionnaire did not accept valid answer")
	}
}

func TestQuestionnairePanelClearsOnCancelAndDeferral(t *testing.T) {
	for _, action := range []struct {
		name, input string
		want        error
	}{
		{"cancel", string(ctrlC), context.Canceled},
		{"escape", string(byte(29)), errQuestionDeferred},
		{"tab", ctrlPageDownSequence, errQuestionDeferred},
	} {
		t.Run(action.name, func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.manager.(*layoutManager).queued = map[string][]session.QueuedPrompt{"main": {{RequestID: "queued", Prompt: "queued task"}}}
			u.queue.expanded = true
			result, frames := startQuestionnaire(t, u, []question.Question{{Text: "Which database?", AllowCustom: true}})
			waitQuestionnaireFrame(t, frames, "Answer 1/1>")
			u.input.route([]byte(action.input))
			select {
			case got := <-result:
				if !errors.Is(got.err, action.want) {
					t.Fatalf("result = %+v, want %v", got, action.want)
				}
			case <-time.After(time.Second):
				t.Fatal("questionnaire did not release the input")
			}
			if u.questionPanel != nil || strings.Contains(questionnaireFrame(u), "Answer 1/1>") || !u.queue.expanded {
				t.Fatal("question panel or prompt remained, or previous queue state was lost")
			}
		})
	}
}

func TestQuestionnairePanelRedactsBeforeWrappingAndKeepsOriginalAnswer(t *testing.T) {
	u, _ := layoutFixture(t)
	u.width = 30
	policy, err := redaction.New(redaction.Config{}, []string{tuiTestSecret})
	if err != nil {
		t.Fatal(err)
	}
	u.redaction = policy
	item := question.Question{Text: "Choose " + tuiTestSecret, Options: []string{tuiTestSecret, "safe"}, OptionDescriptions: []string{"Description " + tuiTestSecret}}
	result, frames := startQuestionnaire(t, u, []question.Question{item})
	frame := waitQuestionnaireFrame(t, frames, "Answer 1/1>")
	if strings.Contains(frame, tuiTestSecret) || !strings.Contains(frame, redaction.Marker) {
		t.Fatalf("question panel did not redact sensitive text:\n%s", frame)
	}
	u.input.route([]byte("1\r"))
	select {
	case got := <-result:
		if got.err != nil || len(got.answers) != 1 || got.answers[0] != tuiTestSecret {
			t.Fatal("display filtering changed the selected answer")
		}
	case <-time.After(time.Second):
		t.Fatal("questionnaire did not accept answer")
	}
}
