package tui

import (
	"fmt"

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
	if s.Missing > 0 && s.TotalTokens == 0 {
		return "unknown"
	}
	suffix := ""
	if s.Missing > 0 {
		suffix = "?"
	}
	label := fmt.Sprintf("I:%s O:%s%s", compactTokenCount(s.InputTokens), compactTokenCount(s.OutputTokens), suffix)
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

func compactTokenUnit(tokens, unit int, suffix string) string {
	scaled := (tokens*10 + unit/2) / unit
	value := fmt.Sprintf("%d", scaled/10)
	if fraction := scaled % 10; fraction != 0 {
		value += fmt.Sprintf(".%d", fraction)
	}
	return value + suffix
}
