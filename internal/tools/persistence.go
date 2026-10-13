package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type savedTools struct {
	Cwd            string `json:"cwd,omitempty"`
	Disabled       map[string]bool
	Grants, Skills []string
}

func (r *Registry) SaveTools() json.RawMessage {
	r.grantMu.RLock()
	grants := append([]string(nil), r.grants...)
	cwd := r.cwd
	r.grantMu.RUnlock()
	s := savedTools{Disabled: r.disabled, Grants: grants, Cwd: cwd}
	if selection, ok := r.skills.(interface{ Selected() []string }); ok {
		s.Skills = selection.Selected()
	}
	data, _ := json.Marshal(s)
	return data
}

func (r *Registry) RestoreTools(data json.RawMessage) error {
	var s savedTools
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	return r.restoreTools(s)
}

// RestoreToolSelections restores toggles and launch-workspace skills while
// resetting the directory and grants to the launch workspace.
func (r *Registry) RestoreToolSelections(data json.RawMessage) error {
	var s savedTools
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s.Grants = nil
	s.Cwd = ""
	return r.restoreTools(s)
}

func (r *Registry) restoreTools(s savedTools) error {
	r.disabled = map[string]bool{}
	for name, disabled := range s.Disabled {
		if _, ok := r.handlers[name]; ok {
			r.disabled[name] = disabled
		}
	}
	r.updateSandboxNetworkAccess()
	r.grantMu.Lock()
	r.grants = []string{r.root}
	r.cwd = r.root
	r.grantMu.Unlock()
	r.restoreWarnings = nil
	for _, path := range s.Grants {
		canonical, err := filepath.EvalSymlinks(path)
		info, statErr := os.Stat(path)
		valid := err == nil && statErr == nil && info.IsDir() && filepath.IsAbs(path) && canonical == path && filepath.Dir(path) != path
		for _, protected := range r.protected {
			if path == protected {
				valid = false
			}
		}
		if !valid {
			r.restoreWarnings = append(r.restoreWarnings, fmt.Sprintf("Saved directory grant discarded because its path is no longer valid: %s", path))
			continue
		}
		r.addGrant(path)
	}
	// Grants must be restored first. Resuming never prompts for or silently
	// grants access to a saved working directory.
	if s.Cwd != "" {
		canonical, canonicalErr := filepath.EvalSymlinks(s.Cwd)
		valid := filepath.IsAbs(s.Cwd) && canonicalErr == nil && canonical == s.Cwd
		if r.sandbox != nil {
			valid = valid && r.isGranted(s.Cwd)
		}
		var err error
		if valid {
			_, err = r.ChangeWorkingDirectory(context.Background(), s.Cwd)
		}
		if !valid || err != nil {
			r.restoreWarnings = append(r.restoreWarnings, fmt.Sprintf("Saved working directory is missing, invalid, or unauthorized; using launch workspace %s: %s", r.root, s.Cwd))
		}
	}
	if selection, ok := r.skills.(interface{ Set([]string) }); ok {
		selection.Set(s.Skills)
	}
	return nil
}

func (r *Registry) RestoreWarnings() []string { return append([]string(nil), r.restoreWarnings...) }
