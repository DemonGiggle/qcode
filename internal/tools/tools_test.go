package tools

import (
	"context"
	"encoding/json"
	"errors"
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
