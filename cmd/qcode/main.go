package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"qcode/internal/agent"
	"qcode/internal/config"
	"qcode/internal/demo"
	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/session"
	"qcode/internal/skills"
	"qcode/internal/tools"
	"qcode/internal/trace"
	"qcode/internal/tui"
)

var version = "dev"

type options struct {
	learningBudget       int
	searchBackend        string
	provider             string
	model                string
	baseURL              string
	apiKey               string
	root                 string
	contextWindow        int
	autoCompactThreshold int
	disableAutoCompact   bool
	maxSteps             int
	agentTimeout         time.Duration
	jsonEvents           bool
	listProviders        bool
	showVersion          bool
	demo                 bool
	sandbox              bool
	dangerSkipTLSVerify  bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qcode:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdin *os.File, stdout, stderr *os.File) error {
	opts := options{learningBudget: learning.DefaultBudget, autoCompactThreshold: agent.DefaultAutoCompactThreshold}
	configPath := ""
	var configuredSkillPaths []string
	flags := flag.NewFlagSet("qcode", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.provider, "provider", env("QCODE_PROVIDER", "ollama"), "LLM provider: ollama, openai, or opencode-go")
	flags.StringVar(&opts.model, "model", env("QCODE_MODEL", "qwen2.5-coder:7b"), "model identifier")
	flags.StringVar(&opts.baseURL, "base-url", os.Getenv("QCODE_BASE_URL"), "provider API base URL")
	flags.StringVar(&opts.apiKey, "api-key", firstEnv("QCODE_API_KEY", "OPENAI_API_KEY"), "API key (prefer QCODE_API_KEY or OPENAI_API_KEY)")
	flags.StringVar(&opts.root, "cwd", ".", "workspace root")
	flags.IntVar(&opts.contextWindow, "context-window", 0, "model context capacity in tokens for status display (0: automatic)")
	flags.BoolVar(&opts.disableAutoCompact, "disable-auto-compact", false, "disable automatic conversation compaction")
	flags.IntVar(&opts.maxSteps, "max-steps", 32, "maximum model turns per request")
	flags.DurationVar(&opts.agentTimeout, "agent-timeout", agent.DefaultConsultationTimeout, "time allowed for agent consultations, including queue time")
	flags.BoolVar(&opts.jsonEvents, "json-events", false, "emit action events as JSON Lines")
	flags.BoolVar(&opts.listProviders, "list-providers", false, "list built-in providers")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.BoolVar(&opts.demo, "demo", false, "run without an LLM or real tool execution")
	flags.BoolVar(&opts.sandbox, "sandbox", false, "isolate tools with bubblewrap (Linux only)")
	flags.BoolVar(&opts.dangerSkipTLSVerify, "danger-skip-tls-verify", false, "skip TLS certificate verification (insecure)")
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
	if opts.contextWindow < 0 {
		return fmt.Errorf("context-window must be non-negative")
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
		cfg, loadedPath, err := config.Load()
		if err != nil {
			return err
		}
		configPath = loadedPath
		configuredSkillPaths = append([]string(nil), cfg.Skills.Paths...)
		setFlags := make(map[string]bool)
		flags.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
		applyConfig(&opts, cfg, setFlags)
	}
	if !opts.demo && opts.model == "" {
		return errors.New("model must not be empty")
	}
	if opts.agentTimeout <= 0 {
		return errors.New("agent-timeout must be a positive duration")
	}

	root, err := filepath.Abs(opts.root)
	if err != nil {
		return err
	}
	promptText := strings.TrimSpace(strings.Join(flags.Args(), " "))
	stdinPiped := false
	if stat, statErr := stdin.Stat(); statErr == nil {
		stdinPiped = stat.Mode()&os.ModeCharDevice == 0
	}
	interactive := promptText == "" && !stdinPiped
	sandboxActive := false
	sandboxPath := ""
	sandboxNotice := ""
	sandboxChoice := false
	if opts.sandbox && !opts.demo {
		if tools.WorkspaceExposesHome(root) {
			sandboxNotice = "Sandbox disabled: the selected workspace contains your home directory and would expose its secrets."
			sandboxChoice = interactive
		} else if checkedPath, checkErr := tools.CheckSandbox(root, false); checkErr != nil {
			sandboxNotice = "Sandbox unavailable: " + checkErr.Error()
			sandboxChoice = interactive
		} else {
			sandboxActive = true
			sandboxPath = checkedPath
			sandboxNotice = "Sandbox enabled: only approved folders can be changed; home is hidden; network is blocked until a web tool is enabled."
		}
		if !sandboxActive && !interactive {
			fmt.Fprintln(stderr, "WARNING:", sandboxNotice, "Continuing without sandbox.")
		}
	}
	protectedPaths := []string(nil)
	if configPath != "" {
		protectedPaths = append(protectedPaths, configPath)
	}
	skillLocations := skills.Locations(root, configuredSkillPaths...)
	loadSkills := func() ([]prompt.SkillSummary, error) {
		return skillCatalogData(root, configuredSkillPaths...)
	}
	skillSelection := skills.NewLazySelection(root, configuredSkillPaths...)
	registry, err := tools.NewWithOptions(root, tools.Options{Sandbox: sandboxActive, BubblewrapPath: sandboxPath, ProtectedPaths: protectedPaths, Skills: skillSelection, SearchBackend: opts.searchBackend, InsecureSkipTLSVerify: opts.dangerSkipTLSVerify})
	if err != nil {
		return err
	}
	providerConfig := llm.Config{BaseURL: opts.baseURL, APIKey: opts.apiKey, InsecureSkipVerify: opts.dangerSkipTLSVerify}
	var provider llm.Provider
	var toolset agent.Toolset = registry
	if opts.demo {
		session := demo.New(registry.Schemas())
		provider = session.Provider
		toolset = session.Tools
		opts.provider = provider.Name()
		opts.model = demo.Model
	} else {
		provider, err = llm.New(opts.provider, providerConfig)
		if err != nil {
			return err
		}
	}
	var learningStore learning.Store
	if !opts.demo {
		dir, err := learning.DefaultDirectory()
		if err != nil {
			fmt.Fprintln(stderr, "Learning unavailable:", err)
		} else {
			learningStore = learning.New(dir, func(message string) { fmt.Fprintln(stderr, "Learning warning:", message) })
		}
	}
	system := prompt.System
	if promptText == "" && stdinPiped {
		data, readErr := io.ReadAll(stdin)
		if readErr != nil {
			return readErr
		}
		promptText = strings.TrimSpace(string(data))
		if promptText == "" {
			return errors.New("stdin contained no prompt")
		}
	}
	if promptText != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return runOneShot(ctx, promptText, provider, opts.model, toolset, stderr, stdout, opts.jsonEvents, opts.maxSteps, system, learningStore, opts.learningBudget)
	}

	// Terminal output must go through term.Terminal so asynchronous-looking stream
	// updates do not corrupt the editable input line.
	ui := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
	ui.SetSkillCatalogLoader(loadSkills)
	ui.SetSkillLocations(skillLocations)
	if opts.demo {
		ui.SetDemoPromptScript(demo.InteractivePrompts(), demo.InteractivePromptDelay, demo.InteractiveQueueDelay)
	}
	if sandboxNotice != "" {
		ui.SetStartupNotice(sandboxNotice, sandboxChoice)
	}
	manager := agent.NewAgentManager(context.Background(), agent.DefaultMaxAgents)
	ui.SetAgentManager(manager)
	mainRegistry := registry
	mainProvider := provider
	mainToolset := toolset
	mainSelection := skillSelection
	configureFactory := func(ui *tui.UI, manager *agent.AgentManager, saved map[string]agent.SavedState) {
		_ = manager.SetConsultationTimeout(opts.agentTimeout)
		manager.SetFactory(func(id, name, model string, isMain bool) (*agent.Agent, error) {
			currentProvider := mainProvider
			currentToolset := mainToolset
			currentRegistry := mainRegistry
			currentSelection := mainSelection
			currentEndpoint := providerConfig.BaseURL
			if !isMain || saved != nil {
				currentSelection = skills.NewLazySelection(root, configuredSkillPaths...)
				createdRegistry, createErr := tools.NewWithOptions(root, tools.Options{Sandbox: sandboxActive, BubblewrapPath: sandboxPath, ProtectedPaths: protectedPaths, Skills: currentSelection, SearchBackend: opts.searchBackend, InsecureSkipTLSVerify: opts.dangerSkipTLSVerify})
				if createErr != nil {
					return nil, createErr
				}
				currentRegistry = createdRegistry
				if opts.demo {
					session := demo.New(createdRegistry.Schemas())
					currentProvider = session.Provider
					currentToolset = session.Tools
				} else {
					providerName, connection := opts.provider, providerConfig
					state, ok := saved[id]
					if !ok {
						state, ok = saved["main"]
					}
					if ok {
						providerName = state.Provider
						connection.BaseURL = state.Endpoint
					}
					currentEndpoint = connection.BaseURL
					createdProvider, providerErr := llm.New(providerName, connection)
					if providerErr != nil {
						return nil, providerErr
					}
					currentProvider = createdProvider
					currentToolset = createdRegistry
				}
			}
			display, response := ui.AddAgentView(id, currentProvider.Name(), model)
			wrappedTools := manager.WrapToolset(id, currentToolset, isMain)
			logger := trace.NewAnimated(display, opts.jsonEvents)
			logger.SetColor(tui.ColorEnabled(stdout))
			logger.SetWidth(tui.OutputWidth(stdout))
			runner := agent.NewWithSystem(currentProvider, model, wrappedTools, logger, response, opts.maxSteps, system)
			runner.SetTaskIndicator(false)
			runner.SetLearning(learningStore, opts.learningBudget)
			if model == opts.model {
				runner.SetContextWindow(opts.contextWindow)
			}
			runner.SetAutoCompact(!opts.disableAutoCompact, opts.autoCompactThreshold)
			runner.SetEndpoint(currentEndpoint)
			ui.SetAgentSkillHandler(id, currentSelection.Set)
			if sandboxActive {
				currentRegistry.SetDirectoryApprover(ui.AgentDirectoryApprover(id))
			}
			return runner, nil
		})
	}
	configureFactory(ui, manager, nil)
	if _, err := manager.CreateMain(opts.model); err != nil {
		return err
	}
	if opts.demo {
		// Seed the scripted collaborator so the demo's delegate_task and
		// get_agent_result calls exercise successful coordination events.
		if _, err := manager.Create(opts.model); err != nil {
			return err
		}
	}
	runner, _ := manager.Agent("main")
	ui.SetRunner(runner)
	if !opts.demo {
		directory, err := session.DefaultDirectory()
		if err != nil {
			return err
		}
		store, err := session.Open(directory, root)
		if err != nil {
			return err
		}
		if err := ui.EnableSessions(store, func(snap session.Snapshot) (*tui.UI, error) {
			staged := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
			staged.SetSkillCatalogLoader(loadSkills)
			staged.SetSkillLocations(skillLocations)
			restored := agent.NewAgentManager(context.Background(), agent.DefaultMaxAgents)
			saved := map[string]agent.SavedState{}
			for _, item := range snap.Agents {
				var state agent.SavedState
				if err := json.Unmarshal(item.State, &state); err != nil {
					return nil, err
				}
				saved[item.Summary.ID] = state
			}
			configureFactory(staged, restored, saved)
			if err := restored.RestoreAgents(snap.Agents, snap.NextID); err != nil {
				restored.Shutdown()
				return nil, err
			}
			if err := restored.RestoreWorkHistory(snap.Work); err != nil {
				restored.Shutdown()
				return nil, err
			}
			staged.SetDetachedAgentManager(restored)
			if err := staged.RestorePresentation(snap.Presentation); err != nil {
				restored.Shutdown()
				return nil, err
			}
			return staged, nil
		}); err != nil {
			return err
		}
	}
	return ui.Run(context.Background())
}

