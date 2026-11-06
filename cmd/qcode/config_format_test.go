package main

import (
	"strings"
	"testing"

	"qcode/internal/config"
)

func testIssues() []config.Issue {
	return []config.Issue{
		{Severity: config.SeverityWarning, File: "/b.toml", Key: "model", Message: "root model is deprecated", Fix: "Move it."},
		{Severity: config.SeverityError, File: "/a.toml", Key: "providers.openai.api_key_path", Message: "missing file", Fix: "Create it."},
		{Severity: config.SeverityError, File: "(merged)", Key: "providers.openai.api_key_path", Message: "both set", Fix: "Keep one."},
	}
}

func TestWriteHumanReportPlainIndent(t *testing.T) {
	var out strings.Builder
	writeHumanReport(&out, []string{"/a.toml", "/b.toml"}, testIssues(), false, 0)
	got := out.String()
	for _, want := range []string{
		"Checked 2 files:\n",
		"  /a.toml\n",
		"  /b.toml\n",
		"/a.toml:\n",
		"  ERROR   providers.openai.api_key_path\n",
		"    problem: missing file\n",
		"    fix:     Create it.\n",
		"/b.toml:\n",
		"  WARNING model\n",
		"(merged):\n",
		"Result: FAIL",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain output must not contain ANSI:\n%s", got)
	}
	// Checked files come before derived groups.
	if strings.Index(got, "/a.toml:") > strings.Index(got, "(merged):") {
		t.Fatalf("merged group should sort last:\n%s", got)
	}
}

func TestWriteHumanReportColor(t *testing.T) {
	var out strings.Builder
	writeHumanReport(&out, []string{"/a.toml"}, testIssues()[1:2], true, 0)
	got := out.String()
	for _, want := range []string{"\x1b[1;31m", "\x1b[1m", "\x1b[36m", "\x1b[0m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("color output missing %q in:\n%s", want, got)
		}
	}
}

func TestWriteHumanReportWrap(t *testing.T) {
	var out strings.Builder
	issues := []config.Issue{{
		Severity: config.SeverityWarning, File: "/a.toml", Key: "model",
		Message: "aaa bbb ccc ddd eee fff ggg hhh iii jjj",
		Fix:     "fix one two three four five six seven",
	}}
	writeHumanReport(&out, []string{"/a.toml"}, issues, false, 30)
	got := out.String()
	lines := strings.Split(got, "\n")
	foundProblem, foundCont := false, false
	for _, line := range lines {
		if strings.HasPrefix(line, "    problem: ") {
			foundProblem = true
			if len(line) > 30 {
				t.Fatalf("problem line exceeds width %d: %q", 30, line)
			}
		} else if strings.HasPrefix(line, "             ") && strings.Contains(line, "bbb") == false {
			// Continuation lines are 13-space indented; at least one should exist.
			foundCont = true
		}
		if len(line) > 30 && !strings.HasPrefix(line, "  /") && !strings.HasPrefix(line, "/a") {
			t.Fatalf("line exceeds width: %q", line)
		}
	}
	if !foundProblem {
		t.Fatalf("no problem line in:\n%s", got)
	}
	if !foundCont {
		t.Fatalf("expected wrapped continuation in:\n%s", got)
	}
}

func TestWriteHumanReportEmpty(t *testing.T) {
	var out strings.Builder
	writeHumanReport(&out, []string{"/a.toml"}, nil, false, 0)
	if !strings.Contains(out.String(), "OK: no errors or warnings.") {
		t.Fatalf("got:\n%s", out.String())
	}
	var none strings.Builder
	writeHumanReport(&none, nil, nil, false, 0)
	if !strings.Contains(none.String(), "No config files found") {
		t.Fatalf("got:\n%s", none.String())
	}
}
