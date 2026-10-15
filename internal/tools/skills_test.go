package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/skills"
)

func TestSkillCompanionUsesBaseDirectoryAndExistingApprovals(t *testing.T) {
	for _, mode := range []string{"unsandboxed", "sandboxed"} {
		for _, location := range []string{"workspace", "user", "custom"} {
			t.Run(mode+"/"+location, func(t *testing.T) {
				root := t.TempDir()
				sandbox := mode == "sandboxed"
				if sandbox && (runtime.GOOS != "linux" || !securePathSupported(root)) {
					t.Skip("secure path confinement requires Linux openat2")
				}
				home := t.TempDir()
				t.Setenv("HOME", home)
				skillRoot := filepath.Join(root, ".qcode", "skills")
				var customPaths []string
				switch location {
				case "user":
					skillRoot = filepath.Join(home, ".qcode", "skills")
				case "custom":
					skillRoot = filepath.Join(t.TempDir(), "team skills")
					customPaths = []string{skillRoot}
				}
				const markdown = "---\ndescription: Query bundled data.\n---\n\nRead reference.txt before using foo.db.\n"
				const reference = "companion reference contents"
				const binary = "SQLite format 3\x00adjacent binary contents\xff"
				skillDir := filepath.Join(skillRoot, "query")
				if err := os.MkdirAll(skillDir, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, content := range map[string]string{"SKILL.md": markdown, "reference.txt": reference, "foo.db": binary} {
					if err := os.WriteFile(filepath.Join(skillDir, name), []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				otherDir := filepath.Join(root, "other directory")
				if err := os.Mkdir(otherDir, 0o755); err != nil {
					t.Fatal(err)
				}
				selection := skills.NewLazySelection(root, customPaths...)
				selection.Set([]string{"query"})
				registry, err := NewWithOptions(root, Options{Sandbox: sandbox, BubblewrapPath: "/unused/bwrap", Skills: selection})
				if err != nil {
					t.Fatal(err)
				}
				approvals := 0
				allow := false
				registry.SetDirectoryApprover(func(_ context.Context, requested, proposed string) (string, bool, error) {
					approvals++
					if requested != filepath.Join(skillDir, "reference.txt") || proposed != skillDir {
						t.Fatalf("unexpected companion approval: %q, %q", requested, proposed)
					}
					return proposed, allow, nil
				})
				// Change directory before loading to exercise the fixed launch root.
				if _, err := call(t, registry, "change_directory", map[string]any{"path": otherDir}); err != nil {
					t.Fatal(err)
				}
				processCwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := call(t, registry, "skill", map[string]any{"name": "query"})
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasSuffix(loaded, "\n\n"+markdown) || strings.Contains(loaded, reference) || strings.Contains(loaded, binary) {
					t.Fatalf("skill load ingested or changed file content: %q", loaded)
				}
				if got, _ := os.Getwd(); got != processCwd || registry.WorkingDirectory() != otherDir || approvals != 0 {
					t.Fatal("loading the skill changed the working directory or requested access")
				}
				// Resolve the adjacent reference using only the returned metadata.
				_, after, ok := strings.Cut(loaded, "\nBase directory: ")
				base, _, _ := strings.Cut(after, "\n")
				if !ok || !filepath.IsAbs(base) || base != skillDir {
					t.Fatalf("invalid skill base directory: %q", base)
				}
				if _, err := call(t, registry, "change_directory", map[string]any{"path": root}); err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"path": filepath.Join(base, "reference.txt")}
				needsApproval := sandbox && location != "workspace"
				if needsApproval {
					if registry.isGranted(base) {
						t.Fatal("loading the skill implicitly granted directory access")
					}
					if _, err := call(t, registry, "read", args); err == nil || approvals != 1 || registry.isGranted(base) {
						t.Fatalf("denied companion read: %v, approvals=%d", err, approvals)
					}
				}
				allow = true
				result, err := call(t, registry, "read", args)
				if err != nil || !strings.Contains(result, reference) {
					t.Fatalf("companion read = %q, %v", result, err)
				}
				wantApprovals := 0
				if needsApproval {
					wantApprovals = 2
				}
				if approvals != wantApprovals {
					t.Fatalf("companion approvals = %d, want %d", approvals, wantApprovals)
				}
			})
		}
	}
}
