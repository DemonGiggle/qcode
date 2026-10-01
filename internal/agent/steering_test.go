package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
)

type steeringProvider struct {
	requests chan llm.Request
	replies  chan llm.Response
}

func (*steeringProvider) Name() string { return "steering-test" }
func (p *steeringProvider) Complete(ctx context.Context, r llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	if stream != nil {
		stream(llm.StreamEvent{Text: "Already streamed. "})
	}
	select {
	case p.requests <- r:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case response := <-p.replies:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

type steeringTools struct {
	entered chan string
	release chan struct{}
	endTurn bool
}

func (*steeringTools) Schemas() []llm.Tool        { return nil }
func (*steeringTools) EnabledSchemas() []llm.Tool { return nil }
func (t *steeringTools) ExecuteDetailed(ctx context.Context, c llm.ToolCall) (llm.ToolResult, error) {
	select {
	case t.entered <- c.ID:
	case <-ctx.Done():
		return llm.ToolResult{}, ctx.Err()
	}
	select {
	case <-t.release:
		return llm.ToolResult{Output: "completed side effect", EndTurn: t.endTurn, UserAnswer: "collected answer", Images: []llm.Image{{MediaType: "image/png", Data: []byte("preserved image")}}}, nil
	case <-ctx.Done():
		return llm.ToolResult{}, ctx.Err()
	}
}
func steeringFixture(t *testing.T, steps int, tools *steeringTools) (*AgentManager, *steeringProvider) {
	t.Helper()
	p := &steeringProvider{requests: make(chan llm.Request, 8), replies: make(chan llm.Response, 8)}
	m := NewAgentManager(context.Background(), 2)
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(p, model, tools, trace.New(io.Discard, false), io.Discard, steps), nil
	})
	if _, err := m.CreateMain("test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, p
}
func nextSteeringRequest(t *testing.T, p *steeringProvider) llm.Request {
	t.Helper()
	select {
	case r := <-p.requests:
		return r
	case <-time.After(time.Second):
		t.Fatal("missing model request")
		return llm.Request{}
	}
}
func steeringSubmit(t *testing.T, m *AgentManager, taskID, text string) session.Submission {
	t.Helper()
	s, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: taskID, Prompt: text, Intent: session.IntentSteer, Source: "test", Actor: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func waitSteeringResult(t *testing.T, m *AgentManager, id string) session.PromptResult {
	t.Helper()
	m.mu.RLock()
	req := m.sessions["main"].active
	if req == nil || req.id != id {
		req = m.results[id]
	}
	m.mu.RUnlock()
	if req == nil {
		t.Fatal("request missing")
	}
	select {
	case <-req.done:
	case <-time.After(time.Second):
		t.Fatal("task did not finish")
	}
	result, err := m.GetResult(id)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func finalSteeringResponse(text string) llm.Response {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: text}}
}

func TestSteeringStreamingReplacementBudgetAndFIFO(t *testing.T) {
	m, p := steeringFixture(t, 1, &steeringTools{})
	original, err := m.Submit("main", "original task")
	if err != nil {
		t.Fatal(err)
	}
	nextSteeringRequest(t, p)
	first := steeringSubmit(t, m, original.RequestID, "obsolete steer")
	latest := steeringSubmit(t, m, original.RequestID, "new direction")
	queued, err := m.Submit("main", "FIFO follow up")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ParentTaskID != original.RequestID || latest.QueuePosition != 0 || queued.QueuePosition != 1 {
		t.Fatalf("submissions: %+v %+v", latest, queued)
	}
	p.replies <- finalSteeringResponse("old final output")
	request := nextSteeringRequest(t, p)
	if got := request.Messages[len(request.Messages)-1].Content; got != "new direction" {
		t.Fatalf("steer missing: %s", got)
	}
	records := m.WorkRecords()
	if len(records) != 2 || records[0].Prompt != "original task" || records[0].Steers[0].State != session.InputSuperseded || records[0].Steers[0].ReplacedBy != latest.RequestID || records[0].Steers[1].State != session.InputDelivered {
		t.Fatalf("journal = %+v", records)
	}
	if err := m.CancelInput("main", latest.RequestID); !errors.Is(err, ErrRequestPending) {
		t.Fatalf("delivered removal=%v", err)
	}
	if _, err := m.GetResult(first.RequestID); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("steer created task result: %v", err)
	}
	p.replies <- finalSteeringResponse("updated final")
	if result := waitSteeringResult(t, m, original.RequestID); result.Response != "updated final" {
		t.Fatalf("result=%+v", result)
	}
	request = nextSteeringRequest(t, p)
	if request.Messages[len(request.Messages)-1].Content != "FIFO follow up" {
		t.Fatal("FIFO work lost")
	}
	if _, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: original.RequestID, Prompt: "stale", Intent: session.IntentSteer}); !errors.Is(err, ErrStaleTask) {
		t.Fatalf("stale task accepted: %v", err)
	}
	p.replies <- finalSteeringResponse("queued final")
	waitSteeringResult(t, m, queued.RequestID)
	copy := m.SaveWorkHistory()
	copy.Records[0].Steers[0].Text = "mutated"
	copy.SteeringEvents[0].Input.Text = "mutated"
	if m.WorkRecords()[0].Steers[0].Text != "obsolete steer" || m.SteeringEvents(0)[0].Input.Text == "mutated" {
		t.Fatal("snapshot aliases manager")
	}
	events := m.SteeringEvents(1)
	if len(events) != 4 || events[len(events)-1].Input.State != session.InputDelivered {
		t.Fatalf("events=%+v", events)
	}
}