func runOneShot(ctx context.Context, promptText string, provider llm.Provider, model string, toolset agent.Toolset, stderr, stdout *os.File, jsonEvents bool, maxSteps int, system string, learningStore learning.Store, learningBudget int) error {
	manager := agent.NewAgentManager(ctx, 1)
	defer manager.Shutdown()
	manager.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		logger := newTraceLogger(stderr, jsonEvents)
		responseWriter := newResponseWriter(stdout)
		runner := agent.NewWithSystem(provider, model, toolset, logger, responseWriter, maxSteps, system)
		runner.SetLearning(learningStore, learningBudget)
		return runner, nil
	})
	if _, err := manager.CreateMain(model); err != nil {
		return err
	}
	_, err := manager.SubmitAndWait(ctx, "main", promptText)
	return err
}

func skillSummaries(catalog *skills.Catalog) []prompt.SkillSummary {
	available := catalog.Skills()
	summaries := make([]prompt.SkillSummary, len(available))
	for i, skill := range available {
		summaries[i] = prompt.SkillSummary{Name: skill.Name, Description: skill.Description}
	}
	return summaries
}

func skillCatalogData(root string, customPaths ...string) ([]prompt.SkillSummary, error) {
	catalog, err := skills.Discover(root, customPaths...)
	if err != nil {
		return nil, err
	}
	return skillSummaries(catalog), nil
}

