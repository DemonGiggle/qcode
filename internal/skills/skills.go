// Package skills discovers and loads workspace-local agent instructions.
package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	fileName    = "SKILL.md"
	maxFileSize = 64 * 1024
)

// Skill is a discovered instruction bundle. Name and Description are safe to
// show in the system prompt; the document itself is only returned on demand.
type Skill struct {
	Name        string
	Description string
	path        string
	root        string
}

// Catalog is the immutable set of skills available for one qcode run.
type Catalog struct {
	root   string
	skills []Skill
}

// Selection makes only user-enabled skills available to the tool.
type Selection struct {
	catalog *Catalog
	mu      sync.RWMutex
	enabled map[string]bool
}

func NewSelection(catalog *Catalog) *Selection {
	return &Selection{catalog: catalog, enabled: map[string]bool{}}
}

func (s *Selection) Set(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = map[string]bool{}
	for _, name := range names {
		for _, skill := range s.catalog.skills {
			if name == skill.Name {
				s.enabled[name] = true
				break
			}
		}
	}
}

func (s *Selection) Load(name string) (string, error) {
	s.mu.RLock()
	enabled := s.enabled[name]
	s.mu.RUnlock()
	if !enabled {
		return "", fmt.Errorf("skill %q is not enabled for this session", name)
	}
	return s.catalog.Load(name)
}

// Discover finds skills in .qcode/skills and .agents/skills. A skill is a
// directory containing SKILL.md. .qcode takes precedence when both locations
// define the same name.
func Discover(root string) (*Catalog, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	// Lower-precedence sources come first so a workspace can override a skill
	// installed for the current user.
	sources := []struct {
		base  string
		label string
	}{}
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		sources = append(sources, struct{ base, label string }{filepath.Join(home, ".qcode", "skills"), "~/.qcode/skills"})
	}
	sources = append(sources,
		struct{ base, label string }{filepath.Join(abs, ".agents", "skills"), ".agents/skills"},
		struct{ base, label string }{filepath.Join(abs, ".qcode", "skills"), ".qcode/skills"},
	)
	byName := make(map[string]Skill)
	for _, source := range sources {
		base := source.base
		if resolved, err := filepath.EvalSymlinks(base); err == nil {
			base = resolved
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills directory %s: %w", source.label, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !validName(entry.Name()) {
				continue
			}
			path := filepath.Join(base, entry.Name(), fileName)
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("inspect skill %q: %w", entry.Name(), err)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			// Never let an instruction file escape the selected workspace through
			// a symlink; skills become model-visible context.
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !within(base, resolved) {
				continue
			}
			description, err := description(path)
			if err != nil {
				return nil, fmt.Errorf("read skill %q: %w", entry.Name(), err)
			}
			byName[entry.Name()] = Skill{Name: entry.Name(), Description: description, path: resolved, root: base}
		}
	}
	items := make([]Skill, 0, len(byName))
	for _, skill := range byName {
		items = append(items, skill)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return &Catalog{root: abs, skills: items}, nil
}

func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Skills returns a defensive copy suitable for rendering in a prompt.
func (c *Catalog) Skills() []Skill {
	if c == nil {
		return nil
	}
	return append([]Skill(nil), c.skills...)
}

// Load returns the complete document for a discovered skill. Names not in the
// startup catalog are deliberately unavailable until qcode is restarted.
func (c *Catalog) Load(name string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no skills are available")
	}
	for _, skill := range c.skills {
		if skill.Name != name {
			continue
		}
		path, err := filepath.EvalSymlinks(skill.path)
		if err != nil {
			return "", err
		}
		if !within(skill.root, path) {
			return "", fmt.Errorf("skill %q is outside its skill directory", name)
		}
		data, err := readSkill(path)
		if err != nil {
			return "", fmt.Errorf("read skill %q: %w", name, err)
		}
		return string(data), nil
	}
	return "", fmt.Errorf("unknown skill %q", name)
}

func description(path string) (string, error) {
	data, err := readSkill(path)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			for _, line := range strings.Split(text[4:4+end], "\n") {
				key, value, ok := strings.Cut(line, ":")
				if ok && strings.TrimSpace(key) == "description" && strings.TrimSpace(value) != "" {
					return oneLine(strings.Trim(strings.TrimSpace(value), "\"'")), nil
				}
			}
			text = strings.TrimSpace(text[4+end+4:])
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return oneLine(line), nil
		}
	}
	return "Workspace instructions", nil
}

func readSkill(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > maxFileSize {
		return nil, fmt.Errorf("exceeds the %d KiB limit", maxFileSize/1024)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("exceeds the %d KiB limit", maxFileSize/1024)
	}
	return data, nil
}

func oneLine(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 160 {
		return value[:157] + "..."
	}
	return value
}
