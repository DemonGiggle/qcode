package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

type managerProvider struct{}

func (*managerProvider) Name() string { return "manager-test" }
func (*managerProvider) Complete(ctx context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	prompt := request.Messages[len(request.Messages)-1].Content
	switch prompt {
	case "block":
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	case "fail":
		return llm.Response{}, errors.New("provider failed")
	case "long":
		return llm.Response{Message: llm.Message{Role: "assistant", Content: strings.Repeat("result", 1000)}}, nil
	default:
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "handled " + prompt}}, nil
	}
}

type managerToolset struct{}

func (*managerToolset) Schemas() []llm.Tool        { return nil }
func (*managerToolset) EnabledSchemas() []llm.Tool { return nil }
func (*managerToolset) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func newTestManager(t *testing.T, maximum int) *AgentManager {
	t.Helper()
	manager := NewAgentManager(context.Background(), maximum)
	manager.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		toolset := manager.WrapToolset(id, &managerToolset{}, main)
		return New(&managerProvider{}, model, toolset, trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := manager.CreateMain("main-model"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	return manager
}

func waitManagerStatus(t *testing.T, manager *AgentManager, id string, want Status) AgentSummary {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		summary, err := manager.Summary(id)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Status == want {
			return summary
		}
		select {
		case <-manager.Events():
		case <-deadline:
			t.Fatalf("agent %s status = %s, want %s", id, summary.Status, want)
		}
	}
}

func TestAgentManagerLifecycleAndReuse(t *testing.T) {
	manager := newTestManager(t, 2)
	created, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create("overflow"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("limit error = %v", err)
	}

	if err := manager.Start(created.ID, "first"); err != nil {
		t.Fatal(err)
	}
	completed := waitManagerStatus(t, manager, created.ID, StatusCompleted)
	if completed.LastOutcome != "handled first" {
		t.Fatalf("outcome = %q", completed.LastOutcome)
	}
	if err := manager.Start(created.ID, "second"); err != nil {
		t.Fatalf("completed agent was not reusable: %v", err)
	}
	completed = waitManagerStatus(t, manager, created.ID, StatusCompleted)
	if completed.LastOutcome != "handled second" {
		t.Fatalf("second outcome = %q", completed.LastOutcome)
	}
	if err := manager.Close(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Agent(created.ID); ok {
		t.Fatal("closed agent still retained its runner")
	}
}

func TestAgentManagerShutdownCancelsAndCleansUp(t *testing.T) {
	manager := newTestManager(t, 2)
	worker, _ := manager.Create("worker-model")
	if err := manager.Start(worker.ID, "block"); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, worker.ID, StatusRunning)
	manager.Shutdown()
	summary, err := manager.Summary(worker.ID)
	if err != nil || summary.Status != StatusCancelled {
		t.Fatalf("shutdown summary = %+v, %v", summary, err)
	}
	if _, open := <-manager.Events(); open {
		for range manager.Events() {
		}
	}
}

func waitManagerFailure(t *testing.T, manager *AgentManager, id string) AgentSummary {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		summary, err := manager.Summary(id)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Status == StatusIdle && strings.Contains(summary.Error, "provider failed") {
			return summary
		}
		select {
		case <-manager.Events():
		case <-deadline:
			t.Fatalf("agent %s did not become reusable after failure: %+v", id, summary)
		}
	}
}

