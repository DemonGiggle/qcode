package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

type blockingCompactRunner struct{ release <-chan struct{} }

func (*blockingCompactRunner) Run(context.Context, string) error { return nil }
func (r *blockingCompactRunner) Compact(context.Context) (string, error) {
	<-r.release
	return "Conversation compacted.", nil
}

type compactNavigationManager struct {
	agentController
	main      Runner
	worker    Runner
	submitted bool
}

func (m *compactNavigationManager) SubmitCompact(id string) (session.Submission, error) {
	if id != "main" {
		return session.Submission{}, fmt.Errorf("unexpected agent %s", id)
	}
	m.submitted = true
	return session.Submission{TargetID: id}, nil
}
func (m *compactNavigationManager) List() []session.Summary {
	return []session.Summary{{ID: "main", Name: "main", Model: "test", Status: session.StatusRunning}, {ID: "agent-1", Name: "agent-1", Model: "test", Status: session.StatusIdle}}
}
func (m *compactNavigationManager) Summary(id string) (session.Summary, error) {
	for _, summary := range m.List() {
		if summary.ID == id {
			return summary, nil
		}
	}
	return session.Summary{}, fmt.Errorf("unknown agent %s", id)
}
func (m *compactNavigationManager) Runner(id string) (any, bool) {
	if id == "main" {
		return m.main, true
	}
	if id == "agent-1" {
		return m.worker, true
	}
	return nil, false
}

func TestTabSwitchDuringManualCompaction(t *testing.T) {
	terminal, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	release := make(chan struct{})
	main := &blockingCompactRunner{release: release}
	manager := &compactNavigationManager{main: main, worker: statusRunner{}}
	u := New(terminal, terminal, main, "test", "test", t.TempDir())
	u.AddAgentView("main", "test", "test")
	u.AddAgentView("agent-1", "test", "test")
	u.SetDetachedAgentManager(manager)

	done := make(chan struct{})
	go func() {
		u.compactConversation(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("/compact blocked the UI loop")
	}
	defer close(release)
	if !manager.submitted {
		t.Fatal("compaction was not submitted to the agent queue")
	}
	u.input.route([]byte(altNextTab))
	u.handlePendingTabSwitch()
	if u.activeAgent != "agent-1" {
		t.Fatalf("tab switch during compaction selected %s", u.activeAgent)
	}
	if lines := strings.Join(u.views["main"].display.Lines(), "\n"); !strings.Contains(lines, "Compacting conversation") {
		t.Fatalf("main tab lacks compaction progress: %q", lines)
	}
}
