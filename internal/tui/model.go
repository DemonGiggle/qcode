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
	selected, result, err := selectOptionWithResult(in, out, models, current, visible, width, color, noun, searchable)
	return selected, result == selectorAccepted, err
}

func selectOptionWithResult(in io.Reader, out io.Writer, models []string, current string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, error) {
	selected, result, _, err := selectOptionWithQuery(in, out, models, current, "", visible, width, color, noun, searchable)
	if result != selectorAccepted {
		selected = ""
	}
	return selected, result, err
}

func selectOptionWithQuery(in io.Reader, out io.Writer, models []string, current, initialQuery string, visible, width int, color bool, noun string, searchable bool) (string, selectorResult, string, error) {
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
	start := selectorInitialStart(selected, len(matches), visible)
	renderModelSelector(out, models, matches, selected, start, visible, width, query, color, noun, searchable)
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
				selected = (selected - 1 + len(matches)) % len(matches)
				start = selectorStart(selected, len(matches), visible, start)
			case arrowDownSequence:
				selected = (selected + 1) % len(matches)
				start = selectorStart(selected, len(matches), visible, start)
			case selectorPageUp:
				selected, start = selectorPage(selected, start, len(matches), visible, -1)
			case selectorPageDown:
				selected, start = selectorPage(selected, start, len(matches), visible, 1)
			}
			if start != oldStart {
				clearModelSelector(out, rows)
				renderModelSelector(out, models, matches, selected, start, visible, width, query, color, noun, searchable)
			} else if selected != oldSelected {
				replaceSelectorRow(out, rows, 1+oldSelected-start, renderModelLine(models, matches, oldSelected, false, width, color))
				replaceSelectorRow(out, rows, 1+selected-start, renderModelLine(models, matches, selected, true, width, color))
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
			selected = 0
		case string([]byte{ctrlU}):
			if !searchable {
				continue
			}
			query = ""
			matches = matchingModelIndices(models, query)
			selected = 0
		default:
			if !searchable {
				continue
			}
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingModelIndices(models, query)
			selected = 0
		}
		start = selectorInitialStart(selected, len(matches), visible)
		clearModelSelector(out, rows)
		renderModelSelector(out, models, matches, selected, start, visible, width, query, color, noun, searchable)
	}
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
	header := fmt.Sprintf("Select %s (%d/%d) | Up/Down, PgUp/PgDn", noun, len(matches), len(models))
	if searchable {
		header += " | Search: " + query
	}
	header = selectorHeader(header, width)
	fmt.Fprintln(out, header)
	for row := 0; row < visible; row++ {
		matchIndex := start + row
		if matchIndex >= len(matches) {
			if row == 0 && len(matches) == 0 {
				fmt.Fprintln(out, "  No matching models")
			} else {
				fmt.Fprintln(out)
			}
			continue
		}
		fmt.Fprintln(out, renderModelLine(models, matches, matchIndex, matchIndex == selected, width, color))
	}
}

func renderModelLine(models []string, matches []int, matchIndex int, current bool, width int, color bool) string {
	marker := "  "
	if current {
		marker = "> "
	}
	model := sanitizeDiffLine(models[matches[matchIndex]], "<ESC>")
	if width > 2 {
		model = truncateDiffLine(model, width-2, false)
	}
	line := marker + model
	if color && current {
		line = cyan + bold + line + reset
	}
	return line
}

func clearModelSelector(out io.Writer, rows int) {
	clearSelector(out, rows)
}
