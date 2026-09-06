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
	if err := manager.Start(worker.ID, "busy"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("busy error = %v", err)
	}
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
