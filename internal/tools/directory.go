package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WorkingDirectory returns this registry's directory without changing the
// process directory. The launch workspace remains fixed for session and skills.
func (r *Registry) WorkingDirectory() string {
	r.grantMu.RLock()
	defer r.grantMu.RUnlock()
	return r.cwd
}

// ValidateWorkingDirectory resolves and authorizes an existing destination,
// without switching directories. Child creation uses this before allocation.
func (r *Registry) ValidateWorkingDirectory(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("directory path must not be empty")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.WorkingDirectory(), path)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory", path)
	}
	if r.sandbox != nil {
		if _, err := r.authorizePath(ctx, canonical); err != nil {
			return "", err
		}
	}
	file, err := r.openFile(canonical, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return canonical, nil
}

func (r *Registry) ChangeWorkingDirectory(ctx context.Context, path string) (string, error) {
	canonical, err := r.ValidateWorkingDirectory(ctx, path)
	if err != nil {
		return "", err
	}
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.cwd = canonical
	return canonical, nil
}

func (r *Registry) changeDirectory(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	return r.ChangeWorkingDirectory(ctx, args.Path)
}

// Journal paths stay relative to the launch workspace, or absolute when the
// file is elsewhere, so identical filenames in worktrees cannot collide.
func (r *Registry) changedFilePath(path string) string {
	if pathContains(r.root, path) {
		if relative, err := filepath.Rel(r.root, path); err == nil {
			return filepath.ToSlash(relative)
		}
	}
	return path
}
