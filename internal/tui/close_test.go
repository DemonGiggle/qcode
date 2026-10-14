package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/question"
	"qcode/internal/session"
	"qcode/internal/trace"
)

type closeUIProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*closeUIProvider) Name() string { return "close-ui" }
func (p *closeUIProvider) Complete(ctx context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	if p.started != nil {
		p.once.Do(func() { close(p.started) })
		<-ctx.Done()
		<-p.release
		return llm.Response{}, ctx.Err()
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "historical answer"}}, nil
}

type closeUITools struct{}

func (*closeUITools) Schemas() []llm.Tool        { return nil }
func (*closeUITools) EnabledSchemas() []llm.Tool { return nil }
func (*closeUITools) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func newCloseUI(t *testing.T, delayed bool) (*UI, *control.Host, *closeUIProvider, string) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	u := New(nil, out, nil, "test", "model", t.TempDir())
	h := control.NewHost(context.Background(), 2)
	u.SetAgentManager(h)
	p := &closeUIProvider{}
	if delayed {
		p.started, p.release = make(chan struct{}), make(chan struct{})
	}
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		provider := p
		if main {
			provider = &closeUIProvider{}
		}
		display, response := u.AddAgentView(id, provider.Name(), model)
		return agent.New(provider, model, h.WrapToolset(id, &closeUITools{}, main), trace.New(display, false), response, 3), nil
	})
	if _, err := h.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	worker, err := h.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if p.release != nil {
			select {
			case <-p.release:
			default:
				close(p.release)
			}
		}
		u.shutdownAgentManager()
	})
	if err := u.switchAgent(worker.ID); err != nil {
		t.Fatal(err)
	}
	return u, h, p, worker.ID
}

func TestClosedAgentSharedTabCleanupAndBrowserFallback(t *testing.T) {
	for _, viaCommand := range []bool{false, true} {
		t.Run(map[bool]string{false: "tool", true: "command"}[viaCommand], func(t *testing.T) {
			u, h, _, id := newCloseUI(t, false)
			if _, err := h.SubmitAndWait(context.Background(), id, "recorded task"); err != nil {
				t.Fatal(err)
			}
			h.FlushEvents()
			u.screenMu.Lock()
			u.drafts[id], u.drafts["main"] = "discard worker draft", "keep main draft"
			u.views[id].response.diffList = []string{"worker diff"}
			u.views[id].queue.expanded = true
			u.questionPanel = &questionPanel{agentID: id}
			u.screenMu.Unlock()
			u.questions = []*questionRequest{{agentID: id, result: make(chan questionResult, 1)}}
			u.approvals[id] = []*approvalRequest{{result: make(chan approvalResult, 1)}}
			u.modeDecisions = []*modeDecisionRequest{{agentID: id}}
			if viaCommand {
				u.handleAgentCommand(context.Background(), []string{"/agent", "close", id, "--yes"})
			} else {
				// The same tool boundary used by the main LLM emits closure.
				_, err := h.WrapToolset("main", &closeUITools{}, true).ExecuteDetailed(context.Background(), llm.ToolCall{Name: "close_agent", Arguments: json.RawMessage(`{"agent_id":"` + id + `"}`)})
				if err != nil {
					t.Fatal(err)
				}
			}
			// Browser snapshots filter stale views even before the UI consumes
			// the queued close and switches its active runner.
			presentation := u.RemotePresentation()
			if presentation.Active != "main" || len(presentation.Views) != 1 || presentation.Views[0].ID != "main" {
				t.Fatalf("browser snapshot: %+v", presentation)
			}
			h.FlushEvents()
			u.handlePendingAgentClosures()
			if u.activeAgent != "main" || len(u.views) != 1 || u.views[id] != nil || u.drafts[id] != "" || u.drafts["main"] != "keep main draft" {
				t.Fatalf("tab cleanup: active=%s views=%v drafts=%v", u.activeAgent, u.views, u.drafts)
			}
			if len(u.questions) != 0 || len(u.approvals[id]) != 0 || len(u.modeDecisions) != 0 || u.questionPanel != nil {
				t.Fatal("closed tab retained dialogs")
			}
			main, _ := h.Runner("main")
			if u.runner != main {
				t.Fatal("selected runner retained closed agent")
			}
			if len(h.WorkRecords()) != 1 || h.WorkRecords()[0].Response != "historical answer" {
				t.Fatal("closed work lost")
			}
			exported, err := u.GenerateExport("pretty")
			if err != nil || !strings.Contains(string(exported.Data), "historical answer") {
				t.Fatalf("closed export: %s %v", exported.Data, err)
			}
		})
	}
}

