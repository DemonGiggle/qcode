package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options controls optional protections around model-triggered tools.
type Options struct {
	SearchBackend         string
	Sandbox               bool
	AllowNetwork          bool
	InsecureSkipTLSVerify bool
	BubblewrapPath        string
	ProtectedPaths        []string
	SandboxCommandPaths   []string
	Skills                SkillLoader
}

// SkillLoader supplies previously discovered workspace skill instructions.
// Keeping discovery outside the tool registry makes its roots and precedence
// explicit at application startup.
type SkillLoader interface {
	Load(name string) (string, error)
}

type sandboxState struct {
	bwrap                  string
	home                   string
	allowNetwork           bool
	configuredAllowNetwork bool
	insecureSkipTLSVerify  bool
	protected              []string
	commandPaths           []string
}

type environmentVariable struct{ name, value string }

// These are the documented opt-outs understood by common command-line TLS
// clients. QCODE_INSECURE_SKIP_TLS_VERIFY gives scripts a portable qcode
// signal; arbitrary programs still need to opt in themselves.
var insecureTLSEnvironment = []environmentVariable{
	{"QCODE_INSECURE_SKIP_TLS_VERIFY", "true"},
	{"GIT_SSL_NO_VERIFY", "true"},
	{"NODE_TLS_REJECT_UNAUTHORIZED", "0"},
	{"NPM_CONFIG_STRICT_SSL", "false"},
	{"PYTHONHTTPSVERIFY", "0"},
}

// WorkspaceExposesHome reports whether making root writable would expose the
// user's entire home directory (or more) to a tool.
func WorkspaceExposesHome(root string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return false
	}
	if canonical, evalErr := filepath.EvalSymlinks(home); evalErr == nil {
		home = canonical
	}
	if canonical, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		root = canonical
	}
	return pathContains(root, home)
}

// CheckSandbox verifies that the bubblewrap primitives used for shell calls
// can actually start on this host. Optional commandPaths are mounted read-only
// during the probe so misconfigured allowlist entries fail early. Callers
// should pass already-resolved paths from ResolveSandboxCommandPaths; the
// probe defensively skips entries that disappeared since resolution.
func CheckSandbox(root string, allowNetwork bool, commandPaths ...string) (string, error) {
	// Allow callers to pass a single slice flattened via `resolved...` as well
	// as repeated values; flattening is handled by the variadic signature.
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("sandbox mode requires Linux and bubblewrap")
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return "", fmt.Errorf("bubblewrap is not installed; install `bubblewrap` to enable sandbox mode")
	}
	if !securePathSupported(root) {
		return "", fmt.Errorf("this Linux kernel does not support secure openat2 path confinement")
	}
	home, _ := os.UserHomeDir()
	if canonical, evalErr := filepath.EvalSymlinks(home); evalErr == nil {
		home = canonical
	}
	var filtered []string
	for _, p := range commandPaths {
		if p == "" {
			continue
		}
		cleaned := filepath.Clean(p)
		if cleaned == string(filepath.Separator) {
			continue
		}
		if !pathExists(cleaned) {
			continue
		}
		filtered = append(filtered, cleaned)
	}
	state := sandboxState{bwrap: bwrap, home: home, allowNetwork: allowNetwork, configuredAllowNetwork: allowNetwork, commandPaths: filtered}
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, bwrap, state.arguments(root, []string{root}, "/bin/true")...)
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		if probeCtx.Err() != nil {
			return "", fmt.Errorf("bubblewrap sandbox probe timed out: %w", probeCtx.Err())
		}
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return "", fmt.Errorf("bubblewrap cannot create the required sandbox: %s", detail)
		}
		return "", fmt.Errorf("bubblewrap cannot create the required sandbox: %w", runErr)
	}
	return bwrap, nil
}

func (s sandboxState) command(ctx context.Context, root string, grants []string, command string) *exec.Cmd {
	return exec.CommandContext(ctx, s.bwrap, s.arguments(root, grants, "/bin/sh", "-c", trackedShellCommand(command))...)
}

