package tui

import (
	"bytes"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/session"
)

// Explicit zero must render as zero, never as unknown/unavailable.
func TestUsageReportExplicitZero(t *testing.T) {
	s := llm.SessionUsage{
		Requests: 2, InputReported: 2, OutputReported: 2,
		InputTokens: 0, OutputTokens: 0, TotalTokens: 0,
		CacheReadReported: 2, CacheWriteReported: 2,
		CacheReadTokens: 0, CacheWriteTokens: 0,
	}
	report := formatUsageReport(s)
	for _, want := range []string{
		"Input tokens: 0",
		"Output tokens: 0",
		"Total tokens: 0",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "unavailable") && strings.Contains(report, "Input tokens: unavailable") {
		t.Fatalf("explicit zero shown as unavailable:\n%s", report)
	}
	label := usageLabelFor(tokenRunner{usage: s}, false)
	if label != "I:0 O:0 T:0" {
		t.Fatalf("explicit-zero label = %q", label)
	}
}

// All missing must show unknown in the compact line and unavailable in detail.
func TestUsageReportAllMissing(t *testing.T) {
	s := llm.SessionUsage{Requests: 2, Missing: 2}
	if got := usageLabelFor(tokenRunner{usage: s}, true); got != "unknown" {
		t.Fatalf("all-missing label = %q", got)
	}
	report := formatUsageReport(s)
	for _, want := range []string{
		"Input tokens: unavailable",
		"Output tokens: unavailable",
		"Total tokens: unavailable",
		"Cache-read tokens: unavailable",
		"Cache-write tokens: unavailable",
		"Missing completions: 2",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

// Partially reported sums use the "reported for m/n" shape.
func TestUsageReportPartialCoverage(t *testing.T) {
	s := llm.SessionUsage{
		Requests: 3,
		InputTokens: 500, InputReported: 2,
		OutputTokens: 300, OutputReported: 1,
		TotalTokens: 800,
		CacheReadTokens: 400, CacheReadReported: 1,
		CacheWriteTokens: 0, CacheWriteReported: 0,
		Missing: 1,
		CacheRatioInputTokens: 250, CacheRatioReadTokens: 200, CacheRatioRequests: 1,
	}
	report := formatUsageReport(s)
	for _, want := range []string{
		"Input tokens: 500 (reported for 2/3 requests)",
		"Output tokens: 300 (reported for 1/3 requests)",
		"input reported for 2/3 requests, output reported for 1/3 requests",
		"Cache-read tokens: 400 (reported for 1/3 requests)",
		"Cache-write tokens: unavailable",
		"Cached input share (reported subset): 80.0% (based on 1/3 requests)",
		"Missing completions: 1",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
	// Compact line must mark the partial total.
	label := usageLabelFor(tokenRunner{usage: s}, true)
	if !strings.Contains(label, "?") {
		t.Fatalf("partial label missing ?: %q", label)
	}
	// Cache-write with zero coverage is unavailable, never zero.
	if strings.Contains(report, "Cache-write tokens: 0") {
		t.Fatalf("cache-write zero coverage shown as zero:\n%s", report)
	}
}

// Input-only and output-only requests mark the missing side without showing zero.
func TestUsageLabelInputOnlyOutputOnly(t *testing.T) {
	inputOnly := llm.SessionUsage{Requests: 2, InputReported: 2, InputTokens: 500, TotalTokens: 500}
	label := usageLabelFor(tokenRunner{usage: inputOnly}, true)
	if !strings.Contains(label, "I:500") || !strings.Contains(label, "O:?") || !strings.Contains(label, "?") {
		t.Fatalf("input-only label = %q", label)
	}
	outputOnly := llm.SessionUsage{Requests: 2, OutputReported: 2, OutputTokens: 300, TotalTokens: 300}
	label = usageLabelFor(tokenRunner{usage: outputOnly}, false)
	if !strings.Contains(label, "O:300") || !strings.Contains(label, "I:?") || !strings.Contains(label, "?") {
		t.Fatalf("output-only label = %q", label)
	}
}

// Legacy totals stay uncertain and keep the marker.
func TestUsageReportLegacyUncertain(t *testing.T) {
	s := llm.SessionUsage{InputTokens: 500, OutputTokens: 80, TotalTokens: 580, Missing: 1, LegacyUncertain: true}
	label := usageLabelFor(tokenRunner{usage: s}, true)
	if !strings.Contains(label, "?") {
		t.Fatalf("legacy label missing ?: %q", label)
	}
	report := formatUsageReport(s)
	if !strings.Contains(report, "Legacy uncertain totals: yes") {
		t.Fatalf("legacy line missing:\n%s", report)
	}
}

// Mixed providers accumulate as plain token sums.
func TestUsageReportMixedProvidersAreTokenSums(t *testing.T) {
	var s llm.SessionUsage
	s.Add(llm.NewUsage(llm.IntPtr(100), llm.IntPtr(10), llm.IntPtr(60), nil, nil, nil))
	s.Add(llm.NewUsage(llm.IntPtr(200), llm.IntPtr(20), nil, nil, nil, nil))
	if s.InputTokens != 300 || s.OutputTokens != 30 || s.TotalTokens != 330 {
		t.Fatalf("mixed sums = %+v", s)
	}
	report := formatUsageReport(s)
	if !strings.Contains(report, "Input tokens: 300") || !strings.Contains(report, "Total tokens: 330") {
		t.Fatalf("mixed report:\n%s", report)
	}
}

// Empty session shows zero and names the empty state.
func TestUsageReportNoRequests(t *testing.T) {
	report := formatUsageReport(llm.SessionUsage{})
	if !strings.Contains(report, "no requests yet") {
		t.Fatalf("empty header:\n%s", report)
	}
	if !strings.Contains(report, "Input tokens: 0") {
		t.Fatalf("empty input:\n%s", report)
	}
	if got := usageLabelFor(tokenRunner{usage: llm.SessionUsage{}}, false); got != "I:0 O:0 T:0" {
		t.Fatalf("empty label = %q", got)
	}
	// Paired ratio with no eligible input is unavailable.
	if !strings.Contains(report, "Cached input share (reported subset): unavailable") {
		t.Fatalf("empty ratio:\n%s", report)
	}
}

// Narrow-width and ASCII/Unicode status layouts keep the compact line valid.
func TestUsageStatusNarrowASCIIUnicode(t *testing.T) {
	for _, unicode := range []bool{true, false} {
		for _, width := range []int{30, 36, 80, 120} {
			label := usageLabelFor(tokenRunner{usage: llm.SessionUsage{
				Requests: 3, InputReported: 2, InputTokens: 1500,
				OutputReported: 3, OutputTokens: 300, TotalTokens: 1800, Missing: 1,
			}}, unicode)
			bar := statusBar("openai", "gpt", "/w", width, unicode, false, "73% left", label, "2/32")
			for _, line := range strings.Split(bar, "\n") {
				if visibleWidth(line) > width {
					t.Fatalf("unicode=%v width=%d overflow: %q", unicode, width, bar)
				}
				if strings.HasPrefix(line, " ") {
					t.Fatalf("not left-aligned: %q", bar)
				}
			}
			if width >= 80 && !strings.Contains(bar, "I:") {
				t.Fatalf("compact tokens dropped unexpectedly: %q", bar)
			}
			// Full cache statistics never belong in the narrow bar.
			if strings.Contains(bar, "Cache-read") || strings.Contains(bar, "Cached input share") {
				t.Fatalf("detailed cache leaked into status bar: %q", bar)
			}
		}
	}
	unicodeLabel := usageLabelFor(tokenRunner{usage: llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540, Requests: 2, InputReported: 2, OutputReported: 2}}, true)
	if !strings.Contains(unicodeLabel, "Σ:") {
		t.Fatalf("unicode total marker = %q", unicodeLabel)
	}
	asciiLabel := usageLabelFor(tokenRunner{usage: llm.SessionUsage{InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540, Requests: 2, InputReported: 2, OutputReported: 2}}, false)
	if !strings.Contains(asciiLabel, "T:") || strings.Contains(asciiLabel, "Σ:") {
		t.Fatalf("ascii total marker = %q", asciiLabel)
	}
}

// /usage reads the active agent, so tab switches change the view.
func TestUsageCommandActiveTabSwitching(t *testing.T) {
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output)}
	u.runner = tokenRunner{usage: llm.SessionUsage{Requests: 1, InputReported: 1, InputTokens: 100, OutputReported: 1, OutputTokens: 10, TotalTokens: 110}}
	u.handleUsageCommand([]string{"/usage"})
	first := output.String()
	output.Reset()
	u.runner = tokenRunner{usage: llm.SessionUsage{Requests: 1, InputReported: 1, InputTokens: 900, OutputReported: 1, OutputTokens: 90, TotalTokens: 990}}
	u.handleUsageCommand([]string{"/usage"})
	second := output.String()
	if !strings.Contains(first, "Input tokens: 100") || !strings.Contains(second, "Input tokens: 900") {
		t.Fatalf("tab switch did not change view:\n%s\n---\n%s", first, second)
	}
}

