package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"qcode/internal/llm"
)

func TestWorkingDirectoryPathsAndAgentIsolation(t *testing.T) {
	processCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, external := t.TempDir(), t.TempDir()
	sub := filepath.Join(root, "sub")
	nested := filepath.Join(sub, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	a, _ := New(root)
	b, _ := New(root)
	for _, step := range []struct{ path, want string }{{"sub", sub}, {"nested", nested}, {"..", sub}, {external, external}} {
		got, err := call(t, a, "change_directory", map[string]any{"path": step.path})
		if err != nil || got != step.want || a.WorkingDirectory() != step.want {
			t.Fatalf("switch %q = %q, %v", step.path, got, err)
		}
		if b.WorkingDirectory() != root {
			t.Fatal("switch moved another agent")
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(sub, link); err != nil {
			t.Fatal(err)
		}
		if got, err := call(t, a, "change_directory", map[string]any{"path": link}); err != nil || got != sub {
			t.Fatalf("symlink = %q, %v", got, err)
		}
	}
	if got, _ := os.Getwd(); got != processCwd {
		t.Fatalf("process cwd changed to %s", got)
	}
}

func TestWorkingDirectoryInvalidDisabledAndCancelled(t *testing.T) {
	root := t.TempDir()
	r, _ := New(root)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "missing", file} {
		if _, err := call(t, r, "change_directory", map[string]any{"path": path}); err == nil {
			t.Fatalf("accepted %q", path)
		}
		if r.WorkingDirectory() != root {
			t.Fatal("invalid switch changed cwd")
		}
	}
	r.DisableTool("change_directory")
	if _, err := call(t, r, "change_directory", map[string]any{"path": t.TempDir()}); err == nil {
		t.Fatal("disabled switch ran")
	}
	for _, tool := range r.EnabledSchemas() {
		if tool.Name == "change_directory" {
			t.Fatal("disabled switch advertised")
		}
	}
	r.EnableTool("change_directory")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ChangeWorkingDirectory(ctx, t.TempDir()); err != context.Canceled {
		t.Fatal(err)
	}
	if r.WorkingDirectory() != root {
		t.Fatal("cancelled switch changed cwd")
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		denied := filepath.Join(root, "denied")
		if err := os.Mkdir(denied, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(denied, 0o755) })
		if _, err := r.ChangeWorkingDirectory(context.Background(), denied); err == nil {
			t.Fatal("unreadable directory accepted")
		}
	}
}

func TestWorkingDirectorySandboxAuthorization(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	r, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/bin/true"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeWorkingDirectory(context.Background(), external); err == nil {
		t.Fatal("implicitly granted access")
	}
	approvals := 0
	r.SetDirectoryApprover(func(ctx context.Context, requested, proposed string) (string, bool, error) {
		approvals++
		if requested != external || proposed != external {
			t.Fatalf("approval = %q, %q", requested, proposed)
		}
		return "", false, nil
	})
	if _, err := r.ChangeWorkingDirectory(context.Background(), external); err == nil || r.WorkingDirectory() != root || r.isGranted(external) {
		t.Fatal("denial changed state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) { cancel(); return external, true, nil })
	if _, err := r.ChangeWorkingDirectory(ctx, external); err != context.Canceled || r.WorkingDirectory() != root || r.isGranted(external) {
		t.Fatal("cancelled approval changed state", err)
	}
	r.SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) { approvals++; return external, true, nil })
	if _, err := r.ChangeWorkingDirectory(context.Background(), external); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ChangeWorkingDirectory(context.Background(), "."); err != nil || approvals != 2 {
		t.Fatal("existing grant prompted again", err)
	}
	args := strings.Join(r.sandbox.command(context.Background(), r.WorkingDirectory(), r.grantPaths(), "pwd").Args, " ")
	if !strings.Contains(args, "--chdir "+external) {
		t.Fatal(args)
	}
	r.ResetSession()
	if r.WorkingDirectory() != root || r.isGranted(external) {
		t.Fatal("reset retained cwd or grant")
	}
}

