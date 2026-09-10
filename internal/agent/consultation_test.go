package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

type consultationProvider func(context.Context, llm.Request) (llm.Response, error)

func (consultationProvider) Name() string { return "consultation-test" }
func (p consultationProvider) Complete(ctx context.Context, req llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	return p(ctx, req)
}

func consultationManager(t *testing.T, provider llm.Provider, workers int) *AgentManager {
	t.Helper()
	m := NewAgentManager(context.Background(), workers+1)
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(provider, model, m.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 8), nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < workers; i++ {
		if _, err := m.Create("model"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(m.Shutdown)
	return m
}

type consultationOutcome struct {
	replies []ConsultationReply
	err     error
}

func asyncConsult(m *AgentManager, ctx context.Context, requests ...ConsultationRequest) <-chan consultationOutcome {
	done := make(chan consultationOutcome, 1)
	go func() { replies, err := m.Consult(ctx, requests); done <- consultationOutcome{replies, err} }()
	return done
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for test event")
		var zero T
		return zero
	}
}

func TestConsultDispatchesAllAndWaitsForSpecificReplies(t *testing.T) {
	started := make(chan string, 2)
	first, second := make(chan struct{}), make(chan struct{})
	p := consultationProvider(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		text := req.Messages[len(req.Messages)-1].Content
		if text != "old" {
			started <- text
			gate := first
			if text == "second" {
				gate = second
			}
			select {
			case <-gate:
			case <-ctx.Done():
				return llm.Response{}, ctx.Err()
			}
		}
		return llm.Response{Message: llm.Message{Role: "assistant", Content: text + " answer"}}, nil
	})
	m := consultationManager(t, p, 2)
	if _, err := m.SubmitAndWait(context.Background(), "agent-1", "old"); err != nil {
		t.Fatal(err)
	}
	done := asyncConsult(m, context.Background(), ConsultationRequest{"agent-1", "first"}, ConsultationRequest{"agent-2", "second"})
	seen := map[string]bool{receive(t, started): true, receive(t, started): true}
	if !seen["first"] || !seen["second"] {
		t.Fatal(seen)
	}
	close(first)
	select {
	case result := <-done:
		t.Fatalf("returned before all replies: %+v", result)
	case <-time.After(15 * time.Millisecond):
	}
	close(second)
	result := receive(t, done)
	if result.err != nil || len(result.replies) != 2 {
		t.Fatal(result)
	}
	for i, want := range []string{"first answer", "second answer"} {
		if result.replies[i].Status != "completed" || result.replies[i].Response != want || result.replies[i].RequestID == "" {
			t.Fatal(result.replies)
		}
	}
	if result.replies[0].RequestID == result.replies[1].RequestID {
		t.Fatal("request IDs collide")
	}
}

