package tester

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func loadScenarios(directory string, selected []string) ([]Scenario, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read scenarios: %w", err)
	}
	wanted := make(map[string]bool, len(selected))
	for _, name := range selected {
		wanted[name] = true
	}
	var scenarios []Scenario
	for _, entry := range entries {
		if !entry.IsDir() || len(wanted) > 0 && !wanted[entry.Name()] {
			continue
		}
		path := filepath.Join(directory, entry.Name(), "scenario.json")
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", path, readErr)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		var scenario Scenario
		if decodeErr := decoder.Decode(&scenario); decodeErr != nil {
			return nil, fmt.Errorf("decode %s: %w", path, decodeErr)
		}
		if err := validateScenario(scenario, entry.Name()); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		scenarios = append(scenarios, scenario)
	}
	if len(scenarios) == 0 {
		return nil, fmt.Errorf("no scenarios found in %s", directory)
	}
	if len(wanted) > len(scenarios) {
		found := make(map[string]bool, len(scenarios))
		for _, scenario := range scenarios {
			found[scenario.Name] = true
		}
		var missing []string
		for name := range wanted {
			if !found[name] {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			return nil, fmt.Errorf("unknown scenario(s): %s", strings.Join(missing, ", "))
		}
	}
	return scenarios, nil
}

func validateScenario(s Scenario, directoryName string) error {
	if s.Version != 1 || s.Name == "" || s.Name != directoryName || strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("requires version 1, matching name, and prompt")
	}
	if s.Timeout == "" {
		return fmt.Errorf("requires timeout")
	}
	if timeout, err := time.ParseDuration(s.Timeout); err != nil || timeout <= 0 {
		return fmt.Errorf("invalid timeout %q", s.Timeout)
	}
	for path := range s.SeedFiles {
		if !safeRelative(path) {
			return fmt.Errorf("seed file %q is outside workspace", path)
		}
	}
	for path := range s.Assertions.Files {
		if !safeRelative(path) {
			return fmt.Errorf("asserted file %q is outside workspace", path)
		}
	}
	for path := range s.Assertions.FileContains {
		if !safeRelative(path) {
			return fmt.Errorf("asserted file %q is outside workspace", path)
		}
	}
	for _, path := range s.Assertions.AbsentFiles {
		if !safeRelative(path) {
			return fmt.Errorf("absent file %q is outside workspace", path)
		}
	}
	return nil
}

func safeRelative(path string) bool {
	clean := filepath.Clean(path)
	return path != "" && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
