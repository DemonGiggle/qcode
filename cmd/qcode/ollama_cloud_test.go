package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"qcode/internal/config"
	"qcode/internal/llm"
	"qcode/internal/redaction"
	"qcode/internal/tui"
)

const testCloudKey = "test-only-cloud-credential"

func TestCloudLaunchProfileAndKeyPrecedence(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", testCloudKey)
	for _, name := range []string{"QCODE_API_KEY", "QCODE_PROVIDER", "QCODE_MODEL", "QCODE_THINKING", "QCODE_BASE_URL"} {
		t.Setenv(name, "")
	}
	cfg := config.Config{Provider: "ollama", Model: "local-only", Providers: map[string]config.ProviderProfile{
		"work-cloud": {Type: profileValue(llm.OllamaCloudKind), APIKey: profileValue("profile-key")},
	}}
	opts := options{provider: "work-cloud", model: "qwen2.5-coder:7b"}
	flags := map[string]bool{"provider": true}
	applyConfig(&opts, cfg, flags)
	if opts.model != "" || opts.apiKey != testCloudKey {
		t.Fatal("wrong initial Cloud model or key")
	}
	if got := config.ProviderEnvironmentAPIKey("ollama"); got != "" {
		t.Fatal("local Ollama picked up the Cloud key")
	}
	policy, _ := redaction.New(redaction.Config{}, redaction.EnvironmentValues())
	if strings.Contains(policy.Text(redaction.Terminal, "literal "+testCloudKey), testCloudKey) {
		t.Fatal("Cloud key not registered for startup redaction")
	}
	t.Setenv("QCODE_API_KEY", "generic-key")
	cfg.ProviderModels = map[string]string{"work-cloud": "Saved:Model"}
	applyConfig(&opts, cfg, flags)
	if opts.model != "Saved:Model" || opts.apiKey != "generic-key" {
		t.Fatal("remembered model or generic key ignored")
	}
	flags["api-key"], flags["model"] = true, true
	opts.apiKey, opts.model = "flag-key", "Exact:Direct"
	applyConfig(&opts, cfg, flags)
	if opts.model != "Exact:Direct" || opts.apiKey != "flag-key" {
		t.Fatal("flags did not win")
	}
	t.Setenv("QCODE_API_KEY", "")
	t.Setenv("OLLAMA_API_KEY", "")
	applyProviderConnection(&opts, cfg, nil)
	if opts.apiKey != "profile-key" {
		t.Fatal("profile key not used")
	}
}

func TestCloudListingAndCheckpointEndpointAuthorization(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	policy, _ := redaction.New(redaction.Config{}, nil)
	var out bytes.Buffer
	if err := listConfiguredProviders(&out, config.Config{}, policy); err != nil || !strings.Contains(out.String(), "ollama-cloud (Need Setup First)") {
		t.Fatalf("missing availability marker: %q, %v", out.String(), err)
	}
	t.Setenv("OLLAMA_API_KEY", testCloudKey)
	cfg := config.Config{Providers: map[string]config.ProviderProfile{
		"work-cloud": {Type: profileValue(llm.OllamaCloudKind), BaseURL: profileValue("https://configured.test/gateway/api/")},
	}}
	out.Reset()
	if err := listConfiguredProviders(&out, cfg, policy); err != nil || !strings.Contains(out.String(), "\nollama-cloud\n") || !strings.Contains(out.String(), "\nwork-cloud (ollama-cloud)\n") {
		t.Fatalf("cloud unavailable: %q, %v", out.String(), err)
	}
	b := providerBuilder{config: cfg, policy: policy, launchProvider: "ollama", launch: llm.Config{APIKey: "unrelated-launch-key", HTTP: providerHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "configured.test" || r.Header.Get("Authorization") != "Bearer "+testCloudKey {
			t.Errorf("wrong endpoint or credential: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"done":true}`))}, nil
	})}}
	savedEndpoint := "https://checkpoint-only.test"
	prepared, err := b.prepare(context.Background(), "work-cloud", &savedEndpoint)
	if err != nil || prepared.Endpoint != "https://configured.test/gateway" {
		t.Fatalf("saved endpoint was not ignored: %q %v", prepared.Endpoint, err)
	}
	if _, err := prepared.Provider.Complete(context.Background(), llm.Request{Model: "Exact:Direct"}, nil); err != nil {
		t.Fatal(err)
	}
	b.config = config.Config{}
	prepared, err = b.prepare(context.Background(), llm.OllamaCloudKind, &savedEndpoint)
	if err != nil || prepared.Endpoint != "https://ollama.com" {
		t.Fatalf("default Cloud endpoint was not used on restore: %q %v", prepared.Endpoint, err)
	}
}