func TestConsultPartialFailureTimeoutAndRejection(t *testing.T) {
	m := newTestManager(t, 7)
	for i := 0; i < 6; i++ {
		if _, err := m.Create("model"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SetConsultationTimeout(60 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := m.Close("agent-4"); err != nil {
		t.Fatal(err)
	}
	m.queueLimit = 1
	if err := m.Start("agent-5", "block"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start("agent-5", "queued"); err != nil {
		t.Fatal(err)
	}
	requests := []ConsultationRequest{{"agent-1", "inspect"}, {"agent-2", "fail"}, {"agent-3", "block"}, {"agent-4", "inspect"}, {"agent-5", "inspect"}, {"missing", "inspect"}}
	result := receive(t, asyncConsult(m, context.Background(), requests...))
	if result.err != nil {
		t.Fatal(result.err)
	}
	for i, status := range []string{"completed", "failed", "timed_out", "failed", "failed", "failed"} {
		if result.replies[i].Status != status {
			t.Fatalf("reply %d: %+v", i, result.replies[i])
		}
		if i > 0 && (result.replies[i].Error == "" || result.replies[i].Response != "") {
			t.Fatal(result.replies[i])
		}
	}
	if len(m.ConsultationEvents(0)) != len(requests) {
		t.Fatal("missing terminal events")
	}
	if summary, _ := m.Summary("agent-5"); summary.Status != StatusRunning || summary.QueueDepth != 1 {
		t.Fatalf("unrelated queue was affected: %+v", summary)
	}
}

func TestConsultQueuedTimeoutPreservesOtherRequests(t *testing.T) {
	p := &queueProvider{started: make(chan string, 4), release: make(chan struct{}, 4)}
	m := newQueueTestManager(t, 2, p)
	worker, _ := m.Create("model")
	_ = m.SetConsultationTimeout(50 * time.Millisecond)
	if err := m.Start(worker.ID, "unrelated running"); err != nil {
		t.Fatal(err)
	}
	if text := receive(t, p.started); text != "unrelated running" {
		t.Fatal(text)
	}
	done := asyncConsult(m, context.Background(), ConsultationRequest{worker.ID, "expired query"})
	// Wait until the consultation is queued, then append another user request.
	deadline := time.After(time.Second)
	for {
		summary, _ := m.Summary(worker.ID)
		if summary.QueueDepth == 1 {
			break
		}
		select {
		case <-m.Events():
		case <-deadline:
			t.Fatal("query was not queued")
		}
	}
	next, err := m.Submit(worker.ID, "unrelated queued")
	if err != nil {
		t.Fatal(err)
	}
	result := receive(t, done)
	if result.err != nil || result.replies[0].Status != "timed_out" {
		t.Fatal(result)
	}
	if summary, _ := m.Summary(worker.ID); summary.Status != StatusRunning || summary.QueueDepth != 1 {
		t.Fatal(summary)
	}
	p.release <- struct{}{}
	if text := receive(t, p.started); text != "unrelated queued" {
		t.Fatalf("expired request ran: %q", text)
	}
	p.release <- struct{}{}
	if result, err := waitPromptResult(t, m, next.RequestID); err != nil || result.Response != "handled unrelated queued" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestExpiredQueuedConsultationNeverStartsWhenWorkerBecomesFree(t *testing.T) {
	p := &queueProvider{started: make(chan string, 4), release: make(chan struct{}, 4)}
	m := newQueueTestManager(t, 2, p)
	worker, _ := m.Create("model")
	if err := m.Start(worker.ID, "first"); err != nil {
		t.Fatal(err)
	}
	receive(t, p.started)
	deadline := time.Now().Add(20 * time.Millisecond)
	// No consultation waiter runs here: expiry must also be enforced when the
	// manager advances its queue, even if the waiting goroutine is delayed.
	req, _, err := m.submitRequest(worker.ID, "expired", deadline)
	if err != nil {
		t.Fatal(err)
	}
	next, err := m.Submit(worker.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	<-time.After(time.Until(deadline))
	p.release <- struct{}{}
	if text := receive(t, p.started); text != "next" {
		t.Fatalf("expired consultation ran: %s", text)
	}
	p.release <- struct{}{}
	if _, err := waitPromptResult(t, m, next.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetResult(req.id); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestConsultAllFailuresStillReturnsNormally(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	result := receive(t, asyncConsult(m, context.Background(), ConsultationRequest{worker.ID, "fail"}, ConsultationRequest{"missing", "question"}))
	if result.err != nil || len(result.replies) != 2 {
		t.Fatal(result)
	}
	for _, reply := range result.replies {
		if reply.Status != "failed" || reply.Response != "" {
			t.Fatal(reply)
		}
	}
}

func TestConsultCallerCancellationAndShutdown(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "caller", true: "shutdown"}[shutdown], func(t *testing.T) {
			p := &queueProvider{started: make(chan string, 2), release: make(chan struct{}, 2)}
			m := newQueueTestManager(t, 2, p)
			worker, _ := m.Create("model")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := asyncConsult(m, ctx, ConsultationRequest{worker.ID, "question"})
			receive(t, p.started)
			if shutdown {
				m.Shutdown()
			} else {
				cancel()
			}
			result := receive(t, done)
			if shutdown && !errors.Is(result.err, ErrManagerClosed) || !shutdown && !errors.Is(result.err, context.Canceled) {
				t.Fatal(result.err)
			}
			if result.replies[0].Status != "cancelled" || len(m.ConsultationEvents(0)) != 1 {
				t.Fatal(result)
			}
		})
	}
}

func TestConsultLateReplyCannotOverwriteTimeoutOrStartConcurrentRun(t *testing.T) {
	started := make(chan string, 3)
	gate := make(chan struct{})
	p := consultationProvider(func(_ context.Context, req llm.Request) (llm.Response, error) {
		text := req.Messages[len(req.Messages)-1].Content
		started <- text
		if text == "slow" {
			<-gate
		} // Deliberately ignore cancellation.
		return llm.Response{Message: llm.Message{Role: "assistant", Content: text + " answer"}}, nil
	})
	m := consultationManager(t, p, 1)
	var released bool
	t.Cleanup(func() {
		if !released {
			close(gate)
		}
	})
	_ = m.SetConsultationTimeout(50 * time.Millisecond)
	done := asyncConsult(m, context.Background(), ConsultationRequest{"agent-1", "slow"})
	receive(t, started)
	result := receive(t, done)
	if result.err != nil || result.replies[0].Status != "timed_out" {
		t.Fatal(result)
	}
	next, err := m.Submit("agent-1", "next")
	if err != nil || next.QueuePosition != 1 {
		t.Fatalf("runner slot released too early: %+v %v", next, err)
	}
	select {
	case text := <-started:
		t.Fatalf("concurrent request started: %s", text)
	default:
	}
	close(gate)
	released = true
	if text := receive(t, started); text != "next" {
		t.Fatal(text)
	}
	if _, err := waitPromptResult(t, m, next.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetResult(result.replies[0].RequestID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late result replaced timeout: %v", err)
	}
	work := m.SaveWorkHistory()
	if work.Records[0].Status != "timed_out" || work.Records[0].Response != "" || len(work.Events) != 1 {
		t.Fatalf("late completion changed journal: %+v", work)
	}
}

func TestConsultEventsSurviveFullChannelAndSnapshotsAreIndependent(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	for len(m.events) < cap(m.events) {
		m.events <- ManagerEvent{}
	}
	result := receive(t, asyncConsult(m, context.Background(), ConsultationRequest{worker.ID, "inspect"}))
	if result.err != nil {
		t.Fatal(result.err)
	}
	events := m.ConsultationEvents(0)
	if len(events) != 1 || events[0].Status != "completed" {
		t.Fatal(events)
	}
	events[0].Status = "changed"
	state := m.SaveWorkHistory()
	state.Events[0].Status = "changed"
	if m.ConsultationEvents(0)[0].Status != "completed" {
		t.Fatal("snapshot aliases event log")
	}
	if len(m.ConsultationEvents(1)) != 0 {
		t.Fatal("event cursor did not advance")
	}
}

func TestConsultValidationAndMainOnlyTool(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	for _, requests := range [][]ConsultationRequest{nil, {{"main", "question"}}, {{worker.ID, " "}}, {{worker.ID, "one"}, {worker.ID, "two"}}} {
		if _, err := m.Consult(context.Background(), requests); err == nil {
			t.Fatalf("accepted %+v", requests)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Consult(ctx, []ConsultationRequest{{worker.ID, "question"}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(m.SaveWorkHistory().Records) != 0 {
		t.Fatal("invalid batch submitted work")
	}
	main, _ := m.Agent("main")
	sub, _ := m.Agent(worker.ID)
	for _, tool := range []string{"consult_agents", "search_agent_work"} {
		if !hasSchema(main.tools.EnabledSchemas(), tool) || hasSchema(sub.tools.EnabledSchemas(), tool) {
			t.Fatal("coordination tool leaked to worker")
		}
	}
	result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "consult_agents", Arguments: json.RawMessage(`{"requests":[{"agent_id":"agent-1","prompt":"fail"}]}`)})
	if err != nil || !strings.Contains(result.Output, `"status":"failed"`) {
		t.Fatalf("child failure must be data: %+v %v", result, err)
	}
}