// /usage is read-only and safe while the agent is running.
func TestUsageCommandSafeWhileRunning(t *testing.T) {
	var output bytes.Buffer
	usage := llm.SessionUsage{Requests: 2, InputReported: 2, InputTokens: 500, OutputReported: 2, OutputTokens: 50, TotalTokens: 550}
	u := &UI{
		display:     newHistoryWriter(&output),
		runner:      tokenRunner{usage: usage},
		manager:     &layoutManager{states: map[string]session.Status{"main": session.StatusRunning}},
		activeAgent: "main",
	}
	u.handleUsageCommand([]string{"/usage"})
	if !strings.Contains(output.String(), "Input tokens: 500") {
		t.Fatalf("running agent usage missing:\n%s", output.String())
	}
	// Read-only: the snapshot is unchanged and no usage error is printed.
	if got := u.runner.(tokenRunner).usage; got != usage {
		t.Fatalf("runner mutated: %+v", got)
	}
	u.handleUsageCommand([]string{"/usage", "extra"})
	if !strings.Contains(output.String(), "Usage: /usage") {
		t.Fatalf("bad args did not print usage:\n%s", output.String())
	}
}

// Ratio wording and rounding.
func TestUsageReportCachedShareRounding(t *testing.T) {
	s := llm.SessionUsage{Requests: 3, CacheRatioInputTokens: 3, CacheRatioReadTokens: 1, CacheRatioRequests: 2}
	report := formatUsageReport(s)
	if !strings.Contains(report, "Cached input share (reported subset): 33.3% (based on 2/3 requests)") {
		t.Fatalf("rounding:\n%s", report)
	}
	if strings.Contains(strings.ToLower(report), "hit rate") {
		t.Fatalf("ratio uses forbidden rate language:\n%s", report)
	}
	empty := formatUsageReport(llm.SessionUsage{Requests: 2})
	if !strings.Contains(empty, "Cached input share (reported subset): unavailable") {
		t.Fatalf("zero denominator:\n%s", empty)
	}
}

