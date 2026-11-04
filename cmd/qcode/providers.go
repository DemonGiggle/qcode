package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/redaction"
)

// providerBuilder owns the launch snapshot. Generic flags/environment overrides
// belong to the launch provider; a different backend must resolve its own profile.
// Credentials are never part of an agent checkpoint or a creation request.
type providerBuilder struct {
	config         config.Config
	launchProvider string
	launch         llm.Config
	policy         *redaction.Policy
	warnings       io.Writer
	loadMetadata   func(context.Context, string, string, llm.HTTPDoer) (*llm.ProviderModelMeta, error)
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
	} else if name == "openai" {
		if value := os.Getenv("OPENAI_API_KEY"); value != "" {
			key = value
		}
	}
	if endpoint != nil {
		base = *endpoint // An empty saved endpoint means the provider's default.
	}
	return llm.Config{BaseURL: base, APIKey: key, HTTP: b.launch.HTTP,
		InsecureSkipVerify: b.launch.InsecureSkipVerify, ChatGPTOAuthPath: b.config.ChatGPT.OAuthPath,
		RegisterSecrets: b.policy.RegisterSecrets}
}

// availability reports whether name can be selected without additional setup.
// It mirrors prepare's credential checks without network or model-metadata
// work so the /provider picker can disable unconfigured backends. The hint
// describes the setup step when unavailable.
func (b *providerBuilder) availability(name string) (bool, string) {
	connection := b.connection(name, nil)
	if name == "chatgpt" {
		if connection.APIKey != "" {
			return false, "provider chatgpt uses ChatGPT OAuth (qcode login chatgpt); clear api-key"
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
	if (name == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(name == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL)) {
		if connection.APIKey == "" {
			return false, fmt.Sprintf("provider %s requires an API key: configure [providers.%s].api_key or supply a launch API-key override", name, name)
		}
	}
	return true, ""
}

func (b *providerBuilder) prepare(ctx context.Context, name string, endpoint *string) (result preparedProvider, err error) {
	defer func() { err = b.policy.Error(redaction.Terminal, err) }()
	connection := b.connection(name, endpoint)
	b.policy.RegisterSecrets(connection.APIKey)
	result.Endpoint = connection.BaseURL
	if name == "chatgpt" {
		if connection.APIKey != "" {
			return result, errors.New("provider chatgpt uses ChatGPT OAuth (qcode login chatgpt); clear api-key")
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
	if connection.APIKey == "" && ((name == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(name == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL))) {
		return result, fmt.Errorf("provider %s requires an API key: configure [providers.%s].api_key or supply a launch API-key override", name, name)
	}
	if (name == "openai" && llm.IsOfficialOpenAIEndpoint(connection.BaseURL)) ||
		(name == "opencode-go" && llm.IsOfficialOpenCodeGoEndpoint(connection.BaseURL)) {
		meta := llm.EmptyProviderModelMeta(name)
		path, metaErr := llm.ProviderModelMetaCachePath(name)
		if metaErr == nil {
			load := b.loadMetadata
			if load == nil {
				load = llm.LoadProviderModelMeta
			}
			metaCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			meta, metaErr = load(metaCtx, path, name, connection.HTTP)
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
	result.Provider, err = llm.New(name, connection)
	return result, err
}

// An override was requested even if its value was empty. Never replace an
// explicit --api-key= or --base-url= with a profile value.
func applyProviderConnection(opts *options, cfg config.Config, flags map[string]bool) {
	base, key := cfg.ProviderConnection(opts.provider)
	if !flags["base-url"] && os.Getenv("QCODE_BASE_URL") == "" {
		opts.baseURL = base
	}
	if !flags["api-key"] && firstEnv("QCODE_API_KEY", "OPENAI_API_KEY") == "" {
		opts.apiKey = key
	}
}
