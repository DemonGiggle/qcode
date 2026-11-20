package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/mcpclient"
	"qcode/internal/redaction"
	"qcode/internal/tui"
)

var setupBuiltInProviders = []string{"ollama", llm.OllamaCloudKind, "openai", "opencode-go", "gemini", "chatgpt"}

var defaultSandboxCandidatePaths = []string{
	"~/.local/bin",
	"~/bin",
	"~/go/bin",
	"~/.cargo/bin",
	"~/.npm-global/bin",
	"~/.local/share/pnpm",
	"~/.bun/bin",
	"~/.deno/bin",
}

const backMenuItem = "< Back"

type setupUI interface {
	SelectOptionNav(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, tui.SetupNavResult, error)
	SelectModelNav(models []string, current string) (string, tui.SetupNavResult, error)
	SelectThinkingNav(levels []string, current string) (string, tui.SetupNavResult, error)
	SelectThemeNav(current string) (string, tui.SetupNavResult, error)
	PromptLineNav(label, defaultVal string, secret bool) (string, tui.SetupNavResult, error)
}

type setupProbeResult struct {
	models      []string
	thinkingCap func(string) llm.ThinkingCapability
	metadata    llm.ModelMetadataProvider
}

type setupDeps struct {
	newUI        func(in io.Reader, out io.Writer) setupUI
	loadConfig   func() (config.Config, []string, []error, error)
	userCfgPath  func() (string, error)
	newWriter    func() (*config.RuntimePreferenceWriter, error)
	writeKeyFile func(path, key string) (string, error)
	backupConfig func(path string) (string, error)
	chatgptLogin func(oauthPath string, stdin io.Reader, stdout io.Writer) error
	probeModels  func(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error)
	checkConfig  func() ([]config.Issue, []string, error)
	goos         string
}

func defaultSetupDeps() setupDeps {
	return setupDeps{
		newUI: func(in io.Reader, out io.Writer) setupUI {
			return tui.NewSetupUI(in, out)
		},
		loadConfig:   config.Load,
		userCfgPath:  config.UserConfigPath,
		newWriter:    config.NewRuntimePreferenceWriter,
		writeKeyFile: config.WriteAPIKeyFile,
		backupConfig: config.BackupUserConfig,
		chatgptLogin: func(oauthPath string, stdin io.Reader, stdout io.Writer) error {
			httpClient := &http.Client{Timeout: 30 * time.Second}
			return chatGPTLoginFlow(oauthPath, stdin, stdout, httpClient)
		},
		probeModels: defaultProbeProviderModels,
		checkConfig: config.CheckDefault,
		goos:        runtime.GOOS,
	}
}

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer, isTerminal bool) error {
	return runSetupWithDeps(args, stdin, stdout, stderr, isTerminal, defaultSetupDeps())
}

func runSetupWithDeps(args []string, stdin io.Reader, stdout, stderr io.Writer, isTerminal bool, deps setupDeps) error {
	fs := flag.NewFlagSet("qcode setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: qcode setup")
	}
	if !isTerminal {
		return fmt.Errorf("setup requires an interactive terminal")
	}

	cfg, _, diagnostics, err := deps.loadConfig()
	policy, _ := redaction.New(cfg.Redaction, append(redaction.EnvironmentValues(), cfg.Secrets()...))
	if writer, ok := stderr.(*redaction.DiagnosticWriter); ok {
		writer.Policy.RegisterSecrets(cfg.Secrets()...)
	}
	writeConfigDiagnostics(stderr, diagnostics, err)
	if err != nil {
		return err
	}

	userPath, err := deps.userCfgPath()
	if err != nil {
		return err
	}
	writer, err := deps.newWriter()
	if err != nil {
		return err
	}
	writer.SetLegacyConfig(cfg)

	ui := deps.newUI(stdin, stdout)
	fmt.Fprintln(stdout, "qcode setup (menuconfig)")
	fmt.Fprintf(stdout, "User config: %s\n", userPath)
	fmt.Fprintln(stdout, "Navigate with Up/Down, Enter to open/select, Esc to go back.")
	fmt.Fprintln(stdout, "")

	state := newSetupState(cfg, userPath)
	saveAndLeave, err := runMainMenu(ui, state, stdin, stdout, stderr, policy, deps)
	if err != nil {
		return err
	}
	if !saveAndLeave {
		fmt.Fprintln(stdout, "Exited without saving; no changes were written.")
		return nil
	}

	if !state.hasChanges() {
		fmt.Fprintln(stdout, "No changes to save.")
		return nil
	}

	if deps.backupConfig != nil {
		backupPath, err := deps.backupConfig(userPath)
		if err != nil {
			return err
		}
		if backupPath != "" {
			fmt.Fprintf(stdout, "Backed up existing config to %s\n", backupPath)
		}
	}

	if err := applySetupState(writer, state, deps); err != nil {
		return err
	}

	printSetupSummary(stdout, userPath, state)

	issues, inspected, err := deps.checkConfig()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "")
	writeHumanReport(stdout, inspected, issues, configUseColor(stdout), configTermWidth(stdout))
	if config.HasErrors(issues) {
		return fmt.Errorf("config check found %d error(s)", countErrors(issues))
	}
	return nil
}

