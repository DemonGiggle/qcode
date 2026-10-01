package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

type steeringUIController struct {
	agentController
	summary     session.Summary
	submissions []session.PromptSubmission
	submitErr   error
	events      []session.SteeringEvent
}

func (m *steeringUIController) Summary(string) (session.Summary, error) {
	return m.summary, nil
}

func (m *steeringUIController) SubmitPrompt(input session.PromptSubmission) (session.Submission, error) {
	m.submissions = append(m.submissions, input)
	return session.Submission{}, m.submitErr
}

func (*steeringUIController) PendingInputs(string) []session.QueuedPrompt { return nil }
func (*steeringUIController) CancelInput(string, string) error            { return nil }

func (m *steeringUIController) SteeringEvents(after uint64) []session.SteeringEvent {
	var events []session.SteeringEvent
	for _, event := range m.events {
		if event.Sequence > after {
			events = append(events, event)
		}
	}
	return events
}

func TestSteeringLifecycleLogsOnlyInVerboseMode(t *testing.T) {
	manager := &steeringUIController{}
	for i, state := range []session.InputState{session.InputPending, session.InputReplanning, session.InputDelivered} {
		manager.events = append(manager.events, session.SteeringEvent{
			Sequence: uint64(i + 1), AgentID: "main",
			Input: session.SteeringInput{ID: "request-2", Source: "terminal", Actor: "local-tui", State: state, Text: "skip generated files"},
		})
	}
	for _, verbose := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "verbose"}[verbose], func(t *testing.T) {
			history := newHistoryWriter(io.Discard)
			u := &UI{manager: manager, verbose: verbose, views: map[string]*agentView{}}
			u.views["main"] = &agentView{display: &agentDisplay{ui: u, id: "main", history: history}}
			u.replaySteeringEvents()
			got := strings.Join(history.Lines(), "\n")
			if !verbose && got != "" {
				t.Fatalf("quiet steering history = %q", got)
			}
			if verbose {
				for _, want := range []string{
					"Steer request-2 (terminal/local-tui): pending",
					"Steer request-2 (terminal/local-tui): replanning",
					"Steer request-2 (terminal/local-tui): delivered — skip generated files",
				} {
					if !strings.Contains(got, want) {
						t.Fatalf("verbose history missing %q: %q", want, got)
					}
				}
			}
			if u.steeringCursor != 3 {
				t.Fatalf("cursor = %d, want 3", u.steeringCursor)
			}
			u.verbose = true
			u.replaySteeringEvents()
			if next := strings.Join(history.Lines(), "\n"); next != got {
				t.Fatalf("enabling verbose replayed consumed events: %q", next)
			}
		})
	}
}

func TestComposerSubmissionAfterTaskFinishesStartsOrdinaryTask(t *testing.T) {
	for _, status := range []session.Status{session.StatusCompleted, session.StatusFailed, session.StatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			manager := &steeringUIController{summary: session.Summary{ID: "main", Status: status}}
			u := New(nil, nil, nil, "test", "test", ".")
			u.manager, u.activeAgent = manager, "main"
			u.composerReading, u.observedTaskID = true, "request-1"
			u.submissionIntent = session.IntentAutomatic
			u.terminal.KeyHandler('\r')
			u.composerReading = false
			if err := u.runActiveTask(context.Background(), "next task"); err != nil {
				t.Fatal(err)
			}
			if len(manager.submissions) != 1 {
				t.Fatalf("submissions = %+v", manager.submissions)
			}
			got := manager.submissions[0]
			if got.ObservedTaskID != "" || got.Intent != session.IntentAutomatic || got.Prompt != "next task" {
				t.Fatalf("finished task retained by composer: %+v", got)
			}
		})
	}
}

func TestComposerObservesTaskStartingWhileIdle(t *testing.T) {
	manager := &steeringUIController{summary: session.Summary{ID: "main", Status: session.StatusRunning, ActiveTaskID: "request-1"}}
	u := New(nil, nil, nil, "test", "test", ".")
	u.manager, u.activeAgent, u.composerReading = manager, "main", true
	u.submissionIntent = session.IntentAutomatic
	u.terminal.KeyHandler('\r')
	u.composerReading = false
	if err := u.runActiveTask(context.Background(), "update this task"); err != nil {
		t.Fatal(err)
	}
	if got := manager.submissions[0].ObservedTaskID; got != "request-1" {
		t.Fatalf("observed task = %q, want request-1", got)
	}
}

func TestComposerRetainsDraftWhenObservedTaskChanges(t *testing.T) {
	for _, changedBeforeEnter := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion after Enter", true: "next task before Enter"}[changedBeforeEnter], func(t *testing.T) {
			conflict := errors.New("observed task is no longer accepting steering")
			manager := &steeringUIController{summary: session.Summary{ID: "main", Status: session.StatusRunning, ActiveTaskID: "request-1"}, submitErr: conflict}
			u := New(nil, nil, nil, "test", "test", ".")
			u.manager, u.activeAgent = manager, "main"
			u.composerReading, u.observedTaskID = true, "request-1"
			u.submissionIntent = session.IntentAutomatic
			if changedBeforeEnter {
				manager.summary.ActiveTaskID = "request-2"
			}
			u.terminal.KeyHandler('\r')
			u.composerReading = false
			if !changedBeforeEnter {
				manager.summary.ActiveTaskID = ""
				manager.summary.Status = session.StatusCompleted
			}
			const draft = "keep this instruction 你好"
			if err := u.runActiveTask(context.Background(), draft); !errors.Is(err, conflict) {
				t.Fatalf("submission error = %v, want conflict", err)
			}
			if len(manager.submissions) != 1 || manager.submissions[0].ObservedTaskID != "request-1" {
				t.Fatalf("steer redirected or retried: %+v", manager.submissions)
			}
			if got := u.drafts["main"]; got != draft {
				t.Fatalf("draft = %q, want %q", got, draft)
			}
			var restored []byte
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for len(restored) < len(draft) {
				select {
				case next := <-u.input.injected:
					restored = append(restored, next)
				case <-deadline.C:
					t.Fatal("draft was not restored to the composer")
				}
			}
			if string(restored) != draft {
				t.Fatalf("restored composer = %q, want %q", restored, draft)
			}
		})
	}
}