// Ollama never reports cache reads: zero coverage stays unavailable, never zero.
func TestUsageReportOllamaCacheReadUnavailable(t *testing.T) {
	s := llm.SessionUsage{
		Requests: 2, InputReported: 2, InputTokens: 400,
		OutputReported: 2, OutputTokens: 80, TotalTokens: 480,
	}
	report := formatUsageReport(s)
	if !strings.Contains(report, "Cache-read tokens: unavailable") {
		t.Fatalf("ollama cache-read:\n%s", report)
	}
	if strings.Contains(report, "Cache-read tokens: 0") {
		t.Fatalf("ollama cache-read shown as zero:\n%s", report)
	}
}

// /help knows /usage via the shared command table.
func TestUsageHelpListsCommand(t *testing.T) {
	if _, ok := findSlashCommand("/usage"); !ok {
		t.Fatal("/usage missing from slashCommands")
	}
	if _, ok := findSlashCommand("usage"); !ok {
		t.Fatal("usage without slash missing")
	}
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output), width: 100}
	u.printCommandHelp(nil)
	if !strings.Contains(output.String(), "/usage") {
		t.Fatalf("help list missing /usage:\n%s", output.String())
	}
	output.Reset()
	u.printCommandHelp([]string{"/usage"})
	if !strings.Contains(output.String(), "/usage") {
		t.Fatalf("help detail missing /usage:\n%s", output.String())
	}
}