func TestSteeringFinishesToolSkipsRemainingAndOverridesEndTurn(t *testing.T) {
	for _, endTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "tools", true: "end-turn"}[endTurn], func(t *testing.T) {
			tools := &steeringTools{entered: make(chan string, 2), release: make(chan struct{}), endTurn: endTurn}
			m, p := steeringFixture(t, 1, tools)
			original, _ := m.Submit("main", "tool task")
			nextSteeringRequest(t, p)
			p.replies <- llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "first", Name: "shell"}, {ID: "second", Name: "write"}}}}
			select {
			case id := <-tools.entered:
				if id != "first" {
					t.Fatal(id)
				}
			case <-time.After(time.Second):
				t.Fatal("tool did not start")
			}
			steeringSubmit(t, m, original.RequestID, "replan after the effect")
			close(tools.release)
			request := nextSteeringRequest(t, p)
			var results []llm.Message
			for _, msg := range request.Messages {
				if msg.Role == "tool" {
					results = append(results, msg)
				}
			}
			if len(results) != 2 || results[0].ToolCallID != "first" || results[1].ToolCallID != "second" {
				t.Fatalf("tool protocol=%+v", results)
			}
			if results[0].Content == results[1].Content {
				t.Fatal("completed and skipped calls indistinguishable")
			}
			if request.Messages[len(request.Messages)-2].Content != "collected answer" || request.Messages[len(request.Messages)-1].Content != "replan after the effect" {
				t.Fatal("answer/steer protocol order lost")
			}
			if len(request.Messages[len(request.Messages)-3].Images) != 1 {
				t.Fatal("completed tool image lost")
			}
			select {
			case <-tools.entered:
				t.Fatal("unstarted action executed")
			default:
			}
			p.replies <- finalSteeringResponse("updated")
			waitSteeringResult(t, m, original.RequestID)
		})
	}
}

func TestSteeringPendingRemovalAndCompletionArbitration(t *testing.T) {
	m, p := steeringFixture(t, 2, &steeringTools{})
	original, _ := m.Submit("main", "task")
	nextSteeringRequest(t, p)
	steer := steeringSubmit(t, m, original.RequestID, "remove me")
	if err := m.CancelInput("main", steer.RequestID); err != nil {
		t.Fatal(err)
	}
	queued, _ := m.Submit("main", "remove FIFO")
	if err := m.CancelInput("main", queued.RequestID); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	input := m.sessions["main"].active.input
	m.mu.RUnlock()
	if item := input.take(true); item != nil {
		t.Fatal("cancelled steer delivered")
	}
	if _, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: original.RequestID, Prompt: "too late", Intent: session.IntentSteer}); !errors.Is(err, ErrStaleTask) {
		t.Fatal(err)
	}
	p.replies <- finalSteeringResponse("done")
	waitSteeringResult(t, m, original.RequestID)
	if len(m.PendingInputs("main")) != 0 {
		t.Fatal("removed FIFO remained")
	}
}

func TestSteeringCancelledAndInterruptedResume(t *testing.T) {
	m, p := steeringFixture(t, 1, &steeringTools{})
	original, _ := m.Submit("main", "task")
	nextSteeringRequest(t, p)
	steeringSubmit(t, m, original.RequestID, "undelivered")
	saved := m.SaveWorkHistory()
	restored := newTestManager(t, 2)
	if err := restored.RestoreWorkHistory(saved); err != nil {
		t.Fatal(err)
	}
	if got := restored.WorkRecords()[0].Steers[0].State; got != session.InputInterrupted {
		t.Fatal(got)
	}
	if restored.WorkRecords()[0].Status != "interrupted" || len(restored.PendingInputs("main")) != 0 {
		t.Fatal("resume resubmitted work")
	}
	if err := m.Cancel("main"); err != nil {
		t.Fatal(err)
	}
	m.mu.RLock()
	req := m.sessions["main"].active
	m.mu.RUnlock()
	if req != nil {
		<-req.done
	}
	if got := m.WorkRecords()[0].Steers[0].State; got != session.InputCancelled {
		t.Fatal(got)
	}
}

