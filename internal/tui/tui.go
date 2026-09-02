package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	cyan   = "\x1b[36m"
	green  = "\x1b[32m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
)

type Runner interface {
	Run(context.Context, string) error
}

type verboseRunner interface {
	SetVerbose(bool)
}

type unicodeRunner interface {
	SetUnicode(bool)
}

type readWriter struct {
	io.Reader
	io.Writer
}

type UI struct {
	terminal       *term.Terminal
	display        *historyWriter
	responseWriter *MarkdownWriter
	commandMenu    slashCommandMenu
	input          *interruptReader
	in             *os.File
	out            *os.File
	runner         Runner
	provider       string
	model          string
	root           string
	verbose        bool
	width          int
	height         int
	unicode        bool
	pageMu         sync.Mutex
	pageOffset     int
	pageActive     bool
}

func New(in, out *os.File, runner Runner, provider, model, root string) *UI {
	input := newInterruptReader(in)
	rw := readWriter{Reader: input, Writer: out}
	t := term.NewTerminal(rw, cyan+bold+"> "+reset)
	width, height := terminalSize(out)
	unicodeEnabled := UnicodeEnabled()
	t.SetSize(width, height)
	display := newHistoryWriter(t)
	responseWriter := NewMarkdownWriter(display, ColorEnabled(out), width)
	responseWriter.SetUnicode(unicodeEnabled)
	responseWriter.EnableDiffs()
	u := &UI{
		terminal:       t,
		display:        display,
		responseWriter: responseWriter,
		commandMenu:    slashCommandMenu{out: t, color: ColorEnabled(out), width: width},
		input:          input,
		in:             in,
		out:            out,
		runner:         runner,
		provider:       provider,
		model:          model,
		root:           root,
		width:          width,
		height:         height,
		unicode:        unicodeEnabled,
	}
	t.AutoCompleteCallback = u.completeSlashCommand
	input.setPageHandler(u.showPage)
	u.SetRunner(runner)
	return u
}

func (u *UI) Writer() io.Writer { return u.display }

func (u *UI) ResponseWriter() io.Writer { return u.responseWriter }

func (u *UI) SetRunner(runner Runner) {
	u.runner = runner
	if configurable, ok := runner.(verboseRunner); ok {
		configurable.SetVerbose(u.verbose)
	}
	if configurable, ok := runner.(unicodeRunner); ok {
		configurable.SetUnicode(u.unicode)
	}
}

func (u *UI) Run(ctx context.Context) error {
	if u.runner == nil {
		return fmt.Errorf("terminal UI has no agent runner")
	}
	if !term.IsTerminal(int(u.in.Fd())) || !term.IsTerminal(int(u.out.Fd())) {
		return fmt.Errorf("interactive mode requires a terminal; pass a prompt argument for one-shot mode")
	}
	state, err := term.MakeRaw(int(u.in.Fd()))
	if err != nil {
		return fmt.Errorf("enable terminal mode: %w", err)
	}
	defer term.Restore(int(u.in.Fd()), state)
	u.input.start()

	u.printHeader()
	for {
		line, err := u.terminal.ReadLine()
		u.commandMenu.dismiss(u.out)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u.display.AddLine("> " + line)
		u.resetPage()
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "/diff" {
			u.expandDiff(fields)
			continue
		}
		switch line {
		case "/quit", "/exit":
			return nil
		case "/clear":
			fmt.Fprint(u.terminal, "\x1b[2J\x1b[H")
			u.display.Clear()
			u.responseWriter.ResetDiffs()
			u.printHeader()
			continue
		case "/help":
			u.printCommandHelp()
			continue
		case "/verbose":
			u.verbose = !u.verbose
			if configurable, ok := u.runner.(verboseRunner); ok {
				configurable.SetVerbose(u.verbose)
			}
			state := "off"
			if u.verbose {
				state = "on"
			}
			u.printSystemMessage(fmt.Sprintf("%sVerbose tracing: %s%s", dim, state, reset))
			continue
		}
		u.responseWriter.ResetDiffs()
		fmt.Fprintln(u.display, green+bold+"assistant"+reset)
		started := time.Now()
		taskCtx, cancel := context.WithCancel(ctx)
		u.input.setCancel(cancel)
		err = u.runner.Run(taskCtx, line)
		u.input.setCancel(nil)
		cancel()
		if errors.Is(err, context.Canceled) {
			u.printSystemMessage(yellow + "Cancelled" + reset)
		} else if err != nil {
			u.printSystemMessage(yellow + "error: " + err.Error() + reset)
		} else {
			u.printSystemMessage(fmt.Sprintf("%s%sCompleted in %s%s", magenta, bold, formatRunDuration(time.Since(started)), reset))
		}
	}
}