func TestAgentManagerCancellationAndFailureReuse(t *testing.T) {
	manager := newTestManager(t, 2)
	worker, _ := manager.Create("worker-model")
	if err := manager.Start(worker.ID, "block"); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, worker.ID, StatusRunning)
	if err := manager.Cancel(worker.ID); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, worker.ID, StatusCancelled)

	if err := manager.Start(worker.ID, "fail"); err != nil {
		t.Fatal(err)
	}
	failed := waitManagerFailure(t, manager, worker.ID)
	if !strings.Contains(failed.Error, "provider failed") {
		t.Fatalf("failure = %+v", failed)
	}
	if err := manager.Start(worker.ID, "recovered"); err != nil {
		t.Fatalf("failed worker was not reusable: %v", err)
	}
	completed := waitManagerStatus(t, manager, worker.ID, StatusCompleted)
	if completed.LastOutcome != "handled recovered" || completed.Error != "" {
		t.Fatalf("recovered worker = %+v", completed)
	}

	if err := manager.Start("main", "fail"); err != nil {
		t.Fatal(err)
	}
	if failed = waitManagerFailure(t, manager, "main"); !strings.Contains(failed.Error, "provider failed") {
		t.Fatalf("main failure = %+v", failed)
	}
	if err := manager.Start("main", "recovered"); err != nil {
		t.Fatalf("failed main was not reusable: %v", err)
	}
	completed = waitManagerStatus(t, manager, "main", StatusCompleted)
	if completed.LastOutcome != "handled recovered" || completed.Error != "" {
		t.Fatalf("recovered main = %+v", completed)
	}
}

type queueProvider struct {
	started chan string
	release chan struct{}
}

func (*queueProvider) Name() string { return "queue-test" }

func (p *queueProvider) Complete(ctx context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	prompt := request.Messages[len(request.Messages)-1].Content
	select {
	case p.started <- prompt:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case <-p.release:
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "handled " + prompt}}, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func newQueueTestManager(t *testing.T, maximum int, provider *queueProvider) *AgentManager {
	t.Helper()
	manager := NewAgentManager(context.Background(), maximum)
	manager.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(provider, model, manager.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := manager.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Shutdown)
	return manager
}

