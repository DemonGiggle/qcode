package tui

import (
	"fmt"
	"io"
	"strings"
)

type slashCommand struct {
	name        string
	description string
}

var slashCommands = []slashCommand{
	{name: "/clear", description: "Clear the conversation display"},
	{name: "/exit", description: "Exit qcode"},
	{name: "/help", description: "Show available commands"},
	{name: "/quit", description: "Exit qcode"},
	{name: "/verbose", description: "Toggle detailed action traces"},
}

func matchingSlashCommands(line string) []slashCommand {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, " \t\r\n") {
		return nil
	}
	matches := make([]slashCommand, 0, len(slashCommands))
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, line) {
			matches = append(matches, command)
		}
	}
	return matches
}

type slashCommandMenu struct {
	out     io.Writer
	color   bool
	visible int
}

func (m *slashCommandMenu) update(commands []slashCommand) {
	var output strings.Builder
	for range m.visible {
		output.WriteString("\x1b[1A\r\x1b[2K")
	}
	for _, command := range commands {
		if m.color {
			fmt.Fprintf(&output, "  %s%-8s%s %s%s%s\n", cyan, command.name, reset, dim, command.description, reset)
		} else {
			fmt.Fprintf(&output, "  %-8s %s\n", command.name, command.description)
		}
	}
	if output.Len() > 0 {
		_, _ = io.WriteString(m.out, output.String())
	}
	m.visible = len(commands)
}

// dismiss removes menu rows after ReadLine has advanced below the submitted
// command. Delete-line shifts that command up instead of leaving blank space.
func (m *slashCommandMenu) dismiss(out io.Writer) {
	if m.visible == 0 {
		return
	}
	fmt.Fprintf(out, "\x1b[%dA\r\x1b[%dM\x1b[1B\r", m.visible+1, m.visible)
	m.visible = 0
}
