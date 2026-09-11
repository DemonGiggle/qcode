package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
)

func call(t *testing.T, registry *Registry, name string, args any) (string, error) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return registry.Execute(context.Background(), llm.ToolCall{Name: name, Arguments: data})
}

func TestFileToolWorkflow(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, registry, "write", map[string]any{"path": "src/a.txt", "content": "alpha\nbeta\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, registry, "edit", map[string]any{"path": "src/a.txt", "old_text": "beta", "new_text": "gamma"}); err != nil {
		t.Fatal(err)
	}
	result, err := call(t, registry, "read", map[string]any{"path": "src/a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "gamma") {
		t.Fatalf("read result = %q", result)
	}
	result, err = call(t, registry, "search", map[string]any{"path": ".", "pattern": "gam+"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, filepath.Join("src", "a.txt")+":2:gamma") {
		t.Fatalf("search result = %q", result)
	}
	data, err := os.ReadFile(filepath.Join(root, "src", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\ngamma\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestLargeFileSearchPagesAndTargetedRead(t *testing.T) {
	root := t.TempDir()
	data := strings.Repeat("padding\n", 310000) + "WON_EI first\ncontext\nWON_EI second\n"
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := call(t, r, "search", map[string]any{"pattern": "WON_EI", "max_results": 1})
	if err != nil || !strings.Contains(first, "large.txt:310001:WON_EI first") || !strings.Contains(first, "offset=1") {
		t.Fatalf("first page: %q, %v", first, err)
	}
	second, err := call(t, r, "search", map[string]any{"pattern": "WON_EI", "max_results": 1, "offset": 1})
	if err != nil || !strings.Contains(second, "large.txt:310003:WON_EI second") || strings.Contains(second, "more matches") {
		t.Fatalf("second page: %q, %v", second, err)
	}
	excerpt, err := call(t, r, "read", map[string]any{"path": "large.txt", "offset": 310001, "limit": 2})
	if err != nil || !strings.Contains(excerpt, "context") || !strings.Contains(excerpt, "offset=310003") || strings.Contains(excerpt, "padding") {
		t.Fatalf("excerpt: %q, %v", excerpt, err)
	}
}

func TestReadOutputBudgetContinuesWithoutDroppingLines(t *testing.T) {
	root := t.TempDir()
	data := strings.Repeat("x", 40000) + "\nsecond\n" + strings.Repeat("y", 40000) + "\n"
	if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := call(t, r, "read", map[string]any{"path": "wide.txt"})
	if err != nil || len(result) > maxOutput || !strings.Contains(result, "offset=3") || !strings.Contains(result, "second") {
		t.Fatalf("read length %d, err %v", len(result), err)
	}
	result, err = call(t, r, "read", map[string]any{"path": "wide.txt", "offset": 3})
	if err != nil || !strings.Contains(result, strings.Repeat("y", 40000)) {
		t.Fatalf("continuation length %d, err %v", len(result), err)
	}
}

func TestSearchReportsIncompleteScan(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(strings.Repeat("x", 2*1024*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := call(t, r, "search", map[string]any{"pattern": "WON_EI"})
	if err != nil || !strings.Contains(result, "incomplete search") {
		t.Fatalf("search: %q, %v", result, err)
	}
}

func TestFileToolsRejectParentEscape(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(t, registry, "read", map[string]any{"path": "../secret"})
	if err == nil || !strings.Contains(err.Error(), "outside workspace") {
		t.Fatalf("error = %v", err)
	}
}

func TestViewImageLoadsSupportedImage(t *testing.T) {
	for _, test := range []struct {
		name      string
		mediaType string
		data      []byte
	}{
		{name: "screen.png", mediaType: "image/png", data: []byte("\x89PNG\r\n\x1a\nimage-data")},
		{name: "photo.jpg", mediaType: "image/jpeg", data: []byte("\xff\xd8\xff\xdbimage-data")},
		{name: "graphic.webp", mediaType: "image/webp", data: []byte("RIFF\x00\x00\x00\x00WEBPVP8 image-data")},
		{name: "image.gif", mediaType: "image/gif", data: []byte("GIF89aimage-data")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, test.name), test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			registry, err := New(root)
			if err != nil {
				t.Fatal(err)
			}

			result, err := registry.ExecuteDetailed(context.Background(), llm.ToolCall{
				Name: "view_image", Arguments: json.RawMessage(`{"path":"` + test.name + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Images) != 1 || result.Images[0].MediaType != test.mediaType || string(result.Images[0].Data) != string(test.data) {
				t.Fatalf("images = %#v", result.Images)
			}
			if !strings.Contains(result.Output, "Loaded image") {
				t.Fatalf("output = %q", result.Output)
			}
		})
	}
}

func TestViewImageRejectsNonImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = registry.ExecuteDetailed(context.Background(), llm.ToolCall{
		Name: "view_image", Arguments: json.RawMessage(`{"path":"notes.txt"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported image type") {
		t.Fatalf("error = %v, want unsupported image type", err)
	}
}

func TestShellCapturesOutput(t *testing.T) {
	registry, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := call(t, registry, "shell", map[string]any{"command": "printf qcode"})
	if err != nil {
		t.Fatal(err)
	}
	if result != "qcode" {
		t.Fatalf("result = %q", result)
	}
}

func TestShellInsecureTLSVerifyEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell variable expansion")
	}
	registry, err := NewWithOptions(t.TempDir(), Options{InsecureSkipTLSVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := call(t, registry, "shell", map[string]any{"command": "printf '%s' \"$QCODE_INSECURE_SKIP_TLS_VERIFY,$GIT_SSL_NO_VERIFY,$NODE_TLS_REJECT_UNAUTHORIZED,$NPM_CONFIG_STRICT_SSL,$PYTHONHTTPSVERIFY\""})
	if err != nil {
		t.Fatal(err)
	}
	if result != "true,true,0,false,0" {
		t.Fatalf("shell TLS environment = %q", result)
	}
}

func TestCancelledToolDoesNotModifyWorkspace(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = registry.Execute(ctx, llm.ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"cancelled.txt","content":"nope"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v, want context cancellation", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "cancelled.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled write created a file: %v", statErr)
	}
}

func TestShellCancellationStopsBackgroundChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-specific")
	}
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := registry.Execute(ctx, llm.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"touch started && sleep 30 & wait"}`)})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(root, "started")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shell error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+C did not stop the shell process group")
	}
}

func TestShellCancellationKeepsBackgroundJobsAttached(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-specific")
	}
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := registry.Execute(ctx, llm.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"touch started && sleep 30 &"}`)})
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, statErr := os.Stat(filepath.Join(root, "started")); statErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background shell command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shell error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl+C did not stop the attached background job")
	}
}

func TestReadAcceptsStringDecimalOffsetAndShowsContinuation(t *testing.T) {
	root := t.TempDir()
	lines := make([]string, 350)
	for index := range lines {
		lines[index] = "line " + strconv.Itoa(index+1)
	}
	if err := os.WriteFile(filepath.Join(root, "foo.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), llm.ToolCall{
		Name: "read", Arguments: json.RawMessage(`{"limit":100,"offset":"200.0","path":"foo.txt"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "   200\tline 200") || !strings.Contains(result, "   299\tline 299") {
		t.Fatalf("result does not contain requested range:\n%s", result)
	}
	if strings.Contains(result, "line 199\n") || strings.Contains(result, "line 300\n") {
		t.Fatalf("result escaped requested range:\n%s", result)
	}
	if !strings.Contains(result, "use offset=300 to continue") {
		t.Fatalf("missing continuation hint:\n%s", result)
	}
}

func TestToolsRejectUnknownArguments(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "foo.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(context.Background(), llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"foo.txt","offest":2}`)})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

type testSkillLoader map[string]string

func (s testSkillLoader) Load(name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	return value, nil
}

func TestSkillToolLoadsOnlyDiscoveredSkills(t *testing.T) {
	registry, err := NewWithOptions(t.TempDir(), Options{Skills: testSkillLoader{"review": "Review instructions"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), llm.ToolCall{Name: "skill", Arguments: json.RawMessage(`{"name":"review"}`)})
	if err != nil || result != "Review instructions" {
		t.Fatalf("skill result = %q, %v", result, err)
	}
	if _, err := registry.Execute(context.Background(), llm.ToolCall{Name: "skill", Arguments: json.RawMessage(`{"name":"missing"}`)}); err == nil {
		t.Fatal("missing skill succeeded")
	}
	for _, schema := range registry.Schemas() {
		if schema.Name == "skill" {
			return
		}
	}
	t.Fatal("skill schema missing")
}
