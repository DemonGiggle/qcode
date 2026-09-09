package tui

import (
	"fmt"
	"io"
)

const (
	selectorPageUp   = "\x1b[5~"
	selectorPageDown = "\x1b[6~"
)

func readSelectorKey(in io.Reader) (string, error) {
	var first [1]byte
	if _, err := io.ReadFull(in, first[:]); err != nil {
		return "", err
	}
	if first[0] != 27 {
		return string(first[:]), nil
	}
	var tail [2]byte
	if _, err := io.ReadFull(in, tail[:]); err != nil {
		return string(first[:]), nil
	}
	key := string(append(first[:], tail[:]...))
	if tail[0] != '[' || (tail[1] != '5' && tail[1] != '6') {
		return key, nil
	}
	var terminator [1]byte
	if _, err := io.ReadFull(in, terminator[:]); err != nil {
		return key, nil
	}
	return key + string(terminator[:]), nil
}

func selectorVisible(total, requested int) int {
	if requested < 1 || requested > total {
		return total
	}
	return requested
}

func selectorInitialStart(selected, total, visible int) int {
	return selectorStart(selected, total, visible, 0)
}

func selectorStart(selected, total, visible, _ int) int {
	if total == 0 || visible == 0 {
		return 0
	}
	return selected / visible * visible
}

func selectorPage(selected, _ int, total, visible, direction int) (int, int) {
	selected = min(total-1, max(0, selected+direction*visible))
	return selected, selectorStart(selected, total, visible, 0)
}

// replaceSelectorRow changes one row while preserving the cursor immediately
// below the selector. row is zero-based from the top of the rendered block.
func replaceSelectorRow(out io.Writer, rows, row int, line string) {
	fmt.Fprintf(out, "\x1b[s\x1b[%dA\r\x1b[2K%s\x1b[u", rows-row, line)
}

func clearSelector(out io.Writer, rows int) {
	for range rows {
		fmt.Fprint(out, "\x1b[1A\r\x1b[2K")
	}
}