func TestSavePendingClosureNormalizesViewsDraftsAndActive(t *testing.T) {
	u, h, p, id := newCloseUI(t, true)
	store, err := session.Open(t.TempDir(), u.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.EnableSessions(store, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Submit(id, "pending closure journal"); err != nil {
		t.Fatal(err)
	}
	<-p.started
	if err := h.Close(id); err != nil {
		t.Fatal(err)
	}
	h.FlushEvents()
	u.screenMu.Lock()
	u.drafts[id], u.drafts["main"] = "worker draft", "main draft"
	u.screenMu.Unlock()
	if got := u.RemotePresentation(); len(got.Views) != 2 {
		t.Fatal("closing tab removed before exit")
	} else {
		for _, view := range got.Views {
			if view.ID == id && view.Status != "closing" {
				t.Fatalf("closing status: %+v", view)
			}
		}
	}
	if err := u.saveSession(false); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(u.persistence.current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var presentation savedPresentation
	if err := json.Unmarshal(saved.Presentation, &presentation); err != nil {
		t.Fatal(err)
	}
	if len(saved.Agents) != 1 || saved.Agents[0].Summary.ID != "main" || saved.NextID != 1 || presentation.Active != "main" || len(presentation.Views) != 1 || len(presentation.Drafts) != 1 || presentation.Drafts["main"] != "main draft" {
		t.Fatalf("saved pending closure: %+v %+v", saved, presentation)
	}
	if saved.Work == nil || len(saved.Work.Records) != 1 || saved.Work.Records[0].AgentID != id {
		t.Fatal("saved closure lost journal")
	}
}

func TestCloseWithdrawsApprovalWaitingQuestionAndDraft(t *testing.T) {
	u, h, p, id := newCloseUI(t, true)
	if _, err := h.Submit(id, "wait for input"); err != nil {
		t.Fatal(err)
	}
	<-p.started
	u.screenMu.Lock()
	u.drafts[id] = "question draft"
	u.screenMu.Unlock()
	answered := make(chan error, 1)
	go func() {
		_, err := u.AgentQuestioner(id)(context.Background(), []question.Question{{Text: "Which database?"}})
		answered <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(h.PendingInteractions()) == 0 || !u.hasPendingQuestion(id) {
		if time.Now().After(deadline) {
			t.Fatal("question did not open")
		}
		time.Sleep(time.Millisecond)
	}
	if err := h.Close(id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-answered:
		if !errors.Is(err, session.ErrInteractionWithdrawn) {
			t.Fatalf("question withdrawal: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("question remained blocked after close")
	}
	if len(h.PendingInteractions()) != 0 || u.hasPendingQuestion(id) {
		t.Fatal("closed agent retained human interaction")
	}
	close(p.release)
	closedDeadline := time.Now().Add(time.Second)
	for len(h.ClosedIDs()) == 0 {
		if time.Now().After(closedDeadline) {
			t.Fatal("run did not exit")
		}
		time.Sleep(time.Millisecond)
	}
	h.FlushEvents()
	u.handlePendingAgentClosures()
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if u.activeAgent != "main" || u.drafts[id] != "" {
		t.Fatalf("withdrawn question retained tab or draft: %s %v", u.activeAgent, u.drafts)
	}
}

type lostCloseController struct {
	*persistenceManager
	mu     sync.Mutex
	closed []string
}

func (m *lostCloseController) ClosedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.closed...)
}

func TestClosedTabsReconcileAtBarrierWithoutCloseNotification(t *testing.T) {
	u, base := persistenceUI(t)
	base.events = make(chan session.Event, 32)
	m := &lostCloseController{persistenceManager: base}
	u.SetAgentManager(m)
	defer u.shutdownAgentManager()
	if err := u.switchAgent("agent-1"); err != nil {
		t.Fatal(err)
	}
	barrier := make(chan struct{})
	m.events <- session.Event{Barrier: barrier}
	<-barrier
	// Model a lost removal notification. Subsequent saturated wakeups and
	// the barrier must still discover the durable tombstone.
	m.mu.Lock()
	m.agents = m.agents[:1]
	m.closed = []string{"agent-1"}
	m.mu.Unlock()
	for i := 0; i < cap(m.events); i++ {
		m.events <- session.Event{Consultation: true}
	}
	barrier = make(chan struct{})
	m.events <- session.Event{Barrier: barrier}
	<-barrier
	u.handlePendingAgentClosures()
	if u.activeAgent != "main" || u.views["agent-1"] != nil {
		t.Fatalf("lost close retained stale tab: %s %v", u.activeAgent, u.views)
	}
}
