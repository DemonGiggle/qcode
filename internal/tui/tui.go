package tui

import (
	"context"
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

type readWriter struct {
	io.Reader
	io.Writer
}

type UI struct {
	terminal *term.Terminal
	in       *os.File
	out      *os.File
	runner   Runner
	provider string
	model    string
	root     string
}

func New(in, out *os.File, runner Runner, provider, model, root string) *UI {
	rw := readWriter{Reader: in, Writer: out}
	t := term.NewTerminal(rw, cyan+bold+"> "+reset)
	t.SetSize(terminalSize(out))
	return &UI{terminal: t, in: in, out: out, runner: runner, provider: provider, model: model, root: root}
}

func (u *UI) Writer() io.Writer { return u.terminal }

func (u *UI) SetRunner(runner Runner) { u.runner = runner }

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

	u.printHeader()
	for {
		line, err := u.terminal.ReadLine()
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
			fmt.Fprintln(u.terminal, dim+"/help  /clear  /quit"+reset)
			continue
		}
		fmt.Fprintln(u.terminal, green+bold+"assistant"+reset)
		if err := u.runner.Run(ctx, line); err != nil {
			fmt.Fprintln(u.terminal, yellow+"error: "+err.Error()+reset)
		}
		fmt.Fprintln(u.terminal)
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
	fmt.Fprintf(u.terminal, "%s%s  ·  actions include timestamps and durations%s\r\n\r\n", dim, root, reset)
}

func terminalSize(out *os.File) (int, int) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}
