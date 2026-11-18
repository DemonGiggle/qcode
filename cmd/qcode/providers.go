package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/redaction"
)

func isGeminiOfficialEndpoint(endpoint string) bool {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	return endpoint == "" || endpoint == "https://generativelanguage.googleapis.com/v1beta/openai" || endpoint == "https://generativelanguage.googleapis.com/v1beta"
}

// providerBuilder owns the launch snapshot. Generic flags/environment overrides
// belong to the launch provider; a different backend must resolve its own profile.
// Credentials are never part of an agent checkpoint or a creation request.
type providerBuilder struct {
	config          config.Config
	launchProvider  string
	launch          llm.Config
	policy          *redaction.Policy
	warnings        io.Writer
	loadMetadata    func(context.Context, string, string, llm.HTTPDoer) (*llm.ProviderModelMeta, error)
	refreshMetadata func(context.Context, string, string, llm.HTTPDoer) (*llm.ProviderModelMeta, error)
}

type preparedProvider struct {
	Provider            llm.Provider
	Endpoint            string
	MissingThinkingMeta bool
}

func (b *providerBuilder) connection(name string, endpoint *string) llm.Config {
	base, key := b.config.ProviderConnection(name)
	if name == b.launchProvider {
		base, key = b.launch.BaseURL, b.launch.APIKey
	} else {
		if value := config.ProviderEnvironmentAPIKey(providerKind(b.config, name)); value != "" {
			key = value
		}
	}
	if endpoint != nil {
		base = *endpoint // An empty saved endpoint means the provider's default.
	}
	return llm.Config{InstanceName: name, BaseURL: base, APIKey: key, HTTP: b.launch.HTTP,
		InsecureSkipVerify: b.launch.InsecureSkipVerify, ChatGPTOAuthPath: b.config.ChatGPT.OAuthPath,
		RegisterSecrets: b.policy.RegisterSecrets}
}

// availability reports whether name can be selected without additional setup.
// It mirrors prepare's credential checks without network or model-metadata
// work so the /provider picker can disable unconfigured backends. The hint
// describes the setup step when unavailable. api_key_path files are already
// resolved into the connection during config load, so an empty key here means
// neither inline, file, nor env provided one.
func (b *providerBuilder) availability(name string) (bool, string) {
	kind, err := resolveProviderKind(b.config, name)
	if err != nil {
		return false, err.Error()
	}
	connection := b.connection(name, nil)
	if kind == "chatgpt" {
		if connection.APIKey != "" {
			return false, fmt.Sprintf("provider %s (chatgpt) uses ChatGPT OAuth (qcode login chatgpt); clear api-key", name)
		}
		if tokens, err := chatgptoauth.Load(connection.ChatGPTOAuthPath); err != nil {
			return false, "run `qcode login chatgpt` to connect a ChatGPT subscription"
		} else if tokens.RefreshToken == "" && tokens.NeedsRefresh(time.Now()) {
			return false, "chatgpt token expired and no refresh token; run `qcode login chatgpt` again"
		} else if tokens.AccessToken == "" {
			return false, "run `qcode login chatgpt` to connect a ChatGPT subscription"
		}
		return true, ""
	}
	if kind == "gemini" && isGeminiOfficialEndpoint(connection.BaseURL) && connection.APIKey == "" {
		return false, fmt.Sprintf("provider %s (gemini) requires an API key: set [providers.%s].api_key_path (preferred), GEMINI_API_KEY, GOOGLE_API_KEY, QCODE_API_KEY, or --api-key", name, name)
	}
	if (kind == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(kind == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL)) {
		if connection.APIKey == "" {
			return false, fmt.Sprintf("provider %s ("+kind+") requires an API key: set [providers.%s].api_key_path (preferred), QCODE_API_KEY, or a launch API-key override", name, name)
		}
	}
	return true, ""
}

