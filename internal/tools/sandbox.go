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
	SearchBackend  string
	Sandbox        bool
	AllowNetwork   bool
	BubblewrapPath string
	ProtectedPaths []string
	Skills         SkillLoader
}

// SkillLoader supplies previously discovered workspace skill instructions.
// Keeping discovery outside the tool registry makes its roots and precedence
// explicit at application startup.
type SkillLoader interface {
	Load(name string) (string, error)
}

type sandboxState struct {
	bwrap        string
	home         string
	allowNetwork bool
	protected    []string
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
// can actually start on this host.
func CheckSandbox(root string, allowNetwork bool) (string, error) {
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
	state := sandboxState{bwrap: bwrap, home: home, allowNetwork: allowNetwork}
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
	return exec.CommandContext(ctx, s.bwrap, s.arguments(root, grants, "/bin/sh", "-c", command)...)
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
		"--setenv", "PATH", safePath(s.home),
	)
	for _, name := range []string{"TERM", "LANG", "LC_ALL", "LC_CTYPE", "COLORTERM", "NO_COLOR"} {
		if value := os.Getenv(name); value != "" {
			args = append(args, "--setenv", name, value)
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

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