func TestCloudSetupProbesOnlyCatalogAndSelectedMetadata(t *testing.T) {
	t.Setenv("OLLAMA_API_KEY", "")
	var tags, show atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCloudKey {
			t.Error("setup did not use staged key")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /proxy/api/tags":
			tags.Add(1)
			fmt.Fprint(w, `{"models":[{"name":"OtherModel"},{"name":"Exact:Cloud"}]}`)
		case "POST /proxy/api/show":
			show.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "Exact:Cloud" {
				t.Errorf("unexpected metadata lookup: %v, %v", body, err)
			}
			fmt.Fprint(w, `{"thinking":{"values":[false,true],"default":true}}`)
		default:
			t.Errorf("setup sent an unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfgPath, keyPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, "cloud.key")
	dangerTLS := true // Self-signed local test fixture.
	cfg := config.Config{DangerSkipTLSVerify: &dangerTLS}
	state := newSetupState(cfg, cfgPath)
	state.activeProvider, state.providerChanged = llm.OllamaCloudKind, true
	ui := &fakeSetupUI{steps: []fakeSetupStep{
		{kind: "prompt", reply: testCloudKey, nav: tui.SetupAccepted},
		{kind: "prompt", reply: keyPath, nav: tui.SetupAccepted},
		{kind: "option", reply: "Set custom base URL", nav: tui.SetupAccepted},
		{kind: "prompt", reply: srv.URL + "/proxy/api", nav: tui.SetupAccepted},
		{kind: "model", reply: "Exact:Cloud", nav: tui.SetupAccepted},
		{kind: "thinking", reply: tui.ModelDefaultThinking, nav: tui.SetupAccepted},
	}}
	var out bytes.Buffer
	policy, _ := redaction.New(redaction.Config{}, nil)
	deps := defaultSetupDeps()
	if nav, err := configureProviderCredentials(ui, state, llm.OllamaCloudKind, strings.NewReader(""), &out, &out, policy, deps); nav != tui.SetupAccepted || err != nil {
		t.Fatalf("configure: %v %v", nav, err)
	}
	if nav, err := verifyAndSelectModel(ui, state, llm.OllamaCloudKind, strings.NewReader(""), &out, &out, policy, deps); nav != tui.SetupAccepted || err != nil {
		t.Fatalf("select: %v %v", nav, err)
	}
	if tags.Load() != 1 || show.Load() != 1 || !strings.Contains(out.String(), "may not validate") {
		t.Fatalf("unexpected setup probe: %s", out.String())
	}
	if err := applySetupState(config.NewRuntimePreferenceWriterForPath(cfgPath), state, deps); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o", info.Mode().Perm())
	}
	text, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(text), testCloudKey) || !strings.Contains(string(text), `provider = "ollama-cloud"`) || !strings.Contains(string(text), `model = "Exact:Cloud"`) || !strings.Contains(string(text), `api_key_path =`) {
		t.Fatalf("invalid config persistence: %s", text)
	}
	t.Setenv("OLLAMA_API_KEY", testCloudKey)
	envState := newSetupState(config.Config{}, cfgPath)
	if _, key := envState.effectiveConnection(llm.OllamaCloudKind); key != testCloudKey || !envState.isConfigured(llm.OllamaCloudKind) {
		t.Fatal("setup didn't accept the environment key")
	}
}

func TestCloudCLIModelRequiredAndLaunchesWithExplicitOrSavedModel(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"QCODE_API_KEY", "QCODE_PROVIDER", "QCODE_MODEL", "QCODE_THINKING", "QCODE_BASE_URL", "ProgramData"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	t.Setenv("XDG_STATE_HOME", home)
	t.Setenv("OLLAMA_API_KEY", testCloudKey)
	cfgPath, err := config.UserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(text string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("provider = 'ollama'\nmodel = 'local-only'\n")
	var chatCalls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCloudKey {
			t.Error("missing CLI key")
		}
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"Exact:Model","context_length":16384,"thinking":{"values":[false,true],"default":true}}]}`)
		case "/api/chat":
			chatCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "Exact:Model" || body["think"] != nil {
				t.Errorf("invalid CLI chat request: %v, %v", body, err)
			}
			fmt.Fprintln(w, `{"message":{"content":"cloud works"},"done":true}`)
		default:
			t.Errorf("unexpected CLI route: %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	call := func(flags ...string) error {
		t.Helper()
		f, err := os.CreateTemp(home, "cli")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		args := []string{"--provider", llm.OllamaCloudKind, "--base-url", srv.URL, "--danger-skip-tls-verify", "--cwd", home}
		args = append(args, flags...)
		return run(append(args, "prompt", "say hello"), f, f, f)
	}
	if err := call(); err == nil || !strings.Contains(err.Error(), "requires a model") || !strings.Contains(err.Error(), "qcode setup") {
		t.Fatalf("missing-model diagnostic: %v", err)
	}
	if err := call("--model", "Exact:Model", "--thinking", "high"); err == nil || !strings.Contains(err.Error(), "does not advertise thinking level") {
		t.Fatalf("unsupported thinking diagnostic: %v", err)
	}
	if chatCalls.Load() != 0 {
		t.Fatal("invalid launch issued a chat request")
	}
	if err := call("--model", "Exact:Model"); err != nil {
		t.Fatal(err)
	}
	writeConfig("[providers.ollama-cloud]\nmodel = 'Exact:Model'\nthinking = 'stale-level'\n")
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if chatCalls.Load() != 2 {
		t.Fatalf("chat calls = %d, want 2", chatCalls.Load())
	}
}
