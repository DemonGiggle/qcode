package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type savedTools struct {
	Disabled       map[string]bool
	Grants, Skills []string
}

func (r *Registry) SaveTools() json.RawMessage {
	r.grantMu.RLock()
	grants := append([]string(nil), r.grants...)
	r.grantMu.RUnlock()
	s := savedTools{Disabled: r.disabled, Grants: grants}
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
	r.disabled = map[string]bool{}
	for name, disabled := range s.Disabled {
		if _, ok := r.handlers[name]; ok {
			r.disabled[name] = disabled
		}
	}
	r.grantMu.Lock()
	r.grants = []string{r.root}
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
	if selection, ok := r.skills.(interface{ Set([]string) }); ok {
		selection.Set(s.Skills)
	}
	return nil
}

func (r *Registry) RestoreWarnings() []string { return append([]string(nil), r.restoreWarnings...) }
