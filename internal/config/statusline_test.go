package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStatuslineHiddenLayers(t *testing.T) {
	dir := t.TempDir()
	low := writeConfig(t, dir, "low.toml", "statusline_hidden = [\"tok\", \"step\"]\n")
	high := writeConfig(t, dir, "high.toml", "statusline_hidden = [\"ctx\"]\n")
	cfg, _, diagnostics := load([]string{high, low})
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	if got, want := strings.Join(cfg.StatuslineHidden, ","), "ctx"; got != want {
		t.Fatalf("hidden = %q, want %q (override, not additive)", got, want)
	}
	empty := writeConfig(t, dir, "empty.toml", "statusline_hidden = []\n")
	emptyCfg, _, emptyDiags := load([]string{empty})
	if len(emptyDiags) != 0 || len(emptyCfg.StatuslineHidden) != 0 {
		t.Fatalf("empty hidden = (%+v, %v)", emptyCfg.StatuslineHidden, emptyDiags)
	}
}

func TestLoadRejectsUnknownStatuslineSegment(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "config.toml", "statusline_hidden = [\"bogus\"]\n")
	_, _, diagnostics := load([]string{path})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0].Error(), "statusline_hidden") {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestPersistStatuslineHiddenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep this comment\nprovider = \"openai\"\nmodel = \"old-model\"\n\n[learning]\ncontext_budget = 1200\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	writer := newRuntimePreferenceWriter(path)
	if err := writer.PersistStatuslineHidden([]string{"tok", "step", "tok", " CTX "}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"# keep this comment", `provider = "openai"`, "[learning]", "statusline_hidden"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q: %s", want, text)
		}
	}
	var parsed Config
	if err := loadAndDecodeForTest(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(parsed.StatuslineHidden, ","), "ctx,step,tok"; got != want {
		t.Fatalf("hidden = %q, want %q (canonical priority order)", got, want)
	}
	if err := writer.PersistStatuslineHidden(nil); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "statusline_hidden") {
		t.Fatalf("reset did not remove override: %s", data)
	}
}

func TestPersistStatuslineHiddenRejectsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := newRuntimePreferenceWriter(path).PersistStatuslineHidden([]string{"bogus"}); err == nil {
		t.Fatal("PersistStatuslineHidden succeeded for unknown segment")
	}
}

func TestPersistStatuslineHiddenSequentialUpdates(t *testing.T) {
	for _, test := range []struct {
		name, key, value, comment, newline string
	}{
		{name: "single line", key: "statusline_hidden", value: `["tok"]`, newline: "\n"},
		{name: "multiline with comments", key: "statusline_hidden", value: "[\n  \"step\", # inside array\n  \"tok\",\n]", comment: " # keep this note", newline: "\n"},
		{name: "empty array", key: "statusline_hidden", value: "[]", newline: "\n"},
		{name: "empty multiline array", key: "statusline_hidden", value: "[\n  # inside empty array\n]", newline: "\n"},
		{name: "quoted key", key: `"statusline_hidden"`, value: `["tok"]`, comment: " # keep this note", newline: "\n"},
		{name: "CRLF", key: "statusline_hidden", value: "[\r\n  \"tok\",\r\n]", comment: " # keep this note", newline: "\r\n"},
		{name: "no final newline", key: "statusline_hidden", value: `["tok"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			before := "# keep this comment\nprovider = \"openai\"\n"
			following := ""
			if test.newline != "" {
				before = strings.ReplaceAll(before, "\n", test.newline)
				following = strings.Join([]string{"unrelated = \"keep\"", "", "[learning]", "context_budget = 1200", "statusline_hidden = [\"ws\"]", ""}, test.newline)
			}
			assignment := test.key + "  =\t"
			after := test.comment + test.newline + following
			if err := os.WriteFile(path, []byte(before+assignment+test.value+after), 0o640); err != nil {
				t.Fatal(err)
			}
			writer := newRuntimePreferenceWriter(path)
			for _, update := range []struct {
				hidden []string
				value  string
			}{
				{[]string{"tok"}, `["tok"]`},
				{[]string{"step", "tok"}, `["step", "tok"]`},
				{[]string{"ctx", "step", "tok"}, `["ctx", "step", "tok"]`},
				{[]string{"ctx", "step", "tok"}, `["ctx", "step", "tok"]`},
				{[]string{"step", "tok"}, `["step", "tok"]`},
				{[]string{"tok"}, `["tok"]`},
				{nil, ""},
			} {
				if err := writer.PersistStatuslineHidden(update.hidden); err != nil {
					t.Fatalf("persist %v: %v", update.hidden, err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var parsed Config
				if err := loadAndDecodeForTest(data, &parsed); err != nil {
					t.Fatalf("config is invalid after persisting %v: %v\n%s", update.hidden, err, data)
				}
				if got, want := strings.Join(parsed.StatuslineHidden, ","), strings.Join(update.hidden, ","); got != want {
					t.Fatalf("hidden = %q, want %q", got, want)
				}
				want := before + assignment + update.value + after
				if len(update.hidden) == 0 {
					want = before + following
					if test.comment != "" {
						want = before + after
					}
				}
				if string(data) != want {
					t.Fatalf("config after persisting %v = %q, want %q", update.hidden, data, want)
				}
				assertMode(t, path, 0o640)
			}
		})
	}
}