func waitPromptResult(t *testing.T, manager *AgentManager, requestID string) (PromptResult, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		result, err := manager.GetResult(requestID)
		if !errors.Is(err, ErrRequestPending) {
			return result, err
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("request %s did not complete", requestID)
	return PromptResult{}, nil
}

func TestAgentPromptQueueIsFIFO(t *testing.T) {
	provider := &queueProvider{started: make(chan string, 4), release: make(chan struct{}, 4)}
	manager := newQueueTestManager(t, 1, provider)

	first, err := manager.Submit("main", "first")
	if err != nil || first.QueuePosition != 0 {
		t.Fatalf("first submission = %+v, %v", first, err)
	}
	second, err := manager.Submit("main", "second")
	if err != nil || second.QueuePosition != 1 {
		t.Fatalf("second submission = %+v, %v", second, err)
	}
	if summary, _ := manager.Summary("main"); summary.QueueDepth != 1 {
		t.Fatalf("queue depth = %d, want 1", summary.QueueDepth)
	}
	if got := <-provider.started; got != "first" {
		t.Fatalf("first started prompt = %q", got)
	}
	select {
	case got := <-provider.started:
		t.Fatalf("queued prompt started early: %q", got)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := manager.GetResult(second.RequestID); !errors.Is(err, ErrRequestPending) {
		t.Fatalf("pending result error = %v", err)
	}

	provider.release <- struct{}{}
	if result, err := waitPromptResult(t, manager, first.RequestID); err != nil || result.Response != "handled first" {
		t.Fatalf("first result = %+v, %v", result, err)
	}
	if got := <-provider.started; got != "second" {
		t.Fatalf("second started prompt = %q", got)
	}
	provider.release <- struct{}{}
	if result, err := waitPromptResult(t, manager, second.RequestID); err != nil || result.Response != "handled second" {
		t.Fatalf("second result = %+v, %v", result, err)
	}
	if summary, _ := manager.Summary("main"); summary.QueueDepth != 0 {
		t.Fatalf("final queue depth = %d", summary.QueueDepth)
	}
}

func TestAgentPromptQueueContinuesAfterCancellation(t *testing.T) {
	manager := newTestManager(t, 1)
	first, err := manager.Submit("main", "block")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Submit("main", "after-cancel")
	if err != nil || second.QueuePosition != 1 {
		t.Fatalf("queued submission = %+v, %v", second, err)
	}
	if err := manager.Cancel("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := waitPromptResult(t, manager, first.RequestID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled result error = %v", err)
	}
	result, err := waitPromptResult(t, manager, second.RequestID)
	if err != nil || result.Response != "handled after-cancel" {
		t.Fatalf("next result = %+v, %v", result, err)
	}
}

func TestSubmitAndWaitReturnsCompleteResult(t *testing.T) {
	provider := &queueProvider{started: make(chan string, 1), release: make(chan struct{}, 1)}
	manager := newQueueTestManager(t, 1, provider)
	provider.release <- struct{}{}
	result, err := manager.SubmitAndWait(context.Background(), "main", "sync")
	if err != nil || result.Response != "handled sync" || result.RequestID == "" || result.TargetID != "main" {
		t.Fatalf("sync result = %+v, %v", result, err)
	}
}

func TestAgentPromptQueuesAreIndependent(t *testing.T) {
	provider := &queueProvider{started: make(chan string, 4), release: make(chan struct{}, 4)}
	manager := newQueueTestManager(t, 2, provider)
	worker, err := manager.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("main", "main-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(worker.ID, "worker-work"); err != nil {
		t.Fatal(err)
	}
	started := map[string]bool{<-provider.started: true, <-provider.started: true}
	if !started["main-work"] || !started["worker-work"] {
		t.Fatalf("started prompts = %v", started)
	}
	provider.release <- struct{}{}
	provider.release <- struct{}{}
}

func TestPromptQueueLimitAndStableTargetErrors(t *testing.T) {
	provider := &queueProvider{started: make(chan string, 4), release: make(chan struct{}, 4)}
	manager := newQueueTestManager(t, 2, provider)
	manager.queueLimit = 1
	if _, err := manager.Submit("missing", "work"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent error = %v", err)
	}
	worker, _ := manager.Create("model")
	if err := manager.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(worker.ID, "work"); !errors.Is(err, ErrClosedAgent) {
		t.Fatalf("closed agent error = %v", err)
	}
	if _, err := manager.Submit("main", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("main", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit("main", "overflow"); !errors.Is(err, ErrPromptQueueFull) {
		t.Fatalf("queue full error = %v", err)
	}
}

func TestPromptResultsRemainFullAndBounded(t *testing.T) {
	manager := newTestManager(t, 1)
	manager.resultLimit = 1
	first, err := manager.SubmitAndWait(context.Background(), "main", "long")
	if err != nil || len(first.Response) <= maxHandoffBytes {
		t.Fatalf("full result length = %d, err = %v", len(first.Response), err)
	}
	if summary, _ := manager.Summary("main"); len(summary.LastOutcome) > maxHandoffBytes {
		t.Fatalf("handoff length = %d", len(summary.LastOutcome))
	}
	if _, err := manager.SubmitAndWait(context.Background(), "main", "next"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetResult(first.RequestID); !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("evicted result error = %v", err)
	}
}

func TestCancellingOneAgentDoesNotCancelAnother(t *testing.T) {
	manager := newTestManager(t, 3)
	first, _ := manager.Create("worker-model")
	second, _ := manager.Create("worker-model")
	if err := manager.Start(first.ID, "block"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(second.ID, "block"); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, first.ID, StatusRunning)
	waitManagerStatus(t, manager, second.ID, StatusRunning)
	if err := manager.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, first.ID, StatusCancelled)
	if summary, _ := manager.Summary(second.ID); summary.Status != StatusRunning {
		t.Fatalf("second agent status = %s, want running", summary.Status)
	}
	if err := manager.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, manager, second.ID, StatusCancelled)
}

func TestMainToolsAndBoundedRoster(t *testing.T) {
	manager := newTestManager(t, 4)
	worker, _ := manager.Create("worker-model")
	manager.mu.Lock()
	manager.sessions[worker.ID].summary.CurrentTask = strings.Repeat("task", 100)
	manager.sessions[worker.ID].summary.LastOutcome = strings.Repeat("result", 1000)
	manager.mu.Unlock()

	roster := manager.RosterContext()
	if len(roster) > maxRosterBytes || !strings.Contains(roster, worker.ID) || strings.Contains(roster, strings.Repeat("result", 1000)) {
		t.Fatalf("roster length=%d text=%q", len(roster), roster)
	}
	main, _ := manager.Agent("main")
	workerAgent, _ := manager.Agent(worker.ID)
	if !hasSchema(main.tools.EnabledSchemas(), "get_agent_result") || hasSchema(workerAgent.tools.EnabledSchemas(), "get_agent_result") {
		t.Fatal("manager tools were not restricted to main")
	}
}

func TestMainCanDelegateAndReadHandoff(t *testing.T) {
	manager := newTestManager(t, 2)
	worker, _ := manager.Create("worker-model")
	main, _ := manager.Agent("main")
	result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{
		Name: "delegate_task", Arguments: []byte(`{"agent_id":"` + worker.ID + `","prompt":"inspect"}`),
	})
	if err != nil || !strings.Contains(result.Output, "accepted") {
		t.Fatalf("delegate result = %+v, %v", result, err)
	}
	waitManagerStatus(t, manager, worker.ID, StatusCompleted)
	result, err = main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{
		Name: "get_agent_result", Arguments: []byte(`{"agent_id":"` + worker.ID + `"}`),
	})
	if err != nil || !strings.Contains(result.Output, "handled inspect") {
		t.Fatalf("handoff result = %+v, %v", result, err)
	}
}

