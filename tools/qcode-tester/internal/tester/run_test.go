package tester

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunScenarioPassesOnlyPromptToQcode(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "qcode")
	program := "#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf '%s' \"$PWD\" > cwd.txt\n"
	if err := os.WriteFile(binary, []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}
	result := runScenario(context.Background(), Options{
		QcodeBinary:        binary,
		ArtifactsDirectory: filepath.Join(directory, "artifacts"),
		KeepArtifacts:      true,
	}, Scenario{
		Name:    "arguments",
		Prompt:  "make a project",
		Timeout: "1s",
		Assertions: Assertions{
			ExitCode: 0,
		},
	})
	if !result.Passed {
		t.Fatalf("scenario failed: %#v", result)
	}
	stdout, err := os.ReadFile(filepath.Join(result.Artifacts, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(stdout); got != "make a project\n" {
		t.Fatalf("qcode arguments = %q, want prompt only", got)
	}
	cwd, err := os.ReadFile(filepath.Join(result.Artifacts, "workspace", "cwd.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(cwd), os.TempDir()) {
		t.Fatalf("qcode cwd %q is not the temporary workspace", cwd)
	}
}
