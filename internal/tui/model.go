package tui

import (
	"fmt"
	"io"
	"strings"
)

const (
	arrowUpSequence   = "\x1b[A"
	arrowDownSequence = "\x1b[B"
)

func selectModel(in io.Reader, out io.Writer, models []string, current string, visible, width int, color bool) (string, bool, error) {
	return selectOption(in, out, models, current, visible, width, color, "model", true)
}

func selectModelWithQuery(in io.Reader, out io.Writer, models []string, current, query string, visible, width int, color bool) (string, selectorResult, string, error) {
	return selectOptionWithQuery(in, out, models, current, query, visible, width, color, "model", true)
}

// selectThinking is intentionally a separate entry point so the two-stage
// model picker can use the same bounded keyboard selector without calling
// thinking levels models in its prompt.
func selectThinking(in io.Reader, out io.Writer, levels []string, current string, visible, width int, color bool) (string, bool, error) {
	return selectOption(in, out, levels, current, visible, width, color, "thinking level", false)
}

func selectThinkingWithBack(in io.Reader, out io.Writer, levels []string, current string, visible, width int, color bool) (string, bool, bool, error) {
	selected, result, err := selectOptionWithResult(in, out, levels, current, visible, width, color, "thinking level", false)
	return selected, result == selectorAccepted, result == selectorBack, err
}

func selectOption(in io.Reader, out io.Writer, models []string, current string, visible, width int, color bool, noun string, searchable bool) (string, bool, error) {
	return selectOptionWithDisabled(in, out, models, nil, current, visible, width, color, noun, searchable)
}

func selectOptionWithDisabled(in io.Reader, out io.Writer, models []string, disabled map[string]bool, current string, visible, width int, color bool, noun string, searchable bool) (string, bool, error) {
	selected, result, err := selectOptionWithResultAndDisabled(in, out, models, disabled, current, visible, width, color, noun, searchable)
	return selected, result == selectorAccepted, err
}

func selectOptionWithResult(in io.Reader, out io.Writer, models []string, current string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, error) {
	return selectOptionWithResultAndDisabled(in, out, models, nil, current, visible, width, color, noun, searchable)
}

func selectOptionWithResultAndDisabled(in io.Reader, out io.Writer, models []string, disabled map[string]bool, current string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, error) {
	selected, result, _, err := selectOptionWithQueryAndDisabled(in, out, models, disabled, current, "", visible, width, color, noun, searchable)
	if result != selectorAccepted {
		selected = ""
	}
	return selected, result, err
}

func selectOptionWithQuery(in io.Reader, out io.Writer, models []string, current, initialQuery string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, string, error) {
	return selectOptionWithQueryAndDisabled(in, out, models, nil, current, initialQuery, visible, width, color, noun, searchable)
}

func selectOptionWithQueryAndDisabled(in io.Reader, out io.Writer, models []string, disabled map[string]bool, current, initialQuery string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, string, error) {
	if len(models) == 0 {
		return "", selectorCancelled, initialQuery, nil
	}
	selected := 0
	for index, model := range models {
		if model == current {
			selected = index
			break
		}
	}
	visible = selectorVisible(len(models), visible)
	rows := visible + 1
	query := initialQuery
	if !searchable {
		query = ""
	}
	matches := matchingModelIndices(models, query)
	selected = selectedMatch(matches, selected)
	selected = adjustSelectedToEnabled(matches, models, disabled, selected)
	start := selectorInitialStart(selected, len(matches), visible)
	renderModelSelectorWithDisabled(out, models, matches, disabled, selected, start, visible, width, query, color, noun, searchable)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			return "", selectorCancelled, query, err
		}
		switch key {
		case "\r", "\n":
			if len(matches) == 0 {
				continue
			}
			if isMatchDisabled(matches, models, disabled, selected) {
				continue
			}
			clearModelSelector(out, rows)
			return models[matches[selected]], selectorAccepted, query, nil
		case string([]byte{ctrlC}):
			clearModelSelector(out, rows)
			return "", selectorCancelled, query, nil
		case "\x1b":
			clearModelSelector(out, rows)
			selectedModel := ""
			if len(matches) > 0 {
				selectedModel = models[matches[selected]]
			}
			return selectedModel, selectorBack, query, nil
		case arrowUpSequence, arrowDownSequence, selectorPageUp, selectorPageDown:
			if len(matches) == 0 {
				continue
			}
			oldSelected, oldStart := selected, start
			switch key {
			case arrowUpSequence:
				selected = nextEnabledMatch(matches, models, disabled, selected, -1)
				start = selectorStart(selected, len(matches), visible, start)
			case arrowDownSequence:
				selected = nextEnabledMatch(matches, models, disabled, selected, 1)
				start = selectorStart(selected, len(matches), visible, start)
			case selectorPageUp:
				paged, _ := selectorPage(selected, start, len(matches), visible, -1)
				selected = adjustPagedToEnabled(matches, models, disabled, selected, paged, -1)
				start = selectorStart(selected, len(matches), visible, start)
			case selectorPageDown:
				paged, _ := selectorPage(selected, start, len(matches), visible, 1)
				selected = adjustPagedToEnabled(matches, models, disabled, selected, paged, 1)
				start = selectorStart(selected, len(matches), visible, start)
			}
			if start != oldStart {
				clearModelSelector(out, rows)
				renderModelSelectorWithDisabled(out, models, matches, disabled, selected, start, visible, width, query, color, noun, searchable)
			} else if selected != oldSelected {
				replaceSelectorRow(out, rows, 1+oldSelected-start, renderModelLineWithDisabled(models, matches, disabled, oldSelected, false, width, color))
				replaceSelectorRow(out, rows, 1+selected-start, renderModelLineWithDisabled(models, matches, disabled, selected, true, width, color))
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if !searchable {
				continue
			}
			if len(query) == 0 {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingModelIndices(models, query)
			selected = firstEnabledMatch(matches, models, disabled)
		case string([]byte{ctrlU}):
			if !searchable {
				continue
			}
			query = ""
			matches = matchingModelIndices(models, query)
			selected = firstEnabledMatch(matches, models, disabled)
		default:
			if !searchable {
				continue
			}
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingModelIndices(models, query)
			selected = firstEnabledMatch(matches, models, disabled)
		}
		start = selectorInitialStart(selected, len(matches), visible)
		clearModelSelector(out, rows)
		renderModelSelectorWithDisabled(out, models, matches, disabled, selected, start, visible, width, query, color, noun, searchable)
	}
}

