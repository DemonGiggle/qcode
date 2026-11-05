// Command qcode-cachebench is an opt-in synthetic prompt-cache benchmark.
//
// Harness only: it replays committed synthetic workloads through
// internal/llm providers and records normalized usage plus monotonic
// latencies as JSONL. It performs no live measurements by default and
// never executes real tools.
//
// Usage:
//
//	qcode-cachebench --dry-run
//	qcode-cachebench --live --provider openai --model <id> --variant baseline \
//	  --repetitions 5 --out bench.jsonl
//	qcode-cachebench --compare before.jsonl after.jsonl
//
// Credentials come from qcode config files and environment variables
// (QCODE_BASE_URL, QCODE_API_KEY, OPENAI_API_KEY). There is intentionally
// no --api-key flag. Network calls require --live; --dry-run validates
// setup without contacting any provider.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/redaction"
)

var buildRevision = "dev"

const schemaVersion = 1

type options struct {
	provider  string
	model     string
	baseURL   string
	variant   string
	reps      int
	out       string
	live      bool
	dryRun    bool
	compareA  string
	compareB  string
	gitRev    string
	scenarios string
	showHelp  bool
	showVer   bool
	timeout   time.Duration
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qcode-cachebench:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	policy, _ := redaction.New(redaction.Config{}, redaction.EnvironmentValues())
	diag := &redaction.DiagnosticWriter{Policy: policy, Sink: redaction.Terminal, Out: stderr}

	opts := options{reps: 5, timeout: 120 * time.Second}
	fs := flag.NewFlagSet("qcode-cachebench", flag.ContinueOnError)
	fs.SetOutput(diag)
	fs.StringVar(&opts.provider, "provider", envOr("QCODE_PROVIDER", "ollama"), "LLM provider: ollama, openai, opencode-go, or chatgpt")
	fs.StringVar(&opts.model, "model", os.Getenv("QCODE_MODEL"), "model identifier")
	fs.StringVar(&opts.baseURL, "base-url", os.Getenv("QCODE_BASE_URL"), "provider API base URL override")
	fs.StringVar(&opts.variant, "variant", "baseline", "variant label recorded on every sample")
	fs.IntVar(&opts.reps, "repetitions", 5, "repetitions per scenario (each uses a fresh routing/cache identity)")
	fs.StringVar(&opts.out, "out", "", "output JSONL path for --live runs")
	fs.BoolVar(&opts.live, "live", false, "opt in to real network completion calls")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "validate setup and scenarios without any network calls")
	fs.StringVar(&opts.gitRev, "git-revision", firstEnvOr("QCODE_GIT_REVISION", buildRevision), "git revision recorded in output (build metadata)")
	fs.StringVar(&opts.scenarios, "scenarios", "", "comma-separated scenario subset (default: all)")
	fs.DurationVar(&opts.timeout, "timeout", 120*time.Second, "per-request timeout for --live runs")
	fs.BoolVar(&opts.showVer, "version", false, "print version")
	fs.Usage = func() {
		fmt.Fprintln(diag, "Usage:")
		fmt.Fprintln(diag, "  qcode-cachebench --dry-run")
		fmt.Fprintln(diag, "  qcode-cachebench --live --provider <p> --model <m> --variant <label> --out <path>")
		fmt.Fprintln(diag, "  qcode-cachebench --compare <before.jsonl> <after.jsonl>")
		fmt.Fprintln(diag, "")
		fmt.Fprintln(diag, "Options:")
		fs.PrintDefaults()
	}
	var compareArgs []string
	rest := args
	if idx := indexFlag(args, "--compare"); idx >= 0 {
		// --compare takes two positional file arguments.
		filtered := append([]string(nil), args[:idx]...)
		compareArgs = args[idx+1:]
		rest = filtered
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if opts.showVer {
		fmt.Fprintln(stdout, buildRevision)
		return nil
	}
	restArgs := fs.Args()
	if len(compareArgs) == 0 && len(restArgs) >= 2 && restArgs[0] == "compare" {
		compareArgs = restArgs[1:]
	} else if len(compareArgs) == 0 && fs.NArg() > 0 && strings.HasSuffix(fs.Arg(0), ".jsonl") {
		// Allow `--compare a b` parsed as positionals after filtering.
	}
	if len(compareArgs) > 0 {
		if len(compareArgs) != 2 {
			return errors.New("--compare requires exactly two JSONL files: --compare <before.jsonl> <after.jsonl>")
		}
		return runCompare(compareArgs[0], compareArgs[1], stdout, stderr)
	}

	if opts.live && opts.dryRun {
		return errors.New("choose one of --live or --dry-run")
	}
	if !opts.live && !opts.dryRun {
		return errors.New("refusing network activity: pass --live to run, --dry-run to validate, or --compare to compare outputs")
	}

	scens, err := selectScenarios(opts.scenarios)
	if err != nil {
		return err
	}
	if strings.TrimSpace(opts.variant) == "" {
		return errors.New("variant must not be empty")
	}
	if opts.reps <= 0 || opts.reps > 100 {
		return errors.New("repetitions must be between 1 and 100")
	}

	// Reuse existing credential/config mechanisms. No API-key flag exists by
	// design; keys come from config files and environment only.
	cfg, _, diagnostics, cfgErr := config.Load()
	for _, d := range diagnostics {
		fmt.Fprintln(diag, "config:", d)
	}
	if cfgErr != nil {
		return cfgErr
	}
	policy.RegisterSecrets(cfg.Secrets()...)
	policy.RegisterSecrets(redaction.EnvironmentValues()...)
	diag.Policy = policy

	baseURL, apiKey := cfg.ProviderConnection(opts.provider)
	if opts.baseURL != "" {
		baseURL = opts.baseURL
	} else if env := os.Getenv("QCODE_BASE_URL"); env != "" && opts.provider == launchProvider(cfg) {
		// Mirror qcode's launch-provider env precedence only for the active
		// provider; explicit --base-url always wins.
		if baseURL == "" {
			baseURL = env
		}
	}
	_ = apiKey // Held for provider construction on live runs only.

	if opts.dryRun {
		return runDryRun(scens, opts, stdout)
	}
	return runLive(context.Background(), cfg, scens, opts, baseURL, apiKey, policy, stdout, stderr)
}

