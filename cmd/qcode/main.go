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
	"qcode/internal/control"
	"qcode/internal/demo"
	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/memo"
	"qcode/internal/prompt"
	"qcode/internal/redaction"
	"qcode/internal/remote"
	"qcode/internal/session"
	"qcode/internal/skills"
	"qcode/internal/tools"
	"qcode/internal/trace"
	"qcode/internal/tui"
	qcodeupdate "qcode/internal/update"
)

var version = "dev"

type options struct {
	learningBudget       int
	searchBackend        string
	provider             string
	model                string
	thinking             string
	interactive          bool
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
	updateModelMeta      bool
	showVersion          bool
	demo                 bool
	sandbox              bool
	sandboxCommandPaths  []string
	dangerSkipTLSVerify  bool
}

// sandboxCommandPathsFlag collects repeatable --sandbox-command-path values.
// When set, it overrides the merged sandbox_command_paths configuration.
type sandboxCommandPathsFlag []string

func (s *sandboxCommandPathsFlag) String() string {
	return strings.Join(*s, string(os.PathListSeparator))
}

func (s *sandboxCommandPathsFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "qcode:", err)
		os.Exit(1)
	}
}

func run(arguments []string, stdin *os.File, stdout, stderr *os.File) (runErr error) {
	policy, _ := redaction.New(redaction.Config{}, redaction.EnvironmentValues())
	diagnosticsOut := &redaction.DiagnosticWriter{Policy: policy, Sink: redaction.Terminal, Out: stderr}
	defer func() { runErr = policy.Error(redaction.Terminal, runErr) }()
	var redactionConfig redaction.Config
	if qcodeupdate.IsReplacementCommand(arguments) {
		return qcodeupdate.RunReplacement(arguments, stdout)
	}
	if len(arguments) > 0 && arguments[0] == "update" {
		err := qcodeupdate.Run(context.Background(), arguments[1:], qcodeupdate.Options{
			CurrentVersion: version,
			Stdout:         stdout,
			Stderr:         stderr,
		})
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(arguments) > 0 && arguments[0] == "login" {
		isTerminal := term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd()))
		return runLogin(arguments[1:], stdin, stdout, diagnosticsOut, isTerminal)
	}
	if len(arguments) > 0 && arguments[0] == "config" {
		return runConfig(arguments[1:], stdout, diagnosticsOut)
	}

	opts := options{learningBudget: learning.DefaultBudget, autoCompactThreshold: agent.DefaultAutoCompactThreshold}
	var configPaths []string
	var configuredSkillPaths []string
	var configuredAutoloadPaths []string
	var configuredAlwaysAutoloadPaths []string
	var configuredSelectedSkills []string
	var configuredSelectedAlwaysSkills []string
	var configuredStatuslineHidden []string
	var configuredTheme string
	var loadedConfig config.Config
	var sandboxCommandPathFlags sandboxCommandPathsFlag
	flags := flag.NewFlagSet("qcode", flag.ContinueOnError)
	flags.SetOutput(diagnosticsOut)
	flags.StringVar(&opts.provider, "provider", env("QCODE_PROVIDER", "ollama"), "LLM provider: ollama, openai, opencode-go, or chatgpt")
	flags.StringVar(&opts.model, "model", env("QCODE_MODEL", "qwen2.5-coder:7b"), "model identifier")
	flags.StringVar(&opts.thinking, "thinking", os.Getenv("QCODE_THINKING"), "thinking level for a model that supports it (for example: low, high, max, off)")
	flags.StringVar(&opts.baseURL, "base-url", os.Getenv("QCODE_BASE_URL"), "provider API base URL")
	flags.StringVar(&opts.apiKey, "api-key", firstEnv("QCODE_API_KEY", "OPENAI_API_KEY"), "API key (prefer QCODE_API_KEY or OPENAI_API_KEY)")
	flags.StringVar(&opts.root, "cwd", ".", "workspace root")
	flags.IntVar(&opts.contextWindow, "context-window", 0, "model context capacity in tokens for status display (0: automatic)")
	flags.BoolVar(&opts.disableAutoCompact, "disable-auto-compact", false, "disable automatic conversation compaction")
	flags.IntVar(&opts.maxSteps, "max-steps", 32, "maximum model turns per request")
	flags.DurationVar(&opts.agentTimeout, "agent-timeout", agent.DefaultConsultationTimeout, "time allowed for agent consultations, including queue time")
	flags.BoolVar(&opts.jsonEvents, "json-events", false, "emit action events as JSON Lines")
	flags.BoolVar(&opts.listProviders, "list-providers", false, "list built-in providers")
	flags.BoolVar(&opts.updateModelMeta, "update-model-meta", false, "refresh provider thinking choices from Models.dev and exit")
	flags.BoolVar(&opts.showVersion, "version", false, "print version")
	flags.BoolVar(&opts.demo, "demo", false, "run without an LLM or real tool execution")
	flags.BoolVar(&opts.sandbox, "sandbox", false, "isolate tools with bubblewrap (Linux only)")
	flags.Var(&sandboxCommandPathFlags, "sandbox-command-path", "trusted executable directory to mount read-only in the sandbox and prepend to PATH (repeatable, overrides sandbox_command_paths)")
	flags.Lookup("api-key").DefValue = ""
	flags.BoolVar(&opts.dangerSkipTLSVerify, "danger-skip-tls-verify", false, "skip TLS certificate verification (insecure)")
	flags.Usage = func() {
		fmt.Fprintf(diagnosticsOut, "Usage: qcode [options] [prompt]\n\nWith no prompt, qcode starts its terminal UI.\nUse 'qcode update' to install the latest release.\nUse 'qcode login chatgpt' to connect a ChatGPT subscription via OAuth.\nUse 'qcode config check [--json]' to validate config files with fix suggestions.\n\nOptions:\n")
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
		cfg, inspectedPaths, diagnostics, err := config.Load()
		policy.RegisterSecrets(cfg.Secrets()...)
		writeConfigDiagnostics(diagnosticsOut, diagnostics, err)
		if err != nil {
			return err
		}
		loadedConfig = cfg
		configPaths = inspectedPaths
		configuredSkillPaths = append([]string(nil), cfg.Skills.Paths...)
		lazyAutoload, alwaysAutoload := cfg.Skills.AutoloadPathsByMode()
		// Legacy autoload_paths entries are lazy; merge both forms.
		configuredAutoloadPaths = append(append([]string(nil), lazyAutoload...), alwaysAutoload...)
		configuredAlwaysAutoloadPaths = append([]string(nil), alwaysAutoload...)
		configuredSelectedSkills, configuredSelectedAlwaysSkills = cfg.Skills.NormalizedSelection()
		configuredStatuslineHidden = append([]string(nil), cfg.StatuslineHidden...)
		configuredTheme = cfg.Theme
		redactionConfig = cfg.Redaction
		setFlags := make(map[string]bool)
		flags.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
		if setFlags["sandbox-command-path"] {
			opts.sandboxCommandPaths = append([]string(nil), sandboxCommandPathFlags...)
		}
		applyConfig(&opts, cfg, setFlags)
		if err := enforceStartupConfigCheck(diagnosticsOut, stdin, stdout, stderr, opts.jsonEvents, opts.provider, opts.apiKey); err != nil {
			return err
		}
	} else if len(sandboxCommandPathFlags) > 0 {
		opts.sandboxCommandPaths = append([]string(nil), sandboxCommandPathFlags...)
	}
	policy, err := redaction.New(redactionConfig, append(append(redaction.EnvironmentValues(), loadedConfig.Secrets()...), opts.apiKey))
	if err != nil {
		return err
	}
	diagnosticsOut.Policy = policy
	if opts.updateModelMeta {
		if opts.provider != "openai" && opts.provider != "opencode-go" {
			return errors.New("--update-model-meta requires --provider openai or opencode-go")
		}
		cachePath, err := llm.ProviderModelMetaCachePath(opts.provider)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		meta, err := llm.RefreshProviderModelMeta(ctx, cachePath, opts.provider, nil)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Updated %s thinking metadata (%d models).\n", opts.provider, len(meta.Models))
		return nil
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
	questionsAvailable := interactive && !opts.jsonEvents && term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd()))
	protectedPaths := append([]string(nil), configPaths...)
	var sandboxCommandPaths []string
	if opts.sandbox && !opts.demo {
		resolved, warnings := tools.ResolveSandboxCommandPaths(opts.sandboxCommandPaths, protectedPaths)
		for _, warning := range warnings {
			fmt.Fprintln(diagnosticsOut, "WARNING:", warning)
		}
		sandboxCommandPaths = resolved
	}
	sandboxActive := false
	sandboxPath := ""
	sandboxNotice := ""
	sandboxChoice := false
	if opts.sandbox && !opts.demo {
		if tools.WorkspaceExposesHome(root) {
			sandboxNotice = "Sandbox disabled: the selected workspace contains your home directory and would expose its secrets."
			sandboxChoice = interactive
		} else if checkedPath, checkErr := tools.CheckSandbox(root, false, sandboxCommandPaths...); checkErr != nil {
			sandboxNotice = "Sandbox unavailable: " + checkErr.Error()
			sandboxChoice = interactive
		} else {
			sandboxActive = true
			sandboxPath = checkedPath
			sandboxNotice = "Sandbox enabled: only approved folders can be changed; home is hidden; web tools start disabled. Enabling either web tool allows networking for web tools and shell commands; disabling both blocks it."
		}
		if !sandboxActive && !interactive {
			fmt.Fprintln(diagnosticsOut, "WARNING:", sandboxNotice, "Continuing without sandbox.")
		}
	}
	allSkillPaths := append(append([]string(nil), configuredSkillPaths...), configuredAutoloadPaths...)
	skillLocations := skills.Locations(root, allSkillPaths...)
	loadSkills := func() ([]prompt.SkillSummary, error) {
		return skillCatalogData(root, allSkillPaths, configuredAutoloadPaths, configuredAlwaysAutoloadPaths)
	}
	loadSkillBodies := func(names []string) (map[string]string, error) {
		return skillBodiesData(root, allSkillPaths, names)
	}
	var autoloadSkills []prompt.SkillSummary
	var autoloadAlwaysBodies map[string]string
	if len(configuredAutoloadPaths) > 0 || len(configuredSelectedSkills) > 0 {
		loaded, alwaysBodies, discoverErr := startupSkillData(root, configuredSkillPaths, configuredAutoloadPaths, configuredAlwaysAutoloadPaths, configuredSelectedSkills, configuredSelectedAlwaysSkills)
		if discoverErr != nil {
			fmt.Fprintln(diagnosticsOut, "WARNING: cannot autoload skills:", discoverErr)
		} else {
			autoloadSkills = loaded
			autoloadAlwaysBodies = alwaysBodies
			for _, name := range configuredSelectedSkills {
				found := false
				for _, summary := range autoloadSkills {
					if summary.Name == name {
						found = true
						break
					}
				}
				if !found {
					fmt.Fprintln(diagnosticsOut, "WARNING: configured skill selection not found, skipping:", name)
				}
			}
		}
	}
	skillSelection := skills.NewLazySelection(root, allSkillPaths...)
	skillSelection.SetSelection(skillNames(autoloadSkills), skillAlwaysNames(autoloadSkills))
	registry, err := tools.NewWithOptions(root, tools.Options{Sandbox: sandboxActive, BubblewrapPath: sandboxPath, ProtectedPaths: protectedPaths, SandboxCommandPaths: sandboxCommandPaths, Skills: skillSelection, SearchBackend: opts.searchBackend, InsecureSkipTLSVerify: opts.dangerSkipTLSVerify})
	if err != nil {
		return err
	}
	providers := &providerBuilder{config: loadedConfig, launchProvider: opts.provider,
		launch: llm.Config{BaseURL: opts.baseURL, APIKey: opts.apiKey, InsecureSkipVerify: opts.dangerSkipTLSVerify},
		policy: policy, warnings: diagnosticsOut}
	var provider llm.Provider
	var toolset agent.Toolset = registry
	if opts.demo {
		session := demo.New(registry.Schemas())
		provider = session.Provider
		toolset = session.Tools
		opts.provider = provider.Name()
		opts.model = demo.Model
	} else {
		prepared, prepareErr := providers.prepare(context.Background(), opts.provider, nil)
		if prepareErr != nil {
			return prepareErr
		}
		provider = prepared.Provider
		if prepared.MissingThinkingMeta && opts.thinking != "" {
			fmt.Fprintln(diagnosticsOut, "WARNING: ignoring configured thinking level because no choices are available")
			opts.thinking = ""
		}
	}
	installProviderFactory := func(ui *tui.UI) {
		if !opts.demo && !opts.jsonEvents {
			ui.SetProviderFactory(func(ctx context.Context, name string) (llm.Provider, string, error) {
				prepared, err := providers.prepare(ctx, name, nil)
				return prepared.Provider, prepared.Endpoint, err
			})
			ui.SetProviderAvailabilityChecker(func(name string) (bool, string) {
				return providers.availability(name)
			})
		}
	}
	var learningStore learning.Store
	var memoStore *memo.FileStore
	if !opts.demo {
		dir, err := learning.DefaultDirectory()
		if err != nil {
			fmt.Fprintln(diagnosticsOut, "Learning unavailable:", err)
		} else {
			learningStore = learning.New(dir, func(message string) { fmt.Fprintln(diagnosticsOut, "Learning warning:", message) })
		}
		memoDir, err := memo.DefaultDirectory()
		if err != nil {
			fmt.Fprintln(diagnosticsOut, "Memos unavailable:", err)
		} else {
			memoStore = memo.New(memoDir, func(message string) { fmt.Fprintln(diagnosticsOut, "Memo warning:", message) })
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
		return runOneShot(ctx, promptText, provider, opts.model, opts.thinking, toolset, stderr, stdout, opts.jsonEvents, opts.maxSteps, system, learningStore, opts.learningBudget, autoloadSkills, autoloadAlwaysBodies, policy)
	}

	// Terminal output must go through term.Terminal so asynchronous-looking stream
	// updates do not corrupt the editable input line.
	ui := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
	ui.SetRedaction(policy)
	installProviderFactory(ui)
	ui.SetRememberedProviderModels(loadedConfig.ProviderModels, loadedConfig.ProviderThinking)
	ui.RememberProviderModel(opts.provider, opts.model, opts.thinking)
	ui.SetStatuslineHidden(configuredStatuslineHidden)
	if configuredTheme != "" {
		if err := ui.SetTheme(configuredTheme); err != nil {
			return err
		}
	}
	var runtimePreferences *config.RuntimePreferenceWriter
	if !opts.demo {
		var preferenceErr error
		runtimePreferences, preferenceErr = config.NewRuntimePreferenceWriter()
		if preferenceErr != nil {
			fmt.Fprintln(diagnosticsOut, "WARNING: runtime preferences unavailable:", preferenceErr)
		} else {
			runtimePreferences.SetLegacyConfig(loadedConfig)
			ui.SetRuntimePreferenceWriter(runtimePreferences)
		}
	}
	ui.SetSkillCatalogLoader(loadSkills)
	ui.SetSkillBodyLoader(loadSkillBodies)
	ui.SetSkillLocations(skillLocations)
	if memoStore != nil {
		ui.SetMemoStore(memoStore)
	}
	if opts.demo {
		ui.SetDemoPromptScript(demo.InteractivePrompts(), demo.InteractivePromptDelay, demo.InteractiveQueueDelay)
	}
	if sandboxNotice != "" {
		ui.SetStartupNotice(sandboxNotice, sandboxChoice)
	}
	host := control.NewHost(context.Background(), agent.DefaultMaxAgents)
	defer host.Shutdown()
	ui.SetAgentManager(host)
	remoteControl := remote.New(ui)
	defer remoteControl.Stop()
	ui.SetRemoteService(remoteControl)
	mainRegistry := registry
	mainProvider := provider
	mainToolset := toolset
	mainSelection := skillSelection
	configureFactory := func(ui *tui.UI, host *control.Host, saved map[string]agent.SavedState) {
		_ = host.SetConsultationTimeout(opts.agentTimeout)
		create := func(id, name, model string, isMain bool, selected *agent.CreationBackend) (*agent.Agent, error) {
			currentProvider := mainProvider
			currentToolset := mainToolset
			currentRegistry := mainRegistry
			currentSelection := mainSelection
			currentEndpoint := opts.baseURL
			if !isMain || saved != nil || selected != nil {
				currentSelection = skills.NewLazySelection(root, allSkillPaths...)
				createdRegistry, createErr := tools.NewWithOptions(root, tools.Options{Sandbox: sandboxActive, BubblewrapPath: sandboxPath, ProtectedPaths: protectedPaths, SandboxCommandPaths: sandboxCommandPaths, Skills: currentSelection, SearchBackend: opts.searchBackend, InsecureSkipTLSVerify: opts.dangerSkipTLSVerify})
				if createErr != nil {
					return nil, createErr
				}
				currentRegistry = createdRegistry
				if opts.demo {
					session := demo.New(createdRegistry.Schemas())
					currentProvider = session.Provider
					currentToolset = session.Tools
				} else {
					backend := agent.CreationBackend{Provider: opts.provider, Endpoint: opts.baseURL}
					if selected != nil {
						backend = *selected
					} else if state, ok := saved[id]; ok {
						backend = agent.CreationBackend{Provider: state.Provider, Endpoint: state.Endpoint}
					} else if !isMain {
						var backendErr error
						backend, backendErr = host.Backend("main")
						if backendErr != nil {
							return nil, backendErr
						}
					}
					prepared, providerErr := providers.prepare(context.Background(), backend.Provider, &backend.Endpoint)
					if providerErr != nil {
						return nil, providerErr
					}
					currentEndpoint = prepared.Endpoint
					currentProvider = prepared.Provider
					currentToolset = createdRegistry
				}
			}
			display, response := ui.AddAgentView(id, currentProvider.Name(), model)
			wrappedTools := host.WrapToolset(id, currentToolset, isMain)
			logger := trace.NewAnimated(display, opts.jsonEvents)
			logger.SetColor(tui.ColorEnabled(stdout))
			logger.SetWidth(tui.OutputWidth(stdout))
			runner := agent.NewWithSystem(currentProvider, model, wrappedTools, logger, response, opts.maxSteps, system)
			runner.SetCreationRollback(func() { ui.RemoveAgentView(id) })
			runner.SetRedaction(policy)
			runner.SetInteractiveAvailable(questionsAvailable)
			if saved == nil {
				runner.SetInteractiveMode(opts.interactive)
			}
			if saved == nil && len(autoloadSkills) > 0 {
				currentSelection.SetSelection(skillNames(autoloadSkills), skillAlwaysNames(autoloadSkills))
				runner.SetSkillsWithContents(autoloadSkills, autoloadAlwaysBodies)
			}
			runner.SetVerbose(ui.VerboseEnabled())
			runner.SetQuestioner(ui.AgentQuestioner(id))
			runner.SetTaskIndicator(false)
			runner.SetLearning(learningStore, opts.learningBudget)
			if isMain && saved == nil && selected == nil {
				runner.SetContextWindow(opts.contextWindow)
				if err := runner.SetThinking(opts.thinking); err != nil {
					return nil, err
				}
			}
			runner.SetAutoCompact(!opts.disableAutoCompact, opts.autoCompactThreshold)
			runner.SetEndpoint(currentEndpoint)
			ui.SetAgentSkillHandler(id, func(names, always []string) {
				currentSelection.SetSelection(names, always)
			})
			if sandboxActive {
				currentRegistry.SetDirectoryApprover(ui.AgentDirectoryApprover(id))
			}
			return runner, nil
		}
		host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
			return create(id, name, model, main, nil)
		})
		host.SetProviderFactory(func(id, name, model string, main bool, backend agent.CreationBackend) (*agent.Agent, error) {
			return create(id, name, model, main, &backend)
		})
	}
	configureFactory(ui, host, nil)
	if _, err := host.CreateMain(opts.model); err != nil {
		return err
	}
	if opts.demo {
		// Seed the scripted collaborator so the demo's delegate_task call
		// exercises a successful background coordination event.
		if _, err := host.Create(opts.model); err != nil {
			return err
		}
	}
	runner, _ := host.Agent("main")
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
		store.SetSnapshotFilter(func(snap session.Snapshot) (session.Snapshot, error) { return filterSessionSnapshot(policy, snap) })
		newDetachedUI := func() (*tui.UI, error) {
			staged := tui.New(stdin, stdout, nil, opts.provider, opts.model, root)
			staged.SetRedaction(policy)
			installProviderFactory(staged)
			staged.SetRememberedProviderModels(loadedConfig.ProviderModels, loadedConfig.ProviderThinking)
			staged.RememberProviderModel(opts.provider, opts.model, opts.thinking)
			staged.SetStatuslineHidden(configuredStatuslineHidden)
			if configuredTheme != "" {
				if err := staged.SetTheme(configuredTheme); err != nil {
					return nil, err
				}
			}
			staged.SetRuntimePreferenceWriter(runtimePreferences)
			staged.SetSkillCatalogLoader(loadSkills)
			staged.SetSkillBodyLoader(loadSkillBodies)
			staged.SetSkillLocations(skillLocations)
			if memoStore != nil {
				staged.SetMemoStore(memoStore)
			}
			return staged, nil
		}
		if err := ui.EnableSessions(store, func(snap session.Snapshot) (*tui.UI, error) {
			staged, err := newDetachedUI()
			if err != nil {
				return nil, err
			}
			restored := control.NewHost(context.Background(), agent.DefaultMaxAgents)
			saved := map[string]agent.SavedState{}
			for _, item := range snap.Agents {
				var state agent.SavedState
				if err := json.Unmarshal(item.State, &state); err != nil {
					restored.Shutdown()
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
		}, func(main session.SavedAgent) (*tui.UI, error) {
			var state agent.SavedState
			if err := json.Unmarshal(main.State, &state); err != nil {
				return nil, err
			}
			staged, err := newDetachedUI()
			if err != nil {
				return nil, err
			}
			fresh := control.NewHost(context.Background(), agent.DefaultMaxAgents)
			configureFactory(staged, fresh, map[string]agent.SavedState{"main": state})
			if _, err := fresh.CreateMain(state.Model); err != nil {
				fresh.Shutdown()
				return nil, err
			}
			runner, _ := fresh.Agent("main")
			if err := runner.RestoreFreshState(main.State); err != nil {
				fresh.Shutdown()
				return nil, err
			}
			staged.SetDetachedAgentManager(fresh)
			return staged, nil
		}); err != nil {
			return err
		}
	}
	return ui.Run(context.Background())
}

