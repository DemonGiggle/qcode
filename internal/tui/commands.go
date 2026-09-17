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
	{name: "/agent", description: "Create and manage extra agents"},
	{name: "/clear", description: "Clear the visible conversation"},
	{name: "/compact", description: "Summarize old context to make room"},
	{name: "/diff", description: "View more lines of a recent file change"},
	{name: "/exit", description: "Exit qcode"},
	{name: "/export", description: "Save this session as an HTML file"},
	{name: "/help", description: "List commands and what they do"},
	{name: "/learn", description: "Save, list, remove, or combine instructions"},
	{name: "/maxsteps", description: "Show or change the model-turn limit"},
	{name: "/model", description: "Change the model or thinking level"},
	{name: "/new", description: "Start a fresh conversation"},
	{name: "/plan", description: "Plan, review, or implement changes"},
	{name: "/resume", description: "Continue a saved session"},
	{name: "/remote", description: "Control qcode from a web browser"},
	{name: "/skill", description: "Choose skills for the agent"},
	{name: "/quit", description: "Exit qcode"},
	{name: "/tool", description: "Enable or disable tools"},
	{name: "/verbose", description: "Show or hide detailed action traces"},
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

func (m *slashCommandMenu) setWidth(width int) {
	m.mu.Lock()
	m.width = width
	m.mu.Unlock()
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