type providerDraft struct {
	baseURL          *string
	apiKey           string
	apiKeyPath       *string
	model            string
	modelChanged     bool
	thinking         string
	thinkingChanged  bool
	chatgptOAuthDone bool
}

type setupState struct {
	initial  config.Config
	userPath string

	activeProvider  string
	providerChanged bool
	providers       map[string]*providerDraft

	theme        string
	themeChanged bool

	sandbox             *bool
	sandboxCommandPaths []string
	sandboxPathsChanged bool

	interactive *bool
	mcpServers  map[string]mcpclient.ServerConfig
}

func newSetupState(cfg config.Config, userPath string) *setupState {
	active := strings.TrimSpace(cfg.Provider)
	if active == "" {
		active = "ollama"
	}
	return &setupState{
		initial:        cfg,
		userPath:       userPath,
		activeProvider: active,
		providers:      make(map[string]*providerDraft),
		mcpServers:     make(map[string]mcpclient.ServerConfig),
		theme:          strings.TrimSpace(cfg.Theme),
	}
}

func (s *setupState) draft(provider string) *providerDraft {
	d, ok := s.providers[provider]
	if !ok {
		d = &providerDraft{}
		s.providers[provider] = d
	}
	return d
}

func (s *setupState) effectiveModel(provider string) (string, string) {
	model, thinking := s.initial.EffectiveModel(provider)
	if d, ok := s.providers[provider]; ok {
		if d.modelChanged {
			model = d.model
		}
		if d.thinkingChanged {
			thinking = d.thinking
		}
	}
	return model, thinking
}

func (s *setupState) effectiveConnection(provider string) (baseURL, apiKey string) {
	baseURL, apiKey = s.initial.ProviderConnection(provider)
	if provider == llm.OllamaCloudKind {
		if key := config.ProviderEnvironmentAPIKey(provider); key != "" {
			apiKey = key
		}
	}
	if d, ok := s.providers[provider]; ok {
		if d.baseURL != nil {
			baseURL = *d.baseURL
		}
		if d.apiKey != "" {
			apiKey = d.apiKey
		}
	}
	return baseURL, apiKey
}

func (s *setupState) isConfigured(provider string) bool {
	if d, ok := s.providers[provider]; ok {
		if provider == "chatgpt" && d.chatgptOAuthDone {
			return true
		}
		if d.apiKey != "" || (d.apiKeyPath != nil && strings.TrimSpace(*d.apiKeyPath) != "") {
			return true
		}
		if provider == "ollama" && ((d.baseURL != nil && strings.TrimSpace(*d.baseURL) != "") || d.modelChanged) {
			return true
		}
	}
	return isProviderConfigured(s.initial, provider)
}

func (s *setupState) effectiveTheme() string {
	if s.themeChanged {
		if s.theme == "" {
			return "default"
		}
		return s.theme
	}
	if strings.TrimSpace(s.initial.Theme) == "" {
		return "default"
	}
	return strings.TrimSpace(s.initial.Theme)
}

func (s *setupState) effectiveSandbox() bool {
	if s.sandbox != nil {
		return *s.sandbox
	}
	return s.initial.Sandbox != nil && *s.initial.Sandbox
}

func (s *setupState) effectiveSandboxPaths() []string {
	if s.sandboxPathsChanged {
		return s.sandboxCommandPaths
	}
	return s.initial.SandboxCommandPaths
}

