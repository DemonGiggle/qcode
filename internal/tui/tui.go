package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	cyan   = "\x1b[36m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
)

type Runner interface {
	Run(context.Context, string) error
}

type verboseRunner interface {
	SetVerbose(bool)
}

type readWriter struct {
	io.Reader
	io.Writer
}

type UI struct {
	terminal       *term.Terminal
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
}

func New(in, out *os.File, runner Runner, provider, model, root string) *UI {
	input := newInterruptReader(in)
	rw := readWriter{Reader: input, Writer: out}
	t := term.NewTerminal(rw, cyan+bold+"> "+reset)
	width, height := terminalSize(out)
	t.SetSize(width, height)
	u := &UI{
		terminal:       t,
		responseWriter: NewMarkdownWriter(t, ColorEnabled(out), width),
		commandMenu:    slashCommandMenu{out: t, color: ColorEnabled(out), width: width},
		input:          input,
		in:             in,
		out:            out,
		runner:         runner,
		provider:       provider,
		model:          model,
		root:           root,
		width:          width,
	}
	t.AutoCompleteCallback = u.completeSlashCommand
	u.SetRunner(runner)
	return u
}

func (u *UI) Writer() io.Writer { return u.terminal }

func (u *UI) ResponseWriter() io.Writer { return u.responseWriter }

func (u *UI) SetRunner(runner Runner) {
	u.runner = runner
	if configurable, ok := runner.(verboseRunner); ok {
		configurable.SetVerbose(u.verbose)
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
		switch line {
		case "/quit", "/exit":
			return nil
		case "/clear":
			fmt.Fprint(u.terminal, "\x1b[2J\x1b[H")
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
			fmt.Fprintf(u.terminal, "%sVerbose tracing: %s%s\n", dim, state, reset)
			continue
		}
		fmt.Fprintln(u.terminal, green+bold+"assistant"+reset)
		taskCtx, cancel := context.WithCancel(ctx)
		u.input.setCancel(cancel)
		err = u.runner.Run(taskCtx, line)
		u.input.setCancel(nil)
		cancel()
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(u.terminal, yellow+"Cancelled"+reset)
		} else if err != nil {
			fmt.Fprintln(u.terminal, yellow+"error: "+err.Error()+reset)
		}
		fmt.Fprintln(u.terminal)
	}
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
		fmt.Fprintln(u.terminal, wrapANSI(line, u.width, "         "))
	}
}

func (u *UI) printHeader() {
	root := u.root
	if home, err := os.UserHomeDir(); err == nil {
		if rel, relErr := filepath.Rel(home, root); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root = filepath.Join("~", rel)
		}
	}
	fmt.Fprintf(u.terminal, "\r\n%sqcode%s  %s%s%s\r\n", bold+cyan, reset, dim, u.provider+" / "+u.model, reset)
	fmt.Fprintf(u.terminal, "%s%s  ·  Waiting indicator; /verbose for action traces%s\r\n\r\n", dim, root, reset)
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