func (b *providerBuilder) prepare(ctx context.Context, name string, endpoint *string) (result preparedProvider, err error) {
	defer func() { err = b.policy.Error(redaction.Terminal, err) }()
	kind, kindErr := resolveProviderKind(b.config, name)
	if kindErr != nil {
		return result, kindErr
	}
	connection := b.connection(name, endpoint)
	b.policy.RegisterSecrets(connection.APIKey)
	result.Endpoint = connection.BaseURL
	if kind == "chatgpt" {
		if connection.APIKey != "" {
			return result, fmt.Errorf("provider %s (chatgpt) uses ChatGPT OAuth (qcode login chatgpt); clear api-key", name)
		}
		client := connection.HTTP
		if client == nil {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			if connection.InsecureSkipVerify {
				transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Explicit user opt-out.
			}
			client = &http.Client{Timeout: 30 * time.Second, Transport: transport}
		}
		authCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := chatgptoauth.GetValidTokensWithSecrets(authCtx, client, connection.ChatGPTOAuthPath, b.policy.RegisterSecrets)
		cancel()
		if err != nil {
			return result, err
		}
	}
	if kind == "gemini" && isGeminiOfficialEndpoint(connection.BaseURL) && connection.APIKey == "" {
		return result, fmt.Errorf("provider %s (gemini) requires an API key: set [providers.%s].api_key_path (preferred), GEMINI_API_KEY, GOOGLE_API_KEY, QCODE_API_KEY, or --api-key", name, name)
	}
	if connection.APIKey == "" && ((kind == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(kind == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL))) {
		return result, fmt.Errorf("provider %s ("+kind+") requires an API key: set [providers.%s].api_key_path (preferred), QCODE_API_KEY, or a launch API-key override", name, name)
	}
	if (kind == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(kind == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL)) ||
		(kind == "gemini" && isGeminiOfficialEndpoint(connection.BaseURL)) {
		meta := llm.EmptyProviderModelMeta(kind)
		path, metaErr := llm.ProviderModelMetaCachePath(kind)
		if metaErr == nil {
			load := b.loadMetadata
			if load == nil {
				load = llm.LoadProviderModelMeta
			}
			metaCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			meta, metaErr = load(metaCtx, path, kind, connection.HTTP)
			cancel()
		}
		connection.ModelMeta = meta
		result.MissingThinkingMeta = metaErr != nil && len(meta.Models) == 0
		if metaErr != nil && b.warnings != nil {
			fmt.Fprintf(b.warnings, "WARNING: %s thinking metadata unavailable: %s\n", name, b.policy.Text(redaction.Terminal, metaErr.Error()))
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	result.Provider, err = llm.New(kind, connection)
	return result, err
}

// thinkingAllowed reports whether an explicit thinking level is selectable.
// It mirrors Agent.SetThinking's validation: empty is always allowed (model
// default), otherwise the capability must be adjustable and list the level.
func thinkingAllowed(capability llm.ThinkingCapability, level string) bool {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return true
	}
	if !capability.Adjustable {
		return false
	}
	for _, allowed := range capability.Levels {
		if level == allowed {
			return true
		}
	}
	return false
}

// ensureThinking automatically refreshes stale thinking metadata when an
// explicitly requested level is rejected for the selected model. This covers
// new models (for example gpt-6.2-sol) that the local cache or bundled
// catalog does not know yet:
//
//   - chatgpt refreshes live account metadata via Models() discovery, which
//     populates thinking levels for the current provider instance.
//   - openai, opencode-go, and gemini refresh the Models.dev snapshot (the
//     equivalent of --update-model-meta) and rebuild the provider once.
//
// It only triggers when the requested level is otherwise unsupported, it only
// tries once, and it never fails startup on its own: if the refresh fails or
// the level is still unsupported, the caller's SetThinking reports the
// canonical "not supported" error.
func (b *providerBuilder) ensureThinking(ctx context.Context, name string, endpoint *string, prepared *preparedProvider, model, thinking string) {
	if prepared == nil || prepared.Provider == nil {
		return
	}
	thinking = strings.ToLower(strings.TrimSpace(thinking))
	if thinking == "" {
		return
	}
	thinkingProvider, ok := prepared.Provider.(llm.ThinkingProvider)
	if !ok {
		return
	}
	if thinkingAllowed(thinkingProvider.ThinkingCapability(model), thinking) {
		return
	}
	kind, err := resolveProviderKind(b.config, name)
	if err != nil {
		return
	}
	if kind == "chatgpt" {
		lister, ok := prepared.Provider.(llm.ModelLister)
		if !ok {
			return
		}
		discoveryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, discoveryErr := lister.Models(discoveryCtx)
		cancel()
		if discoveryErr != nil {
			return
		}
		if !thinkingAllowed(thinkingProvider.ThinkingCapability(model), thinking) {
			return
		}
		if b.warnings != nil {
			fmt.Fprintf(b.warnings, "Refreshed %s thinking metadata for model %q.\n", name, model)
		}
		return
	}
	if kind != "openai" && kind != "opencode-go" && kind != "gemini" {
		return
	}
	connection := b.connection(name, endpoint)
	// Only official endpoints use Models.dev metadata; custom base URLs do
	// not load it, so a refresh would be misleading.
	if kind == "openai" && !llm.IsOfficialOpenAIEndpoint(connection.BaseURL) {
		return
	}
	if kind == "opencode-go" && !llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL) {
		return
	}
	if kind == "gemini" && !isGeminiOfficialEndpoint(connection.BaseURL) {
		return
	}
	path, err := llm.ProviderModelMetaCachePath(kind)
	if err != nil {
		return
	}
	refresh := b.refreshMetadata
	if refresh == nil {
		refresh = llm.RefreshProviderModelMeta
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_, refreshErr := refresh(refreshCtx, path, kind, connection.HTTP)
	cancel()
	if refreshErr != nil {
		return
	}
	reprepared, reprepErr := b.prepare(ctx, name, endpoint)
	if reprepErr != nil {
		return
	}
	if tp, ok := reprepared.Provider.(llm.ThinkingProvider); !ok || !thinkingAllowed(tp.ThinkingCapability(model), thinking) {
		return
	}
	*prepared = reprepared
	if b.warnings != nil {
		fmt.Fprintf(b.warnings, "Refreshed %s thinking metadata for model %q.\n", name, model)
	}
}

// An override was requested even if its value was empty. Never replace an
// explicit --api-key= or --base-url= with a profile value.
func applyProviderConnection(opts *options, cfg config.Config, flags map[string]bool) {
	base, key := cfg.ProviderConnection(opts.provider)
	if !flags["base-url"] && os.Getenv("QCODE_BASE_URL") == "" {
		opts.baseURL = base
	}
	if !flags["api-key"] {
		if value := os.Getenv("QCODE_API_KEY"); value != "" {
			opts.apiKey = value
		} else if value := config.ProviderEnvironmentAPIKey(providerKind(cfg, opts.provider)); value != "" {
			opts.apiKey = value
		} else {
			opts.apiKey = key
		}
	}
}

func providerKind(cfg config.Config, name string) string {
	kind, _ := resolveProviderKind(cfg, name)
	return kind
}

// Registered extensions without a config profile retain their registry identity.
func resolveProviderKind(cfg config.Config, name string) (string, error) {
	if _, configured := cfg.Providers[name]; !configured {
		for _, registered := range llm.Names() {
			if registered == name {
				return name, nil
			}
		}
	}
	return cfg.ProviderKind(name)
}

func listConfiguredProviders(out io.Writer, cfg config.Config, policy *redaction.Policy) error {
	names := llm.Names()
	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
	}
	for name := range cfg.Providers {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	builder := providerBuilder{config: cfg, policy: policy}
	for _, name := range names {
		kind, err := resolveProviderKind(cfg, name)
		if err != nil {
			return err
		}
		label := name
		if kind != name {
			label += " (" + kind + ")"
		}
		if available, _ := builder.availability(name); !available {
			label += " (Need Setup First)"
		}
		if _, err := fmt.Fprintln(out, label); err != nil {
			return err
		}
	}
	return nil
}
