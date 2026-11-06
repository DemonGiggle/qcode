package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"qcode/internal/config"
)

// enforceStartupConfigCheck runs the same underlying logic as
// `qcode config check` before a real session starts.
//
//   - no issues: silent, no Enter needed, continue directly.
//   - errors: print the same human report + `qcode config check` tip, then fail.
//   - warnings only: print the same report + tip, then wait for Enter when
//     running in an interactive terminal so the user notices. Non-terminal,
//     piped, or --json-events runs only print and continue.
func enforceStartupConfigCheck(out io.Writer, stdin, stdout, stderr *os.File, jsonEvents bool, launchProvider, launchAPIKey string) error {
	issues, inspected, err := config.CheckDefault()
	if err != nil {
		fmt.Fprintln(out, "WARNING: cannot validate config:", err)
		return nil
	}
	filtered := filterConfigIssuesForStartup(issues, launchProvider, launchAPIKey)
	return handleStartupConfigIssues(out, stdin, stdout, stderr, jsonEvents, filtered, inspected)
}

// handleStartupConfigIssues prints the check report and enforces the
// error/warning gate. It is split out from enforceStartupConfigCheck so tests
// can inject issues without touching the real filesystem.
func handleStartupConfigIssues(out io.Writer, stdin, stdout, stderr *os.File, jsonEvents bool, issues []config.Issue, inspected []string) error {
	if len(issues) == 0 {
		return nil
	}
	color := configUseColor(stderr)
	width := configTermWidth(stderr)
	writeHumanReport(out, inspected, issues, color, width)
	fmt.Fprintln(out, "Tip: run `qcode config check` to see the same report.")
	if config.HasErrors(issues) {
		return fmt.Errorf("config check found %d error(s); fix them and restart (see `qcode config check`)", countErrors(issues))
	}
	if jsonEvents {
		return nil
	}
	if stdin == nil || stdout == nil {
		return nil
	}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return nil
	}
	fmt.Fprintln(out, "Press Enter to continue...")
	_, _ = bufio.NewReader(stdin).ReadString('\n')
	return nil
}

// filterConfigIssuesForStartup drops api_key_path file errors for the launch
// provider when its key already comes from --api-key / env. The config file is
// still worth fixing, but it must not block startup when the runtime already
// has a key.
func filterConfigIssuesForStartup(issues []config.Issue, launchProvider, launchAPIKey string) []config.Issue {
	if strings.TrimSpace(launchAPIKey) == "" || strings.TrimSpace(launchProvider) == "" {
		return issues
	}
	if launchProvider == "chatgpt" {
		return issues
	}
	wantKey := "providers." + launchProvider + ".api_key_path"
	filtered := make([]config.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Severity == config.SeverityError && issue.Key == wantKey {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered
}
