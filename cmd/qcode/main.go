package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/tools"
	"qcode/internal/trace"
	"qcode/internal/tui"
)

var version = "dev"

type options struct {
	provider      string
	model         string
	baseURL       string
	apiKey        string
	root          string
	maxSteps      int
	jsonEvents    bool
	listProviders bool
	showVersion   bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qcode:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdin *os.File, stdout, stderr *os.File) error {
	var opts options
	flags := flag.NewFlagSet("qcode", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.provider, "provider", env("QCODE_PROVIDER", "ollama"), "LLM provider: ollama, openai, or openai-like")
	flags.StringVar(&opts.model, "model", env("QCODE_MODEL", "qwen2.5-coder:7b"), "model identifier")
	flags.StringVar(&opts.baseURL, "base-url", os.Getenv("QCODE_BASE_URL"), "provider API base URL")
	flags.StringVar(&opts.apiKey, "api-key", firstEnv("QCODE_API_KEY", "OPENAI_API_KEY"), "API key (prefer QCODE_API_KEY or OPENAI_API_KEY)")
	flags.StringVar(&opts.root, "cwd", ".", "workspace root")
	flags.IntVar(&opts.maxSteps, "max-steps", 32, "maximum model turns per request")
	flags.BoolVar(&opts.jsonEvents, "json-events", false, "emit action events as JSON Lines")
	flags.BoolVar(&opts.listProviders, "list-providers", false, "list built-in providers")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: qcode [options] [prompt]\n\nWith no prompt, qcode starts its terminal UI.\n\nOptions:\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}
	if opts.listProviders {
		fmt.Fprintln(stdout, strings.Join(llm.Names(), "\n"))
		return nil
	}
	if opts.model == "" {
		return errors.New("model must not be empty")
	}

	root, err := filepath.Abs(opts.root)
	if err != nil {
		return err
	}
	registry, err := tools.New(root)
	if err != nil {
		return err
	}
	provider, err := llm.New(opts.provider, llm.Config{BaseURL: opts.baseURL, APIKey: opts.apiKey})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	promptText := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if promptText != "" {
		logger := newTraceLogger(stderr, opts.jsonEvents)
		responseWriter := tui.NewMarkdownWriter(stdout, tui.ColorEnabled(stdout))
		runner := agent.New(provider, opts.model, registry, logger, responseWriter, opts.maxSteps)
		return runner.Run(ctx, promptText)
	}
	if stat, statErr := stdin.Stat(); statErr == nil && stat.Mode()&os.ModeCharDevice == 0 {
		data, readErr := io.ReadAll(stdin)
		if readErr != nil {
			return readErr
		}
		promptText = strings.TrimSpace(string(data))
		if promptText == "" {
			return errors.New("stdin contained no prompt")
		}
		logger := newTraceLogger(stderr, opts.jsonEvents)
		responseWriter := tui.NewMarkdownWriter(stdout, tui.ColorEnabled(stdout))
		runner := agent.New(provider, opts.model, registry, logger, responseWriter, opts.maxSteps)
		return runner.Run(ctx, promptText)
	}

	// Terminal output must go through term.Terminal so asynchronous-looking stream
	// updates do not corrupt the editable input line.
	ui := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
	logger := trace.NewAnimated(ui.Writer(), opts.jsonEvents)
	runner := agent.New(provider, opts.model, registry, logger, ui.ResponseWriter(), opts.maxSteps)
	ui.SetRunner(runner)
	return ui.Run(ctx)
}

func newTraceLogger(out *os.File, jsonOutput bool) *trace.Logger {
	if term.IsTerminal(int(out.Fd())) {
		return trace.NewAnimated(out, jsonOutput)
	}
	return trace.New(out, jsonOutput)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
