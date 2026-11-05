package tui

import (
	"fmt"
	"strings"

	"qcode/internal/llm"
)

func (u *UI) usageLabel() string {
	return usageLabelFor(u.runner, u.unicode)
}

func usageLabelFor(value Runner, unicode bool) string {
	runner, ok := value.(interface{ SessionUsage() llm.SessionUsage })
	if !ok {
		return "unknown"
	}
	s := runner.SessionUsage()
	if s.Missing > 0 && s.TotalTokens == 0 && s.Requests == 0 {
		return "unknown"
	}
	if s.Requests > 0 && s.InputReported == 0 && s.OutputReported == 0 {
		return "unknown"
	}
	suffix := ""
	if s.Missing > 0 || s.LegacyUncertain {
		suffix = "?"
	} else if s.Requests > 0 && (s.InputReported < s.Requests || s.OutputReported < s.Requests) {
		// Partial subtotal: some completions omitted one side.
		suffix = "?"
	}
	inputStr := compactTokenCount(s.InputTokens)
	outputStr := compactTokenCount(s.OutputTokens)
	if s.Requests > 0 {
		// Never display an unreported side as a reported zero.
		if s.InputReported == 0 {
			inputStr = "?"
		}
		if s.OutputReported == 0 {
			outputStr = "?"
		}
	}
	label := fmt.Sprintf("I:%s O:%s%s", inputStr, outputStr, suffix)
	label += interfaceGlyph(unicode, " Σ:", " T:") + compactTokenCount(s.TotalTokens)
	return label
}

func (u *UI) stepsLabel() string {
	return stepsLabelFor(u.runner)
}

func stepsLabelFor(value Runner) string {
	runner, ok := value.(stepRunner)
	if !ok {
		return ""
	}
	current, maximum := runner.StepProgress()
	return fmt.Sprintf("%d/%d", current, maximum)
}

func compactTokenCount(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return compactTokenUnit(tokens, 1000, "K")
	}
	return compactTokenUnit(tokens, 1_000_000, "M")
}

// formatReportedSum renders a session sum with its request coverage. A fully
// reported sum prints as a bare number; a partially reported sum appends its
// coverage as "500 (reported for 2/3 requests)". Zero coverage prints as
// "unavailable" so unreported sides (for example Ollama cache reads, which
// are never reported) are never shown as a reported zero. An empty session
// with no requests prints "0".
func formatReportedSum(sum, reported, requests int) string {
	if requests == 0 {
		return "0"
	}
	if reported <= 0 {
		return "unavailable"
	}
	if reported < requests {
		return fmt.Sprintf("%d (reported for %d/%d requests)", sum, reported, requests)
	}
	return fmt.Sprintf("%d", sum)
}

// formatTotalTokens renders the session total, which is the sum of known
// input and known output components. Coverage names both sides because the
// total is a partial subtotal when either side is missing.
func formatTotalTokens(s llm.SessionUsage) string {
	if s.Requests == 0 {
		return "0"
	}
	if s.InputReported == 0 && s.OutputReported == 0 {
		return "unavailable"
	}
	if s.InputReported == s.Requests && s.OutputReported == s.Requests {
		return fmt.Sprintf("%d", s.TotalTokens)
	}
	return fmt.Sprintf("%d (input reported for %d/%d requests, output reported for %d/%d requests)",
		s.TotalTokens, s.InputReported, s.Requests, s.OutputReported, s.Requests)
}

// formatCachedShare renders the paired-sum ratio as a cached input share over
// the reported subset. It uses only CacheRatio sums (requests with both known
// input and known cache-read counts) and keeps the reported-subset wording.
// Zero eligible input or zero eligible requests prints as "unavailable".
func formatCachedShare(s llm.SessionUsage) string {
	if s.CacheRatioRequests <= 0 || s.CacheRatioInputTokens <= 0 {
		return "unavailable"
	}
	if s.CacheRatioReadTokens < 0 || s.CacheRatioReadTokens > s.CacheRatioInputTokens {
		return "unavailable"
	}
	ratio := float64(s.CacheRatioReadTokens) / float64(s.CacheRatioInputTokens)
	return fmt.Sprintf("%.1f%% (based on %d/%d requests)", ratio*100, s.CacheRatioRequests, s.Requests)
}

// formatUsageReport renders the read-only detailed view for /usage. It is a
// pure function of the session snapshot so active-tab switching shows each
// agent's own totals and concurrent reads stay safe.
func formatUsageReport(s llm.SessionUsage) string {
	var header string
	if s.Requests == 0 {
		header = "Usage for active agent (no requests yet):"
	} else if s.Requests == 1 {
		header = "Usage for active agent (1 request):"
	} else {
		header = fmt.Sprintf("Usage for active agent (%d requests):", s.Requests)
	}
	legacy := "no"
	if s.LegacyUncertain {
		legacy = "yes (restored totals may mix pre-schema-2 zeros with reported counts)"
	}
	lines := []string{
		header,
		"  Input tokens: " + formatReportedSum(s.InputTokens, s.InputReported, s.Requests),
		"  Output tokens: " + formatReportedSum(s.OutputTokens, s.OutputReported, s.Requests),
		"  Total tokens: " + formatTotalTokens(s),
		"  Cache-read tokens: " + formatReportedSum(s.CacheReadTokens, s.CacheReadReported, s.Requests),
		"  Cache-write tokens: " + formatReportedSum(s.CacheWriteTokens, s.CacheWriteReported, s.Requests),
		"  Cached input share (reported subset): " + formatCachedShare(s),
		fmt.Sprintf("  Missing completions: %d", s.Missing),
		"  Legacy uncertain totals: " + legacy,
		"  Notes: Cached tokens occupy context and are included in input totals.",
		"  Unknown means unreported, not zero. Partial subtotals are not bills. Mixed-provider totals are token sums only.",
	}
	return strings.Join(lines, "\n")
}

// handleUsageCommand prints the detailed usage view for the active agent. It
// performs no writes and never touches manager configuration, so it is safe
// to run while that agent is working.
func (u *UI) handleUsageCommand(fields []string) {
	if len(fields) != 1 {
		u.printSystemMessage(yellow + "Usage: /usage" + reset)
		return
	}
	runner, ok := u.runner.(interface{ SessionUsage() llm.SessionUsage })
	if !ok {
		u.printSystemMessage(yellow + "Usage is unavailable." + reset)
		return
	}
	u.printSystemMessage(formatUsageReport(runner.SessionUsage()))
}

func compactTokenUnit(tokens, unit int, suffix string) string {
	scaled := (tokens*10 + unit/2) / unit
	value := fmt.Sprintf("%d", scaled/10)
	if fraction := scaled % 10; fraction != 0 {
		value += fmt.Sprintf(".%d", fraction)
	}
	return value + suffix
}