func TestSteeringDuringAutoCompactionAndManualUnavailable(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[manual], func(t *testing.T) {
			m, p := steeringFixture(t, 2, &steeringTools{})
			a, _ := m.Agent("main")
			a.messages = append(a.messages, llm.Message{Role: "user", Content: "earlier task"}, llm.Message{Role: "assistant", Content: "earlier response"})
			a.SetContextWindow(100000)
			a.contextUsage = &llm.Usage{InputTokens: 90000}
			a.contextMessages = len(a.messages)
			a.publishContext()
			var original session.Submission
			var err error
			if manual {
				original, err = m.SubmitCompact("main")
			} else {
				original, err = m.Submit("main", "new task")
			}
			if err != nil {
				t.Fatal(err)
			}
			nextSteeringRequest(t, p)
			sub, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: original.RequestID, Prompt: "after compaction", Intent: session.IntentSteer})
			if manual {
				if !errors.Is(err, ErrSteeringUnavailable) {
					t.Fatalf("manual steer=%+v %v", sub, err)
				}
				queued, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", Prompt: "queued task", Intent: session.IntentQueue})
				if err != nil || queued.QueuePosition != 1 {
					t.Fatal("manual compaction blocked queue")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			response := finalSteeringResponse("summary")
			response.Usage = &llm.Usage{InputTokens: 30, OutputTokens: 10}
			p.replies <- response
			request := nextSteeringRequest(t, p)
			expected := "after compaction"
			if manual {
				expected = "queued task"
			}
			if request.Messages[len(request.Messages)-1].Content != expected {
				t.Fatalf("compaction boundary: %+v", request.Messages)
			}
			response = finalSteeringResponse("done")
			response.Usage = &llm.Usage{InputTokens: 20, OutputTokens: 5}
			p.replies <- response
			waitSteeringResult(t, m, original.RequestID)
			// The FIFO task may still be finishing in the manual case; shutdown waits.
			if !manual && a.SessionUsage().TotalTokens != 65 {
				t.Fatalf("steering reset usage: %+v", a.SessionUsage())
			}
		})
	}
}

func TestSteeringReplacementVersusRemovalAndCompletionRaces(t *testing.T) {
	for i := 0; i < 50; i++ {
		m, p := steeringFixture(t, 2, &steeringTools{})
		original, _ := m.Submit("main", "race task")
		nextSteeringRequest(t, p)
		pending := steeringSubmit(t, m, original.RequestID, "first")
		gate := make(chan struct{})
		replaced := make(chan error, 1)
		removed := make(chan error, 1)
		go func() {
			<-gate
			_, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: original.RequestID, Prompt: "second", Intent: session.IntentSteer})
			replaced <- err
		}()
		go func() { <-gate; removed <- m.CancelInput("main", pending.RequestID) }()
		close(gate)
		if err := <-replaced; err != nil {
			t.Fatal(err)
		}
		err := <-removed
		if err != nil && !errors.Is(err, ErrRequestPending) {
			t.Fatal(err)
		}
		m.mu.RLock()
		input := m.sessions["main"].active.input
		m.mu.RUnlock()
		item := input.take(false)
		if item == nil || item.Text != "second" {
			t.Fatal("newest pending steer lost")
		}
		if err := m.CancelInput("main", item.ID); !errors.Is(err, ErrRequestPending) {
			t.Fatal("processing steer removed")
		}
		// Cancel this fixture explicitly so no model request is needed to finish it.
		_ = m.Cancel("main")
		m.Shutdown()
	}
}

func TestModeHandoffVersionRejectsIdenticalReplacement(t *testing.T) {
	m, _ := steeringFixture(t, 1, &steeringTools{})
	a, _ := m.Agent("main")
	plan := Plan{Title: "same plan"}
	a.savePlan(plan)
	text, version, ready := a.TakeModeDecision(session.InteractionPlanDecision)
	if !ready {
		t.Fatal("missing decision")
	}
	a.ClearLatestPlan()
	a.savePlan(plan)
	nextText, nextVersion, ready := a.TakeModeDecision(session.InteractionPlanDecision)
	if !ready || nextText != text || nextVersion == version {
		t.Fatal("identical replacement retained obsolete handoff identity")
	}
}
