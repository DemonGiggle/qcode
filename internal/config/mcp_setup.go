package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"qcode/internal/mcpclient"
)

var mcpServerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// AddMCPServer adds a server to the user config without altering existing entries.
func (w *RuntimePreferenceWriter) AddMCPServer(name string, server mcpclient.ServerConfig) (string, error) {
	if w == nil {
		return "", errors.New("config writer is nil")
	}
	if err := ValidateMCPServer(name, server); err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	data, mode, exists, err := readRuntimeConfig(w.path)
	if err != nil {
		return "", err
	}
	var current Config
	if err = toml.Unmarshal(data, &current); err != nil {
		return "", fmt.Errorf("read existing config: %w", err)
	}
	if _, ok := current.MCPServers[name]; ok {
		return "", fmt.Errorf("server %q already exists in user config", name)
	}
	nl := detectedNewline(data)
	var b strings.Builder
	if len(data) > 0 {
		if !endsInNewline(data) {
			b.WriteString(nl)
		}
		b.WriteString(nl)
	}
	fmt.Fprintf(&b, "[mcp_servers.%s]%s", name, nl)
	if server.Command != "" {
		fmt.Fprintf(&b, "command = %s%s", tomlString(server.Command), nl)
		if len(server.Args) > 0 {
			parts := make([]string, 0, len(server.Args))
			for _, a := range server.Args {
				parts = append(parts, tomlString(a))
			}
			fmt.Fprintf(&b, "args = [%s]%s", strings.Join(parts, ", "), nl)
		}
		if len(server.Env) > 0 {
			keys := make([]string, 0, len(server.Env))
			for k := range server.Env {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, tomlString(k)+" = "+tomlString(server.Env[k]))
			}
			fmt.Fprintf(&b, "env = { %s }%s", strings.Join(parts, ", "), nl)
		}
	} else {
		fmt.Fprintf(&b, "url = %s%s", tomlString(server.URL), nl)
	}
	updated := append(append([]byte(nil), data...), []byte(b.String())...)
	var check Config
	if err = toml.Unmarshal(updated, &check); err != nil {
		return "", fmt.Errorf("cannot add server to existing config: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(w.path), 0700); err != nil {
		return "", err
	}
	if !exists {
		mode = 0600
	}
	if err = writeRuntimeConfigAtomically(w.path, updated, mode); err != nil {
		return "", err
	}
	return w.path, nil
}

// ValidateMCPServer checks a server before staging or persisting it.
func ValidateMCPServer(name string, server mcpclient.ServerConfig) error {
	if !mcpServerName.MatchString(name) {
		return errors.New("server name must contain 1–64 letters, digits, underscores, or hyphens")
	}
	if (server.Command == "") == (server.URL == "") {
		return errors.New("specify exactly one of command or URL")
	}
	if server.URL != "" {
		u, err := url.Parse(server.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return errors.New("URL must be an HTTP or HTTPS endpoint without credentials")
		}
	}
	if !utf8.ValidString(server.Command) || strings.ContainsRune(server.Command, 0) {
		return errors.New("invalid command")
	}
	for _, arg := range server.Args {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return errors.New("invalid argument")
		}
	}
	for key, value := range server.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return errors.New("invalid environment variable")
		}
	}
	return nil
}
