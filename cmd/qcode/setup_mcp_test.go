package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/config"
	"qcode/internal/tui"
)

func TestMCPWizardStagesAndSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := newSetupState(config.Config{}, "")
	ui := &fakeSetupUI{steps: []fakeSetupStep{
		{kind: "option", reply: "Add MCP server (0 configured, 0 staged)", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "local", nav: tui.SetupAccepted},
		{kind: "option", reply: "Local command (stdio)", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "server", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "--stdio", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "SECRET=value", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "", nav: tui.SetupAccepted},
		{kind: "option", reply: "Stage server", nav: tui.SetupAccepted},
		{kind: "option", reply: backMenuItem, nav: tui.SetupAccepted},
	}}
	var output bytes.Buffer
	cancelled, err := runMCPMenu(ui, state, &output)
	if err != nil || cancelled || !state.hasChanges() {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	path := filepath.Join(os.Getenv("HOME"), ".local", "etc", "qcode", "config.toml")
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wizard wrote before saving")
	}
	w, err := config.NewRuntimePreferenceWriter()
	if err != nil {
		t.Fatal(err)
	}
	if err = applySetupState(w, state, setupDeps{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[mcp_servers.local]") || !strings.Contains(string(data), "--stdio") {
		t.Fatalf("config: %s", data)
	}
}
func TestMCPWizardBackDiscardsDraft(t *testing.T) {
	state := newSetupState(config.Config{}, "")
	ui := &fakeSetupUI{steps: []fakeSetupStep{{kind: "option", reply: "Add MCP server (0 configured, 0 staged)", nav: tui.SetupAccepted}, {kind: "prompt", reply: "remote", nav: tui.SetupAccepted}, {kind: "option", reply: "Remote HTTP", nav: tui.SetupAccepted}, {kind: "prompt", reply: "https://example.com", nav: tui.SetupAccepted}, {kind: "option", nav: tui.SetupBack}, {kind: "option", reply: backMenuItem, nav: tui.SetupAccepted}}}
	cancelled, err := runMCPMenu(ui, state, &bytes.Buffer{})
	if err != nil || cancelled || state.hasChanges() {
		t.Fatalf("cancelled=%v changes=%v err=%v", cancelled, state.hasChanges(), err)
	}
}

func TestMCPWizardRemoteAndDuplicate(t *testing.T) {
	state := newSetupState(config.Config{}, "")
	ui := &fakeSetupUI{steps: []fakeSetupStep{
		{kind: "option", reply: "Add MCP server (0 configured, 0 staged)", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "remote", nav: tui.SetupAccepted},
		{kind: "option", reply: "Remote HTTP", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "https://example.com/mcp", nav: tui.SetupAccepted},
		{kind: "option", reply: "Stage server", nav: tui.SetupAccepted},
		{kind: "option", reply: "Add MCP server (0 configured, 1 staged)", nav: tui.SetupAccepted},
		{kind: "prompt", reply: "remote", nav: tui.SetupAccepted},
		{kind: "option", reply: backMenuItem, nav: tui.SetupAccepted},
	}}
	var out bytes.Buffer
	cancelled, err := runMCPMenu(ui, state, &out)
	if err != nil || cancelled || state.mcpServers["remote"].URL != "https://example.com/mcp" || !strings.Contains(out.String(), "already staged") {
		t.Fatalf("state=%#v output=%s err=%v", state.mcpServers, out.String(), err)
	}
}
