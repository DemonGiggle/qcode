package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

func TestCloseAgentToolContract(t *testing.T) {
	m := newTestManager(t, 3)
	main, _ := m.Agent("main")
	worker, _ := m.Create("model")
	child, _ := m.Agent(worker.ID)
	if !hasTool(main.enabledSchemas(), "close_agent") || hasTool(child.enabledSchemas(), "close_agent") {
		t.Fatal("close_agent must be main-only")
	}
	for _, mode := range []string{"plan", "skillplan"} {
		main.SetPlanMode(mode == "plan")
		main.SetSkillPlanMode(mode == "skillplan")
		if hasTool(main.enabledSchemas(), "close_agent") {
			t.Fatalf("close_agent visible in %s", mode)
		}
		if _, err := main.executeDetailed(context.Background(), llm.ToolCall{Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"agent-1"}`)}); err == nil {
			t.Fatalf("close_agent executed in %s", mode)
		}
	}
	main.SetSkillPlanMode(false)
	tool := m.WrapToolset("main", &managerToolset{}, true)
	for _, args := range []string{`{`, `null`, `{}`, `{"agent_id":null}`, `{"agent_id":7}`, `{"agent_id":"agent-1","force":true}`, `{"agent_id":" "}`, `{"agent_id":"main"}`, `{"agent_id":"unknown"}`} {
		if _, err := tool.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "close_agent", Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("invalid args accepted: %s", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	call := llm.ToolCall{Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"agent-1"}`)}
	if _, err := tool.ExecuteDetailed(ctx, call); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	if _, err := m.Summary(worker.ID); err != nil {
		t.Fatal("cancelled caller changed roster")
	}
	for i := 0; i < 2; i++ {
		result, err := tool.ExecuteDetailed(context.Background(), call)
		if err != nil || result.EndTurn || !strings.Contains(result.Output, "agent-1 closed") {
			t.Fatalf("close result: %+v, %v", result, err)
		}
	}
	if _, ok := m.Agent(worker.ID); ok || child.checkpoint.Load() != nil || len(child.messages) != 0 {
		t.Fatal("closed agent retained live context")
	}
	if next, err := m.Create("model"); err != nil || next.ID != "agent-2" {
		t.Fatalf("slot reuse: %+v, %v", next, err)
	}
}

type closeProvider struct{}

func (*closeProvider) Name() string { return "close-test" }
func (*closeProvider) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "write-1", Name: "write", Arguments: json.RawMessage(`{}`)}}}}, nil
}

