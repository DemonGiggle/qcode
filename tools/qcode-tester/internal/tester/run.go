package tester

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func Run(ctx context.Context, options Options) (Report, error) {
	binary, err := filepath.Abs(options.QcodeBinary)
	if err != nil {
		return Report{}, err
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		return Report{}, fmt.Errorf("qcode binary %q is unavailable", options.QcodeBinary)
	}
	options.QcodeBinary = binary
	scenarios, err := loadScenarios(options.ScenarioDirectory, options.Scenarios)
	if err != nil {
		return Report{}, err
	}
	report := Report{Results: make([]Result, 0, len(scenarios))}
	for _, scenario := range scenarios {
		result := runScenario(ctx, options, scenario)
		report.Results = append(report.Results, result)
		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report, nil
}

func runScenario(parent context.Context, options Options, scenario Scenario) Result {
	result := Result{Name: scenario.Name}
	timeout, _ := time.ParseDuration(scenario.Timeout)
	workspace, err := os.MkdirTemp("", "qcode-tester-"+scenario.Name+"-")
	if err != nil {
		result.Failures = append(result.Failures, err.Error())
		return result
	}
	defer os.RemoveAll(workspace)
	if err := writeFiles(workspace, scenario.SeedFiles); err != nil {
		result.Failures = append(result.Failures, err.Error())
		return result
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	started := time.Now()
	// Deliberately pass no qcode options. The target resolves its provider,
	// credentials, model, and configuration through its ordinary lookup path.
	command := exec.CommandContext(ctx, options.QcodeBinary, scenario.Prompt)
	command.Dir = workspace
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	result.Duration = time.Since(started).Round(time.Millisecond)
	result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	result.ExitCode = exitCode(runErr)
	result.Failures = append(result.Failures, checkScenario(scenario, result, stdout.String())...)
	result.Failures = append(result.Failures, workspaceAssertions(workspace, scenario.Assertions)...)
	if runErr != nil && !result.TimedOut && result.ExitCode == scenario.Assertions.ExitCode {
		result.Failures = append(result.Failures, "qcode exited with an unexpected execution error: "+runErr.Error())
	}
	result.Passed = len(result.Failures) == 0
	if !result.Passed || options.KeepArtifacts {
		result.Artifacts = retainArtifacts(options.ArtifactsDirectory, scenario.Name, workspace, stdout.Bytes(), stderr.Bytes(), result)
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return exited.ExitCode()
	}
	return -1
}

func writeFiles(root string, files map[string]string) error {
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func checkScenario(s Scenario, result Result, stdout string) []string {
	var failures []string
	if result.TimedOut != s.Assertions.ExpectTimeout {
		failures = append(failures, fmt.Sprintf("timeout=%t, want %t", result.TimedOut, s.Assertions.ExpectTimeout))
	}
	if result.ExitCode != s.Assertions.ExitCode {
		failures = append(failures, fmt.Sprintf("exit code %d, want %d", result.ExitCode, s.Assertions.ExitCode))
	}
	for _, expected := range s.Assertions.StdoutContains {
		if !strings.Contains(stdout, expected) {
			failures = append(failures, fmt.Sprintf("stdout does not contain %q", expected))
		}
	}
	return failures
}

func retainArtifacts(base, name, workspace string, stdout, stderr []byte, result Result) string {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return ""
	}
	path, err := os.MkdirTemp(base, name+"-")
	if err != nil {
		return ""
	}
	_ = os.WriteFile(filepath.Join(path, "stdout.txt"), stdout, 0o644)
	_ = os.WriteFile(filepath.Join(path, "stderr.txt"), stderr, 0o644)
	data, _ := json.MarshalIndent(result, "", "  ")
	_ = os.WriteFile(filepath.Join(path, "report.json"), data, 0o644)
	_ = copyTree(filepath.Join(path, "workspace"), workspace)
	_ = writeManifest(filepath.Join(path, "workspace_manifest.json"), workspace)
	return path
}

type manifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func writeManifest(path, root string) error {
	var entries []manifestEntry
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		entries = append(entries, manifestEntry{Path: filepath.ToSlash(rel), Size: int64(len(data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
		return nil
	})
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func copyTree(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func workspaceAssertions(root string, assertions Assertions) []string {
	var failures []string
	for path, expected := range assertions.Files {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(data) != expected {
			failures = append(failures, fmt.Sprintf("file %q did not match expected content", path))
		}
	}
	for path, expected := range assertions.FileContains {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || !strings.Contains(string(data), expected) {
			failures = append(failures, fmt.Sprintf("file %q does not contain %q", path, expected))
		}
	}
	for _, path := range assertions.AbsentFiles {
		if _, err := os.Stat(filepath.Join(root, path)); err == nil {
			failures = append(failures, fmt.Sprintf("file %q should be absent", path))
		}
	}
	return failures
}