func applyConfig(opts *options, cfg config.Config, setFlags map[string]bool) {
	if !setFlags["agent-timeout"] && cfg.AgentTimeout != nil {
		opts.agentTimeout, _ = time.ParseDuration(*cfg.AgentTimeout) // Validated by config.Load.
	}
	opts.searchBackend = cfg.WebSearch.Backend
	if cfg.Learning.ContextBudget != nil {
		opts.learningBudget = *cfg.Learning.ContextBudget
	}
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
	// A configured capacity belongs to the configured model and provider.
	if providerConfigApplies && (cfg.Model == "" || cfg.Model == opts.model) && !setFlags["context-window"] && cfg.ContextWindow != nil {
		opts.contextWindow = *cfg.ContextWindow
	}
	if cfg.AutoCompactThreshold != nil {
		opts.autoCompactThreshold = *cfg.AutoCompactThreshold
	}
	if !setFlags["disable-auto-compact"] && cfg.DisableAutoCompact != nil {
		opts.disableAutoCompact = *cfg.DisableAutoCompact
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
	if !setFlags["sandbox"] && cfg.Sandbox != nil {
		opts.sandbox = *cfg.Sandbox
	}
	if !setFlags["danger-skip-tls-verify"] && cfg.DangerSkipTLSVerify != nil {
		opts.dangerSkipTLSVerify = *cfg.DangerSkipTLSVerify
	}
}

func newTraceLogger(out *os.File, jsonOutput bool) *trace.Logger {
	var logger *trace.Logger
	if term.IsTerminal(int(out.Fd())) {
		logger = trace.NewAnimated(out, jsonOutput)
	} else {
		logger = trace.New(out, jsonOutput)
	}
	logger.SetColor(tui.ColorEnabled(out))
	logger.SetUnicode(tui.UnicodeEnabled())
	logger.SetWidth(tui.OutputWidth(out))
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
