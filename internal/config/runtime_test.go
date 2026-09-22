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
	if err := writer.PersistModel("new-model", ""); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "model = \"new-model\"\n"; got != want {
		t.Fatalf("new config = %q, want %q", got, want)
	}
	if err := writer.PersistModel("new-model", "low"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "model = \"new-model\"\nthinking = \"low\"\n"; got != want {
		t.Fatalf("explicit thinking config = %q, want %q", got, want)
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
	if err := writer.PersistModel("new-model", ""); err != nil {
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
		`model = "new-model" # keep this inline comment`,
		`max_steps = 64`,
		`unrelated = "keep"`,
		"# keep this thinking note",
		"[learning]",
		"context_budget = 1200",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("updated config missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "thinking =") {
		t.Fatalf("cleared thinking override remains: %s", got)
	}
	assertMode(t, path, 0o640)

	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != "new-model" || parsed.MaxSteps == nil || *parsed.MaxSteps != 64 || parsed.Thinking != "" {
		t.Fatalf("parsed config = %+v", parsed)
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
	if strings.Index(text, "max_steps = 64") > strings.Index(text, "[learning]") {
		t.Fatalf("max_steps was not inserted before the table: %s", text)
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

func loadAndDecodeForTest(data []byte, cfg *Config) error {
	return toml.Unmarshal(data, cfg)
}