func runOneShot(ctx context.Context, promptText string, provider llm.Provider, model, thinking string, toolset agent.Toolset, stderr, stdout *os.File, jsonEvents bool, maxSteps int, system string, learningStore learning.Store, learningBudget int, autoloadSkills []prompt.SkillSummary, autoloadAlwaysBodies map[string]string, policies ...*redaction.Policy) error {
	var policy *redaction.Policy
	if len(policies) > 0 {
		policy = policies[0]
	}
	host := control.NewHost(ctx, 1)
	defer host.Shutdown()
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		logger := newTraceLogger(stderr, jsonEvents)
		responseWriter := newResponseWriter(stdout)
		responseWriter.SetRedaction(policy)
		runner := agent.NewWithSystem(provider, model, toolset, logger, responseWriter, maxSteps, system)
		runner.SetRedaction(policy)
		runner.SetInteractiveAvailable(false)
		if len(autoloadSkills) > 0 {
			runner.SetSkillsWithContents(autoloadSkills, autoloadAlwaysBodies)
		}
		if err := runner.SetThinking(thinking); err != nil {
			return nil, err
		}
		runner.SetLearning(learningStore, learningBudget)
		return runner, nil
	})
	if _, err := host.CreateMain(model); err != nil {
		return err
	}
	_, err := host.SubmitAndWait(ctx, "main", promptText)
	return err
}

