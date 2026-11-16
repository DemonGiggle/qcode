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
	"qcode/internal/redaction"
	"qcode/internal/tui"
)

const keepOriginalPrefix = "Keep original config"

var setupBuiltInProviders = []string{"ollama", "openai", "opencode-go", "gemini", "chatgpt"}

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

type setupUI interface {
	SelectOption(noun string, options []string, disabled map[string]bool, current string, searchable bool) (string, bool, error)
	SelectModel(models []string, current string) (string, bool, error)
	SelectThinking(levels []string, current string) (string, bool, error)
	SelectTheme(current string) (string, bool, error)
	PromptLine(label, defaultVal string, secret bool) (string, bool, error)
}

type setupProbeResult struct {
	models      []string
	thinkingCap func(string) llm.ThinkingCapability
}

type setupDeps struct {
	newUI        func(in io.Reader, out io.Writer) setupUI
	loadConfig   func() (config.Config, []string, []error, error)
	userCfgPath  func() (string, error)
	newWriter    func() (*config.RuntimePreferenceWriter, error)
	writeKeyFile func(path, key string) (string, error)
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
	fmt.Fprintln(stdout, "qcode setup wizard")
	fmt.Fprintf(stdout, "User config: %s\n\n", userPath)

	plan, cancelled, err := runBasicSetup(ui, cfg, userPath, stdin, stdout, stderr, policy, deps)
	if err != nil {
		return err
	}
	if cancelled {
		fmt.Fprintln(stdout, "Setup cancelled; no changes were saved.")
		return nil
	}

	advChoice, accepted, err := ui.SelectOption(
		"next step",
		[]string{
			"Finish setup and save",
			"Continue to Advanced Setup (theme, sandbox, autoreview, interactive)",
		},
		nil,
		"Finish setup and save",
		false,
	)
	if err != nil {
		return err
	}
	if !accepted {
		fmt.Fprintln(stdout, "Setup cancelled; no changes were saved.")
		return nil
	}
	if strings.HasPrefix(advChoice, "Continue to Advanced Setup") {
		advCancelled, err := runAdvancedSetup(ui, cfg, &plan, deps.goos)
		if err != nil {
			return err
		}
		if advCancelled {
			fmt.Fprintln(stdout, "Setup cancelled; no changes were saved.")
			return nil
		}
	}

	if err := applySetupPlan(writer, &plan, deps); err != nil {
		return err
	}

	printSetupSummary(stdout, userPath, &plan)

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

type setupPlan struct {
	provider        string
	keepProvider    bool
	baseURL         *string
	apiKey          string
	apiKeyPath      *string
	model           string
	keepModel       bool
	thinking        string
	keepThinking    bool
	thinkingCleared bool

	theme               string
	keepTheme           bool
	sandbox             *bool
	sandboxCommandPaths []string
	setSandboxPaths     bool
	autoreview          *bool
	interactive         *bool
}

func runBasicSetup(ui setupUI, cfg config.Config, userPath string, stdin io.Reader, stdout, stderr io.Writer, policy *redaction.Policy, deps setupDeps) (setupPlan, bool, error) {
	var plan setupPlan

	// 1. Provider selection
	providerOptions := make([]string, 0, len(setupBuiltInProviders)+1)
	labelToProvider := make(map[string]string, len(setupBuiltInProviders)+1)
	currentProvider := strings.TrimSpace(cfg.Provider)
	var keepProviderLabel string
	if currentProvider != "" {
		keepProviderLabel = fmt.Sprintf("%s (%s)", keepOriginalPrefix, currentProvider)
		providerOptions = append(providerOptions, keepProviderLabel)
		labelToProvider[keepProviderLabel] = currentProvider
	}
	defaultChoice := ""
	for _, p := range setupBuiltInProviders {
		label := p
		if isProviderConfigured(cfg, p) {
			label = p + " (configured)"
		}
		if p == currentProvider {
			defaultChoice = label
		}
		providerOptions = append(providerOptions, label)
		labelToProvider[label] = p
	}
	if keepProviderLabel != "" {
		defaultChoice = keepProviderLabel
	}

	chosenLabel, accepted, err := ui.SelectOption("provider", providerOptions, nil, defaultChoice, false)
	if err != nil {
		return plan, false, err
	}
	if !accepted {
		return plan, true, nil
	}
	if chosenLabel == keepProviderLabel {
		plan.keepProvider = true
		plan.provider = currentProvider
	} else {
		plan.provider = labelToProvider[chosenLabel]
	}

	// 2. Credentials & Endpoint
	overrideBaseURL, overrideAPIKey, cancelled, err := configureProviderCredentials(ui, cfg, userPath, &plan, stdin, stdout, deps)
	if err != nil || cancelled {
		return plan, cancelled, err
	}
	registerSetupSecret(policy, stderr, overrideAPIKey)

	// 3. Live verification & Model selection
	var models []string
	var thinkingCap func(string) llm.ThinkingCapability
	manualModel := false

	for {
		fmt.Fprintf(stdout, "Verifying %s connection and fetching models...\n", plan.provider)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		res, probeErr := deps.probeModels(ctx, cfg, plan.provider, overrideBaseURL, overrideAPIKey, policy)
		cancel()
		if probeErr == nil {
			models = res.models
			thinkingCap = res.thinkingCap
			break
		}

		msg := probeErr.Error()
		if policy != nil {
			msg = policy.Text(redaction.Terminal, msg)
		}
		fmt.Fprintf(stdout, "Connection check failed: %s\n", msg)

		action, ok, selErr := ui.SelectOption(
			"connection failure action",
			[]string{
				"Retry connection",
				"Re-enter credentials / endpoint",
				"Enter model name manually (skip live check)",
				"Cancel setup",
			},
			nil,
			"Retry connection",
			false,
		)
		if selErr != nil {
			return plan, false, selErr
		}
		if !ok || action == "Cancel setup" {
			return plan, true, nil
		}
		if action == "Re-enter credentials / endpoint" {
			overrideBaseURL, overrideAPIKey, cancelled, err = configureProviderCredentials(ui, cfg, userPath, &plan, stdin, stdout, deps)
			if err != nil || cancelled {
				return plan, cancelled, err
			}
			registerSetupSecret(policy, stderr, overrideAPIKey)
			continue
		}
		if action == "Enter model name manually (skip live check)" {
			manualModel = true
			break
		}
	}

	// 4. Model selection
	currentModel, currentThinking := cfg.EffectiveModel(plan.provider)
	if currentModel != "" {
		keepModelLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, currentModel)
		choice, ok, err := ui.SelectOption(
			"model action",
			[]string{keepModelLabel, "Select a different model"},
			nil,
			keepModelLabel,
			false,
		)
		if err != nil {
			return plan, false, err
		}
		if !ok {
			return plan, true, nil
		}
		if choice == keepModelLabel {
			plan.keepModel = true
			plan.model = currentModel
		}
	}

	if !plan.keepModel {
		if manualModel || len(models) == 0 {
			m, ok, err := ui.PromptLine(fmt.Sprintf("%s model", plan.provider), currentModel, false)
			if err != nil {
				return plan, false, err
			}
			if !ok {
				return plan, true, nil
			}
			m = strings.TrimSpace(m)
			if m == "" {
				return plan, false, errors.New("model must not be empty")
			}
			plan.model = m
		} else {
			m, ok, err := ui.SelectModel(models, currentModel)
			if err != nil {
				return plan, false, err
			}
			if !ok {
				return plan, true, nil
			}
			plan.model = m
		}
	}

	// 5. Thinking level selection
	thinkingSelected := false
	if thinkingCap != nil {
		cap := thinkingCap(plan.model)
		if cap.Adjustable && len(cap.Levels) > 0 {
			thinkingSelected = true
			levels := make([]string, 0, len(cap.Levels)+1)
			keepThinkLabel := ""
			if currentThinking != "" {
				keepThinkLabel = fmt.Sprintf("%s (%s)", keepOriginalPrefix, currentThinking)
				levels = append(levels, keepThinkLabel)
			}
			levels = append(levels, cap.Levels...)
			defaultThink := cap.Default
			if keepThinkLabel != "" {
				defaultThink = keepThinkLabel
			}
			chosenThink, ok, err := ui.SelectThinking(levels, defaultThink)
			if err != nil {
				return plan, false, err
			}
			if !ok {
				return plan, true, nil
			}
			if keepThinkLabel != "" && chosenThink == keepThinkLabel {
				plan.keepThinking = true
				plan.thinking = currentThinking
			} else {
				plan.thinking = chosenThink
			}
		}
	}
	if !thinkingSelected && currentThinking != "" {
		plan.keepThinking = true
		plan.thinking = currentThinking
	}

	return plan, false, nil
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

func configureProviderCredentials(ui setupUI, cfg config.Config, userPath string, plan *setupPlan, stdin io.Reader, stdout io.Writer, deps setupDeps) (overrideBaseURL, overrideAPIKey string, cancelled bool, err error) {
	existingBase, existingKey := cfg.ProviderConnection(plan.provider)
	profile := cfg.Providers[plan.provider]

	switch plan.provider {
	case "chatgpt":
		if isProviderConfigured(cfg, "chatgpt") {
			keepLabel := fmt.Sprintf("%s (ChatGPT OAuth configured)", keepOriginalPrefix)
			choice, ok, err := ui.SelectOption(
				"ChatGPT authentication",
				[]string{keepLabel, "Re-authenticate with ChatGPT"},
				nil,
				keepLabel,
				false,
			)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			if choice == keepLabel {
				return "", "", false, nil
			}
		}
		oauthPath := cfg.ChatGPT.OAuthPath
		if oauthPath == "" {
			oauthPath = chatgptoauth.DefaultPath
		}
		if err := deps.chatgptLogin(oauthPath, stdin, stdout); err != nil {
			return "", "", false, err
		}
		return "", "", false, nil

	case "ollama":
		if profile.BaseURL != nil && strings.TrimSpace(*profile.BaseURL) != "" {
			keepLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, *profile.BaseURL)
			choice, ok, err := ui.SelectOption(
				"Ollama base URL",
				[]string{keepLabel, "Change Ollama base URL"},
				nil,
				keepLabel,
				false,
			)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			if choice == keepLabel {
				return *profile.BaseURL, "", false, nil
			}
		}
		defURL := existingBase
		if defURL == "" {
			defURL = "http://127.0.0.1:11434"
		}
		url, ok, err := ui.PromptLine("Ollama base URL", defURL, false)
		if err != nil {
			return "", "", false, err
		}
		if !ok {
			return "", "", true, nil
		}
		url = strings.TrimSpace(url)
		if profile.BaseURL != nil || url != defURL {
			plan.baseURL = &url
		}
		return url, "", false, nil

	default: // openai, opencode-go, gemini
		needNewKey := true
		if existingKey != "" || (profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != "") {
			keepLabel := fmt.Sprintf("%s (API key configured)", keepOriginalPrefix)
			choice, ok, err := ui.SelectOption(
				fmt.Sprintf("%s API key", plan.provider),
				[]string{keepLabel, "Enter a new API key (saved to 0600 key file)"},
				nil,
				keepLabel,
				false,
			)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			if choice == keepLabel {
				needNewKey = false
				overrideAPIKey = existingKey
			}
		}
		if needNewKey {
			key, ok, err := ui.PromptLine(fmt.Sprintf("%s API key", plan.provider), "", true)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return "", "", false, errors.New("API key must not be empty")
			}
			defaultPath, err := config.DefaultAPIKeyPath(plan.provider)
			if err != nil {
				return "", "", false, err
			}
			if profile.APIKeyPath != nil && strings.TrimSpace(*profile.APIKeyPath) != "" {
				defaultPath = *profile.APIKeyPath
			}
			keyPath, ok, err := ui.PromptLine("Save API key to file", defaultPath, false)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			keyPath = strings.TrimSpace(keyPath)
			resolvedKeyPath, err := config.ResolveAPIKeyPath(keyPath, filepath.Dir(userPath))
			if err != nil {
				return "", "", false, err
			}
			plan.apiKey = key
			plan.apiKeyPath = &resolvedKeyPath
			overrideAPIKey = key
		}

		// Optional custom base URL
		if profile.BaseURL != nil && strings.TrimSpace(*profile.BaseURL) != "" {
			keepLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, *profile.BaseURL)
			choice, ok, err := ui.SelectOption(
				fmt.Sprintf("%s base URL", plan.provider),
				[]string{keepLabel, "Set custom base URL", "Reset to provider default"},
				nil,
				keepLabel,
				false,
			)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			switch choice {
			case keepLabel:
				overrideBaseURL = *profile.BaseURL
			case "Reset to provider default":
				empty := ""
				plan.baseURL = &empty
				overrideBaseURL = ""
			default:
				u, ok, err := ui.PromptLine(fmt.Sprintf("%s base URL", plan.provider), *profile.BaseURL, false)
				if err != nil || !ok {
					return "", "", !ok, err
				}
				u = strings.TrimSpace(u)
				plan.baseURL = &u
				overrideBaseURL = u
			}
		} else {
			choice, ok, err := ui.SelectOption(
				fmt.Sprintf("%s endpoint", plan.provider),
				[]string{"Use default endpoint", "Set custom base URL"},
				nil,
				"Use default endpoint",
				false,
			)
			if err != nil {
				return "", "", false, err
			}
			if !ok {
				return "", "", true, nil
			}
			if choice == "Set custom base URL" {
				u, ok, err := ui.PromptLine(fmt.Sprintf("%s base URL", plan.provider), existingBase, false)
				if err != nil || !ok {
					return "", "", !ok, err
				}
				u = strings.TrimSpace(u)
				if u != "" {
					plan.baseURL = &u
					overrideBaseURL = u
				}
			} else {
				overrideBaseURL = existingBase
			}
		}
		return overrideBaseURL, overrideAPIKey, false, nil
	}
}