func isMatchDisabled(matches []int, models []string, disabled map[string]bool, pos int) bool {
	if len(disabled) == 0 || pos < 0 || pos >= len(matches) {
		return false
	}
	return disabled[models[matches[pos]]]
}

func firstEnabledMatch(matches []int, models []string, disabled map[string]bool) int {
	if len(disabled) == 0 {
		return 0
	}
	for pos, match := range matches {
		if !disabled[models[match]] {
			return pos
		}
	}
	return 0
}

func adjustSelectedToEnabled(matches []int, models []string, disabled map[string]bool, selected int) int {
	if len(matches) == 0 || len(disabled) == 0 {
		return selected
	}
	if selected < 0 || selected >= len(matches) {
		return firstEnabledMatch(matches, models, disabled)
	}
	if !isMatchDisabled(matches, models, disabled, selected) {
		return selected
	}
	// Prefer the next enabled option, wrapping once.
	for step := 1; step <= len(matches); step++ {
		next := (selected + step) % len(matches)
		if !isMatchDisabled(matches, models, disabled, next) {
			return next
		}
	}
	return selected
}

func nextEnabledMatch(matches []int, models []string, disabled map[string]bool, selected, direction int) int {
	if len(matches) == 0 {
		return selected
	}
	if len(disabled) == 0 {
		if direction < 0 {
			return (selected - 1 + len(matches)) % len(matches)
		}
		return (selected + 1) % len(matches)
	}
	for step := 1; step <= len(matches); step++ {
		next := selected + direction*step
		next %= len(matches)
		if next < 0 {
			next += len(matches)
		}
		if !isMatchDisabled(matches, models, disabled, next) {
			return next
		}
	}
	return selected
}

func adjustPagedToEnabled(matches []int, models []string, disabled map[string]bool, oldSelected, paged, direction int) int {
	if len(matches) == 0 || len(disabled) == 0 {
		return paged
	}
	if !isMatchDisabled(matches, models, disabled, paged) {
		return paged
	}
	next := nextEnabledMatch(matches, models, disabled, paged, direction)
	// If paging lands in a disabled run with no enabled option ahead before
	// wrapping, keep the previous selection when it is still enabled.
	if next == paged && !isMatchDisabled(matches, models, disabled, oldSelected) {
		return oldSelected
	}
	return next
}

func matchingModelIndices(models []string, query string) []int {
	query = strings.ToLower(query)
	matches := make([]int, 0, len(models))
	for index, model := range models {
		if strings.Contains(strings.ToLower(model), query) {
			matches = append(matches, index)
		}
	}
	return matches
}

func selectedMatch(matches []int, modelIndex int) int {
	for index, match := range matches {
		if match == modelIndex {
			return index
		}
	}
	return 0
}

func renderModelSelector(out io.Writer, models []string, matches []int, selected, start, visible, width int, query string, color bool, noun string, searchable bool) {
	renderModelSelectorWithDisabled(out, models, matches, nil, selected, start, visible, width, query, color, noun, searchable)
}

func renderModelSelectorWithDisabled(out io.Writer, models []string, matches []int, disabled map[string]bool, selected, start, visible, width int, query string, color bool, noun string, searchable bool) {
	header := fmt.Sprintf("Select %s (%d/%d) | Up/Down, PgUp/PgDn", noun, len(matches), len(models))
	if searchable {
		header += " | Search: " + query
	}
	header = selectorHeader(header, width)
	printSelectorRow(out, header)
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				printSelectorRow(out, "  No matching models")
			} else {
				printSelectorRow(out, "")
			}
			continue
		}
		printSelectorRow(out, renderModelLineWithDisabled(models, matches, disabled, matchIndex, matchIndex == selected, width, color))
	}
}

func renderModelLine(models []string, matches []int, matchIndex int, current bool, width int, color bool) string {
	return renderModelLineWithDisabled(models, matches, nil, matchIndex, current, width, color)
}

func renderModelLineWithDisabled(models []string, matches []int, disabled map[string]bool, matchIndex int, current bool, width int, color bool) string {
	marker := "  "
	if current {
		marker = "> "
	}
	model := sanitizeDiffLine(models[matches[matchIndex]], "<ESC>")
	if width > 2 {
		model = truncateDiffLine(model, width-2, false)
	}
	line := marker + model
	isDisabled := len(disabled) > 0 && disabled[models[matches[matchIndex]]]
	if isDisabled {
		if color {
			line = dim + line + reset
		}
		return line
	}
	if color && current {
		line = cyan + bold + line + reset
	}
	return line
}

func clearModelSelector(out io.Writer, rows int) {
	clearSelector(out, rows)
}
