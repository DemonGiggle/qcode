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
	fileName            = "SKILL.md"
	maxFileSize         = 64 * 1024
	maxDescriptionBytes = 8 * 1024
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
	root      string
	locations []string
	skills    []Skill
}

// Selection makes only user-enabled skills available to the tool.
type Selection struct {
	catalog     *Catalog
	root        string
	customPaths []string
	mu          sync.RWMutex
	enabled     map[string]bool
}

func NewSelection(catalog *Catalog) *Selection {
	return &Selection{catalog: catalog, enabled: map[string]bool{}}
}

// NewLazySelection defers skill discovery until a selected skill is loaded.
// This keeps application startup independent of the number and size of skill
// files.
func NewLazySelection(root string, customPaths ...string) *Selection {
	return &Selection{root: root, customPaths: append([]string(nil), customPaths...), enabled: map[string]bool{}}
}

func (s *Selection) Selected() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var names []string
	for name, enabled := range s.enabled {
		if enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (s *Selection) Set(names []string) {
	s.mu.RLock()
	catalog := s.catalog
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = map[string]bool{}
	for _, name := range names {
		if catalog == nil {
			s.enabled[name] = true
			continue
		}
		for _, skill := range catalog.skills {
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
	catalog, err := s.catalogForLoad()
	if err != nil {
		return "", err
	}
	return catalog.Load(name)
}

func (s *Selection) catalogForLoad() (*Catalog, error) {
	s.mu.RLock()
	catalog := s.catalog
	root := s.root
	customPaths := append([]string(nil), s.customPaths...)
	s.mu.RUnlock()
	if catalog != nil {
		return catalog, nil
	}
	if root == "" {
		return nil, fmt.Errorf("no skills are available")
	}
	return Discover(root, customPaths...)
}

// Locations returns the built-in and configured skill directories without
// reading any directories or skill files. Missing locations are included so
// callers can show users every path qcode will check on demand.
func Locations(_ string, customPaths ...string) []string {
	locations := []string{"~/.qcode/skills", ".agents/skills", ".qcode/skills"}
	for _, configured := range customPaths {
		if configured = strings.TrimSpace(configured); configured != "" {
			locations = append(locations, configured)
		}
	}
	return locations
}

// Discover finds skills in the built-in locations and any custom paths. A
// skill is a directory containing SKILL.md. Later locations take precedence
// when multiple locations define the same name.
func Discover(root string, customPaths ...string) (*Catalog, error) {
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
	for _, configured := range customPaths {
		configured = strings.TrimSpace(configured)
		if configured == "" {
			continue
		}
		base := configured
		if base == "~" || strings.HasPrefix(base, "~/") || strings.HasPrefix(base, "~"+string(filepath.Separator)) {
			if home, homeErr := os.UserHomeDir(); homeErr == nil {
				if base == "~" {
					base = home
				} else {
					base = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(base, "~/"), "~"+string(filepath.Separator)))
				}
			}
		}
		if !filepath.IsAbs(base) {
			base = filepath.Join(abs, base)
		}
		sources = append(sources, struct{ base, label string }{filepath.Clean(base), configured})
	}
	byName := make(map[string]Skill)
	locations := Locations(abs, customPaths...)
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
	return &Catalog{root: abs, locations: locations, skills: items}, nil
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

// Locations returns the configured and built-in skill directories in the
// order in which they are searched. Missing directories are retained so the
// UI can explain every location that qcode checks.
func (c *Catalog) Locations() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.locations...)
}

// Path returns the discovered SKILL.md path for display to the user.
func (s Skill) Path() string { return s.path }

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
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDescriptionBytes))
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
