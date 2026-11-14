package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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

func TestGeminiLaunchCredentialPrecedence(t *testing.T) {
	t.Setenv("QCODE_API_KEY", "qcode-key")
	t.Setenv("GEMINI_API_KEY", "gemini-key")
	t.Setenv("GOOGLE_API_KEY", "google-key")
	profile := "profile-key"
	cfg := config.Config{Providers: map[string]config.ProviderProfile{"gemini": {APIKey: &profile}}}
	for _, tc := range []struct {
		name string
		set  map[string]string
		flag string
		want string
	}{
		{name: "flag", flag: "flag-key", want: "flag-key"},
		{name: "QCODE_API_KEY", set: map[string]string{"QCODE_API_KEY": "qcode-key"}, want: "qcode-key"},
		{name: "GEMINI_API_KEY", set: map[string]string{"QCODE_API_KEY": "", "GEMINI_API_KEY": "gemini-key"}, want: "gemini-key"},
		{name: "GOOGLE_API_KEY", set: map[string]string{"QCODE_API_KEY": "", "GEMINI_API_KEY": "", "GOOGLE_API_KEY": "google-key"}, want: "google-key"},
		{name: "profile", set: map[string]string{"QCODE_API_KEY": "", "GEMINI_API_KEY": "", "GOOGLE_API_KEY": ""}, want: "profile-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.set {
				t.Setenv(key, value)
			}
			opts := options{provider: "gemini", apiKey: tc.flag}
			flags := map[string]bool{}
			if tc.flag != "" {
				flags["api-key"] = true
			}
			applyProviderConnection(&opts, cfg, flags)
			if opts.apiKey != tc.want {
				t.Fatalf("api key = %q, want %q", opts.apiKey, tc.want)
			}
		})
	}
}