func runAdvancedSetup(ui setupUI, cfg config.Config, plan *setupPlan, goos string) (bool, error) {
	// 1. Theme
	currentTheme := strings.TrimSpace(cfg.Theme)
	themeDisplay := currentTheme
	if themeDisplay == "" {
		themeDisplay = "default"
	} else {
		themeDisplay += " (configured)"
	}
	keepThemeLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, themeDisplay)
	themeAction, ok, err := ui.SelectOption(
		"terminal theme",
		[]string{keepThemeLabel, "Choose a theme (live preview)"},
		nil,
		keepThemeLabel,
		false,
	)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if themeAction == keepThemeLabel {
		plan.keepTheme = true
	} else {
		chosenTheme, ok, err := ui.SelectTheme(cfg.Theme)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		plan.theme = chosenTheme
	}

	// 2. Sandbox (Linux only)
	if goos == "linux" {
		sbDisplay := "default: disabled"
		if cfg.Sandbox != nil {
			if *cfg.Sandbox {
				sbDisplay = "configured: enabled"
			} else {
				sbDisplay = "configured: disabled"
			}
		}
		keepSandboxLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, sbDisplay)
		sbChoice, ok, err := ui.SelectOption(
			"Linux bubblewrap sandbox",
			[]string{keepSandboxLabel, "Enable sandbox", "Disable sandbox"},
			nil,
			keepSandboxLabel,
			false,
		)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
		sandboxEnabled := cfg.Sandbox != nil && *cfg.Sandbox
		if sbChoice == "Enable sandbox" {
			v := true
			plan.sandbox = &v
			sandboxEnabled = true
		} else if sbChoice == "Disable sandbox" {
			v := false
			plan.sandbox = &v
			sandboxEnabled = false
		}

		if sandboxEnabled {
			detected := detectExistingPaths(defaultSandboxCandidatePaths)
			if len(cfg.SandboxCommandPaths) > 0 || len(detected) > 0 {
				var pathOpts []string
				keepPathsLabel := ""
				if len(cfg.SandboxCommandPaths) > 0 {
					keepPathsLabel = fmt.Sprintf("%s (%d path(s) configured)", keepOriginalPrefix, len(cfg.SandboxCommandPaths))
					pathOpts = append(pathOpts, keepPathsLabel)
				}
				useDetectedLabel := fmt.Sprintf("Use detected executable paths (%d found)", len(detected))
				if len(detected) > 0 {
					pathOpts = append(pathOpts, useDetectedLabel)
				}
				pathOpts = append(pathOpts, "Skip / leave empty")

				defaultPathOpt := pathOpts[0]
				pathChoice, ok, err := ui.SelectOption("sandbox_command_paths", pathOpts, nil, defaultPathOpt, false)
				if err != nil {
					return false, err
				}
				if !ok {
					return true, nil
				}
				if pathChoice == useDetectedLabel {
					plan.sandboxCommandPaths = detected
					plan.setSandboxPaths = true
				}
			}
		}
	}

	// 3. Autoreview
	arDisplay := "default: enabled"
	if cfg.AutoReview != nil {
		if *cfg.AutoReview {
			arDisplay = "configured: enabled"
		} else {
			arDisplay = "configured: disabled"
		}
	}
	keepARLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, arDisplay)
	arChoice, ok, err := ui.SelectOption(
		"automatic code review (autoreview)",
		[]string{keepARLabel, "Enable autoreview", "Disable autoreview"},
		nil,
		keepARLabel,
		false,
	)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if arChoice == "Enable autoreview" {
		v := true
		plan.autoreview = &v
	} else if arChoice == "Disable autoreview" {
		v := false
		plan.autoreview = &v
	}

	// 4. Interactive questions
	intDisplay := "default: disabled"
	if cfg.Interactive != nil {
		if *cfg.Interactive {
			intDisplay = "configured: enabled"
		} else {
			intDisplay = "configured: disabled"
		}
	}
	keepIntLabel := fmt.Sprintf("%s (%s)", keepOriginalPrefix, intDisplay)
	intChoice, ok, err := ui.SelectOption(
		"interactive clarifying questions",
		[]string{keepIntLabel, "Enable interactive questions", "Disable interactive questions"},
		nil,
		keepIntLabel,
		false,
	)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	if intChoice == "Enable interactive questions" {
		v := true
		plan.interactive = &v
	} else if intChoice == "Disable interactive questions" {
		v := false
		plan.interactive = &v
	}

	return false, nil
}

