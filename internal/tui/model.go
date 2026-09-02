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
	if visible < 1 || visible > len(models) {
		visible = len(models)
	}
	rows := visible + 1
	query := ""
	matches := matchingModelIndices(models, query)
	selected = selectedMatch(matches, selected)
	renderModelSelector(out, models, matches, selected, visible, width, query, color)
	buffer := make([]byte, 1)
	for {
		if _, err := io.ReadFull(in, buffer); err != nil {
			return "", false, err
		}
		switch buffer[0] {
		case '\r', '\n':
			if len(matches) == 0 {
				continue
			}
			clearModelSelector(out, rows)
			return models[matches[selected]], true, nil
		case ctrlC, 27:
			if buffer[0] == 27 {
				sequence := make([]byte, 2)
				if _, err := io.ReadFull(in, sequence); err != nil {
					clearModelSelector(out, rows)
					return "", false, nil
				}
				key := string(append([]byte{27}, sequence...))
				if key == arrowUpSequence {
					if len(matches) > 0 {
						selected = (selected - 1 + len(matches)) % len(matches)
					}
				} else if key == arrowDownSequence {
					if len(matches) > 0 {
						selected = (selected + 1) % len(matches)
					}
				} else {
					continue
				}
				clearModelSelector(out, rows)
				renderModelSelector(out, models, matches, selected, visible, width, query, color)
				continue
			}
			clearModelSelector(out, rows)
			return "", false, nil
		case 8, 127:
			if len(query) == 0 {
				continue
			}
			query = query[:len(query)-1]
			matches = matchingModelIndices(models, query)
			selected = 0
		case ctrlU:
			query = ""
			matches = matchingModelIndices(models, query)
			selected = 0
		default:
			if buffer[0] < 32 || buffer[0] > 126 {
				continue
			}
			query += string(buffer[0])
			matches = matchingModelIndices(models, query)
			selected = 0
		}
		clearModelSelector(out, rows)
		renderModelSelector(out, models, matches, selected, visible, width, query, color)
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

func renderModelSelector(out io.Writer, models []string, matches []int, selected, visible, width int, query string, color bool) {
	header := fmt.Sprintf("Select model (%d/%d)  Search: %s", len(matches), len(models), query)
	if width > 0 {
		header = truncateDiffLine(header, width, false)
	}
	fmt.Fprintln(out, header)
	start := selected - visible/2
	if start < 0 {
		start = 0
	}
	if start+visible > len(matches) {
		start = len(matches) - visible
		if start < 0 {
			start = 0
		}
	}
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
		index := matches[matchIndex]
		marker := "  "
		if matchIndex == selected {
			marker = "> "
		}
		model := sanitizeDiffLine(models[index], "<ESC>")
		if width > 2 {
			model = truncateDiffLine(model, width-2, false)
		}
		if color && matchIndex == selected {
			fmt.Fprintf(out, "%s%s%s%s\n", cyan+bold, marker, model, reset)
		} else {
			fmt.Fprintln(out, marker+model)
		}
	}
}

func clearModelSelector(out io.Writer, rows int) {
	for range rows {
		fmt.Fprint(out, "\x1b[1A\r\x1b[2K")
	}
}
