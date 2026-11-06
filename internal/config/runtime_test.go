package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestUserConfigPath(t *testing.T) {
	tests := []struct {
		name, goos, home, configDir, want string
	}{
		{name: "linux", goos: "linux", home: "/home/ada", want: "/home/ada/.local/etc/qcode/config.toml"},
		{name: "darwin", goos: "darwin", configDir: "/Users/ada/Library/Application Support", want: "/Users/ada/Library/Application Support/qcode/config.toml"},
		{name: "windows", goos: "windows", configDir: filepath.Join("C:", "Users", "ada", "AppData", "Roaming"), want: filepath.Join("C:", "Users", "ada", "AppData", "Roaming", "qcode", "config.toml")},
		{name: "other unix", goos: "freebsd", configDir: "/home/ada/.config", want: "/home/ada/.config/qcode/config.toml"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := userConfigPath(test.goos, test.home, test.configDir); got != test.want {
				t.Fatalf("userConfigPath = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPersistModelCreatesOnlyRequestedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "qcode", "config.toml")
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistModel("openai", "new-model", ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"[providers.openai]", `model = "new-model"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("new config missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "provider_models") || strings.Contains(text, "provider_thinking") {
		t.Fatalf("deprecated memory in new config: %q", text)
	}
	if strings.Contains(text, "thinking") {
		t.Fatalf("unexpected thinking in new config: %q", text)
	}
	if err := writer.PersistModel("openai", "new-model", "low"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text = string(data)
	for _, want := range []string{"[providers.openai]", `model = "new-model"`, `thinking = "low"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("explicit thinking config missing %q: %q", want, text)
		}
	}
	if strings.Contains(text, "provider_models") || strings.Contains(text, "provider_thinking") {
		t.Fatalf("deprecated memory in explicit config: %q", text)
	}
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)
}

func TestPersistUpdatesManagedKeysAndPreservesTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `# keep this comment
provider = "openai"
model = "old-model" # keep this inline comment
thinking = "high" # keep this thinking note
max_steps = 16
unrelated = "keep"

[learning]
context_budget = 1200
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistModel("openai", "new-model", ""); err != nil {
		t.Fatal(err)
	}
	if err := writer.PersistMaxSteps(64); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{
		"# keep this comment",
		`provider = "openai"`,
		`max_steps = 64`,
		`unrelated = "keep"`,
		"# keep this inline comment",
		"# keep this thinking note",
		"[learning]",
		"context_budget = 1200",
		"[providers.openai]",
		`model = "new-model"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated config missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "provider_models") || strings.Contains(got, "provider_thinking") {
		t.Fatalf("deprecated memory remains: %s", got)
	}
	// Deprecated root model/thinking must not be reintroduced.
	if strings.Contains(got, `model = "old-model"`) || strings.Contains(got, `model = "new-model" # keep this inline comment`) {
		t.Fatalf("deprecated root model remains: %s", got)
	}
	if strings.Contains(got, "thinking =") && !strings.Contains(got, "[providers.openai]") {
		t.Fatalf("cleared thinking override remains in root: %s", got)
	}
	assertMode(t, path, 0o640)

	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "" || parsed.Thinking != "" || parsed.MaxSteps == nil || *parsed.MaxSteps != 64 {
		t.Fatalf("deprecated root not removed: %+v", parsed)
	}
	if model, _, ok := parsed.ProviderModel("openai"); !ok || model != "new-model" {
		t.Fatalf("profile model missing: %+v", parsed.Providers)
	}
}

func TestPersistInsertsRootKeysBeforeTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `provider = "ollama"

[learning]
context_budget = 1200
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newRuntimePreferenceWriter(path).PersistMaxSteps(64); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if want := strings.Replace(original, "[learning]", "max_steps = 64\n[learning]", 1); text != want {
		t.Fatalf("updated config = %q, want %q", text, want)
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.MaxSteps == nil || *parsed.MaxSteps != 64 {
		t.Fatalf("parsed max_steps = %+v", parsed.MaxSteps)
	}
}

func TestPersistRejectsMalformedConfigWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("model = [\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := newRuntimePreferenceWriter(path).PersistMaxSteps(64); err == nil {
		t.Fatal("PersistMaxSteps succeeded for malformed TOML")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("malformed config changed from %q to %q", original, got)
	}
	assertMode(t, path, 0o640)
}

func TestPersistReportsUnwritableTargetWithoutChangingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "config.toml")
	if err := newRuntimePreferenceWriter(path).PersistMaxSteps(64); err == nil {
		t.Fatal("PersistMaxSteps succeeded below a regular file")
	}
	data, err := os.ReadFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("unwritable parent changed to %q", data)
	}
	if err := newRuntimePreferenceWriter(path).PersistTheme("dracula"); err == nil {
		t.Fatal("PersistTheme succeeded below a regular file")
	}
}

func TestPersistThemePreservesTOMLAndValidatesIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := `# retain this comment
provider = "openai"
theme = "default" # keep this note