func skillSummaries(catalog *skills.Catalog) []prompt.SkillSummary {
	return summariesForSkills(catalog.Skills())
}

func summariesForSkills(available []skills.Skill) []prompt.SkillSummary {
	summaries := make([]prompt.SkillSummary, len(available))
	for i, skill := range available {
		summaries[i] = prompt.SkillSummary{Name: skill.Name, Description: skill.Description, SizeBytes: skill.SizeBytes}
	}
	return summaries
}

func skillNames(summaries []prompt.SkillSummary) []string {
	names := make([]string, len(summaries))
	for i, skill := range summaries {
		names[i] = skill.Name
	}
	return names
}

func skillAlwaysNames(summaries []prompt.SkillSummary) []string {
	var names []string
	for _, skill := range summaries {
		if skill.AlwaysLoad {
			names = append(names, skill.Name)
		}
	}
	return names
}

func markAutoloadFlags(summaries []prompt.SkillSummary, catalog *skills.Catalog, autoloadPaths, alwaysPaths []string) []prompt.SkillSummary {
	autoloadSet := map[string]bool{}
	if catalog != nil {
		for _, skill := range catalog.SkillsInPaths(autoloadPaths...) {
			autoloadSet[skill.Name] = true
		}
	}
	alwaysSet := map[string]bool{}
	if catalog != nil {
		for _, skill := range catalog.SkillsInPaths(alwaysPaths...) {
			alwaysSet[skill.Name] = true
		}
	}
	for i, summary := range summaries {
		if autoloadSet[summary.Name] {
			summaries[i].Autoload = true
		}
		if alwaysSet[summary.Name] {
			summaries[i].Autoload = true
			summaries[i].AlwaysLoad = true
		}
	}
	return summaries
}

