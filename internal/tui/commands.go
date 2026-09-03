package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

type slashCommand struct {
	name        string
	description string
}

var slashCommands = []slashCommand{
	{name: "/clear", description: "Clear the conversation display"},
	{name: "/diff", description: "Expand a recent file diff"},
	{name: "/exit", description: "Exit qcode"},
	{name: "/help", description: "Show available commands"},
	{name: "/model", description: "Select a provider model"},
	{name: "/new", description: "Start a session with fresh context"},
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
	mu      sync.Mutex
	out     io.Writer
	color   bool
	visible int
	width   int
}

func (m *slashCommandMenu) update(commands []slashCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var output strings.Builder
	for range m.visible {
		output.WriteString("\x1b[1A\r\x1b[2K")
	}
	rows := 0
	for _, command := range commands {
		var line string
		if m.color {
			line = fmt.Sprintf("  %s%-8s%s %s%s%s", cyan, command.name, reset, dim, command.description, reset)
		} else {
			line = fmt.Sprintf("  %-8s %s", command.name, command.description)
		}
		line = wrapANSI(line, m.width, "            ")
		rows += strings.Count(line, "\n") + 1
		output.WriteString(line)
		output.WriteByte('\n')
	}
	if output.Len() > 0 {
		_, _ = io.WriteString(m.out, output.String())
	}
	m.visible = rows
}

// dismiss removes menu rows after ReadLine has advanced below the submitted
// command. Delete-line shifts that command up instead of leaving blank space.
func (m *slashCommandMenu) dismiss(out io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.visible == 0 {
		return
	}
	fmt.Fprintf(out, "\x1b[%dA\r\x1b[%dM\x1b[1B\r", m.visible+1, m.visible)
	m.visible = 0
}

func (m *slashCommandMenu) reset() {
	m.mu.Lock()
	m.visible = 0
	m.mu.Unlock()
}
