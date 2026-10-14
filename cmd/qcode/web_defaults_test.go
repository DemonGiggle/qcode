package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/tools"
)

func TestStartupWebDefaultsFollowEffectiveSandbox(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux sandbox and configuration fixtures")
	}
	for _, tc := range []struct {
		name         string
		config       string
		flags        []string
		probeExit    string
		missingBwrap bool
		exposesHome  bool
		piped        bool
		wantSandbox  bool
		wantProbe    bool
		wantWarning  string
	}{
		{name: "outside sandbox"},
		{name: "configured sandbox", config: "sandbox = true\n", wantSandbox: true, wantProbe: true},
		{name: "sandbox flag", flags: []string{"--sandbox"}, wantSandbox: true, wantProbe: true},
		{name: "flag disables configured sandbox", config: "sandbox = true\n", flags: []string{"--sandbox=false"}},
		{name: "flag enables configured sandbox", config: "sandbox = false\n", flags: []string{"--sandbox"}, wantSandbox: true, wantProbe: true},
		{name: "backend outside sandbox", config: "[web_search]\nbackend = \"duckduckgo\"\n"},
		{name: "backend inside sandbox", config: "sandbox = true\n[web_search]\nbackend = \"duckduckgo\"\n", wantSandbox: true, wantProbe: true},
		{name: "sandbox probe fails", config: "sandbox = true\n", probeExit: "1", wantProbe: true, wantWarning: "Sandbox unavailable:"},
		{name: "bubblewrap missing", flags: []string{"--sandbox"}, missingBwrap: true, wantWarning: "Sandbox unavailable:"},
		{name: "workspace exposes home", config: "sandbox = true\n", exposesHome: true, wantWarning: "Sandbox disabled:"},
		{name: "piped outside sandbox", piped: true},
		{name: "piped inside sandbox", flags: []string{"--sandbox"}, piped: true, wantSandbox: true, wantProbe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root, bin := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin)
			probePath := filepath.Join(t.TempDir(), "probe-arguments")
			t.Setenv("QCODE_TEST_SANDBOX_PROBE", probePath)
			if !tc.missingBwrap {
				exit := tc.probeExit
				if exit == "" {
					exit = "0"
				}
				script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >\"$QCODE_TEST_SANDBOX_PROBE\"\nexit " + exit + "\n"
				if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				if tc.wantProbe {
					_, err := tools.CheckSandbox(root, false)
					if err != nil && strings.Contains(err.Error(), "openat2") {
						t.Skip("secure openat2 path confinement unavailable")
					}
					if err := os.Remove(probePath); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.exposesHome {
				root = home
			}
			configPath := filepath.Join(home, ".local", "etc", "qcode", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte(tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
			provider := &autoloadProvider{}
			const providerName = "web-defaults-cli-test"
			llm.Register(providerName, func(llm.Config) (llm.Provider, error) { return provider, nil })
			newFile := func(name string) *os.File {
				t.Helper()
				file, err := os.CreateTemp(t.TempDir(), name)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { file.Close() })
				return file
			}
			in, out, diagnostics := newFile("stdin"), newFile("stdout"), newFile("stderr")
			args := []string{"--provider", providerName, "--model", "test", "--cwd", root}
			args = append(args, tc.flags...)
			if tc.piped {
				if _, err := in.WriteString("hello"); err != nil {
					t.Fatal(err)
				}
				if _, err := in.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
			} else {
				args = append(args, "hello")
			}
			if err := run(args, in, out, diagnostics); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"web_fetch", "web_search", "request_directory_access"} {
				advertised := false
				for _, tool := range provider.request.Tools {
					if tool.Name == name {
						advertised = true
					}
				}
				want := !tc.wantSandbox
				if name == "request_directory_access" {
					want = tc.wantSandbox
				}
				if advertised != want {
					t.Fatalf("%s advertised = %v, want %v", name, advertised, want)
				}
			}
			probeArgs, err := os.ReadFile(probePath)
			if tc.wantProbe {
				if err != nil || !strings.Contains(string(probeArgs), "--unshare-net") {
					t.Fatalf("startup did not probe network isolation: %q, %v", probeArgs, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unexpected sandbox probe: %q, %v", probeArgs, err)
			}
			warnings, err := os.ReadFile(diagnostics.Name())
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantWarning != "" {
				if !strings.Contains(string(warnings), tc.wantWarning) || !strings.Contains(string(warnings), "Continuing without sandbox.") {
					t.Fatalf("fallback warning missing: %s", warnings)
				}
			} else if strings.Contains(string(warnings), "Continuing without sandbox.") {
				t.Fatalf("unexpected fallback: %s", warnings)
			}
		})
	}
}
