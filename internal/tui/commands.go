package tui

import (
	"fmt"
	"io"
	"strings"
	"sync"
)

type slashCommand struct {
	name        string
	usage       string
	description string
	arguments   []helpArgument
	examples    []string
}

type helpArgument struct {
	name        string
	description string
}

var slashCommands = []slashCommand{
	{
		name: "/agent", usage: "/agent [new [name]|list|switch <id>|rename <id> <name>|cancel <id>|close <id> [--yes]]",
		description: "Create and manage extra agents",
		arguments: []helpArgument{
			{name: "new [name]", description: "Create an agent, optionally with a name."},
			{name: "list", description: "Show agents and their current status."},
			{name: "switch <id>", description: "Make another agent active."},
			{name: "rename <id> <name>", description: "Give an agent a new display name."},
			{name: "cancel <id>", description: "Stop the agent's current work."},
			{name: "close <id> [--yes]", description: "Close an agent; --yes skips confirmation."},
		},
		examples: []string{"/agent new review", "/agent switch agent-2"},
	},
	{
		name: "/clear", usage: "/clear", description: "Clear the visible conversation and redraw the header",
		examples: []string{"/clear"},
	},
	{
		name: "/compact", usage: "/compact", description: "Summarize old context to make room for new work",
		examples: []string{"/compact"},
	},
	{
		name: "/diff", usage: "/diff [number]", description: "View more lines of a recent file change",
		arguments: []helpArgument{{name: "number", description: "Diff number to expand; omit it to expand the latest diff."}},
		examples:  []string{"/diff", "/diff 2"},
	},
	{
		name: "/exit", usage: "/exit", description: "Save the session and exit qcode",
		examples: []string{"/exit"},
	},
	{
		name: "/export", usage: "/export [path]", description: "Save this session as an HTML file",
		arguments: []helpArgument{{name: "path", description: "Output path; defaults to a timestamped file in the workspace."}},
		examples:  []string{"/export", "/export review.html"},
	},
	{
		name: "/help", usage: "/help [command]", description: "List commands or explain one command",
		arguments: []helpArgument{{name: "command", description: "Command to explain; the leading / is optional."}},
		examples:  []string{"/help", "/help model"},
	},
	{
		name: "/history", usage: "/history", description: "Browse completed prompts and responses for the active agent",
		examples: []string{"/history"},
	},
	{
		name: "/learn", usage: "/learn [list|forget <id>|compact]", description: "Save, list, remove, or combine reusable instructions",
		arguments: []helpArgument{
			{name: "list", description: "Show saved learning and its IDs."},
			{name: "forget <id>", description: "Review and remove one saved item."},
			{name: "compact", description: "Review changes that consolidate duplicate items."},
		},
		examples: []string{"/learn", "/learn list", "/learn forget record-123"},
	},
	{
		name: "/maxsteps", usage: "/maxsteps [positive integer]", description: "Show or change the model-turn limit for each request",
		arguments: []helpArgument{{name: "positive integer", description: "New limit; omit it to show the current limit."}},
		examples:  []string{"/maxsteps", "/maxsteps 64"},
	},
	{
		name: "/model", usage: "/model [<model> [thinking]]", description: "Choose a model and optional thinking level",
		arguments: []helpArgument{
			{name: "model", description: "Provider model ID; omit it to open the model picker."},
			{name: "thinking", description: "Optional level supported by that model, such as low or high."},
		},
		examples: []string{"/model", "/model gpt-5.6-luna medium"},
	},
	{
		name: "/new", usage: "/new", description: "Start a fresh conversation for the active agent",
		examples: []string{"/new"},
	},
	{
		name: "/plan", usage: "/plan [off|show|act]", description: "Plan, review, or implement changes",
		arguments: []helpArgument{
			{name: "off", description: "Leave Plan mode without implementing the plan."},
			{name: "show", description: "Open the latest submitted plan."},
			{name: "act", description: "Approve and start implementing the latest plan."},
		},
		examples: []string{"/plan", "/plan show", "/plan act"},
	},
	{
		name: "/resume", usage: "/resume [session-id]", description: "Continue a saved session",
		arguments: []helpArgument{{name: "session-id", description: "Session to restore; omit it to open the session picker."}},
		examples:  []string{"/resume", "/resume 20260917-abc123"},
	},
	{
		name: "/remote", usage: "/remote", description: "Control qcode from a web browser",
		examples: []string{"/remote"},
	},
	{
		name: "/skill", usage: "/skill [name[,name...]]", description: "Choose skills for the agent",
		arguments: []helpArgument{{name: "name[,name...]", description: "Skills to enable; omit it to open the picker, or use none to clear them."}},
		examples:  []string{"/skill", "/skill review,testing", "/skill none"},
	},
	{
		name: "/quit", usage: "/quit", description: "Save the session and exit qcode",
		examples: []string{"/quit"},
	},
	{
		name: "/tool", usage: "/tool [<name> <on|off>]", description: "Enable or disable tools",
		arguments: []helpArgument{
			{name: "name", description: "Tool to change; omit both arguments to open the tool picker."},
			{name: "on|off", description: "Whether to enable or disable the tool."},
		},
		examples: []string{"/tool", "/tool web_search on"},
	},
	{
		name: "/verbose", usage: "/verbose", description: "Show or hide detailed action traces",
		examples: []string{"/verbose"},
	},
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

func findSlashCommand(name string) (slashCommand, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	for _, command := range slashCommands {
		if command.name == name {
			return command, true
		}
	}
	return slashCommand{}, false
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
