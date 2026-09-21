package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSandboxCommandPathsValidation(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	protectedFile := filepath.Join(base, "config.toml")
	if err := os.WriteFile(protectedFile, []byte("api_key = \"secret\""), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linkbin")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(base, "missing")
	fileAsDir := filepath.Join(base, "file")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	raw := []string{
		bin,
		link, // symlinked duplicate of bin
		bin,  // exact duplicate
		"  ", // invalid blank
		missing,
		fileAsDir,
		string(filepath.Separator), // root rejected
		base,                       // contains protected file -> rejected
		protectedFile,              // protected file itself -> rejected
	}
	resolved, warnings := ResolveSandboxCommandPaths(raw, []string{protectedFile})
	if len(resolved) != 1 || resolved[0] != bin {
		t.Fatalf("resolved = %q, want [%q]", resolved, bin)
	}
	// 2 duplicates + blank + missing + file + root + parent-of-protected + protected = 8 warnings
	if len(warnings) != 8 {
		t.Fatalf("warnings = %q, want 8 entries", warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"duplicate", "empty", "does not exist", "not an existing directory", "root", "protected"} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Fatalf("warnings lack %q: %q", want, warnings)
		}
	}
}

func TestResolveSandboxCommandPathsExpandsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, warnings := ResolveSandboxCommandPaths([]string{"~/.local/bin"}, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %q", warnings)
	}
	if len(resolved) != 1 || resolved[0] != bin {
		t.Fatalf("resolved = %q, want [%q]", resolved, bin)
	}
	// Idempotent: canonical input passes through.
	again, _ := ResolveSandboxCommandPaths(resolved, nil)
	if len(again) != 1 || again[0] != bin {
		t.Fatalf("idempotent resolved = %q", again)
	}
}

func TestSandboxArgumentsMountsCommandPathsReadOnly(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/bin")
	state := sandboxState{bwrap: "/usr/bin/bwrap", home: home, commandPaths: []string{bin}}
	args := strings.Join(state.arguments(root, []string{root}, "/bin/true"), "\x00")
	if !strings.Contains(args, "--tmpfs\x00"+home) {
		t.Fatalf("home not hidden: %q", args)
	}
	if !strings.Contains(args, "--ro-bind\x00"+bin+"\x00"+bin) {
		t.Fatalf("command dir not ro-bound: %q", args)
	}
	if strings.Contains(args, "--bind\x00"+bin+"\x00"+bin) {
		t.Fatalf("command dir must not be read/write: %q", args)
	}
	if strings.Index(args, "--tmpfs\x00"+home) > strings.Index(args, "--ro-bind\x00"+bin) {
		t.Fatalf("home must be hidden before command dir is rebound: %q", args)
	}
	// PATH prepends the allowlisted dir ahead of the safe system path.
	pathIdx := strings.Index(args, "--setenv\x00PATH\x00")
	if pathIdx < 0 {
		t.Fatalf("PATH not set: %q", args)
	}
	pathValue := args[pathIdx:]
	if !strings.Contains(pathValue, bin+":") && !strings.HasSuffix(pathValue, bin) {
		t.Fatalf("PATH does not prepend command dir: %q", args)
	}
	binPos := strings.Index(pathValue, bin)
	usrPos := strings.Index(pathValue, "/usr/bin")
	if usrPos >= 0 && binPos > usrPos {
		t.Fatalf("command dir must precede system PATH: %q", args)
	}
}

func TestSandboxCommandPathsSurviveReset(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "tools")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWithOptions(root, Options{Sandbox: true, BubblewrapPath: "/unused/bwrap", SandboxCommandPaths: []string{bin}})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.sandbox.commandPaths) != 1 {
		t.Fatalf("commandPaths = %q", registry.sandbox.commandPaths)
	}
	before := strings.Join(registry.sandbox.arguments(registry.root, registry.grantPaths(), "/bin/true"), " ")
	if !strings.Contains(before, "--ro-bind "+bin+" "+bin) {
		t.Fatalf("missing ro-bind before reset: %q", before)
	}
	registry.ResetSession()
	if len(registry.sandbox.commandPaths) != 1 {
		t.Fatalf("reset cleared persistent command paths: %q", registry.sandbox.commandPaths)
	}
	after := strings.Join(registry.sandbox.arguments(registry.root, registry.grantPaths(), "/bin/true"), " ")
	if !strings.Contains(after, "--ro-bind "+bin+" "+bin) {
		t.Fatalf("missing ro-bind after reset: %q", after)
	}
	if len(registry.grantPaths()) != 1 || registry.grantPaths()[0] != registry.root {
		t.Fatalf("grants changed unexpectedly: %q", registry.grantPaths())
	}
}

func TestSandboxCommandCoveredByGrantSkipsRedundantBind(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "tools")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	state := sandboxState{bwrap: "/usr/bin/bwrap", home: "/home/ada", commandPaths: []string{nested}}
	args := strings.Join(state.arguments(root, []string{root}, "/bin/true"), "\x00")
	// Already visible read/write via the workspace grant: no extra ro-bind.
	if strings.Contains(args, "--ro-bind\x00"+nested) {
		t.Fatalf("redundant ro-bind for grant-covered dir: %q", args)
	}
	// Still on PATH.
	if !strings.Contains(args, nested) {
		t.Fatalf("covered dir missing from PATH: %q", args)
	}
}

func TestNewWithOptionsResolvesRawCommandPaths(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(bin), "link")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	registry, err := NewWithOptions(root, Options{
		Sandbox:             true,
		BubblewrapPath:      "/unused/bwrap",
		SandboxCommandPaths: []string{bin, link, filepath.Join(t.TempDir(), "missing"), ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.sandbox.commandPaths) != 1 || registry.sandbox.commandPaths[0] != bin {
		t.Fatalf("commandPaths = %q, want [%q]", registry.sandbox.commandPaths, bin)
	}
}
