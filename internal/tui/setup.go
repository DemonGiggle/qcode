package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
	qtheme "qcode/internal/theme"
)

// SetupUI provides standalone interactive TUI pickers and prompts for CLI
// commands like `qcode setup`, reusing internal/tui's raw-mode selectors
// without starting the full chat UI.
type SetupUI struct {
	in     io.Reader
	out    io.Writer
	inFile *os.File
	width  int
	height int
	color  bool
}

// NewSetupUI constructs a SetupUI for the given input and output streams.
// When in/out are terminal files, raw mode and terminal dimensions are used;
// otherwise sensible defaults are used for testing.
func NewSetupUI(in io.Reader, out io.Writer) *SetupUI {
	ui := &SetupUI{
		in:     in,
		out:    out,
		width:  80,
		height: 24,
	}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		ui.inFile = f
	}
	if f, ok := out.(*os.File); ok {
		ui.color = ColorEnabled(f)
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			ui.width = w
			ui.height = h
		}
	}
	return ui
}

func (s *SetupUI) withRawMode(fn func() error) error {
	if s.inFile == nil {
		return fn()
	}
	state, err := term.MakeRaw(int(s.inFile.Fd()))
	if err != nil {
		return err
	}
	defer func() {
		_ = term.Restore(int(s.inFile.Fd()), state)
	}()
	return fn()
}

func (s *SetupUI) visibleRows(total int) int {
	maxRows := s.height - 6
	if maxRows < 4 {
		maxRows = 4
	}
	if maxRows > 14 {
		maxRows = 14
	}
	if total < maxRows {
		return max(1, total)
	}
	return maxRows
}

// SelectOption displays an interactive arrow-key option picker.
func (s *SetupUI) SelectOption(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, bool, error) {
	var selected string
	var accepted bool
	err := s.withRawMode(func() error {
		var selErr error
		selected, accepted, selErr = selectOptionWithDisabled(
			s.in, s.out, options, disabled, current,
			s.visibleRows(len(options)), s.width, s.color, noun, searchable,
		)
		return selErr
	})
	return selected, accepted, err
}

// SelectModel displays a searchable interactive model picker.
func (s *SetupUI) SelectModel(models []string, current string) (string, bool, error) {
	var selected string
	var result selectorResult
	err := s.withRawMode(func() error {
		var selErr error
		selected, result, _, selErr = selectModelWithQuery(
			s.in, s.out, models, current, "",
			s.visibleRows(len(models)), s.width, s.color,
		)
		return selErr
	})
	return selected, result == selectorAccepted, err
}

// SelectThinking displays an interactive thinking-level picker.
func (s *SetupUI) SelectThinking(levels []string, current string) (string, bool, error) {
	var selected string
	var accepted bool
	err := s.withRawMode(func() error {
		var selErr error
		selected, accepted, selErr = selectThinking(
			s.in, s.out, levels, current,
			s.visibleRows(len(levels)), s.width, s.color,
		)
		return selErr
	})
	return selected, accepted, err
}

// SelectTheme displays the interactive theme picker with live preview.
func (s *SetupUI) SelectTheme(current string) (string, bool, error) {
	palettes := qtheme.All()
	budget := s.height - 4
	if budget < themePickerHeaderRows+4 {
		budget = themePickerHeaderRows + 4
	}
	if budget > 24 {
		budget = 24
	}
	visible := themePickerVisible(budget, len(palettes))
	var selected string
	var accepted bool
	err := s.withRawMode(func() error {
		var selErr error
		selected, accepted, selErr = selectTheme(
			s.in, s.out, palettes, current,
			visible, s.width, budget, s.color,
		)
		return selErr
	})
	return selected, accepted, err
}

// PromptLine prompts the user for a single line of text. If secret is true,
// typed characters are masked with '*' in terminal mode. Pressing Enter on an
// empty input returns defaultVal (if non-empty). Ctrl+C or Esc cancels and
// returns accepted=false.
func (s *SetupUI) PromptLine(label, defaultVal string, secret bool) (string, bool, error) {
	if defaultVal != "" && !secret {
		fmt.Fprintf(s.out, "%s [%s]: ", label, defaultVal)
	} else {
		fmt.Fprintf(s.out, "%s: ", label)
	}

	if s.inFile == nil {
		reader := bufio.NewReader(s.in)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", false, err
		}
		if err == io.EOF && line == "" {
			return "", false, nil
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return defaultVal, true, nil
		}
		return line, true, nil
	}

	var buf []rune
	cancelled := false
	err := s.withRawMode(func() error {
		b := make([]byte, 1)
		var utf8Buf []byte
		for {
			n, rerr := s.in.Read(b)
			if rerr != nil {
				return rerr
			}
			if n == 0 {
				continue
			}
			ch := b[0]
			switch ch {
			case '\r', '\n':
				fmt.Fprint(s.out, "\r\n")
				return nil
			case ctrlC, 0x1b:
				cancelled = true
				fmt.Fprint(s.out, "\r\n")
				return nil
			case 0x7f, 0x08: // Backspace / DEL
				if len(buf) > 0 {
					buf = buf[:len(buf)-1]
					fmt.Fprint(s.out, "\b \b")
				}
			default:
				if ch < 0x20 {
					continue
				}
				utf8Buf = append(utf8Buf, ch)
				if !utf8.FullRune(utf8Buf) {
					continue
				}
				r, _ := utf8.DecodeRune(utf8Buf)
				utf8Buf = utf8Buf[:0]
				if r == utf8.RuneError {
					continue
				}
				buf = append(buf, r)
				if secret {
					fmt.Fprint(s.out, "*")
				} else {
					fmt.Fprintf(s.out, "%c", r)
				}
			}
		}
	})
	if err != nil {
		return "", false, err
	}
	if cancelled {
		return "", false, nil
	}
	val := string(buf)
	if val == "" {
		return defaultVal, true, nil
	}
	return val, true, nil
}
