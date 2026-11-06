package main

import (
	"os"
	"strings"
	"testing"

	"qcode/internal/config"
)

func testGateFiles(t *testing.T) (stdin, stdout, stderr *os.File) {
	t.Helper()
	dir := t.TempDir()
	mk := func(name string) *os.File {
		f, err := os.CreateTemp(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	return mk("stdin"), mk("stdout"), mk("stderr")
}

func TestHandleStartupConfigIssuesEmptySilent(t *testing.T) {
	stdin, stdout, stderr := testGateFiles(t)
	var out strings.Builder
	if err := handleStartupConfigIssues(&out, stdin, stdout, stderr, false, nil, nil); err != nil {
		t.Fatalf("empty issues should not fail: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("empty issues should be silent, got %q", out.String())
	}
}

func TestHandleStartupConfigIssuesWarningsNoPauseWhenNonTerminal(t *testing.T) {
	stdin, stdout, stderr := testGateFiles(t)
	var out strings.Builder
	issues := []config.Issue{
		{Severity: config.SeverityWarning, File: "/a.toml", Key: "model", Message: "root model is deprecated", Fix: "Move it."},
	}
	if err := handleStartupConfigIssues(&out, stdin, stdout, stderr, false, issues, []string{"/a.toml"}); err != nil {
		t.Fatalf("warnings should not fail: %v", err)
	}
	got := out.String()
	for _, want := range []string{"WARNING", "Tip: run `qcode config check`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Press Enter") {
		t.Fatalf("non-terminal must not prompt for Enter: %q", got)
	}
}

func TestHandleStartupConfigIssuesWarningsNoPauseWhenJSONEvents(t *testing.T) {
	stdin, stdout, stderr := testGateFiles(t)
	var out strings.Builder
	issues := []config.Issue{
		{Severity: config.SeverityWarning, File: "/a.toml", Key: "model", Message: "root model is deprecated", Fix: "Move it."},
	}
	if err := handleStartupConfigIssues(&out, stdin, stdout, stderr, true, issues, []string{"/a.toml"}); err != nil {
		t.Fatalf("json-events warnings should not fail: %v", err)
	}
	if strings.Contains(out.String(), "Press Enter") {
		t.Fatalf("json-events must not prompt: %q", out.String())
	}
}

func TestHandleStartupConfigIssuesErrorsFail(t *testing.T) {
	stdin, stdout, stderr := testGateFiles(t)
	var out strings.Builder
	issues := []config.Issue{
		{Severity: config.SeverityError, File: "/a.toml", Key: "providers.openai.api_key_path", Message: "missing file", Fix: "Create it."},
	}
	if err := handleStartupConfigIssues(&out, stdin, stdout, stderr, false, issues, []string{"/a.toml"}); err == nil {
		t.Fatal("errors should fail startup")
	}
	got := out.String()
	for _, want := range []string{"ERROR", "Tip: run `qcode config check`"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFilterConfigIssuesForStartupEnvOverride(t *testing.T) {
	issues := []config.Issue{
		{Severity: config.SeverityError, File: "/a.toml", Key: "providers.openai.api_key_path", Message: "missing file", Fix: "Create it."},
		{Severity: config.SeverityError, File: "/a.toml", Key: "providers.ollama.api_key_path", Message: "missing file", Fix: "Create it."},
	}
	filtered := filterConfigIssuesForStartup(issues, "openai", "sk-env")
	if len(filtered) != 1 || filtered[0].Key != "providers.ollama.api_key_path" {
		t.Fatalf("env override should drop active provider file error, got %#v", filtered)
	}
	kept := filterConfigIssuesForStartup(issues, "openai", "")
	if len(kept) != 2 {
		t.Fatalf("without env override all errors stay, got %#v", kept)
	}
}
