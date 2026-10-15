package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func TestInspectionToolsOutsideWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	root, external := filepath.Join(base, "workspace"), filepath.Join(base, "extra")
	for _, dir := range []string{root, external} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(external, "notes.txt"), []byte("outside context\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "image.png"), picture.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sandbox := range []bool{false, true} {
		mode := "unsandboxed"
		if sandbox {
			mode = "sandboxed"
		}
		t.Run(mode, func(t *testing.T) {
			if sandbox && (runtime.GOOS != "linux" || !securePathSupported(root)) {
				t.Skip("secure path confinement requires Linux openat2")
			}
			for _, style := range []struct{ name, dir string }{{"absolute", external}, {"parent-relative", filepath.Join("..", "extra")}} {
				for _, test := range []struct {
					tool, path, want string
				}{
					{"read", filepath.Join(style.dir, "notes.txt"), "outside context"},
					{"list", style.dir, "image.png\nnotes.txt"},
					{"search", style.dir, filepath.Join("..", "extra", "notes.txt") + ":1:outside context"},
					{"view_image", filepath.Join(style.dir, "image.png"), "Loaded image"},
				} {
					t.Run(style.name+"/"+test.tool, func(t *testing.T) {
						registry, err := NewWithOptions(root, Options{Sandbox: sandbox, BubblewrapPath: "/unused/bwrap"})
						if err != nil {
							t.Fatal(err)
						}
						args := map[string]any{"path": test.path}
						if test.tool == "search" {
							args["pattern"] = "outside context"
						}
						data, err := json.Marshal(args)
						if err != nil {
							t.Fatal(err)
						}
						call := llm.ToolCall{Name: test.tool, Arguments: data}
						checkDenied := func() {
							t.Helper()
							if _, err := registry.ExecuteDetailed(context.Background(), call); err == nil || !strings.Contains(err.Error(), "outside approved directories") {
								t.Fatalf("unapproved inspection error = %v", err)
							}
						}
						approvals := 0
						if sandbox {
							checkDenied()
							registry.SetDirectoryApprover(func(_ context.Context, requested, proposed string) (string, bool, error) {
								approvals++
								want := external
								if test.tool == "read" || test.tool == "view_image" {
									want = filepath.Join(external, filepath.Base(test.path))
								}
								if requested != want || proposed != external {
									t.Fatalf("approval = (%q, %q), want (%q, %q)", requested, proposed, want, external)
								}
								return proposed, true, nil
							})
						}
						for i := 0; i < 2; i++ {
							result, err := registry.ExecuteDetailed(context.Background(), call)
							if err != nil || !strings.Contains(result.Output, test.want) {
								t.Fatalf("inspection = (%q, %v), want %q", result.Output, err, test.want)
							}
							if test.tool == "view_image" && (len(result.Images) != 1 || result.Images[0].MediaType != "image/png" || !bytes.Equal(result.Images[0].Data, picture.Bytes())) {
								t.Fatalf("image result = %#v", result.Images)
							}
						}
						if sandbox {
							if approvals != 1 {
								t.Fatalf("approval requests = %d, want 1", approvals)
							}
							registry.ResetSession()
							registry.SetDirectoryApprover(nil)
							checkDenied()
						}
					})
				}
			}
		})
	}
}

func TestWriteAndEditRejectOutsideWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "notes.txt")
	const original = "original context\n"
	if err := os.WriteFile(file, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join("..", "notes.txt")} {
		for _, tool := range []string{"write", "edit"} {
			args := map[string]any{"path": path}
			if tool == "write" {
				args["content"] = "changed context\n"
			} else {
				args["old_text"], args["new_text"] = "original", "changed"
			}
			if _, err := call(t, registry, tool, args); err == nil || !strings.Contains(err.Error(), "outside workspace") {
				t.Fatalf("%s %q error = %v", tool, path, err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != original {
				t.Fatalf("external file = %q, %v", data, err)
			}
		}
	}
	for _, path := range []string{filepath.Join(base, "new", "file.txt"), filepath.Join("..", "new", "file.txt")} {
		if _, err := call(t, registry, "write", map[string]any{"path": path, "content": "new"}); err == nil || !strings.Contains(err.Error(), "outside workspace") {
			t.Fatalf("write %q error = %v", path, err)
		}
		if _, err := os.Stat(filepath.Join(base, "new")); !os.IsNotExist(err) {
			t.Fatalf("rejected write created parent directory: %v", err)
		}
	}
}
