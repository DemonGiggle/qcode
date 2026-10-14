package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

func newDirectoryManager(t *testing.T, root string, options tools.Options) (*AgentManager, map[string]*tools.Registry) {
	t.Helper()
	m := NewAgentManager(context.Background(), 10)
	registries := map[string]*tools.Registry{}
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		r, err := tools.NewWithOptions(root, options)
		if err != nil {
			return nil, err
		}
		registries[id] = r
		return New(&resumeProvider{}, model, m.WrapToolset(id, r, main), trace.New(io.Discard, false), io.Discard, 20), nil
	})
	if _, err := m.CreateMain("model"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m, registries
}

func directoryCall(t *testing.T, a *Agent, path string) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": path})
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "change_directory", Arguments: args}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingDirectoryCreationInheritanceAndExplicitCwd(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	nested := filepath.Join(sub, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	m, registries := newDirectoryManager(t, root, tools.Options{})
	main, _ := m.Agent("main")
	directoryCall(t, main, "sub")
	main.ToggleTool("write", false)
	main.SetSkills([]prompt.SkillSummary{{Name: "review"}})
	main.SetMaxSteps(11)
	for _, tc := range []struct{ cwd, want string }{{"", sub}, {"nested", nested}, {t.TempDir(), ""}} {
		if tc.want == "" {
			tc.want = tc.cwd
		}
		args := map[string]any{"task": "inspect"}
		if tc.cwd != "" {
			args["cwd"] = tc.cwd
		}
		data, _ := json.Marshal(args)
		result, err := main.executeDetailed(context.Background(), llm.ToolCall{Name: "create_agent", Arguments: data})
		if err != nil || !result.EndTurn {
			t.Fatal(result, err)
		}
		list := m.List()
		summary := list[len(list)-1]
		child, _ := m.Agent(summary.ID)
		if child.WorkingDirectory() != tc.want || summary.Cwd != tc.want || child.ToolEnabled("write") || child.MaxSteps() != 11 || len(child.SelectedSkills()) != 1 {
			t.Fatalf("child settings: %+v", summary)
		}
		waitManagerStatus(t, m, summary.ID, StatusCompleted)
		provider := child.provider.(*resumeProvider)
		if !strings.Contains(provider.request.Messages[0].Content, tc.want) {
			t.Fatal("task began before directory was applied", provider.request.Messages[0])
		}
		if !strings.Contains(m.RosterContext(), tc.want) {
			t.Fatal("roster omitted cwd")
		}
		if main.WorkingDirectory() != sub {
			t.Fatal("creation changed parent directory")
		}
	}
	// Interactive /agent new still uses ordinary creation defaults.
	plain, err := m.Create("model")
	if err != nil || plain.Cwd != root || registries[plain.ID].WorkingDirectory() != root {
		t.Fatal(plain, err)
	}
}

func TestWorkingDirectoryCreationFailsBeforeAllocation(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	m, registries := newDirectoryManager(t, root, tools.Options{Sandbox: true, BubblewrapPath: "/bin/true"})
	main, _ := m.Agent("main")
	registries["main"].SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) { return "", false, nil })
	for _, cwd := range []string{"", external, filepath.Join(root, "missing")} {
		args, _ := json.Marshal(map[string]string{"cwd": cwd, "task": "must not run"})
		if _, err := main.executeDetailed(context.Background(), llm.ToolCall{Name: "create_agent", Arguments: args}); err == nil {
			t.Fatal("invalid creation succeeded")
		}
		if len(m.List()) != 1 || len(registries) != 1 || len(m.WorkRecords()) != 0 || main.WorkingDirectory() != root {
			t.Fatal("failed creation allocated or queued a child")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	registries["main"].SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) { cancel(); return external, true, nil })
	if _, err := m.CreateInheritedAt(ctx, "main", "", external); err != context.Canceled || len(m.List()) != 1 || len(registries) != 1 {
		t.Fatal("cancelled creation allocated a child", err)
	}
	registries["main"].SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) { return external, true, nil })
	created, err := m.CreateInheritedAt(context.Background(), "main", "", external)
	if err != nil || created.Cwd != external || main.WorkingDirectory() != root {
		t.Fatal(created, err)
	}
	if _, err := registries[created.ID].ChangeWorkingDirectory(context.Background(), external); err != nil {
		t.Fatal("approved grant did not reach child", err)
	}
}

type rejectedDirectoryToolset struct{ managerToolset }

func (*rejectedDirectoryToolset) RestoreTools(json.RawMessage) error {
	return errors.New("inheritance failed")
}