func applySetupPlan(writer *config.RuntimePreferenceWriter, plan *setupPlan, deps setupDeps) error {
	if plan.baseURL != nil || plan.apiKeyPath != nil {
		if err := writer.PersistProviderConnection(plan.provider, plan.baseURL, plan.apiKeyPath); err != nil {
			return err
		}
	}
	if plan.apiKey != "" && plan.apiKeyPath != nil {
		if _, err := deps.writeKeyFile(*plan.apiKeyPath, plan.apiKey); err != nil {
			return err
		}
	}
	if !plan.keepProvider || !plan.keepModel || (!plan.keepThinking && plan.thinking != "") {
		if plan.keepProvider {
			if !plan.keepModel || !plan.keepThinking {
				if err := writer.PersistModel(plan.provider, plan.model, plan.thinking); err != nil {
					return err
				}
			}
		} else {
			if err := writer.PersistProvider(plan.provider, plan.model, plan.thinking); err != nil {
				return err
			}
		}
	}
	if !plan.keepTheme && plan.theme != "" {
		if err := writer.PersistTheme(plan.theme); err != nil {
			return err
		}
	}
	if plan.sandbox != nil {
		if err := writer.PersistSandbox(*plan.sandbox); err != nil {
			return err
		}
	}
	if plan.setSandboxPaths {
		if err := writer.PersistSandboxCommandPaths(plan.sandboxCommandPaths); err != nil {
			return err
		}
	}
	if plan.autoreview != nil {
		if err := writer.PersistAutoreview(*plan.autoreview); err != nil {
			return err
		}
	}
	if plan.interactive != nil {
		if err := writer.PersistInteractive(*plan.interactive); err != nil {
			return err
		}
	}
	return nil
}

