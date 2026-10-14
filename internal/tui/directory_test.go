package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

// The second model turn blocks after the directory switch so presentation
// can be checked while the task is still running, before its final answer.
type directoryPresentationProvider struct {
	destination string
	ready       chan struct{}
	release     chan struct{}
}

func (*directoryPresentationProvider) Name() string { return "directory-presentation" }
func (p *directoryPresentationProvider) Complete(ctx context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	if request.Messages[len(request.Messages)-1].Role == "user" {
		args, _ := json.Marshal(map[string]string{"path": p.destination})
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "switch", Name: "change_directory", Arguments: args}}}}, nil
	}
	close(p.ready)
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-p.release:
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func TestWorkingDirectoryPresentationDuringTaskAndTabSwitch(t *testing.T) {
	root, workerDir := t.TempDir(), t.TempDir()
	mainDir := filepath.Join(root, "main-directory")
	if err := os.Mkdir(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := New(nil, out, nil, "test", "model", root)
	u.width, u.height, u.fixedInput = 420, 24, true
	host := control.NewHost(context.Background(), 3)
	u.SetAgentManager(host)
	defer u.shutdownAgentManager()
	providers := map[string]*directoryPresentationProvider{}
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		r, err := tools.New(root)
		if err != nil {
			return nil, err
		}
		dir := workerDir
		if main {
			dir = mainDir
		}
		p := &directoryPresentationProvider{destination: dir, ready: make(chan struct{}), release: make(chan struct{})}
		providers[id] = p
		display, response := u.AddAgentView(id, p.Name(), model)
		return agent.New(p, model, host.WrapToolset(id, r, main), trace.New(display, false), response, 5), nil
	})
	if _, err := host.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	worker, err := host.Create("model")
	if err != nil {
		t.Fatal(err)
	}
	if err := u.switchAgent("main"); err != nil {
		t.Fatal(err)
	}
	u.renderInput(inputPrompt, "draft", 3)
	sub := host.Subscribe(context.Background(), 0)
	defer sub.Close()
	updates := u.SubscribePresentation(context.Background())
	if err := host.Start("main", "switch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-providers["main"].ready:
	case <-time.After(2 * time.Second):
		t.Fatal("main did not switch")
	}
	deadline := time.After(2 * time.Second)
	for {
		u.screenMu.Lock()
		frame := strings.Join(u.inputScreenRows, "\n")
		u.screenMu.Unlock()
		if strings.Contains(frame, mainDir) {
			break
		}
		select {
		case <-updates:
		case <-deadline:
			t.Fatal("terminal did not update cwd during task", frame)
		}
	}
	if !strings.Contains(u.RemotePresentation().StatusBar, mainDir) {
		t.Fatal("browser did not update cwd")
	}
	seenDirectoryEvent := false
	for !seenDirectoryEvent {
		select {
		case event := <-sub.Events:
			seenDirectoryEvent = event.DirectoryChanged && event.Agent.Cwd == mainDir
		case <-deadline:
			t.Fatal("control plane did not publish directory event")
		}
	}
	if err := host.Start(worker.ID, "switch"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-providers[worker.ID].ready:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not switch")
	}
	presentation := u.RemotePresentation()
	if presentation.Active != "main" || !strings.Contains(presentation.StatusBar, mainDir) || strings.Contains(presentation.StatusBar, workerDir) {
		t.Fatal("background switch changed active footer", presentation.StatusBar)
	}
	for _, view := range presentation.Views {
		want := mainDir
		if view.ID == worker.ID {
			want = workerDir
		}
		if view.Cwd != want || !strings.Contains(view.StatusBar, want) || view.Status != string(session.StatusRunning) {
			t.Fatalf("wrong browser view: %+v", view)
		}
		if strings.Contains(strings.Join(view.Lines, "\n"), "Completed in") {
			t.Fatal("directory event reported completion")
		}
	}
	if err := u.switchAgent(worker.ID); err != nil {
		t.Fatal(err)
	}
	if got := u.RemotePresentation(); got.Active != worker.ID || !strings.Contains(got.StatusBar, workerDir) {
		t.Fatal("tab switch did not display selected cwd", got.StatusBar)
	}
	u.screenMu.Lock()
	frame := strings.Join(u.inputScreenRows, "\n")
	u.screenMu.Unlock()
	if !strings.Contains(frame, workerDir) {
		t.Fatal("terminal tab switch retained old directory", frame)
	}
	// Concurrent directory reads and terminal/browser tab switches exercise the
	// synchronized accessors and status formatting without touching history.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_ = u.RemotePresentation()
		}
	}()
	for i := 0; i < 30; i++ {
		id := "main"
		if i%2 != 0 {
			id = worker.ID
		}
		if err := u.switchAgent(id); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestWorkingDirectoryPresentationEventDoesNotNotifyCompletion(t *testing.T) {
	u := New(nil, nil, nil, "test", "model", "launch")
	u.AddAgentView("main", "test", "model")
	u.AddAgentView("agent-1", "test", "model")
	event := session.Event{DirectoryChanged: true, Agent: session.Summary{ID: "agent-1", Name: "worker", Status: session.StatusCompleted, Cwd: "other"}}
	u.handleAgentEvent(event)
	for _, view := range u.views {
		if lines := strings.Join(view.display.Lines(), "\n"); lines != "" {
			t.Fatal("directory update emitted task notification", lines)
		}
	}
	if u.views["agent-1"].unseen {
		t.Fatal("directory update marked background task complete")
	}
}

func TestWorkingDirectoryCreationFailureDiscardsBrowserView(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	u := New(nil, nil, nil, "test", "model", root)
	m := agent.NewAgentManager(context.Background(), 3)
	defer m.Shutdown()
	u.manager = m
	m.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		r, err := tools.New(root)
		if err != nil {
			return nil, err
		}
		display, response := u.AddAgentView(id, "test", model)
		runner := agent.New(&directoryPresentationProvider{}, model, m.WrapToolset(id, r, main), trace.New(display, false), response, 5)
		runner.SetCreationRollback(func() { u.RemoveAgentView(id) })
		if !main {
			if err := os.Remove(destination); err != nil {
				return nil, err
			}
		}
		return runner, nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	// Destination disappears between the caller's validation and child setup.
	if _, err := m.CreateInheritedAt(context.Background(), "main", "", destination); err == nil {
		t.Fatal("disappearing destination accepted")
	}
	if len(m.List()) != 1 || len(u.RemotePresentation().Views) != 1 || len(m.WorkRecords()) != 0 {
		t.Fatal("failed setup left a child or browser tab")
	}
}
