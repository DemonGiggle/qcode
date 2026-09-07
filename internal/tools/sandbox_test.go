package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/llm"
)

func TestSandboxArgumentsHideHomeBeforeBindingWorkspace(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "src", "project")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	state := sandboxState{bwrap: "/usr/bin/bwrap", home: home}
	args := state.arguments(root, []string{root}, "/bin/true")
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{"--unshare-net", "--ro-bind\x00/\x00/", "--tmpfs\x00" + home, "--bind\x00" + root + "\x00" + root, "--clearenv"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("sandbox arguments lack %q:\n%q", expected, args)
		}
	}
	if strings.Contains(joined, "must-not-leak") || strings.Contains(joined, "OPENAI_API_KEY") {
		t.Fatalf("sandbox arguments leaked provider credentials: %q", args)
	}
	if strings.Index(joined, "--tmpfs\x00"+home) > strings.Index(joined, "--bind\x00"+root) {
		t.Fatalf("home must be hidden before workspace is rebound: %q", args)
	}
}

func TestSandboxArgumentsCanAllowNetwork(t *testing.T) {
	state := sandboxState{home: "/home/ada", allowNetwork: true}
	if strings.Contains(strings.Join(state.arguments("/work", []string{"/work"}, "/bin/true"), " "), "--unshare-net") {
		t.Fatal("network-enabled policy still unshared the network namespace")
	}
}

func TestSandboxArgumentsSetInsecureTLSEnvironment(t *testing.T) {
	state := sandboxState{home: "/home/ada", insecureSkipTLSVerify: true}
	arguments := strings.Join(state.arguments("/work", []string{"/work"}, "/bin/true"), "\x00")
	for _, variable := range insecureTLSEnvironment {
		want := "--setenv\x00" + variable.name + "\x00" + variable.value
		if !strings.Contains(arguments, want) {
			t.Fatalf("sandbox arguments lack %q: %q", want, arguments)
		}
	}
}

func TestSandboxMasksProtectedConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\""), 0o600); err != nil {
		t.Fatal(err)
	}
	state := sandboxState{home: "/home/ada", protected: []string{configPath}}
	args := strings.Join(state.arguments(filepath.Dir(configPath), []string{filepath.Dir(configPath)}, "/bin/true"), "\x00")
	if !strings.Contains(args, "--ro-bind\x00/dev/null\x00"+configPath) {
		t.Fatalf("protected config is not masked: %q", args)
	}
}

func TestSandboxDirectoryGrantAndReset(t *testing.T) {
	if runtime.GOOS != "linux" || !securePathSupported(t.TempDir()) {
		t.Skip("secure path confinement requires Linux openat2")
	}
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	extra := filepath.Join(base, "extra")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(extra, "notes.txt")
	if err := os.WriteFile(secret, []byte("approved context"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	registry.SetDirectoryApprover(func(_ context.Context, requested, proposed string) (string, bool, error) {
		requests++
		if requested != secret || proposed != extra {
			t.Fatalf("approval = (%q, %q), want (%q, %q)", requested, proposed, secret, extra)
		}
		return proposed, true, nil
	})
	result, err := registry.Execute(context.Background(), llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"` + secret + `"}`)})
	if err != nil || !strings.Contains(result, "approved context") || requests != 1 {
		t.Fatalf("approved read = (%q, %v), requests=%d", result, err, requests)
	}

	registry.ResetSession()
	registry.SetDirectoryApprover(nil)
	_, err = registry.Execute(context.Background(), llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"` + secret + `"}`)})
	if err == nil || !strings.Contains(err.Error(), "outside approved directories") {
		t.Fatalf("read after reset error = %v", err)
	}
}

func TestSandboxRejectsRootGrant(t *testing.T) {
	if runtime.GOOS != "linux" || !securePathSupported(t.TempDir()) {
		t.Skip("secure path confinement requires Linux openat2")
	}
	root := t.TempDir()
	registry, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	registry.SetDirectoryApprover(func(context.Context, string, string) (string, bool, error) {
		return string(filepath.Separator), true, nil
	})
	_, err = registry.Execute(context.Background(), llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"/etc/hosts"}`)})
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("root grant error = %v", err)
	}
}

func TestSandboxSymlinkEscapeNeedsApproval(t *testing.T) {
	if runtime.GOOS != "linux" || !securePathSupported(t.TempDir()) {
		t.Skip("secure path confinement requires Linux openat2")
	}
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/usr/bin/bwrap"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Execute(context.Background(), llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"escape"}`)})
	if err == nil || !strings.Contains(err.Error(), "outside approved directories") {
		t.Fatalf("symlink escape error = %v", err)
	}
}