func printSetupSummary(out io.Writer, userPath string, plan *setupPlan) {
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "Saved setup to %s:\n", userPath)
	if plan.keepProvider {
		fmt.Fprintf(out, "  provider: %s (kept original)\n", plan.provider)
	} else {
		fmt.Fprintf(out, "  provider: %s\n", plan.provider)
	}
	if plan.baseURL != nil {
		if *plan.baseURL == "" {
			fmt.Fprintln(out, "  base_url: (reset to default)")
		} else {
			fmt.Fprintf(out, "  base_url: %s\n", *plan.baseURL)
		}
	}
	if plan.apiKeyPath != nil {
		fmt.Fprintf(out, "  api_key_path: %s (0600)\n", *plan.apiKeyPath)
	}
	if plan.keepModel {
		fmt.Fprintf(out, "  model: %s (kept original)\n", plan.model)
	} else {
		fmt.Fprintf(out, "  model: %s\n", plan.model)
	}
	if !plan.keepThinking && plan.thinking != "" {
		fmt.Fprintf(out, "  thinking: %s\n", plan.thinking)
	}
	if !plan.keepTheme && plan.theme != "" {
		fmt.Fprintf(out, "  theme: %s\n", plan.theme)
	}
	if plan.sandbox != nil {
		fmt.Fprintf(out, "  sandbox: %v\n", *plan.sandbox)
	}
	if plan.setSandboxPaths {
		fmt.Fprintf(out, "  sandbox_command_paths: %d path(s)\n", len(plan.sandboxCommandPaths))
	}
	if plan.autoreview != nil {
		fmt.Fprintf(out, "  autoreview: %v\n", *plan.autoreview)
	}
	if plan.interactive != nil {
		fmt.Fprintf(out, "  interactive: %v\n", *plan.interactive)
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
	return setupProbeResult{
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
