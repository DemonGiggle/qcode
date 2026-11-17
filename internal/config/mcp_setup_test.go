package config

import (
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"qcode/internal/mcpclient"
	"strings"
	"testing"
)

func TestAddMCPServerPreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep me\nmodel = \"example\"\n\n[skills]\npaths = [\"work\"]\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	w := newRuntimePreferenceWriter(path)
	server := mcpclient.ServerConfig{Command: "program", Args: []string{"two words", `a"b`}, Env: map[string]string{"KEY": "value"}}
	if _, err := w.AddMCPServer("my-server", server); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), original) {
		t.Fatalf("lost content: %s", data)
	}
	var cfg Config
	if err = toml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.MCPServers["my-server"]
	if got.Command != server.Command || len(got.Args) != 2 || got.Args[1] != server.Args[1] || got.Env["KEY"] != "value" {
		t.Fatalf("got %#v", got)
	}
	if _, err = w.AddMCPServer("my-server", server); err == nil {
		t.Fatal("duplicate accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("duplicate changed file")
	}
}
func TestAddMCPServerValidation(t *testing.T) {
	w := newRuntimePreferenceWriter(filepath.Join(t.TempDir(), "config.toml"))
	for _, tc := range []struct {
		name string
		s    mcpclient.ServerConfig
	}{{"bad.name", mcpclient.ServerConfig{Command: "x"}}, {"good", mcpclient.ServerConfig{URL: "ftp://example.com"}}, {"good", mcpclient.ServerConfig{Command: "x", URL: "https://example.com"}}} {
		if _, err := w.AddMCPServer(tc.name, tc.s); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
	}
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Fatal("invalid setup wrote config")
	}
}
