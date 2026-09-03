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
	"qcode/internal/config"
	"qcode/internal/demo"
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
	demo          bool
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
	flags.StringVar(&opts.provider, "provider", env("QCODE_PROVIDER", "ollama"), "LLM provider: ollama, openai, openai-like, or opencode-go")
	flags.StringVar(&opts.model, "model", env("QCODE_MODEL", "qwen2.5-coder:7b"), "model identifier")
	flags.StringVar(&opts.baseURL, "base-url", os.Getenv("QCODE_BASE_URL"), "provider API base URL")
	flags.StringVar(&opts.apiKey, "api-key", firstEnv("QCODE_API_KEY", "OPENAI_API_KEY"), "API key (prefer QCODE_API_KEY or OPENAI_API_KEY)")
	flags.StringVar(&opts.root, "cwd", ".", "workspace root")
	flags.IntVar(&opts.maxSteps, "max-steps", 32, "maximum model turns per request")
	flags.BoolVar(&opts.jsonEvents, "json-events", false, "emit action events as JSON Lines")
	flags.BoolVar(&opts.listProviders, "list-providers", false, "list built-in providers")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.BoolVar(&opts.demo, "demo", false, "run without an LLM or real tool execution")
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
	if !opts.demo {
		cfg, _, err := config.Load()
		if err != nil {
			return err
		}
		setFlags := make(map[string]bool)
		flags.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
		applyConfig(&opts, cfg, setFlags)
	}
	if !opts.demo && opts.model == "" {
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
	var provider llm.Provider
	var toolset agent.Toolset = registry
	if opts.demo {
		session := demo.New(registry.Schemas())
		provider = session.Provider
		toolset = session.Tools
		opts.provider = provider.Name()
		opts.model = demo.Model
	} else {
		provider, err = llm.New(opts.provider, llm.Config{BaseURL: opts.baseURL, APIKey: opts.apiKey})
		if err != nil {
			return err
		}
	}
	promptText := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if promptText != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		logger := newTraceLogger(stderr, opts.jsonEvents)
		responseWriter := newResponseWriter(stdout)
		runner := agent.New(provider, opts.model, toolset, logger, responseWriter, opts.maxSteps)
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
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		logger := newTraceLogger(stderr, opts.jsonEvents)
		responseWriter := newResponseWriter(stdout)
		runner := agent.New(provider, opts.model, toolset, logger, responseWriter, opts.maxSteps)
		return runner.Run(ctx, promptText)
	}

	// Terminal output must go through term.Terminal so asynchronous-looking stream
	// updates do not corrupt the editable input line.
	ui := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
	logger := trace.NewAnimated(ui.Writer(), opts.jsonEvents)
	runner := agent.New(provider, opts.model, toolset, logger, ui.ResponseWriter(), opts.maxSteps)
	ui.SetRunner(runner)
	return ui.Run(context.Background())
}

func applyConfig(opts *options, cfg config.Config, setFlags map[string]bool) {
	if !setFlags["provider"] && os.Getenv("QCODE_PROVIDER") == "" && cfg.Provider != "" {
		opts.provider = cfg.Provider
	}
	// A model, base URL, and API key describe the configured provider. Do not
	// carry them over when a higher-precedence source selects another provider.
	// They can still be supplied explicitly through their own flag or env var.
	providerConfigApplies := cfg.Provider == "" || opts.provider == cfg.Provider
	if providerConfigApplies && !setFlags["model"] && os.Getenv("QCODE_MODEL") == "" && cfg.Model != "" {
		opts.model = cfg.Model
	}
	if providerConfigApplies && !setFlags["base-url"] && os.Getenv("QCODE_BASE_URL") == "" && cfg.BaseURL != "" {
		opts.baseURL = cfg.BaseURL
	}
	if providerConfigApplies && !setFlags["api-key"] && firstEnv("QCODE_API_KEY", "OPENAI_API_KEY") == "" && cfg.APIKey != "" {
		opts.apiKey = cfg.APIKey
	}
	if !setFlags["max-steps"] && cfg.MaxSteps != nil {
		opts.maxSteps = *cfg.MaxSteps
	}
}

func newTraceLogger(out *os.File, jsonOutput bool) *trace.Logger {
	var logger *trace.Logger
	if term.IsTerminal(int(out.Fd())) {
		logger = trace.NewAnimated(out, jsonOutput)
	} else {
		logger = trace.New(out, jsonOutput)
	}
	logger.SetUnicode(tui.UnicodeEnabled())
	return logger
}

func newResponseWriter(out *os.File) *tui.MarkdownWriter {
	writer := tui.NewMarkdownWriter(out, tui.ColorEnabled(out), tui.OutputWidth(out))
	writer.SetUnicode(tui.UnicodeEnabled())
	return writer
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