func (s sandboxState) arguments(root string, grants []string, command ...string) []string {
	args := []string{
		"--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid",
		"--unshare-ipc", "--unshare-uts", "--cap-drop", "ALL",
	}
	if !s.allowNetwork {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev")
	if pathExists("/tmp") {
		args = append(args, "--tmpfs", "/tmp")
	}
	if pathExists("/run") {
		args = append(args, "--tmpfs", "/run")
	}
	if s.home != "" && s.home != "/" && pathExists(s.home) {
		args = append(args, "--tmpfs", s.home)
	}

	unique := map[string]bool{}
	for _, grant := range grants {
		if grant != "" {
			unique[filepath.Clean(grant)] = true
		}
	}
	paths := make([]string, 0, len(unique))
	for path := range unique {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) == len(paths[j]) {
			return paths[i] < paths[j]
		}
		return len(paths[i]) < len(paths[j])
	})
	// Persistent trusted command directories are mounted read-only at their
	// original absolute paths. They stay distinct from session grants (which
	// are read/write): only the ancestor scaffolding overlaps, and the
	// read/write grant bind below wins when a command directory sits inside
	// the workspace. Skipping a mount already covered by a grant keeps PATH
	// intact without a redundant bind.
	for _, dir := range s.commandPaths {
		if dir == "" {
			continue
		}
		cleaned := filepath.Clean(dir)
		if cleaned == string(filepath.Separator) || !pathExists(cleaned) {
			continue
		}
		covered := false
		for _, grant := range paths {
			if pathContains(grant, cleaned) {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		if s.home != "" && pathContains(s.home, cleaned) {
			args = append(args, directoryArgs(filepath.Dir(cleaned), s.home)...)
		}
		args = append(args, "--ro-bind", cleaned, cleaned)
	}
	for _, grant := range paths {
		if s.home != "" && pathContains(s.home, grant) {
			args = append(args, directoryArgs(filepath.Dir(grant), s.home)...)
		}
		args = append(args, "--bind", grant, grant)
	}
	for _, protected := range s.protected {
		visible := s.home == "" || !pathContains(s.home, protected)
		for _, grant := range paths {
			visible = visible || pathContains(grant, protected)
		}
		for _, dir := range s.commandPaths {
			if dir != "" && pathContains(filepath.Clean(dir), protected) {
				visible = true
				break
			}
		}
		if visible && pathExists(protected) {
			args = append(args, "--ro-bind", "/dev/null", protected)
		}
	}

	tempHome := "/tmp/qcode-home-" + strconv.Itoa(os.Getuid())
	args = append(args,
		"--dir", tempHome,
		"--chdir", root,
		"--clearenv",
		"--setenv", "HOME", tempHome,
		"--setenv", "PATH", sandboxPATH(s.commandPaths, s.home),
	)
	for _, name := range []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "NO_COLOR"} {
		if value := os.Getenv(name); value != "" {
			args = append(args, "--setenv", name, value)
		}
	}
	if s.insecureSkipTLSVerify {
		for _, variable := range insecureTLSEnvironment {
			args = append(args, "--setenv", variable.name, variable.value)
		}
	}
	args = append(args, "--")
	return append(args, command...)
}

func directoryArgs(path, stop string) []string {
	var paths []string
	for path != stop && path != "." && path != string(filepath.Separator) {
		paths = append(paths, path)
		path = filepath.Dir(path)
	}
	var args []string
	for i := len(paths) - 1; i >= 0; i-- {
		args = append(args, "--dir", paths[i])
	}
	return args
}

func safePath(home string) string {
	var kept []string
	for _, path := range filepath.SplitList(os.Getenv("PATH")) {
		if path == "" || !filepath.IsAbs(path) || home != "" && pathContains(home, path) {
			continue
		}
		kept = append(kept, path)
	}
	if len(kept) > 0 {
		return strings.Join(kept, string(os.PathListSeparator))
	}
	return "/usr/local/bin:/usr/bin:/bin"
}