func skillCatalogData(root string, customPaths, autoloadPaths, alwaysPaths []string) ([]prompt.SkillSummary, error) {
	catalog, err := skills.Discover(root, customPaths...)
	if err != nil {
		return nil, err
	}
	summaries := skillSummaries(catalog)
	return markAutoloadFlags(summaries, catalog, autoloadPaths, alwaysPaths), nil
}

func skillBodiesData(root string, customPaths, names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	catalog, err := skills.Discover(root, customPaths...)
	if err != nil {
		return nil, err
	}
	bodies := make(map[string]string, len(names))
	for _, name := range names {
		body, err := catalog.Load(name)
		if err != nil {
			return nil, err
		}
		bodies[name] = body
	}
	return bodies, nil
}

func autoloadSkillData(root string, configuredPaths, autoloadPaths, alwaysPaths []string) ([]prompt.SkillSummary, map[string]string, error) {
	allPaths := append(append([]string(nil), configuredPaths...), autoloadPaths...)
	catalog, err := skills.Discover(root, allPaths...)
	if err != nil {
		return nil, nil, err
	}
	autoloaded := catalog.SkillsInPaths(autoloadPaths...)
	summaries := summariesForSkills(autoloaded)
	summaries = markAutoloadFlags(summaries, catalog, autoloadPaths, alwaysPaths)
	bodies := make(map[string]string)
	for i, summary := range summaries {
		if !summary.AlwaysLoad {
			continue
		}
		body, err := catalog.Load(summary.Name)
		if err != nil {
			// Warn-and-fallback to lazy on oversize or read errors.
			summaries[i].AlwaysLoad = false
			continue
		}
		bodies[summary.Name] = body
	}
	return summaries, bodies, nil
}