func (s *setupState) effectiveInteractive() bool {
	if s.interactive != nil {
		return *s.interactive
	}
	return s.initial.Interactive != nil && *s.initial.Interactive
}

func (s *setupState) hasChanges() bool {
	if s.providerChanged || s.themeChanged || s.sandbox != nil || s.sandboxPathsChanged || s.interactive != nil || len(s.mcpServers) > 0 {
		return true
	}
	for _, d := range s.providers {
		if d.baseURL != nil || d.apiKeyPath != nil || d.modelChanged || d.thinkingChanged {
			return true
		}
	}
	return false
}

func runMainMenu(ui setupUI, state *setupState, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (bool, error) {
	current := ""
	for {
		model, _ := state.effectiveModel(state.activeProvider)
		if model == "" {
			model = "unset"
		}
		providersItem := fmt.Sprintf("Providers & Models  --->  (active: %s / %s)", state.activeProvider, model)
		themeItem := fmt.Sprintf("Appearance (Theme)  --->  (%s)", state.effectiveTheme())

		intStr := "off"
		if state.effectiveInteractive() {
			intStr = "on"
		}
		behaviorItem := fmt.Sprintf("Behavior            --->  (interactive: %s)", intStr)

		options := []string{providersItem, themeItem}
		var sandboxItem string
		if deps.goos == "linux" {
			sbStr := "disabled"
			if state.effectiveSandbox() {
				sbStr = "enabled"
			}
			sandboxItem = fmt.Sprintf("Sandbox (Linux)     --->  (%s)", sbStr)
			options = append(options, sandboxItem)
		}
		options = append(options, behaviorItem, "MCP servers         --->", "Save and Leave", "Exit without saving")

		choice, nav, err := ui.SelectOptionNav("main menu", options, nil, current, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return false, nil
		}
		if nav == tui.SetupBack {
			// At main menu, Esc keeps the user in the menu rather than discarding staged work.
			continue
		}
		current = choice

		switch choice {
		case providersItem:
			cancelled, err := runProvidersMenu(ui, state, stdin, stdout, stderr, policy, deps)
			if err != nil || cancelled {
				return false, err
			}
		case themeItem:
			chosen, tnav, err := ui.SelectThemeNav(state.effectiveTheme())
			if err != nil {
				return false, err
			}
			if tnav == tui.SetupAccepted && chosen != "" {
				state.theme = chosen
				state.themeChanged = true
			}
		case sandboxItem:
			cancelled, err := runSandboxMenu(ui, state)
			if err != nil || cancelled {
				return false, err
			}
		case behaviorItem:
			cancelled, err := runBehaviorMenu(ui, state)
			if err != nil || cancelled {
				return false, err
			}
		case "MCP servers         --->":
			cancelled, err := runMCPMenu(ui, state, stdout)
			if err != nil || cancelled {
				return false, err
			}
		case "Save and Leave":
			return true, nil
		case "Exit without saving":
			return false, nil
		}
	}
}

func runProvidersMenu(ui setupUI, state *setupState, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (bool, error) {
	currentProvider := state.activeProvider
	for {
		options := make([]string, 0, len(setupBuiltInProviders)+1)
		labelToProvider := make(map[string]string, len(setupBuiltInProviders))
		defaultChoice := ""
		for _, p := range setupBuiltInProviders {
			var tags []string
			if p == state.activeProvider {
				tags = append(tags, "active")
			}
			if state.isConfigured(p) {
				tags = append(tags, "configured")
			}
			label := p
			if len(tags) > 0 {
				label = fmt.Sprintf("%s (%s)", p, strings.Join(tags, ", "))
			}
			if p == currentProvider {
				defaultChoice = label
			}
			options = append(options, label)
			labelToProvider[label] = p
		}
		options = append(options, backMenuItem)

		choice, nav, err := ui.SelectOptionNav("provider", options, nil, defaultChoice, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupBack || choice == backMenuItem {
			return false, nil
		}

		p := labelToProvider[choice]
		currentProvider = p
		cancelled, err := runProviderSubmenu(ui, state, p, stdin, stdout, stderr, policy, deps)
		if err != nil || cancelled {
			return cancelled, err
		}
	}
}

func runProviderSubmenu(ui setupUI, state *setupState, provider string, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (bool, error) {
	currentItem := ""
	for {
		model, thinking := state.effectiveModel(provider)
		if model == "" {
			model = "unset"
		}
		modelSummary := model
		if thinking != "" {
			modelSummary = fmt.Sprintf("%s, thinking: %s", model, thinking)
		}

		activeLabel := "Set as active default provider"
		if state.activeProvider == provider {
			activeLabel = fmt.Sprintf("Active default provider (%s)", provider)
		}
		credLabel := "Configure credentials & endpoint"
		if state.isConfigured(provider) {
			credLabel = "Configure credentials & endpoint (configured)"
		}
		modelLabel := fmt.Sprintf("Verify connection & select model (%s)", modelSummary)
		if provider == llm.OllamaCloudKind {
			modelLabel = fmt.Sprintf("Fetch Cloud catalog & select model (%s)", modelSummary)
		}

		options := []string{activeLabel, credLabel, modelLabel, backMenuItem}
		choice, nav, err := ui.SelectOptionNav(provider+" menu", options, nil, currentItem, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupBack || choice == backMenuItem {
			return false, nil
		}
		currentItem = choice

		switch choice {
		case activeLabel:
			if state.activeProvider != provider {
				state.activeProvider = provider
				state.providerChanged = true
			}
		case credLabel:
			navRes, err := configureProviderCredentials(ui, state, provider, stdin, stdout, stderr, policy, deps)
			if err != nil {
				return false, err
			}
			if navRes == tui.SetupCancelled {
				return true, nil
			}
		case modelLabel:
			navRes, err := verifyAndSelectModel(ui, state, provider, stdin, stdout, stderr, policy, deps)
			if err != nil {
				return false, err
			}
			if navRes == tui.SetupCancelled {
				return true, nil
			}
		}
	}
}

func configureProviderCredentials(ui setupUI, state *setupState, provider string, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (tui.SetupNavResult, error) {
	existingBase, existingKey := state.effectiveConnection(provider)
	profile := state.initial.Providers[provider]
	draft := state.draft(provider)

	switch provider {
	case "chatgpt":
		if state.isConfigured("chatgpt") {
			choice, nav, err := ui.SelectOptionNav(
				"ChatGPT authentication",
				[]string{"Re-authenticate with ChatGPT", backMenuItem},
				nil,
				"Re-authenticate with ChatGPT",
				false,
			)
			if err != nil || nav != tui.SetupAccepted {
				return nav, err
			}
			if choice == backMenuItem {
				return tui.SetupBack, nil
			}
		}
		oauthPath := state.initial.ChatGPT.OAuthPath
		if oauthPath == "" {
			oauthPath = chatgptoauth.DefaultPath
		}
		if err := deps.chatgptLogin(oauthPath, stdin, stdout); err != nil {
			return tui.SetupCancelled, err
		}
		draft.chatgptOAuthDone = true
		return tui.SetupAccepted, nil

	case "ollama":
		defURL := existingBase
		if defURL == "" {
			defURL = "http://127.0.0.1:11434"
		}
		url, nav, err := ui.PromptLineNav("Ollama base URL", defURL, false)
		if err != nil || nav != tui.SetupAccepted {
			return nav, err
		}
		url = strings.TrimSpace(url)
		if profile.BaseURL != nil || url != defURL {
			draft.baseURL = &url
		}
		return tui.SetupAccepted, nil

	default: // API-key providers
		var stagedKey string
		var stagedKeyPath *string

		if existingKey != "" || (profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != "") || draft.apiKeyPath != nil {
			choice, nav, err := ui.SelectOptionNav(
				provider+" API key",
				[]string{"Keep existing API key", "Enter a new API key (saved to 0600 key file)", backMenuItem},
				nil,
				"Keep existing API key",
				false,
			)
			if err != nil || nav != tui.SetupAccepted {
				return nav, err
			}
			if choice == backMenuItem {
				return tui.SetupBack, nil
			}
			if choice == "Enter a new API key (saved to 0600 key file)" {
				k, kp, navRes, err := promptNewAPIKey(ui, provider, state.userPath, profile, draft)
				if err != nil || navRes != tui.SetupAccepted {
					return navRes, err
				}
				stagedKey = k
				stagedKeyPath = &kp
			}
		} else {
			k, kp, navRes, err := promptNewAPIKey(ui, provider, state.userPath, profile, draft)
			if err != nil || navRes != tui.SetupAccepted {
				return navRes, err
			}
			stagedKey = k
			stagedKeyPath = &kp
		}

		// Optional custom base URL
		endpointOpts := []string{"Use default endpoint", "Set custom base URL"}
		defaultEndpointOpt := "Use default endpoint"
		if (profile.BaseURL != nil && strings.TrimSpace(*profile.BaseURL) != "") || (draft.baseURL != nil && strings.TrimSpace(*draft.baseURL) != "") {
			endpointOpts = []string{
				fmt.Sprintf("Keep current (%s)", existingBase),
				"Set custom base URL",
				"Reset to provider default",
			}
			defaultEndpointOpt = endpointOpts[0]
		}
		endpointOpts = append(endpointOpts, backMenuItem)

		epChoice, nav, err := ui.SelectOptionNav(provider+" endpoint", endpointOpts, nil, defaultEndpointOpt, false)
		if err != nil || nav != tui.SetupAccepted {
			return nav, err
		}
		if epChoice == backMenuItem {
			return tui.SetupBack, nil
		}

		var stagedBaseURL *string
		switch epChoice {
		case "Set custom base URL":
			u, navRes, err := ui.PromptLineNav(provider+" base URL", existingBase, false)
			if err != nil || navRes != tui.SetupAccepted {
				return navRes, err
			}
			u = strings.TrimSpace(u)
			if u != "" {
				stagedBaseURL = &u
			}
		case "Reset to provider default":
			empty := ""
			stagedBaseURL = &empty
		}

		if stagedBaseURL != nil && provider == llm.OllamaCloudKind {
			if _, err := llm.OllamaCloudBaseURL(*stagedBaseURL); err != nil {
				fmt.Fprintln(stderr, err)
				return tui.SetupBack, nil
			}
		}
		if stagedKey != "" {
			registerSetupSecret(policy, stderr, stagedKey)
			draft.apiKey = stagedKey
			draft.apiKeyPath = stagedKeyPath
		}
		if stagedBaseURL != nil {
			draft.baseURL = stagedBaseURL
		}
		return tui.SetupAccepted, nil
	}
}

func promptNewAPIKey(ui setupUI, provider, userPath string, profile config.ProviderProfile, draft *providerDraft) (string, string, tui.SetupNavResult, error) {
	key, nav, err := ui.PromptLineNav(provider+" API key", "", true)
	if err != nil || nav != tui.SetupAccepted {
		return "", "", nav, err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", tui.SetupBack, errors.New("API key must not be empty")
	}
	defaultPath, err := config.DefaultAPIKeyPath(provider)
	if err != nil {
		return "", "", tui.SetupCancelled, err
	}
	if draft.apiKeyPath != nil && strings.TrimSpace(*draft.apiKeyPath) != "" {
		defaultPath = *draft.apiKeyPath
	} else if profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != "" {
		defaultPath = *profile.APIKeyPath
	}
	keyPath, nav, err := ui.PromptLineNav("Save API key to file", defaultPath, false)
	if err != nil || nav != tui.SetupAccepted {
		return "", "", nav, err
	}
	resolvedKeyPath, err := config.ResolveAPIKeyPath(strings.TrimSpace(keyPath), filepath.Dir(userPath))
	if err != nil {
		return "", "", tui.SetupCancelled, err
	}
	return key, resolvedKeyPath, tui.SetupAccepted, nil
}

func verifyAndSelectModel(ui setupUI, state *setupState, provider string, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (tui.SetupNavResult, error) {
	var models []string
	var thinkingCap func(string) llm.ThinkingCapability
	var metadata llm.ModelMetadataProvider
	manualModel := false

	for {
		overrideBase, overrideKey := state.effectiveConnection(provider)
		if provider == llm.OllamaCloudKind {
			fmt.Fprintln(stdout, "Fetching Ollama Cloud models (catalog access may not validate the API key)...")
		} else {
			fmt.Fprintf(stdout, "Verifying %s connection and fetching models...\n", provider)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		res, probeErr := deps.probeModels(ctx, state.initial, provider, overrideBase, overrideKey, policy)
		cancel()
		if probeErr == nil {
			models = res.models
			thinkingCap = res.thinkingCap
			metadata = res.metadata
			break
		}

		msg := probeErr.Error()
		if policy != nil {
			msg = policy.Text(redaction.Terminal, msg)
		}
		fmt.Fprintf(stdout, "Connection check failed: %s\n", msg)

		action, nav, selErr := ui.SelectOptionNav(
			"connection failure action",
			[]string{
				"Retry connection",
				"Re-enter credentials & endpoint",
				"Enter model name manually (skip live check)",
				backMenuItem,
			},
			nil,
			"Retry connection",
			false,
		)
		if selErr != nil {
			return tui.SetupCancelled, selErr
		}
		if nav == tui.SetupCancelled {
			return tui.SetupCancelled, nil
		}
		if nav == tui.SetupBack || action == backMenuItem {
			return tui.SetupBack, nil
		}
		if action == "Re-enter credentials & endpoint" {
			credNav, err := configureProviderCredentials(ui, state, provider, stdin, stdout, stderr, policy, deps)
			if err != nil {
				return tui.SetupCancelled, err
			}
			if credNav == tui.SetupCancelled {
				return tui.SetupCancelled, nil
			}
			continue
		}
		if action == "Enter model name manually (skip live check)" {
			manualModel = true
			break
		}
	}

	currentModel, currentThinking := state.effectiveModel(provider)
	draft := state.draft(provider)

	for {
		var chosenModel string
		if manualModel || len(models) == 0 {
			m, nav, err := ui.PromptLineNav(provider+" model", currentModel, false)
			if err != nil {
				return tui.SetupCancelled, err
			}
			if nav != tui.SetupAccepted {
				return nav, nil
			}
			m = strings.TrimSpace(m)
			if m == "" {
				return tui.SetupBack, errors.New("model must not be empty")
			}
			chosenModel = m
		} else {
			m, nav, err := ui.SelectModelNav(models, currentModel)
			if err != nil {
				return tui.SetupCancelled, err
			}
			if nav != tui.SetupAccepted {
				return nav, nil
			}
			chosenModel = m
		}

		if metadata != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			metadata.RefreshModelMetadata(ctx, chosenModel)
			cancel()
		}
		if thinkingCap != nil {
			cap := thinkingCap(chosenModel)
			if cap.Adjustable && len(cap.Levels) > 0 {
				defaultThink := currentThinking
				if defaultThink == "" {
					defaultThink = cap.Default
				}
				levels := cap.Levels
				if provider == llm.OllamaCloudKind {
					levels = append([]string{tui.ModelDefaultThinking}, levels...)
					if !thinkingAllowed(cap, currentThinking) || currentThinking == "" {
						defaultThink = tui.ModelDefaultThinking
					}
				}
				chosenThink, nav, err := ui.SelectThinkingNav(levels, defaultThink)
				if chosenThink == tui.ModelDefaultThinking {
					chosenThink = ""
				}
				if err != nil {
					return tui.SetupCancelled, err
				}
				if nav == tui.SetupCancelled {
					return tui.SetupCancelled, nil
				}
				if nav == tui.SetupBack {
					// Step back from thinking picker to model picker.
					currentModel = chosenModel
					continue
				}
				draft.thinking = chosenThink
				draft.thinkingChanged = true
			}
		}
		if provider == llm.OllamaCloudKind && (thinkingCap == nil || !thinkingCap(chosenModel).Adjustable) {
			draft.thinking, draft.thinkingChanged = "", true
		}

		draft.model = chosenModel
		draft.modelChanged = true
		return tui.SetupAccepted, nil
	}
}

func runSandboxMenu(ui setupUI, state *setupState) (bool, error) {
	currentItem := ""
	for {
		sbLabel := "Enable sandbox"
		if state.effectiveSandbox() {
			sbLabel = "Disable sandbox"
		}
		detected := detectExistingPaths(defaultSandboxCandidatePaths)
		paths := state.effectiveSandboxPaths()
		pathsLabel := fmt.Sprintf("sandbox_command_paths (%d configured, %d detected)", len(paths), len(detected))

		options := []string{sbLabel}
		if len(detected) > 0 || len(paths) > 0 {
			options = append(options, pathsLabel)
		}
		options = append(options, backMenuItem)

		choice, nav, err := ui.SelectOptionNav("Linux sandbox", options, nil, currentItem, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupBack || choice == backMenuItem {
			return false, nil
		}
		currentItem = choice

		switch choice {
		case sbLabel:
			v := !state.effectiveSandbox()
			state.sandbox = &v
		case pathsLabel:
			var subOpts []string
			useDetected := fmt.Sprintf("Use detected executable paths (%d found)", len(detected))
			if len(detected) > 0 {
				subOpts = append(subOpts, useDetected)
			}
			if len(paths) > 0 {
				subOpts = append(subOpts, "Clear sandbox_command_paths")
			}
			subOpts = append(subOpts, backMenuItem)
			subChoice, subNav, err := ui.SelectOptionNav("sandbox_command_paths", subOpts, nil, subOpts[0], false)
			if err != nil {
				return false, err
			}
			if subNav == tui.SetupCancelled {
				return true, nil
			}
			if subNav == tui.SetupAccepted {
				if subChoice == useDetected {
					state.sandboxCommandPaths = detected
					state.sandboxPathsChanged = true
				} else if subChoice == "Clear sandbox_command_paths" {
					state.sandboxCommandPaths = nil
					state.sandboxPathsChanged = true
				}
			}
		}
	}
}

func runBehaviorMenu(ui setupUI, state *setupState) (bool, error) {
	currentItem := ""
	for {
		intState := "disabled"
		if state.effectiveInteractive() {
			intState = "enabled"
		}
		intLabel := fmt.Sprintf("Toggle interactive questions (currently: %s)", intState)

		choice, nav, err := ui.SelectOptionNav("behavior", []string{intLabel, backMenuItem}, nil, currentItem, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupBack || choice == backMenuItem {
			return false, nil
		}
		currentItem = choice

		if choice == intLabel {
			v := !state.effectiveInteractive()
			state.interactive = &v
		}
	}
}

func applySetupState(writer *config.RuntimePreferenceWriter, state *setupState, deps setupDeps) error {
	for name, server := range state.mcpServers {
		if _, err := writer.AddMCPServer(name, server); err != nil {
			return err
		}
	}
	for provider, d := range state.providers {
		if d.baseURL != nil || d.apiKeyPath != nil {
			if err := writer.PersistProviderConnection(provider, d.baseURL, d.apiKeyPath); err != nil {
				return err
			}
		}
		if d.apiKey != "" && d.apiKeyPath != nil {
			if _, err := deps.writeKeyFile(*d.apiKeyPath, d.apiKey); err != nil {
				return err
			}
		}
		if d.modelChanged || d.thinkingChanged {
			model, thinking := state.effectiveModel(provider)
			if model != "" {
				if provider == state.activeProvider && state.providerChanged {
					if err := writer.PersistProvider(provider, model, thinking); err != nil {
						return err
					}
				} else {
					if err := writer.PersistModel(provider, model, thinking); err != nil {
						return err
					}
				}
			}
		}
	}
	if state.providerChanged {
		d := state.providers[state.activeProvider]
		if d == nil || (!d.modelChanged && !d.thinkingChanged) {
			model, thinking := state.effectiveModel(state.activeProvider)
			if model == "" {
				if err := writer.PersistProviderConnection(state.activeProvider, nil, nil); err != nil {
					return err
				}
			} else {
				if err := writer.PersistProvider(state.activeProvider, model, thinking); err != nil {
					return err
				}
			}
		}
	}
	if state.themeChanged && state.theme != "" {
		if err := writer.PersistTheme(state.theme); err != nil {
			return err
		}
	}
	if state.sandbox != nil {
		if err := writer.PersistSandbox(*state.sandbox); err != nil {
			return err
		}
	}
	if state.sandboxPathsChanged {
		if err := writer.PersistSandboxCommandPaths(state.sandboxCommandPaths); err != nil {
			return err
		}
	}
	if state.interactive != nil {
		if err := writer.PersistInteractive(*state.interactive); err != nil {
			return err
		}
	}
	return nil
}

func printSetupSummary(out io.Writer, userPath string, state *setupState) {
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Saved setup to %s:\n", userPath)
	for name := range state.mcpServers {
		fmt.Fprintf(out, "  MCP server: %s\n", name)
	}
	if state.providerChanged {
		fmt.Fprintf(out, "  provider: %s\n", state.activeProvider)
	}
	for _, p := range setupBuiltInProviders {
		d, ok := state.providers[p]
		if !ok {
			continue
		}
		if d.baseURL != nil {
			if *d.baseURL == "" {
				fmt.Fprintf(out, "  providers.%s.base_url: (reset to default)\n", p)
			} else {
				fmt.Fprintf(out, "  providers.%s.base_url: %s\n", p, *d.baseURL)
			}
		}
		if d.apiKeyPath != nil {
			fmt.Fprintf(out, "  providers.%s.api_key_path: %s (0600)\n", p, *d.apiKeyPath)
		}
		if d.modelChanged {
			fmt.Fprintf(out, "  providers.%s.model: %s\n", p, d.model)
		}
		if d.thinkingChanged {
			fmt.Fprintf(out, "  providers.%s.thinking: %s\n", p, d.thinking)
		}
	}
	if state.themeChanged && state.theme != "" {
		fmt.Fprintf(out, "  theme: %s\n", state.theme)
	}
	if state.sandbox != nil {
		fmt.Fprintf(out, "  sandbox: %v\n", *state.sandbox)
	}
	if state.sandboxPathsChanged {
		fmt.Fprintf(out, "  sandbox_command_paths: %d path(s)\n", len(state.sandboxCommandPaths))
	}
	if state.interactive != nil {
		fmt.Fprintf(out, "  interactive: %v\n", *state.interactive)
	}
}

func registerSetupSecret(policy *redaction.Policy, stderr io.Writer, secret string) {
	if strings.TrimSpace(secret) == "" {
		return
	}
	if policy != nil {
		policy.RegisterSecrets(secret)
	}
	if writer, ok := stderr.(*redaction.DiagnosticWriter); ok && writer.Policy != nil {
		writer.Policy.RegisterSecrets(secret)
	}
}

func isProviderConfigured(cfg config.Config, name string) bool {
	if name == "chatgpt" {
		oauthPath := cfg.ChatGPT.OAuthPath
		if oauthPath == "" {
			oauthPath = chatgptoauth.DefaultPath
		}
		tokens, err := chatgptoauth.Load(oauthPath)
		if err != nil || tokens.AccessToken == "" {
			return false
		}
		if tokens.RefreshToken == "" && tokens.NeedsRefresh(time.Now()) {
			return false
		}
		return true
	}
	base, key := cfg.ProviderConnection(name)
	profile := cfg.Providers[name]
	if name == "ollama" {
		return strings.TrimSpace(base) != "" || (profile.Model != nil && strings.TrimSpace(*profile.Model) != "")
	}
	if name == llm.OllamaCloudKind && config.ProviderEnvironmentAPIKey(name) != "" {
		return true
	}
	return strings.TrimSpace(key) != "" || (profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != "")
}

func defaultProbeProviderModels(ctx context.Context, cfg config.Config, provider, overrideBaseURL, overrideAPIKey string, policy *redaction.Policy) (setupProbeResult, error) {
	builder := providerBuilder{
		config:         cfg,
		launchProvider: provider,
		launch: llm.Config{
			BaseURL: overrideBaseURL,
			APIKey:  overrideAPIKey,
		},
		policy: policy,
	}
	if provider == llm.OllamaCloudKind && cfg.DangerSkipTLSVerify != nil {
		builder.launch.InsecureSkipVerify = *cfg.DangerSkipTLSVerify
	}
	var endpoint *string
	if overrideBaseURL != "" {
		endpoint = &overrideBaseURL
	}
	prepared, err := builder.prepare(ctx, provider, endpoint)
	if err != nil {
		return setupProbeResult{}, err
	}
	var models []string
	if lister, ok := prepared.Provider.(llm.ModelLister); ok {
		models, err = lister.Models(ctx)
		if err != nil {
			return setupProbeResult{}, err
		}
	}
	var thinkingCap func(string) llm.ThinkingCapability
	if tp, ok := prepared.Provider.(llm.ThinkingProvider); ok {
		thinkingCap = tp.ThinkingCapability
	}
	metadata, _ := prepared.Provider.(llm.ModelMetadataProvider)
	return setupProbeResult{
		metadata:    metadata,
		models:      models,
		thinkingCap: thinkingCap,
	}, nil
}

func detectExistingPaths(candidates []string) []string {
	home, _ := os.UserHomeDir()
	var found []string
	for _, raw := range candidates {
		expanded := raw
		if strings.HasPrefix(raw, "~/") && home != "" {
			expanded = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
		if info, err := os.Stat(expanded); err == nil && info.IsDir() {
			found = append(found, raw)
		}
	}
	return found
}