// sandboxPATH prepends trusted command directories ahead of the safe system
// PATH. Adding a directory to PATH is intentional trust: it is executable
// code selected by the user, not merely a visibility change.
func sandboxPATH(commandPaths []string, home string) string {
	base := safePath(home)
	var prefixes []string
	seen := map[string]bool{}
	for _, dir := range commandPaths {
		if dir == "" {
			continue
		}
		cleaned := filepath.Clean(dir)
		if cleaned == string(filepath.Separator) || !filepath.IsAbs(cleaned) {
			continue
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		prefixes = append(prefixes, cleaned)
	}
	if len(prefixes) == 0 {
		return base
	}
	// Avoid duplicating entries already present in the safe base.
	var suffix []string
	for _, part := range strings.Split(base, string(os.PathListSeparator)) {
		if part == "" || seen[part] {
			continue
		}
		suffix = append(suffix, part)
	}
	joined := strings.Join(prefixes, string(os.PathListSeparator))
	if len(suffix) == 0 {
		return joined
	}
	return joined + string(os.PathListSeparator) + strings.Join(suffix, string(os.PathListSeparator))
}

// ResolveSandboxCommandPaths validates raw allowlist entries and returns
// canonical absolute directories plus a warning per skipped entry.
//
// Each entry is expanded for `~`, resolved to an absolute path, canonicalized
// with EvalSymlinks, deduplicated, and required to be an existing directory.
// The filesystem root and any protected path (including qcode configuration
// files, in either containment direction) are rejected. Resolution is
// idempotent so already-canonical paths pass through unchanged.
func ResolveSandboxCommandPaths(raw []string, protected []string) ([]string, []string) {
	home, _ := os.UserHomeDir()
	var canonProtected []string
	for _, p := range protected {
		if strings.TrimSpace(p) == "" {
			continue
		}
		expanded := expandSandboxHome(strings.TrimSpace(p), home)
		abs, err := filepath.Abs(expanded)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if canonical, err := filepath.EvalSymlinks(abs); err == nil {
			abs = canonical
		} else {
			// For protected files that no longer exist, keep the absolute
			// path so containment checks remain conservative.
			if !strings.HasPrefix(abs, string(filepath.Separator)) && !filepath.IsAbs(abs) {
				continue
			}
		}
		canonProtected = append(canonProtected, abs)
	}
	var resolved []string
	var warnings []string
	seen := map[string]bool{}
	for _, entry := range raw {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: empty path", entry))
			continue
		}
		expanded := expandSandboxHome(trimmed, home)
		abs, err := filepath.Abs(expanded)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: %v", entry, err))
			continue
		}
		abs = filepath.Clean(abs)
		canonical, err := filepath.EvalSymlinks(abs)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: does not exist or cannot be resolved", entry))
			continue
		}
		canonical = filepath.Clean(canonical)
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: not an existing directory", entry))
			continue
		}
		if canonical == string(filepath.Separator) {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: filesystem root is not allowed", entry))
			continue
		}
		rejected := ""
		for _, prot := range canonProtected {
			if prot == "" {
				continue
			}
			cleanProt := filepath.Clean(prot)
			if canonical == cleanProt || pathContains(canonical, cleanProt) || pathContains(cleanProt, canonical) {
				rejected = cleanProt
				break
			}
		}
		if rejected != "" {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: overlaps protected path %q", entry, rejected))
			continue
		}
		if seen[canonical] {
			warnings = append(warnings, fmt.Sprintf("sandbox command path %q skipped: duplicate of already allowlisted directory", entry))
			continue
		}
		seen[canonical] = true
		resolved = append(resolved, canonical)
	}
	if resolved == nil {
		resolved = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return resolved, warnings
}

func expandSandboxHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