func startupSkillData(root string, configuredPaths, autoloadPaths, alwaysPaths, selected, selectedAlways []string) ([]prompt.SkillSummary, map[string]string, error) {
	allPaths := append(append([]string(nil), configuredPaths...), autoloadPaths...)
	catalog, err := skills.Discover(root, allPaths...)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]skills.Skill{}
	for _, skill := range catalog.Skills() {
		byName[skill.Name] = skill
	}
	autoloadSet := map[string]bool{}
	for _, skill := range catalog.SkillsInPaths(autoloadPaths...) {
		autoloadSet[skill.Name] = true
	}
	alwaysAutoloadSet := map[string]bool{}
	for _, skill := range catalog.SkillsInPaths(alwaysPaths...) {
		alwaysAutoloadSet[skill.Name] = true
	}
	selectedSet := map[string]bool{}
	for _, name := range selected {
		if _, ok := byName[name]; ok {
			selectedSet[name] = true
		}
	}
	selectedAlwaysSet := map[string]bool{}
	for _, name := range selectedAlways {
		if _, ok := byName[name]; ok {
			selectedAlwaysSet[name] = true
			selectedSet[name] = true
		}
	}
	var summaries []prompt.SkillSummary
	for _, skill := range catalog.Skills() {
		if !autoloadSet[skill.Name] && !selectedSet[skill.Name] {
			continue
		}
		summary := prompt.SkillSummary{Name: skill.Name, Description: skill.Description, SizeBytes: skill.SizeBytes}
		if autoloadSet[skill.Name] || alwaysAutoloadSet[skill.Name] {
			summary.Autoload = true
		}
		if alwaysAutoloadSet[skill.Name] || selectedAlwaysSet[skill.Name] {
			summary.AlwaysLoad = true
		}
		summaries = append(summaries, summary)
	}
	bodies := make(map[string]string)
	for i, summary := range summaries {
		if !summary.AlwaysLoad {
			continue
		}
		body, err := catalog.Load(summary.Name)
		if err != nil {
			summaries[i].AlwaysLoad = false
			continue
		}
		bodies[summary.Name] = body
	}
	return summaries, bodies, nil
}

