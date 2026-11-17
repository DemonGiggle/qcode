package main

import (
	"fmt"
	"io"
	"strings"

	"qcode/internal/config"
	"qcode/internal/mcpclient"
	"qcode/internal/tui"
)

func runMCPMenu(ui setupUI, state *setupState, out io.Writer) (bool, error) {
	for {
		label := fmt.Sprintf("Add MCP server (%d configured, %d staged)", len(state.initial.MCPServers), len(state.mcpServers))
		choice, nav, err := ui.SelectOptionNav("MCP servers", []string{label, backMenuItem}, nil, "", false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupBack || choice == backMenuItem {
			return false, nil
		}
		if choice != label {
			continue
		}
		name, nav, err := ui.PromptLineNav("Server name (letters, digits, _ or -)", "", false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav != tui.SetupAccepted {
			continue
		}
		name = strings.TrimSpace(name)
		if _, ok := state.initial.MCPServers[name]; ok {
			fmt.Fprintf(out, "MCP server %q already exists.\n", name)
			continue
		}
		if _, ok := state.mcpServers[name]; ok {
			fmt.Fprintf(out, "MCP server %q is already staged.\n", name)
			continue
		}
		transport, nav, err := ui.SelectOptionNav("MCP transport", []string{"Local command (stdio)", "Remote HTTP", backMenuItem}, nil, "", false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav != tui.SetupAccepted || transport == backMenuItem {
			continue
		}
		var server mcpclient.ServerConfig
		switch transport {
		case "Local command (stdio)":
			server.Command, nav, err = ui.PromptLineNav("Command", "", false)
			if err != nil {
				return false, err
			}
			if nav == tui.SetupCancelled {
				return true, nil
			}
			if nav != tui.SetupAccepted {
				continue
			}
			abandoned := false
			for {
				arg, n, e := ui.PromptLineNav("Argument (empty to finish)", "", false)
				if e != nil {
					return false, e
				}
				if n == tui.SetupCancelled {
					return true, nil
				}
				if n != tui.SetupAccepted {
					abandoned = true
					break
				}
				if arg == "" {
					break
				}
				server.Args = append(server.Args, arg)
			}
			if abandoned {
				continue
			}
			for {
				value, n, e := ui.PromptLineNav("Environment NAME=value (empty to finish; saved in config)", "", true)
				if e != nil {
					return false, e
				}
				if n == tui.SetupCancelled {
					return true, nil
				}
				if n != tui.SetupAccepted {
					abandoned = true
					break
				}
				if value == "" {
					break
				}
				key, v, ok := strings.Cut(value, "=")
				if !ok || key == "" {
					fmt.Fprintln(out, "Environment override must be NAME=value.")
					continue
				}
				if server.Env == nil {
					server.Env = map[string]string{}
				}
				server.Env[key] = v
			}
			if abandoned {
				continue
			}
		case "Remote HTTP":
			server.URL, nav, err = ui.PromptLineNav("Streamable HTTP URL", "", false)
			if err != nil {
				return false, err
			}
			if nav == tui.SetupCancelled {
				return true, nil
			}
			if nav != tui.SetupAccepted {
				continue
			}
		default:
			continue
		}
		if err = config.ValidateMCPServer(name, server); err != nil {
			fmt.Fprintf(out, "Invalid MCP server: %v\n", err)
			continue
		}
		confirm, nav, err := ui.SelectOptionNav("MCP servers can run tools without per-call approval; unavailable in sandbox", []string{"Stage server", backMenuItem}, nil, backMenuItem, false)
		if err != nil {
			return false, err
		}
		if nav == tui.SetupCancelled {
			return true, nil
		}
		if nav == tui.SetupAccepted && confirm == "Stage server" {
			state.mcpServers[name] = server
		}
	}
}