func TestGeminiProviderCredentialsAndDefaultEndpoint(t *testing.T) {
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "gemini-env")
	t.Setenv("GOOGLE_API_KEY", "google-env")
	t.Setenv("OPENAI_API_KEY", "openai-env")
	policy, _ := redaction.New(redaction.Config{}, nil)
	builder := &providerBuilder{config: config.Config{Providers: map[string]config.ProviderProfile{
		"gemini": {APIKey: profileValue("profile-key")},
	}}, launchProvider: "openai", policy: policy}
	connection := builder.connection("gemini", nil)
	if connection.APIKey != "gemini-env" || connection.BaseURL != "" {
		t.Fatalf("Gemini connection = %+v", connection)
	}
	if ok, _ := builder.availability("gemini"); !ok {
		t.Fatal("Gemini environment key should enable the default provider")
	}
	prepared, err := builder.prepare(context.Background(), "gemini", nil)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Provider.Name() != "gemini" || prepared.Endpoint != "" {
		t.Fatalf("prepared Gemini provider = %+v", prepared)
	}
	t.Setenv("OPENAI_API_KEY", "")
	if ok, _ := builder.availability("openai"); ok {
		t.Fatal("Gemini environment key must not enable OpenAI")
	}
	t.Setenv("GEMINI_API_KEY", "")
	if got := builder.connection("gemini", nil).APIKey; got != "google-env" {
		t.Fatalf("GOOGLE_API_KEY fallback = %q", got)
	}
	t.Setenv("GOOGLE_API_KEY", "")
	if got := builder.connection("gemini", nil).APIKey; got != "profile-key" {
		t.Fatalf("profile fallback = %q", got)
	}
	delete(builder.config.Providers, "gemini")
	if got := builder.connection("gemini", nil).APIKey; got != "" {
		t.Fatalf("missing key = %q", got)
	}
	if ok, hint := builder.availability("gemini"); ok || !strings.Contains(hint, "GEMINI_API_KEY") {
		t.Fatalf("missing-key availability = %v, %q", ok, hint)
	}
	if _, err := builder.prepare(context.Background(), "gemini", nil); err == nil || !strings.Contains(err.Error(), "GOOGLE_API_KEY") {
		t.Fatalf("missing-key guidance = %v", err)
	}
	custom := "https://proxy.example/v1"
	builder.config.Providers["gemini"] = config.ProviderProfile{BaseURL: profileValue(custom), APIKey: profileValue("proxy-key")}
	prepared, err = builder.prepare(context.Background(), "gemini", nil)
	if err != nil || prepared.Endpoint != custom {
		t.Fatalf("custom endpoint = %+v, %v", prepared, err)
	}
}

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
	if err := chatgptoauth.Save(path, chatgptoauth.Tokens{AccessToken: "old-access-test-secret", RefreshToken: "old-refresh-test-secret", ExpiresAt: time.Now().Add(-time.Hour).Unix(), ClientID: "oaiapp_test123", ExtAgentHostID: "urn:uuid:host-test"}); err != nil {
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
	if err := chatgptoauth.Save(path, chatgptoauth.Tokens{AccessToken: "valid-access-test-secret", RefreshToken: "valid-refresh-test-secret", ExpiresAt: time.Now().Add(time.Hour).Unix(), ClientID: "oaiapp_test123", ExtAgentHostID: "urn:uuid:host-test"}); err != nil {
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

func TestOpenAIGeminiOpenAIRoundTripKeepsCredentialsIsolated(t *testing.T) {
	for _, name := range []string{"QCODE_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		t.Setenv(name, "")
	}
	newCatalog := func(wantKey string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Errorf("unexpected catalog path %q", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+wantKey {
				t.Errorf("credential = %q, want provider-specific key", got)
			}
			io.WriteString(w, `{"data":[{"id":"shared-model"}]}`)
		}))
	}
	openaiServer := newCatalog("openai-profile-secret")
	defer openaiServer.Close()
	geminiServer := newCatalog("gemini-profile-secret")
	defer geminiServer.Close()
	policy, _ := redaction.New(redaction.Config{}, []string{"launch-only-secret", "openai-profile-secret", "gemini-profile-secret"})
	builder := &providerBuilder{
		launchProvider: "ollama",
		launch:         llm.Config{APIKey: "launch-only-secret"},
		policy:         policy,
		config: config.Config{Providers: map[string]config.ProviderProfile{
			"openai": {BaseURL: profileValue(openaiServer.URL), APIKey: profileValue("openai-profile-secret")},
			"gemini": {BaseURL: profileValue(geminiServer.URL), APIKey: profileValue("gemini-profile-secret")},
		}},
	}
	for _, name := range []string{"openai", "gemini", "openai"} {
		prepared, err := builder.prepare(context.Background(), name, nil)
		if err != nil {
			t.Fatalf("prepare %s: %v", name, err)
		}
		if prepared.Provider.Name() != name {
			t.Fatalf("prepared provider = %q, want %q", prepared.Provider.Name(), name)
		}
		models, err := prepared.Provider.(llm.ModelLister).Models(context.Background())
		if err != nil || !reflect.DeepEqual(models, []string{"shared-model"}) {
			t.Fatalf("%s model discovery = %v, %v", name, models, err)
		}
	}
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

func TestProviderAvailabilityMirrorsCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	policy, _ := redaction.New(redaction.Config{}, nil)
	builder := &providerBuilder{launchProvider: "ollama", policy: policy}
	if ok, _ := builder.availability("ollama"); !ok {
		t.Fatal("ollama should always be selectable")
	}
	if ok, hint := builder.availability("openai"); ok || !strings.Contains(hint, "[providers.openai].api_key") {
		t.Fatalf("openai availability = %v, %q", ok, hint)
	}
	if ok, hint := builder.availability("opencode-go"); ok || !strings.Contains(hint, "[providers.opencode-go].api_key") {
		t.Fatalf("opencode-go availability = %v, %q", ok, hint)
	}
	builder.config.ChatGPT.OAuthPath = filepath.Join(t.TempDir(), "missing.json")
	if ok, hint := builder.availability("chatgpt"); ok || !strings.Contains(hint, "qcode login chatgpt") {
		t.Fatalf("chatgpt availability = %v, %q", ok, hint)
	}
	// Custom endpoints stay selectable without a key.
	builder.config.Providers = map[string]config.ProviderProfile{
		"openai": {BaseURL: profileValue("http://local.test/v1")},
	}
	if ok, _ := builder.availability("openai"); !ok {
		t.Fatal("custom openai endpoint should not require a key")
	}
}