func applyConfig(opts *options, cfg config.Config, setFlags map[string]bool) {
	if cfg.Interactive != nil {
		opts.interactive = *cfg.Interactive
	}
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
	// Converged precedence for model/thinking: [providers.<name>] model/thinking
	// first, then the deprecated provider_models/provider_thinking memory, then
	// deprecated root model/thinking when the root provider matches.
	providerConfigApplies := cfg.Provider == "" || opts.provider == cfg.Provider
	if !setFlags["model"] && os.Getenv("QCODE_MODEL") == "" {
		if profileModel, _, ok := cfg.ProviderModel(opts.provider); ok {
			opts.model = profileModel
		} else if rememberedModel, _, ok := cfg.RememberedProviderModel(opts.provider); ok {
			opts.model = rememberedModel
		} else if providerConfigApplies && cfg.Model != "" {
			opts.model = cfg.Model
		}
	}
	if !setFlags["thinking"] && os.Getenv("QCODE_THINKING") == "" {
		if profileThinking, ok := cfg.ProfileThinking(opts.provider); ok {
			opts.thinking = profileThinking
		} else if _, rememberedThinking, ok := cfg.RememberedProviderModel(opts.provider); ok {
			opts.thinking = rememberedThinking
		} else if providerConfigApplies && cfg.Thinking != "" {
			opts.thinking = cfg.Thinking
		}
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
	applyProviderConnection(opts, cfg, setFlags)
	if !setFlags["max-steps"] && cfg.MaxSteps != nil {
		opts.maxSteps = *cfg.MaxSteps
	}
	if !setFlags["sandbox"] && cfg.Sandbox != nil {
		opts.sandbox = *cfg.Sandbox
	}
	if !setFlags["sandbox-command-path"] && cfg.SandboxCommandPaths != nil {
		opts.sandboxCommandPaths = append([]string(nil), cfg.SandboxCommandPaths...)
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

func filterSessionSnapshot(policy *redaction.Policy, snap session.Snapshot) (session.Snapshot, error) {
	filtered, err := tui.FilterSnapshot(policy, snap)
	if err != nil {
		return session.Snapshot{}, err
	}
	if !policy.Enabled(redaction.Persistence) {
		return filtered, nil
	}
	for i := range filtered.Agents {
		data, err := agent.FilterSavedState(policy, filtered.Agents[i].State)
		if err != nil {
			return session.Snapshot{}, err
		}
		filtered.Agents[i].State = data
	}
	return filtered, nil
}