func (u *UI) expandDiff(fields []string) {
	if len(fields) > 2 {
		u.printSystemMessage(yellow + "Usage: /diff [number]" + reset)
		return
	}
	number := 0
	if len(fields) == 2 {
		parsed, err := strconv.Atoi(fields[1])
		if err != nil || parsed < 1 {
			u.printSystemMessage(yellow + "Usage: /diff [number]" + reset)
			return
		}
		number = parsed
	}
	requested, total, ok := u.responseWriter.WriteStoredDiff(number)
	if ok {
		return
	}
	if total == 0 {
		u.printSystemMessage(dim + "No diffs are available from the latest run." + reset)
		return
	}
	u.printSystemMessage(fmt.Sprintf("%sDiff %d not found; available diffs: 1-%d.%s", yellow, requested, total, reset))
}

// printSystemMessage separates status and command feedback from surrounding
// conversation so it remains easy to scan in both the terminal and history.
func (u *UI) printSystemMessage(message string) {
	fmt.Fprintf(u.display, "\n%s\n\n", message)
}

func formatRunDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return "<1ms"
	}
	return duration.Round(time.Millisecond).String()
}

func (u *UI) completeSlashCommand(line string, pos int, key rune) (string, int, bool) {
	if key == '\t' {
		matches := matchingSlashCommands(line)
		if len(matches) == 0 {
			u.commandMenu.update(nil)
			return line, pos, false
		}
		completed := matches[0].name
		u.commandMenu.update(matchingSlashCommands(completed))
		return completed, len(completed), true
	}
	if key < 32 || pos < 0 || pos > len(line) {
		return line, pos, false
	}
	inserted := string(key)
	newLine := line[:pos] + inserted + line[pos:]
	u.commandMenu.update(matchingSlashCommands(newLine))
	return newLine, pos + len(inserted), true
}

func (u *UI) printCommandHelp() {
	for _, command := range slashCommands {
		line := fmt.Sprintf("%s%-8s%s %s%s%s", cyan, command.name, reset, dim, command.description, reset)
		fmt.Fprintln(u.display, wrapANSI(line, u.width, "         "))
	}
}

func (u *UI) printHeader() {
	root := u.root
	if home, err := os.UserHomeDir(); err == nil {
		if rel, relErr := filepath.Rel(home, root); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root = filepath.Join("~", rel)
		}
	}
	fmt.Fprintf(u.display, "\r\n%sqcode%s  %s%s%s\r\n", bold+cyan, reset, dim, u.provider+" / "+u.model, reset)
	separator := "·"
	if !u.unicode {
		separator = "-"
	}
	fmt.Fprintf(u.display, "%s%s  %s  Waiting indicator; /verbose for action traces%s\r\n\r\n", dim, root, separator, reset)
}

func (u *UI) resetPage() {
	u.pageMu.Lock()
	active := u.pageActive
	u.pageOffset = 0
	u.pageActive = false
	u.pageMu.Unlock()
	if !active {
		return
	}

	lines := u.visualHistoryLines()
	page, _ := historyPage(lines, u.height-1, 0, 0)
	var output strings.Builder
	output.WriteString("\x1b[2J\x1b[H")
	output.WriteString(strings.Join(page, "\n"))
	if len(page) > 0 {
		output.WriteByte('\n')
	}
	_, _ = u.terminal.Write([]byte(output.String()))
}

func (u *UI) showPage(direction int) {
	u.pageMu.Lock()
	lines := u.visualHistoryLines()
	pageSize := u.height - 2
	page, offset := historyPage(lines, pageSize, u.pageOffset, direction)
	u.pageOffset = offset
	u.pageActive = true
	u.pageMu.Unlock()

	u.commandMenu.reset()
	start := len(lines) - offset - len(page) + 1
	end := len(lines) - offset
	if len(page) == 0 {
		start, end = 0, 0
	}
	var output strings.Builder
	output.WriteString("\x1b[2J\x1b[H")
	output.WriteString(strings.Join(page, "\n"))
	if len(page) > 0 {
		output.WriteByte('\n')
	}
	separator := "·"
	if !u.unicode {
		separator = "-"
	}
	fmt.Fprintf(&output, "%s[%d-%d of %d %s PgUp/PgDn]%s\n", dim, start, end, len(lines), separator, reset)
	_, _ = u.terminal.Write([]byte(output.String()))
}

func (u *UI) visualHistoryLines() []string {
	logical := u.display.Lines()
	visual := make([]string, 0, len(logical))
	for _, line := range logical {
		visual = append(visual, strings.Split(wrapANSI(line, u.width, ""), "\n")...)
	}
	return visual
}

func terminalSize(out *os.File) (int, int) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}

func ColorEnabled(out *os.File) bool {
	return os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(out.Fd()))
}

func OutputWidth(out *os.File) int {
	if !term.IsTerminal(int(out.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}
