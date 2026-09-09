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
	if len(models) == 0 {
		return "", false, nil
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
	query := ""
	matches := matchingModelIndices(models, query)
	selected = selectedMatch(matches, selected)
	start := selectorInitialStart(selected, len(matches), visible)
	renderModelSelector(out, models, matches, selected, start, visible, width, query, color)
	for {
		key, err := readSelectorKey(in)
		if err != nil {
			return "", false, err
		}
		switch key {
		case "\r", "\n":
			if len(matches) == 0 {
				continue
			}
			clearModelSelector(out, rows)
			return models[matches[selected]], true, nil
		case string([]byte{ctrlC}), "\x1b":
			clearModelSelector(out, rows)
			return "", false, nil
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
				renderModelSelector(out, models, matches, selected, start, visible, width, query, color)
			} else if selected != oldSelected {
				replaceSelectorRow(out, rows, 1+oldSelected-start, renderModelLine(models, matches, oldSelected, false, width, color))
				replaceSelectorRow(out, rows, 1+selected-start, renderModelLine(models, matches, selected, true, width, color))
			}
			continue
		case string([]byte{8}), string([]byte{127}):
			if len(query) == 0 {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingModelIndices(models, query)
			selected = 0
		case string([]byte{ctrlU}):
			query = ""
			matches = matchingModelIndices(models, query)
			selected = 0
		default:
			if len(key) != 1 || key[0] < 32 || key[0] > 126 {
				continue
			}
			query += key
			matches = matchingModelIndices(models, query)
			selected = 0
		}
		start = selectorInitialStart(selected, len(matches), visible)
		clearModelSelector(out, rows)
		renderModelSelector(out, models, matches, selected, start, visible, width, query, color)
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

func renderModelSelector(out io.Writer, models []string, matches []int, selected, start, visible, width int, query string, color bool) {
	header := fmt.Sprintf("Select model (%d/%d) | Up/Down, PgUp/PgDn | Search: %s", len(matches), len(models), query)
	if width > 0 {
		header = truncateDiffLine(header, width, false)
	}
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