func launchProvider(cfg config.Config) string {
	if cfg.Provider != "" {
		return cfg.Provider
	}
	if v := os.Getenv("QCODE_PROVIDER"); v != "" {
		return v
	}
	return "ollama"
}

func runDryRun(scens []scenario, opts options, stdout io.Writer) error {
	totalTurns := 0
	for _, s := range scens {
		totalTurns += len(s.turns(opts.model)) * opts.reps
	}
	fmt.Fprintf(stdout, "cachebench dry-run: %d scenario(s), %d repetition(s), %d request(s) planned, 0 requests sent\n", len(scens), opts.reps, totalTurns)
	for _, s := range scens {
		built := s.turns(opts.model)
		fmt.Fprintf(stdout, "  - %s: %d turns/rep x %d reps (prefix_chars=%d tools=%d)\n",
			s.name, len(built), opts.reps, len(syntheticPrefix), len(syntheticTools("v1")))
	}
	fmt.Fprintln(stdout, "provider/model/protocol resolution and credential presence validated without network calls")
	return nil
}

func runLive(ctx context.Context, cfg config.Config, scens []scenario, opts options, baseURL, apiKey string, policy *redaction.Policy, stdout, stderr io.Writer) error {
	if strings.TrimSpace(opts.model) == "" {
		return errors.New("model must not be empty for --live runs")
	}
	if strings.TrimSpace(opts.out) == "" {
		return errors.New("--out is required for --live runs")
	}
	newProvider := func() (llm.Provider, error) {
		pol := policy
		return llm.New(opts.provider, llm.Config{
			BaseURL:            baseURL,
			APIKey:             apiKey,
			ChatGPTOAuthPath:   cfg.ChatGPT.OAuthPath,
			RegisterSecrets:    pol.RegisterSecrets,
			InsecureSkipVerify: false,
		})
	}
	// Validate provider construction once before opening output.
	probe, err := newProvider()
	if err != nil {
		return err
	}
	_ = probe
	out, err := os.Create(opts.out)
	if err != nil {
		return err
	}
	defer out.Close()

	runner := &benchRunner{
		scenarios: scens,
		opts:      opts,
		protocol:  protocolFor(opts.provider, opts.model),
		newProvider: func() (llm.Provider, error) {
			return newProvider()
		},
		now: time.Now,
	}
	n, werr := runner.run(ctx, out)
	fmt.Fprintf(stderr, "cachebench: wrote %d samples to %s\n", n, opts.out)
	_ = stdout
	return werr
}

func indexFlag(args []string, name string) int {
	for i, a := range args {
		if a == name {
			return i
		}
	}
	return -1
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func firstEnvOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