func TestWorkingDirectoryFilesystemAndShell(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r, _ := New(root)
	if _, err := r.ChangeWorkingDirectory(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"path": "sample.txt", "content": "selected directory\n"})
	result, err := r.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "write", Arguments: data})
	if err != nil || !reflect.DeepEqual(result.ChangedFiles, []string{"sub/sample.txt"}) {
		t.Fatal(result, err)
	}
	if got, err := call(t, r, "read", map[string]any{"path": "sample.txt"}); err != nil || !strings.Contains(got, "selected directory") {
		t.Fatal(got, err)
	}
	if _, err := call(t, r, "edit", map[string]any{"path": "sample.txt", "old_text": "selected", "new_text": "active"}); err != nil {
		t.Fatal(err)
	}
	if got, err := call(t, r, "list", map[string]any{}); err != nil || got != "sample.txt" {
		t.Fatal(got, err)
	}
	if got, err := call(t, r, "search", map[string]any{"pattern": "active"}); err != nil || got != "sample.txt:1:active directory" {
		t.Fatal(got, err)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "test.png"), picture.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = r.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "view_image", Arguments: json.RawMessage(`{"path":"test.png"}`)})
	if err != nil || len(result.Images) != 1 {
		t.Fatal(result, err)
	}
	if runtime.GOOS != "windows" {
		if got, err := call(t, r, "shell", map[string]any{"command": "pwd"}); err != nil || strings.TrimSpace(got) != sub {
			t.Fatal(got, err)
		}
	}
	if _, err := call(t, r, "read", map[string]any{"path": "../outside.txt"}); err == nil {
		t.Fatal("file confinement did not follow selected directory")
	}
	external := t.TempDir()
	if _, err := r.ChangeWorkingDirectory(context.Background(), external); err != nil {
		t.Fatal(err)
	}
	data = json.RawMessage(`{"path":"sample.txt","content":"external"}`)
	result, err = r.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "write", Arguments: data})
	if err != nil || !reflect.DeepEqual(result.ChangedFiles, []string{filepath.Join(external, "sample.txt")}) {
		t.Fatal(result, err)
	}
}

func TestWorkingDirectoryPersistenceAndFallback(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	r, _ := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/bin/true"})
	r.addGrant(external)
	if _, err := r.ChangeWorkingDirectory(context.Background(), external); err != nil {
		t.Fatal(err)
	}
	snapshot := r.SaveTools()
	restored, _ := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/bin/true"})
	restored.SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) {
		t.Fatal("restore prompted for access")
		return "", false, nil
	})
	if err := restored.RestoreTools(snapshot); err != nil || restored.WorkingDirectory() != external {
		t.Fatal(restored.WorkingDirectory(), err)
	}
	for _, cwd := range []string{filepath.Join(root, "missing"), t.TempDir(), "relative"} {
		data, _ := json.Marshal(savedTools{Cwd: cwd})
		if err := restored.RestoreTools(data); err != nil || restored.WorkingDirectory() != root || len(restored.RestoreWarnings()) != 1 {
			t.Fatal(restored.WorkingDirectory(), restored.RestoreWarnings(), err)
		}
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "link")
		if err := os.Symlink(external, link); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(savedTools{Cwd: link, Grants: []string{root}})
		if err := restored.RestoreTools(data); err != nil || restored.WorkingDirectory() != root || len(restored.RestoreWarnings()) != 1 {
			t.Fatal("saved symlink bypassed authorization", err)
		}
	}
	if err := restored.RestoreTools(json.RawMessage(`{"Disabled":{}}`)); err != nil || restored.WorkingDirectory() != root || len(restored.RestoreWarnings()) != 0 {
		t.Fatal("legacy snapshot did not default to launch", err)
	}
	if err := restored.RestoreToolSelections(snapshot); err != nil || restored.WorkingDirectory() != root || restored.isGranted(external) {
		t.Fatal("fresh state imported cwd or grants", err)
	}
}

func TestWorkingDirectoryConcurrentReaders(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	r, _ := New(root)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if cwd := r.WorkingDirectory(); cwd != root && cwd != other {
					t.Errorf("invalid cwd %q", cwd)
				}
			}
		}()
	}
	for i := 0; i < 100; i++ {
		path := root
		if i%2 == 0 {
			path = other
		}
		if _, err := r.ChangeWorkingDirectory(context.Background(), path); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestWorkingDirectoryGitWorktreeAttribution(t *testing.T) {
	root := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git(root, "init")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "same.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", ".")
	git(root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "worktree")
	git(root, "worktree", "add", "--detach", worktree)
	r, _ := New(root)
	for _, dir := range []string{root, worktree} {
		if _, err := r.ChangeWorkingDirectory(context.Background(), filepath.Join(dir, "sub")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "same.txt"), []byte("changed "+dir), 0o644); err != nil {
			t.Fatal(err)
		}
		state := r.WorkspaceState(context.Background())
		want := "sub/same.txt"
		if dir == worktree {
			want = filepath.Join(worktree, "sub", "same.txt")
		}
		if len(state) != 1 || strings.Contains(state[want], ":missing") || state[want] == "" {
			t.Fatalf("git attribution = %v; want %s", state, want)
		}
	}
}
