package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"encoding/json"
	"qcode/internal/agent"
	"qcode/internal/chatgptoauth"
	"qcode/internal/config"
	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/redaction"
	"qcode/internal/trace"
)

func profileValue(value string) *string { return &value }

type providerHTTPFunc func(*http.Request) (*http.Response, error)

func (f providerHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestApplyProviderProfilesAndLaunchPrecedence(t *testing.T) {
	for _, name := range []string{"QCODE_PROVIDER", "QCODE_MODEL", "QCODE_BASE_URL", "QCODE_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(name, "")
	}
	cfg := config.Config{Provider: "ollama", BaseURL: "http://legacy.test", APIKey: "legacy-secret", Providers: map[string]config.ProviderProfile{
		"openai": {BaseURL: profileValue("https://profile.test/v1"), APIKey: profileValue("profile-secret")},
	}}
	opts := options{provider: "openai", model: "launch-model"}
	applyConfig(&opts, cfg, map[string]bool{"provider": true})
	if opts.baseURL != "https://profile.test/v1" || opts.apiKey != "profile-secret" || opts.model != "launch-model" {
		t.Fatalf("profile not selected: %+v", opts)
	}
	opts = options{provider: "openai", baseURL: "flag-url", apiKey: "flag-key"}
	applyConfig(&opts, cfg, map[string]bool{"provider": true, "base-url": true, "api-key": true})
	if opts.baseURL != "flag-url" || opts.apiKey != "flag-key" {
		t.Fatal("profile overrode launch flags")
	}
	opts = options{provider: "openai"}
	applyConfig(&opts, cfg, map[string]bool{"provider": true, "base-url": true, "api-key": true})
	if opts.baseURL != "" || opts.apiKey != "" {
		t.Fatal("profile overrode explicit empty flags")
	}
	t.Setenv("QCODE_BASE_URL", "env-url")
	t.Setenv("QCODE_API_KEY", "env-key")
	opts = options{provider: "openai", baseURL: "env-url", apiKey: "env-key"}
	applyConfig(&opts, cfg, map[string]bool{"provider": true})
	if opts.baseURL != "env-url" || opts.apiKey != "env-key" {
		t.Fatal("profile overrode env")
	}
}

func TestProviderBuilderSeparatesEndpointsKeysAndInstances(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	var aCalls, bCalls int
	var identities []string
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aCalls++
		if r.Header.Get("Authorization") != "Bearer launch-key" {
			t.Error("wrong launch credential")
		}
		io.WriteString(w, `{"data":[{"id":"shared"}]}`)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bCalls++
		if r.Header.Get("Authorization") != "Bearer other-key" {
			t.Error("another backend's credential was forwarded")
		}
		identities = append(identities, r.Header.Get("x-opencode-session"))
		io.WriteString(w, `{"data":[{"id":"shared"}]}`)
	}))
	defer b.Close()
	policy, _ := redaction.New(redaction.Config{}, nil)
	builder := &providerBuilder{launchProvider: "openai", launch: llm.Config{BaseURL: a.URL, APIKey: "launch-key"}, policy: policy,
		config: config.Config{Providers: map[string]config.ProviderProfile{
			"opencode-go": {BaseURL: profileValue(b.URL), APIKey: profileValue("other-key")},
		}}}
	for _, name := range []string{"openai", "opencode-go", "opencode-go"} {
		prepared, err := builder.prepare(context.Background(), name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := prepared.Provider.(llm.ModelLister).Models(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if aCalls != 1 || bCalls != 2 || identities[0] == "" || identities[0] == identities[1] {
		t.Fatal("backend identity was reused")
	}
	if connection := builder.connection("ollama", nil); connection.BaseURL != "" || connection.APIKey != "" {
		t.Fatal("generic launch override escaped its provider")
	}
	if policy.Text(redaction.Terminal, "echo launch-key other-key") != "echo [REDACTED] [REDACTED]" {
		t.Fatal("factory failed to register keys")
	}
	// Saved default endpoints must stay default rather than inheriting a launch URL.
	empty := ""
	if connection := builder.connection("openai", &empty); connection.BaseURL != "" {
		t.Fatal("saved default endpoint was overwritten")
	}
}

func TestProviderBuilderMetadataMatchesTarget(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	policy, _ := redaction.New(redaction.Config{}, nil)
	var loaded []string
	builder := &providerBuilder{launchProvider: "ollama", policy: policy, config: config.Config{Providers: map[string]config.ProviderProfile{
		"openai": {APIKey: profileValue("a")}, "opencode-go": {APIKey: profileValue("b")},
	}}, loadMetadata: func(_ context.Context, _ string, name string, _ llm.HTTPDoer) (*llm.ProviderModelMeta, error) {
		loaded = append(loaded, name)
		return llm.EmptyProviderModelMeta(name), errors.New("offline")
	}}
	for _, name := range []string{"openai", "opencode-go"} {
		prepared, err := builder.prepare(context.Background(), name, nil)
		if err != nil || !prepared.MissingThinkingMeta {
			t.Fatalf("metadata fallback: %v", err)
		}
	}
	if strings.Join(loaded, ",") != "openai,opencode-go" {
		t.Fatal(loaded)
	}
	custom := "http://custom.test/v1"
	if _, err := builder.prepare(context.Background(), "openai", &custom); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatal("custom endpoint loaded official metadata")
	}
}

func TestProviderBuilderCredentialErrorsAndOAuthRedaction(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	policy, _ := redaction.New(redaction.Config{}, nil)
	builder := &providerBuilder{launchProvider: "ollama", policy: policy}
	for _, name := range []string{"openai", "opencode-go"} {
		if _, err := builder.prepare(context.Background(), name, nil); err == nil || !strings.Contains(err.Error(), "requires an API key") {
			t.Fatal(err)
		}
	}
	builder.config.ChatGPT.OAuthPath = filepath.Join(t.TempDir(), "missing.json")
	if _, err := builder.prepare(context.Background(), "chatgpt", nil); err == nil || !strings.Contains(err.Error(), "qcode login chatgpt") {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "oauth.json")
	if err := chatgptoauth.Save(path, chatgptoauth.Tokens{AccessToken: "old-access-test-secret", RefreshToken: "old-refresh-test-secret", ExpiresAt: time.Now().Add(-time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	builder.config.ChatGPT.OAuthPath = path
	builder.launch.APIKey = "launch-key-must-not-be-used-for-chatgpt"
	builder.launch.HTTP = providerHTTPFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader("old-access-test-secret old-refresh-test-secret")), Header: make(http.Header)}, nil
	})
	_, err := builder.prepare(context.Background(), "chatgpt", nil)
	if err == nil || strings.Contains(err.Error(), "old-access-test-secret") || strings.Contains(err.Error(), "old-refresh-test-secret") {
		t.Fatal("OAuth preflight error leaked stored credentials")
	}
	if err := chatgptoauth.Save(path, chatgptoauth.Tokens{AccessToken: "valid-access-test-secret", RefreshToken: "valid-refresh-test-secret", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare(context.Background(), "chatgpt", nil)
	if err != nil || prepared.Provider.Name() != "chatgpt" {
		t.Fatalf("switch inherited launch API key: %v", err)
	}
	if policy.ContainsSecret("unrelated") || !strings.Contains(policy.Text(redaction.Terminal, "valid-access-test-secret"), redaction.Marker) {
		t.Fatal("valid OAuth credential not registered")
	}
}

func TestProviderBuilderRegistersFactoryErrorsAndInactiveKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	policy, _ := redaction.New(redaction.Config{}, nil)
	cfg := config.Config{Providers: map[string]config.ProviderProfile{"openai": {APIKey: profileValue("inactive-secret")}}}
	policy.RegisterSecrets(cfg.Secrets()...)
	if got := policy.Text(redaction.Persistence, "inactive-secret"); got != redaction.Marker {
		t.Fatal("inactive profile unprotected")
	}
	var warnings bytes.Buffer
	builder := &providerBuilder{config: cfg, policy: policy, warnings: &warnings, loadMetadata: func(context.Context, string, string, llm.HTTPDoer) (*llm.ProviderModelMeta, error) {
		return llm.EmptyProviderModelMeta("openai"), errors.New("inactive-secret")
	}}
	if _, err := builder.prepare(context.Background(), "openai", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(warnings.String(), "inactive-secret") {
		t.Fatal("metadata diagnostic leaked a key")
	}
}

type providerBuilderTools struct{}

func (*providerBuilderTools) Schemas() []llm.Tool        { return nil }
func (*providerBuilderTools) EnabledSchemas() []llm.Tool { return nil }
func (*providerBuilderTools) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func TestProviderBuilderMixedSessionUsesCurrentProfileCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer restored-openai-key"
		if r.Header.Get("x-opencode-session") != "" {
			want = "Bearer restored-opencode-key"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("restored backend used startup credentials")
		}
		io.WriteString(w, `{"data":[{"id":"shared"}]}`)
	}))
	defer server.Close()
	policy, _ := redaction.New(redaction.Config{}, nil)
	builder := &providerBuilder{launchProvider: "ollama", launch: llm.Config{APIKey: "wrong-launch-key"}, policy: policy,
		config: config.Config{Providers: map[string]config.ProviderProfile{
			"openai":      {BaseURL: profileValue(server.URL), APIKey: profileValue("restored-openai-key")},
			"opencode-go": {BaseURL: profileValue(server.URL), APIKey: profileValue("restored-opencode-key")},
		}}}
	makeRunner := func(name, model string, endpoint *string) (*agent.Agent, error) {
		prepared, err := builder.prepare(context.Background(), name, endpoint)
		if err != nil {
			return nil, err
		}
		a := agent.New(prepared.Provider, model, &providerBuilderTools{}, trace.New(io.Discard, false), io.Discard, 3)
		a.SetEndpoint(prepared.Endpoint)
		return a, nil
	}
	h := control.NewHost(context.Background(), 3)
	defer h.Shutdown()
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) { return makeRunner("openai", model, nil) })
	if _, err := h.CreateMain("shared"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Create("shared"); err != nil {
		t.Fatal(err)
	}
	prepared, err := builder.prepare(context.Background(), "opencode-go", nil)
	if err != nil {
		t.Fatal(err)
	}
	revision, _ := h.ConfigurationRevision("main")
	if err := h.SwitchBackend("main", revision, agent.BackendSwitch{Provider: prepared.Provider, Endpoint: prepared.Endpoint, Model: "shared"}); err != nil {
		t.Fatal(err)
	}
	saved, next := h.SaveAgents()
	states := map[string]agent.SavedState{}
	for _, item := range saved {
		if bytes.Contains(item.State, []byte("restored-openai-key")) || bytes.Contains(item.State, []byte("restored-opencode-key")) {
			t.Fatal("checkpoint serialized credential")
		}
		var state agent.SavedState
		if err := json.Unmarshal(item.State, &state); err != nil {
			t.Fatal(err)
		}
		states[item.Summary.ID] = state
	}
	restored := control.NewHost(context.Background(), 3)
	defer restored.Shutdown()
	restored.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		state := states[id]
		return makeRunner(state.Provider, model, &state.Endpoint)
	})
	if err := restored.RestoreAgents(saved, next); err != nil {
		t.Fatal(err)
	}
	for _, item := range saved {
		runner, _ := restored.Agent(item.Summary.ID)
		if _, err := runner.ListModels(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	again, _ := restored.SaveAgents()
	for _, item := range again {
		var state agent.SavedState
		if err := json.Unmarshal(item.State, &state); err != nil {
			t.Fatal(err)
		}
		before := states[item.Summary.ID]
		if state.Provider != before.Provider || state.Endpoint != before.Endpoint || state.ProviderSession != before.ProviderSession {
			t.Fatal("restore lost provider identity")
		}
	}
}