type delayedCloseTools struct {
	started     chan struct{}
	cancelled   chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (t *delayedCloseTools) unblock() { t.releaseOnce.Do(func() { close(t.release) }) }

func (*delayedCloseTools) Schemas() []llm.Tool          { return []llm.Tool{{Name: "write"}} }
func (t *delayedCloseTools) EnabledSchemas() []llm.Tool { return t.Schemas() }
func (t *delayedCloseTools) ExecuteDetailed(ctx context.Context, _ llm.ToolCall) (llm.ToolResult, error) {
	close(t.started)
	<-ctx.Done()
	close(t.cancelled)
	<-t.release
	return llm.ToolResult{ChangedFiles: []string{"actual.txt"}}, ctx.Err()
}

func newDelayedCloseManager(t *testing.T) (*AgentManager, AgentSummary, *delayedCloseTools) {
	t.Helper()
	m := NewAgentManager(context.Background(), 2)
	tools := &delayedCloseTools{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&closeProvider{}, model, m.WrapToolset(id, tools, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	worker, err := m.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.unblock(); m.Shutdown() })
	return m, worker, tools
}

func TestCloseActiveAgentCancelsPendingWorkAndWaitsForActualChanges(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "approval"}[waiting], func(t *testing.T) {
			m, worker, tools := newDelayedCloseManager(t)
			active, err := m.Submit(worker.ID, "write a file")
			if err != nil {
				t.Fatal(err)
			}
			<-tools.started
			queued, err := m.Submit(worker.ID, "must never start")
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.SubmitPrompt(session.PromptSubmission{AgentID: worker.ID, ObservedTaskID: active.RequestID, Intent: session.IntentSteer, Prompt: "pending steer"})
			if err != nil {
				t.Fatal(err)
			}
			m.SetWaitingForApproval(worker.ID, waiting)
			result, err := m.WrapToolset("main", &managerToolset{}, true).ExecuteDetailed(context.Background(), llm.ToolCall{Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"agent-1"}`)})
			if err != nil || result.EndTurn || !strings.Contains(result.Output, "closure accepted; cancellation pending") {
				t.Fatalf("async close: %+v, %v", result, err)
			}
			<-tools.cancelled
			if summary, _ := m.Summary(worker.ID); summary.Status != StatusClosing || summary.QueueDepth != 0 {
				t.Fatalf("summary: %+v", summary)
			}
			if _, ok := m.Agent(worker.ID); !ok {
				t.Fatal("runner released before active run exited")
			}
			m.SetWaitingForApproval(worker.ID, false)
			if err := m.Close(worker.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := m.GetResult(active.RequestID); !errors.Is(err, ErrRequestPending) {
				t.Fatalf("active result resolved before exit: %v", err)
			}
			if _, err := m.GetResult(queued.RequestID); !errors.Is(err, context.Canceled) {
				t.Fatalf("queued result: %v", err)
			}
			if len(m.PendingInputs(worker.ID)) != 0 {
				t.Fatal("pending work retained")
			}
			if _, err := m.Submit(worker.ID, "late task"); !errors.Is(err, ErrClosedAgent) {
				t.Fatalf("late task: %v", err)
			}
			if _, err := m.SubmitPrompt(session.PromptSubmission{AgentID: worker.ID, ObservedTaskID: active.RequestID, Intent: session.IntentSteer, Prompt: "late steer"}); !errors.Is(err, ErrClosedAgent) {
				t.Fatalf("late steer: %v", err)
			}
			for _, err := range []error{m.Rename(worker.ID, "late"), m.Reset(worker.ID), m.UpdateModel(worker.ID, "late"), m.Configure(worker.ID, func() error { t.Fatal("late configuration applied"); return nil }), m.InteractionScope(worker.ID, func(string, string) error { t.Fatal("late interaction opened"); return nil })} {
				if err == nil {
					t.Fatal("closing agent accepted a mutation")
				}
			}
			if _, err := m.Create("model"); err == nil {
				t.Fatal("closing agent released slot too early")
			}
			for _, record := range m.WorkRecords() {
				for _, steer := range record.Steers {
					if steer.State != session.InputCancelled {
						t.Fatalf("steer: %+v", steer)
					}
				}
			}
			saved, next, history := m.SaveSessionState()
			if len(saved) != 1 || saved[0].Summary.ID != "main" || next != 1 || len(history.Records) != 2 {
				t.Fatal("pending closure snapshot lost identity or journal")
			}
			tools.unblock()
			deadline := time.After(time.Second)
			for len(m.ClosedIDs()) == 0 {
				select {
				case <-m.Events():
				case <-deadline:
					t.Fatal("closure did not finalize")
				}
			}
			if _, err := m.GetResult(active.RequestID); !errors.Is(err, context.Canceled) {
				t.Fatalf("active result: %v", err)
			}
			record := m.WorkRecords()[0]
			if record.Status != "cancelled" || len(record.ChangedFiles) != 1 || record.ChangedFiles[0] != "actual.txt" {
				t.Fatalf("record: %+v", record)
			}
			if next, err := m.Create("model"); err != nil || next.ID != "agent-2" {
				t.Fatalf("slot reuse: %+v %v", next, err)
			}
		})
	}
}

func TestCloseResolvesConsultationsAndQueuedWaitersDuringShutdown(t *testing.T) {
	m, worker, tools := newDelayedCloseManager(t)
	consulted := make(chan []ConsultationReply, 1)
	go func() {
		replies, _ := m.Consult(context.Background(), []ConsultationRequest{{AgentID: worker.ID, Prompt: "write"}})
		consulted <- replies
	}()
	<-tools.started
	waited := make(chan error, 1)
	go func() { _, err := m.SubmitAndWait(context.Background(), worker.ID, "queued waiter"); waited <- err }()
	deadline := time.After(time.Second)
	for {
		summary, _ := m.Summary(worker.ID)
		if summary.QueueDepth == 1 {
			break
		}
		select {
		case <-m.Events():
		case <-deadline:
			t.Fatal("waiter was not queued")
		}
	}
	if err := m.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued waiter unresolved")
	}
	stopped := make(chan struct{})
	go func() { m.Shutdown(); close(stopped) }()
	tools.unblock()
	select {
	case replies := <-consulted:
		if len(replies) != 1 || replies[0].Status != "cancelled" {
			t.Fatalf("consultation: %+v", replies)
		}
	case <-time.After(time.Second):
		t.Fatal("consultation unresolved")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	if len(m.ClosedIDs()) != 1 {
		t.Fatal("shutdown did not finalize closure")
	}
}

func TestClosePendingSnapshotResumesWithoutHelperAndRetainsHistory(t *testing.T) {
	m, worker, tools := newDelayedCloseManager(t)
	if _, err := m.Submit(worker.ID, "historical closing work"); err != nil {
		t.Fatal(err)
	}
	<-tools.started
	if err := m.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	saved, next, work := m.SaveSessionState()
	restored := NewAgentManager(context.Background(), 2)
	defer restored.Shutdown()
	// Factories restore the same provider identity as the saved agent.
	restored.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&closeProvider{}, model, restored.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if err := restored.RestoreAgents(saved, next); err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreWorkHistory(work); err != nil {
		t.Fatal(err)
	}
	if len(restored.List()) != 1 || len(restored.WorkRecords()) != 1 {
		t.Fatal("restored closing helper or lost history")
	}
	page := restored.SearchWork("historical", worker.ID, 0)
	if len(page.Matches) != 1 {
		t.Fatalf("closed history not searchable: %+v", page)
	}
	if err := restored.Close(worker.ID); err != nil {
		t.Fatalf("repeat restored close: %v", err)
	}
	if created, err := restored.Create("model"); err != nil || created.ID != "agent-2" {
		t.Fatalf("restored identity reuse: %+v %v", created, err)
	}
}

func TestCloseConcurrentCompletion(t *testing.T) {
	m := newTestManager(t, 3)
	for i := 0; i < 30; i++ {
		worker, err := m.Create("model")
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { _, err := m.SubmitAndWait(context.Background(), worker.ID, "fast completion"); result <- err }()
		if err := m.Close(worker.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosedAgent) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("completion waiter stuck")
		}
		deadline := time.After(time.Second)
		for len(m.List()) != 1 {
			select {
			case <-m.Events():
			case <-deadline:
				t.Fatal("concurrent close stuck")
			}
		}
	}
}

func TestCloseIDsSurviveSaturatedNotifications(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	for len(m.events) < cap(m.events) {
		m.events <- ManagerEvent{Consultation: true}
	}
	if err := m.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	if ids := m.ClosedIDs(); len(ids) != 1 || ids[0] != worker.ID {
		t.Fatalf("closed IDs: %v", ids)
	}
	for len(m.events) > 0 {
		if event := <-m.events; event.Closed {
			t.Fatal("close event was not dropped by this test")
		}
	}
}

func TestCloseAfterShutdownRejectsWithoutPublishing(t *testing.T) {
	m := newTestManager(t, 2)
	worker, err := m.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	if err := m.Close(worker.ID); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("close after shutdown: %v", err)
	}
}

type closeMainProvider struct{}

func (*closeMainProvider) Name() string { return "close-main" }
func (*closeMainProvider) Complete(_ context.Context, req llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	if req.Messages[len(req.Messages)-1].Role == "user" {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "close-1", Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"agent-1"}`)},
			{ID: "close-2", Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"agent-2"}`)},
		}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "kept working"}}, nil
}

func TestCloseAgentKeepsMainTurnOpen(t *testing.T) {
	m := NewAgentManager(context.Background(), 3)
	defer m.Shutdown()
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&closeMainProvider{}, model, m.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.Create("model"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.SubmitAndWait(context.Background(), "main", "close helpers and continue")
	if err != nil || result.Response != "kept working" || len(m.List()) != 1 {
		t.Fatalf("main continuation: %+v %v", result, err)
	}
}

type closeShellProvider struct{}

func (*closeShellProvider) Name() string { return "close-shell" }
func (*closeShellProvider) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "shell-1", Name: "shell", Arguments: json.RawMessage(`{"command":"printf changed > actual.txt; sleep 30"}`)}}}}, nil
}

func TestCloseCancelledShellRetainsActualChangedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	m := NewAgentManager(context.Background(), 2)
	defer m.Shutdown()
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		registry, err := tools.New(root)
		if err != nil {
			return nil, err
		}
		return New(&closeShellProvider{}, model, m.WrapToolset(id, registry, main), trace.New(io.Discard, false), io.Discard, 3), nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	worker, err := m.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit(worker.ID, "change a file then wait"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "actual.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell never changed file")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	closedDeadline := time.After(2 * time.Second)
	for len(m.ClosedIDs()) == 0 {
		select {
		case <-m.Events():
		case <-closedDeadline:
			t.Fatal("shell closure did not finish")
		}
	}
	record := m.WorkRecords()[0]
	if record.Status != "cancelled" || len(record.ChangedFiles) != 1 || record.ChangedFiles[0] != "actual.txt" {
		t.Fatalf("cancelled shell attribution: %+v", record)
	}
}
