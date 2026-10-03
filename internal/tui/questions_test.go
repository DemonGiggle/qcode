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