func TestWorkingDirectoryCreationPreparationFailureLeavesNoChild(t *testing.T) {
	m := newTestManager(t, 3)
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&managerProvider{}, model, m.WrapToolset(id, &rejectedDirectoryToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := m.CreateInherited("main", ""); err == nil || len(m.List()) != 1 || len(m.WorkRecords()) != 0 {
		t.Fatal("failed preparation exposed a child", err)
	}
}

func TestWorkingDirectoryRepeatedRelativeCallsAndFreshContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "sub", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Join(root, "sub"), filepath.Join(root, "sub", "sub"), filepath.Join(root, "sub", "sub", "sub")} {
		if err := os.WriteFile(filepath.Join(dir, "sample"), []byte(dir), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := tools.New(root)
	p := &scriptedProvider{}
	for i := 0; i < 3; i++ {
		p.sequence = append(p.sequence, llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"sample"}`)}, llm.ToolCall{Name: "change_directory", Arguments: json.RawMessage(`{"path":"sub"}`)})
	}
	a := New(p, "model", r, trace.New(io.Discard, false), io.Discard, 10)
	if err := a.Run(context.Background(), "inspect nested dirs"); err != nil {
		t.Fatal(err)
	}
	for i, request := range p.requests {
		want := root
		for j := 0; j < i/2; j++ {
			want = filepath.Join(want, "sub")
		}
		if !strings.Contains(request.Messages[0].Content, want) {
			t.Fatal("stale cwd context", request.Messages[0])
		}
	}
	if strings.Contains(a.messages[0].Content, "Current agent working directory:") {
		t.Fatal("cwd context persisted in conversation")
	}
}

func TestWorkingDirectoryModesAndDisabledTool(t *testing.T) {
	r, _ := tools.New(t.TempDir())
	a := New(&resumeProvider{}, "model", r, trace.New(io.Discard, false), io.Discard, 4)
	original := a.WorkingDirectory()
	for _, skillPlan := range []bool{false, true} {
		if skillPlan {
			a.SetSkillPlanMode(true)
		} else {
			a.SetPlanMode(true)
		}
		if a.ToolEnabled("change_directory") || hasSchema(a.enabledSchemas(), "change_directory") {
			t.Fatal("mode advertised switch")
		}
		if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "change_directory", Arguments: json.RawMessage(`{"path":"."}`)}); err == nil || a.WorkingDirectory() != original {
			t.Fatal("mode allowed switch", err)
		}
	}
	a.SetSkillPlanMode(false)
	a.ToggleTool("change_directory", false)
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "change_directory", Arguments: json.RawMessage(`{"path":"."}`)}); err == nil {
		t.Fatal("disabled switch allowed")
	}
}

func TestWorkingDirectoryCheckpointEventsResumeAndNew(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	m, _ := newDirectoryManager(t, root, tools.Options{})
	main, _ := m.Agent("main")
	directoryCall(t, main, external)
	select {
	case event := <-m.Events():
		if !event.DirectoryChanged || event.Agent.Cwd != external || event.Agent.Status != StatusIdle {
			t.Fatal(event)
		}
	case <-time.After(time.Second):
		t.Fatal("directory notification missing")
	}
	saved, next := m.SaveAgents()
	if saved[0].Summary.Cwd != external {
		t.Fatal("summary checkpoint cwd missing")
	}
	restored := NewAgentManager(context.Background(), 10)
	defer restored.Shutdown()
	restored.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		r, _ := tools.New(root)
		return New(&resumeProvider{}, model, restored.WrapToolset(id, r, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if err := restored.RestoreAgents(saved, next); err != nil {
		t.Fatal(err)
	}
	resumed, _ := restored.Agent("main")
	if resumed.WorkingDirectory() != external {
		t.Fatal("resume lost cwd")
	}
	freshRegistry, _ := tools.New(root)
	fresh := New(&resumeProvider{}, "model", freshRegistry, trace.New(io.Discard, false), io.Discard, 4)
	if err := fresh.RestoreFreshState(saved[0].State); err != nil || fresh.WorkingDirectory() != root {
		t.Fatal("fresh state retained directory", err)
	}
	if err := m.Reset("main"); err != nil {
		t.Fatal(err)
	}
	summary, _ := m.Summary("main")
	if main.WorkingDirectory() != root || summary.Cwd != root {
		t.Fatal("/new retained directory", summary)
	}
}

func TestWorkingDirectoryShellAttributionAcrossGitWorktrees(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell commands")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "same.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "worktree")
	git("worktree", "add", "--detach", worktree)
	m, _ := newDirectoryManager(t, root, tools.Options{})
	main, _ := m.Agent("main")
	for _, dir := range []string{root, worktree} {
		directoryCall(t, main, filepath.Join(dir, "sub"))
		if _, err := main.executeDetailed(context.Background(), llm.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"printf changed >> same.txt"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	summary, _ := m.Summary("main")
	if len(summary.ChangedFiles) != 2 {
		t.Fatal("worktree changes collided", summary.ChangedFiles)
	}
	paths := strings.Join(summary.ChangedFiles, "\n")
	if !strings.Contains(paths, "sub/same.txt") || !strings.Contains(paths, filepath.Join(worktree, "sub", "same.txt")) {
		t.Fatal(summary.ChangedFiles)
	}
}
