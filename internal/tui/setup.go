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

type rawLFWriter struct {
	out    io.Writer
	prevCR bool
}

func (w *rawLFWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	buf := make([]byte, 0, len(p)+8)
	for _, b := range p {
		if b == '\n' && !w.prevCR {
			buf = append(buf, '\r', '\n')
		} else {
			buf = append(buf, b)
		}
		w.prevCR = b == '\r'
	}
	if _, err := w.out.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *SetupUI) selectorOut() io.Writer {
	if s.inFile == nil {
		return s.out
	}
	return &rawLFWriter{out: s.out}
}

func (s *SetupUI) refreshSize() {
	if f, ok := s.out.(*os.File); ok {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			s.width = w
			s.height = h
		}
	}
}

func (s *SetupUI) withRawMode(fn func() error) error {
	s.refreshSize()
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

// SetupNavResult describes how a standalone setup prompt or selector ended.
type SetupNavResult int

const (
	SetupCancelled SetupNavResult = iota
	SetupAccepted
	SetupBack
)

func mapSelectorResult(res selectorResult) SetupNavResult {
	switch res {
	case selectorAccepted:
		return SetupAccepted
	case selectorBack:
		return SetupBack
	default:
		return SetupCancelled
	}
}

// SelectOption displays an interactive arrow-key option picker.
func (s *SetupUI) SelectOption(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, bool, error) {
	selected, res, err := s.SelectOptionNav(noun, options, disabled, current, searchable)
	return selected, res == SetupAccepted, err
}

// SelectOptionNav displays an interactive arrow-key option picker and
// distinguishes Esc (SetupBack) from Ctrl+C (SetupCancelled).
func (s *SetupUI) SelectOptionNav(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, SetupNavResult, error) {
	var selected string
	var result selectorResult
	err := s.withRawMode(func() error {
		var selErr error
		selected, result, selErr = selectOptionWithResultAndDisabled(
			s.in, s.selectorOut(), options, disabled, current,
			s.visibleRows(len(options)), s.width, s.color, noun, searchable,
		)
		return selErr
	})
	return selected, mapSelectorResult(result), err
}

// SelectModel displays a searchable interactive model picker.
func (s *SetupUI) SelectModel(models []string, current string) (string, bool, error) {
	selected, res, err := s.SelectModelNav(models, current)
	return selected, res == SetupAccepted, err
}

// SelectModelNav displays a searchable interactive model picker and
// distinguishes Esc (SetupBack) from Ctrl+C (SetupCancelled).
func (s *SetupUI) SelectModelNav(models []string, current string) (string, SetupNavResult, error) {
	var selected string
	var result selectorResult
	err := s.withRawMode(func() error {
		var selErr error
		selected, result, _, selErr = selectModelWithQuery(
			s.in, s.selectorOut(), models, current, "",
			s.visibleRows(len(models)), s.width, s.color,
		)
		return selErr
	})
	return selected, mapSelectorResult(result), err
}

// SelectThinking displays an interactive thinking-level picker.
func (s *SetupUI) SelectThinking(levels []string, current string) (string, bool, error) {
	selected, res, err := s.SelectThinkingNav(levels, current)
	return selected, res == SetupAccepted, err
}

// SelectThinkingNav displays an interactive thinking-level picker and
// distinguishes Esc (SetupBack) from Ctrl+C (SetupCancelled).
func (s *SetupUI) SelectThinkingNav(levels []string, current string) (string, SetupNavResult, error) {
	var selected string
	var result selectorResult
	err := s.withRawMode(func() error {
		var selErr error
		selected, result, selErr = selectOptionWithResult(
			s.in, s.selectorOut(), levels, current,
			s.visibleRows(len(levels)), s.width, s.color, "thinking level", false,
		)
		return selErr
	})
	return selected, mapSelectorResult(result), err
}

// SelectTheme displays the interactive theme picker with live preview.
func (s *SetupUI) SelectTheme(current string) (string, bool, error) {
	selected, res, err := s.SelectThemeNav(current)
	return selected, res == SetupAccepted, err
}

// SelectThemeNav displays the interactive theme picker with live preview.
// Esc/Ctrl+C returns SetupBack so the user returns to the previous menu.
func (s *SetupUI) SelectThemeNav(current string) (string, SetupNavResult, error) {
	palettes := qtheme.All()
	var selected string
	var accepted bool
	err := s.withRawMode(func() error {
		budget := s.height - 4
		if budget < themePickerHeaderRows+4 {
			budget = themePickerHeaderRows + 4
		}
		if budget > 24 {
			budget = 24
		}
		visible := themePickerVisible(budget, len(palettes))
		var selErr error
		selected, accepted, selErr = selectTheme(
			s.in, s.selectorOut(), palettes, current,
			visible, s.width, budget, s.color,
		)
		return selErr
	})
	if err != nil {
		return "", SetupCancelled, err
	}
	if accepted {
		return selected, SetupAccepted, nil
	}
	return "", SetupBack, nil
}

// PromptLine prompts the user for a single line of text. If secret is true,
// typed characters are masked with '*' in terminal mode. Pressing Enter on an
// empty input returns defaultVal (if non-empty). Ctrl+C or Esc cancels and
// returns accepted=false.
func (s *SetupUI) PromptLine(label, defaultVal string, secret bool) (string, bool, error) {
	val, res, err := s.PromptLineNav(label, defaultVal, secret)
	return val, res == SetupAccepted, err
}

// PromptLineNav prompts the user for a single line of text, distinguishing Esc
// (SetupBack) from Ctrl+C (SetupCancelled).
func (s *SetupUI) PromptLineNav(label, defaultVal string, secret bool) (string, SetupNavResult, error) {
	if defaultVal != "" && !secret {
		fmt.Fprintf(s.out, "%s [%s]: ", label, defaultVal)
	} else {
		fmt.Fprintf(s.out, "%s: ", label)
	}

	if s.inFile == nil {
		reader := bufio.NewReader(s.in)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", SetupCancelled, err
		}
		if err == io.EOF && line == "" {
			return "", SetupCancelled, nil
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return defaultVal, SetupAccepted, nil
		}
		return line, SetupAccepted, nil
	}

	var buf []rune
	nav := SetupAccepted
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
			case ctrlC:
				nav = SetupCancelled
				fmt.Fprint(s.out, "\r\n")
				return nil
			case 0x1b:
				nav = SetupBack
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
		return "", SetupCancelled, err
	}
	if nav != SetupAccepted {
		return "", nav, nil
	}
	val := string(buf)
	if val == "" {
		return defaultVal, SetupAccepted, nil
	}
	return val, SetupAccepted, nil
}
