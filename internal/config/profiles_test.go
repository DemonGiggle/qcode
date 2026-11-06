package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestProviderProfileLayersAndProvenance(t *testing.T) {
	dir := t.TempDir()
	low, mid, high := filepath.Join(dir, "low"), filepath.Join(dir, "mid"), filepath.Join(dir, "high")
	for path, text := range map[string]string{
		low:  "provider = 'openai'\nbase_url = 'old-url'\napi_key = 'old-key'\n",
		mid:  "provider = 'ollama'\n[providers.openai]\napi_key = ''\n",
		high: "[providers.openai]\nbase_url = 'new-url'\n[providers.ollama]\napi_key = 'local-key'\n",
	} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _, diagnostics := load([]string{high, mid, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if url, key := cfg.ProviderConnection("openai"); url != "new-url" || key != "" {
		t.Fatalf("openai = %q %q", url, key)
	}
	if url, key := cfg.ProviderConnection("ollama"); url != "" || key != "local-key" {
		t.Fatalf("ollama = %q %q", url, key)
	}
	cfg.Provider = "chatgpt"
	if url, key := cfg.ProviderConnection("chatgpt"); url != "" || key != "" {
		t.Fatal("legacy reassigned")
	}
	if got := cfg.Secrets(); !reflect.DeepEqual(got, []string{"local-key", "old-key"}) {
		t.Fatalf("secrets = %v", got)
	}
	direct := Config{Provider: "openai", BaseURL: "flat-url", APIKey: "flat-key", Providers: map[string]ProviderProfile{"openai": {APIKey: stringPointer("")}}}
	if url, key := direct.ProviderConnection("openai"); url != "flat-url" || key != "" {
		t.Fatal(url, key)
	}
	if url, key := direct.ProviderConnection("ollama"); url != "" || key != "" {
		t.Fatal("flat leaked")
	}
}

func TestProfileValidation(t *testing.T) {
	for _, text := range []string{
		"[providers.unknown]\napi_key='x'", "[providers.chatgpt]\napi_key='x'", "[providers.openai]\nextra='x'", "[providers.openai]\napi_key=123",
	} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		_, _, diagnostics := load([]string{path})
		if len(diagnostics) != 1 {
			t.Fatalf("%s: %v", text, diagnostics)
		}
	}
	if err := validate("test", Config{Providers: map[string]ProviderProfile{"chatgpt": {APIKey: stringPointer("")}}}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistProviderMigrationFormats(t *testing.T) {
	for name, profile := range map[string]string{
		"absent":         "",
		"table":          "[providers.openai]\n# profile comment\napi_key = '' # explicit\n",
		"parent":         "[providers]\nopenai.api_key = '' # explicit\n",
		"dotted":         "providers.openai.api_key = '' # explicit\n",
		"inline-profile": "providers.openai = { api_key = '' } # explicit\n",
		"inline-parent":  "providers = { openai = { api_key = '' } } # explicit\n",
		"inline-empty":   "providers = {}\n",
		"table-inline":   "[providers]\nopenai = {api_key = ''} # explicit\n",
	} {
		for _, crlf := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "-crlf", false: ""}[crlf], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config")
				original := "# root comment\nprovider = 'openai'\nbase_url = 'legacy-url' # url comment\napi_key = 'legacy-key' # key comment\nmodel = 'old'\nthinking = 'high' # thinking comment\n" + profile + "[unrelated]\nvalue = 42 # untouched\n"
				if crlf {
					original = strings.ReplaceAll(original, "\n", "\r\n")
				}
				if err := os.WriteFile(path, []byte(original), 0640); err != nil {
					t.Fatal(err)
				}
				w := newRuntimePreferenceWriter(path)
				w.SetLegacyProvider("ollama") // explicit file owner wins
				if err := w.PersistProvider("ollama", "new", ""); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := decodeConfig(data, false)
				if err != nil {
					t.Fatal(err, string(data))
				}
				if cfg.Provider != "ollama" || cfg.Model != "" || cfg.Thinking != "" || cfg.BaseURL != "" || cfg.APIKey != "" {
					t.Fatalf("bad root: %+v", cfg)
				}
				if len(cfg.ProviderModels) != 0 || len(cfg.ProviderThinking) != 0 {
					t.Fatalf("deprecated memory not removed: %+v %+v", cfg.ProviderModels, cfg.ProviderThinking)
				}
				ollama := cfg.Providers["ollama"]
				if ollama.Model == nil || *ollama.Model != "new" {
					t.Fatalf("bad ollama profile: %+v", ollama)
				}
				if ollama.Thinking != nil && *ollama.Thinking != "" {
					t.Fatalf("thinking not cleared: %+v", ollama)
				}
				p := cfg.Providers["openai"]
				wantKey := "legacy-key"
				if strings.Contains(profile, "explicit") {
					wantKey = ""
				}
				if p.BaseURL == nil || *p.BaseURL != "legacy-url" || p.APIKey == nil || *p.APIKey != wantKey {
					t.Fatalf("bad profile: %+v", p)
				}
				for _, comment := range []string{"# root comment", "# url comment", "# key comment", "# thinking comment", "value = 42 # untouched"} {
					if !bytes.Contains(data, []byte(comment)) {
						t.Fatalf("lost %s: %s", comment, data)
					}
				}
				if crlf && bytes.Contains(bytes.ReplaceAll(data, []byte("\r\n"), nil), []byte("\n")) {
					t.Fatal("mixed newlines")
				}
				assertMode(t, path, 0640)
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := w.PersistProvider("ollama", "new", ""); err != nil {
					t.Fatal(err)
				}
				after, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(before, after) {
					t.Fatal("noop replaced file")
				}
			})
		}
	}
}

func TestPersistProviderOwnerAndFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	original := []byte("api_key = 'local-only'\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	w := newRuntimePreferenceWriter(path)
	if err := w.PersistProvider("ollama", "new", ""); err == nil {
		t.Fatal("missing owner accepted")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, original) {
		t.Fatal("failed write changed file")
	}
	w.SetLegacyProvider("openai")
	if err := w.PersistProvider("ollama", "new", "low"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	cfg, err := decodeConfig(data, false)
	if err != nil {
		t.Fatal(err, string(data))
	}
	if cfg.Providers["openai"].APIKey == nil || *cfg.Providers["openai"].APIKey != "local-only" {
		t.Fatal(string(data))
	}
	if cfg.Model != "" || cfg.Thinking != "" {
		t.Fatalf("deprecated root not removed: %+v", cfg)
	}
	ollama := cfg.Providers["ollama"]
	if ollama.Model == nil || *ollama.Model != "new" {
		t.Fatalf("bad ollama profile: %+v", ollama)
	}
	if ollama.Thinking == nil || *ollama.Thinking != "low" {
		t.Fatalf("bad ollama thinking: %+v", ollama)
	}
	if len(cfg.ProviderModels) != 0 || len(cfg.ProviderThinking) != 0 {
		t.Fatalf("deprecated memory not removed: %+v %+v", cfg.ProviderModels, cfg.ProviderThinking)
	}
	for _, bad := range []string{"model = [", "providers = 2\napi_key = 'x'"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := w.PersistProvider("openai", "new", ""); err == nil {
			t.Fatal("malformed accepted")
		}
		data, _ := os.ReadFile(path)
		if string(data) != bad {
			t.Fatal("malformed modified")
		}
	}
	if err := newRuntimePreferenceWriter(t.TempDir()).PersistProvider("openai", "new", ""); err == nil {
		t.Fatal("directory accepted")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := newRuntimePreferenceWriter(filepath.Join(blocked, "config")).PersistProvider("openai", "new", ""); err == nil {
		t.Fatal("unwritable parent accepted")
	}
}

func TestProviderProfileLoadDottedAndInline(t *testing.T) {
	for _, text := range []string{
		"providers.openai.api_key = 'key'\nproviders.openai.base_url = ''\n",
		"[providers]\nopenai.api_key = 'key'\nopenai.base_url = ''\n",
		"providers = {openai = {api_key = 'key', base_url = ''}}\n",
	} {
		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, _, diagnostics := load([]string{path})
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		if url, key := cfg.ProviderConnection("openai"); url != "" || key != "key" {
			t.Fatal(url, key)
		}
	}
	direct := Config{BaseURL: "flat", APIKey: "key"}
	if url, key := direct.ProviderConnection("openai"); url != "flat" || key != "key" {
		t.Fatal("unowned direct legacy config lost", url, key)
	}
}

func TestPersistProviderCreatesAtomicSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config")
	w := newRuntimePreferenceWriter(path)
	if err := w.PersistProvider("openai", "new", "high"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{`provider = "openai"`, "[providers.openai]", `model = "new"`, `thinking = "high"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("selection missing %q: %q", want, text)
		}
	}
	for _, deprecated := range []string{"[provider_models]", "[provider_thinking]", "provider_models", "provider_thinking"} {
		if strings.Contains(text, deprecated) {
			t.Fatalf("deprecated style written: %q in %q", deprecated, text)
		}
	}
	var parsed Config
	if err := toml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	// decodeConfig handles dotted provider keys without the unaddressable panic.
	if parsed.Provider != "openai" || parsed.Model != "" || parsed.Thinking != "" {
		t.Fatalf("deprecated root not removed: %+v", parsed)
	}
	if model, thinking, ok := parsed.ProviderModel("openai"); !ok || model != "new" || thinking != "high" {
		t.Fatalf("bad profile: %+v", parsed.Providers)
	}
	assertMode(t, path, 0600)
	for _, args := range [][3]string{{"bad", "model", ""}, {"openai", " ", ""}, {"openai", "model", string([]byte{255})}} {
		if err := w.PersistProvider(args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid selection accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(data, after) {
			t.Fatal("invalid selection modified file")
		}
	}
}

func TestProviderPersistenceUsesOriginalUserLayerOwner(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "system.toml", "provider = 'ollama'\n")
	user := writeConfig(t, dir, "user.toml", "base_url = 'http://local.test'\napi_key = 'local-key'\n")
	high := writeConfig(t, dir, "executable.toml", "provider = 'openai'\n")
	cfg, _, diagnostics := load([]string{high, user, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	writer := newRuntimePreferenceWriter(user)
	writer.SetLegacyConfig(cfg)
	if err := writer.PersistProvider("opencode-go", "shared", ""); err != nil {
		t.Fatal(err)
	}
	reloaded, _, diagnostics := load([]string{high, user, low})
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if base, key := reloaded.ProviderConnection("ollama"); base != "http://local.test" || key != "local-key" {
		t.Fatal("original ownership lost")
	}
	if base, key := reloaded.ProviderConnection("openai"); base != "" || key != "" {
		t.Fatal("higher default captured legacy credentials")
	}
	if reloaded.Provider != "openai" {
		t.Fatal("persisted preference overrode higher config")
	}
}

func TestProviderModelsRoundTripAndThinkingCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	w := newRuntimePreferenceWriter(path)
	if err := w.PersistProvider("opencode-go", "muse-spark-1.3-contributor", "high"); err != nil {
		t.Fatal(err)
	}
	if err := w.PersistProvider("chatgpt", "gpt-6-luna", ""); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := decodeConfig(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if model, thinking, ok := cfg.ProviderModel("opencode-go"); !ok || model != "muse-spark-1.3-contributor" || thinking != "high" {
		t.Fatalf("opencode-go profile = %q %q %v", model, thinking, ok)
	}
	if model, thinking, ok := cfg.ProviderModel("chatgpt"); !ok || model != "gpt-6-luna" || thinking != "" {
		t.Fatalf("chatgpt profile = %q %q %v", model, thinking, ok)
	}
	if cfg.Provider != "chatgpt" || cfg.Model != "" {
		t.Fatalf("deprecated root not removed: %+v", cfg)
	}
	// Switching the model for the same provider refreshes both root and memory.
	if err := w.PersistModel("chatgpt", "gpt-6-luna-2", ""); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = decodeConfig(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if model, _, ok := cfg.ProviderModel("chatgpt"); !ok || model != "gpt-6-luna-2" {
		t.Fatalf("chatgpt update lost: %+v", cfg.Providers)
	}
	if cfg.Model != "" {
		t.Fatalf("deprecated root not removed: %+v", cfg)
	}
	// Thinking removal deletes the per-provider thinking entry but keeps the model.
	if err := w.PersistProvider("opencode-go", "muse-spark-1.3-contributor", ""); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = decodeConfig(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, thinking, ok := cfg.ProviderModel("opencode-go"); !ok || thinking != "" {
		t.Fatalf("thinking not cleared: %+v", cfg.Providers)
	}
	if strings.Contains(string(data), "provider_models") || strings.Contains(string(data), "provider_thinking") {
		t.Fatalf("deprecated memory written: %s", data)
	}
	if !strings.Contains(string(data), "[providers.opencode-go]") {
		t.Fatalf("profile table lost: %s", data)
	}
}