type captureManagerProvider struct{ request llm.Request }

func (*captureManagerProvider) Name() string { return "capture" }
func (p *captureManagerProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.request = request
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func TestRequestRosterIsEphemeral(t *testing.T) {
	provider := &captureManagerProvider{}
	runner := New(provider, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	runner.SetRequestContext(func() string { return "\nTEMPORARY ROSTER" })
	if err := runner.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.request.Messages[0].Content, "TEMPORARY ROSTER") {
		t.Fatal("request did not contain dynamic roster")
	}
	if strings.Contains(runner.messages[0].Content, "TEMPORARY ROSTER") {
		t.Fatal("dynamic roster entered stored history")
	}
}

func hasSchema(schemas []llm.Tool, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
}

type concurrencyToolset struct {
	active atomic.Int32
	max    atomic.Int32
	gate   chan struct{}
	once   sync.Once
}

func (*concurrencyToolset) Schemas() []llm.Tool        { return nil }
func (*concurrencyToolset) EnabledSchemas() []llm.Tool { return nil }
func (t *concurrencyToolset) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	active := t.active.Add(1)
	for {
		previous := t.max.Load()
		if active <= previous || t.max.CompareAndSwap(previous, active) {
			break
		}
	}
	t.once.Do(func() { close(t.gate) })
	time.Sleep(20 * time.Millisecond)
	t.active.Add(-1)
	return llm.ToolResult{ChangedFiles: []string{"changed.go"}}, nil
}

func TestWorkspaceMutationsAreSerialized(t *testing.T) {
	manager := NewAgentManager(context.Background(), 2)
	base := &concurrencyToolset{gate: make(chan struct{})}
	first := manager.WrapToolset("first", base, false)
	second := manager.WrapToolset("second", base, false)
	var wg sync.WaitGroup
	for _, toolset := range []Toolset{first, second} {
		wg.Add(1)
		go func(toolset Toolset) {
			defer wg.Done()
			_, _ = toolset.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "write"})
		}(toolset)
	}
	wg.Wait()
	if got := base.max.Load(); got != 1 {
		t.Fatalf("maximum concurrent mutations = %d, want 1", got)
	}
}

func TestChangedStateFiles(t *testing.T) {
	before := map[string]string{"existing.go": " M:10:1", "removed.go": " M:4:1"}
	after := map[string]string{"existing.go": " M:12:2", "added.go": "??:3:2"}
	got := changedStateFiles(before, after)
	want := []string{"added.go", "existing.go", "removed.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changed files = %v, want %v", got, want)
	}
	if got := changedStateFiles(nil, after); got != nil {
		t.Fatalf("non-Git state produced files: %v", got)
	}
}