[learning]
context_budget = 1200
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistTheme(" Dracula "); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# retain this comment", `provider = "openai"`, `theme = "dracula" # keep this note`, "[learning]", "context_budget = 1200"} {
		if !strings.Contains(text, want) {
			t.Fatalf("updated config missing %q: %s", want, text)
		}
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil || parsed.Theme != "dracula" {
		t.Fatalf("decoded theme = %q, err = %v", parsed.Theme, err)
	}
	assertMode(t, path, 0o640)
	if err := writer.PersistTheme("not-a-theme"); err == nil {
		t.Fatal("PersistTheme accepted an unknown theme")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != text {
		t.Fatalf("invalid theme changed config: %q, %v", unchanged, err)
	}
}

func TestPersistThemeCreatesUserPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := newRuntimePreferenceWriter(path).PersistTheme("solarized-light"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "theme = \"solarized-light\"\n"; got != want {
		t.Fatalf("new config = %q, want %q", got, want)
	}
}

func TestPersistInteractiveCreatesExplicitUserPreference(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "nested", "config.toml")
		if err := newRuntimePreferenceWriter(path).PersistInteractive(enabled); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var parsed Config
		if err := loadAndDecodeForTest(data, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.Interactive == nil || *parsed.Interactive != enabled {
			t.Fatalf("saved interactive = %v, want explicit %v", parsed.Interactive, enabled)
		}
	}
}

func TestPersistInteractivePreservesTOMLAndRoundTrips(t *testing.T) {
	for _, original := range []string{
		"# keep this comment\ninteractive = false # keep this note\n\n[skills]\npaths = [\"local\"]\n",
		"# keep this comment\n\"interactive\" = false # keep this note\n\n[skills]\npaths = [\"local\"]\n",
		"# keep this comment\n\n[skills]\npaths = [\"local\"]\ninteractive = false # keep this note\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
			t.Fatal(err)
		}
		writer := newRuntimePreferenceWriter(path)
		for _, enabled := range []bool{true, false, false} {
			if err := writer.PersistInteractive(enabled); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var parsed Config
			if err := loadAndDecodeForTest(data, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Interactive == nil || *parsed.Interactive != enabled {
				t.Fatalf("saved interactive = %v, want %v", parsed.Interactive, enabled)
			}
			for _, want := range []string{"# keep this comment", "# keep this note", "[skills]\npaths = [\"local\"]\n"} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("config lost %q: %s", want, data)
				}
			}
			if strings.HasPrefix(original, "# keep this comment\n\n[skills]") && !strings.HasSuffix(string(data), "interactive = false # keep this note\n") {
				t.Fatalf("nested interactive setting changed: %s", data)
			}
			assertMode(t, path, 0o640)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

func TestPersistSelectedSkillsCreatesSkillsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "qcode", "config.toml")
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistSelectedSkills([]string{"review-go", "alpha"}, []string{"alpha"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"[skills]", `selected = ["alpha", "review-go"]`, `selected_always = ["alpha"]`} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q: %s", want, text)
		}
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(parsed.Skills.Selected, ","), "alpha,review-go"; got != want {
		t.Fatalf("selected = %q, want %q", got, want)
	}
	if got, want := strings.Join(parsed.Skills.SelectedAlways, ","), "alpha"; got != want {
		t.Fatalf("selected_always = %q, want %q", got, want)
	}
}

func TestPersistSelectedSkillsPreservesAndUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep this comment\nprovider = \"openai\"\n\n[skills]\npaths = [\"local\"]\nselected = [\"old\"]\nselected_always = []\n\n[learning]\ncontext_budget = 1200\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistSelectedSkills(nil, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# keep this comment", `provider = "openai"`, "[skills]", `paths = ["local"]`, "selected = []", "selected_always = []", "[learning]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config lost %q: %s", want, text)
		}
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Skills.Selected == nil || len(parsed.Skills.Selected) != 0 {
		t.Fatalf("cleared selected not explicit empty: %#v", parsed.Skills.Selected)
	}
}

func TestPersistSelectedSkillsRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := newRuntimePreferenceWriter(path).PersistSelectedSkills([]string{"Bad Name"}, nil); err == nil {
		t.Fatal("PersistSelectedSkills accepted an invalid skill name")
	}
}

func loadAndDecodeForTest(data []byte, cfg *Config) error {
	return toml.Unmarshal(data, cfg)
}
