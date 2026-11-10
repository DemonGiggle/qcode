package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"qcode/internal/transcript"
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

type shellCommandRunner interface {
	RunShell(context.Context, string) (string, error)
}

var slashCommands = []slashCommand{
	{
		name: "/agent", usage: "/agent [new [model]|list|switch <id>|rename <id> <name>|cancel <id>|close <id> [--yes]|allow-models [list [provider]|clear [provider|--all]]]",
		description: "Create and manage extra agents",
		arguments: []helpArgument{
			{name: "new [model]", description: "Create an agent with a model ID, or open the model picker when omitted. Use rename to set its display name."},
			{name: "list", description: "Show agents and their current status."},
			{name: "switch <id>", description: "Make another agent active."},
			{name: "rename <id> <name>", description: "Give an agent a new display name."},
			{name: "cancel <id>", description: "Stop the agent's current work."},
			{name: "close <id> [--yes]", description: "Close an agent; --yes skips confirmation."},
			{name: "allow-models [list [provider]|clear [provider|--all]]", description: "Curate which models main may use for sub-agents (role only: smart|balanced|cheap); no thinking picker. Main decides thinking_level at create_agent time."},
		},
		examples: []string{"/agent new qwen2.5-coder:7b", "/agent rename agent-1 review", "/agent switch agent-1", "/agent allow-models"},
	},
	{
		name: "/bash", usage: "/bash <cmd>", description: "Run a shell command in the active agent's environment",
		arguments: []helpArgument{{name: "cmd", description: "Shell command to run from the workspace."}},
		examples:  []string{"/bash pwd", "/bash printenv | sort"},
	},
	{
		name: "/clear", usage: "/clear", description: "Clear the visible conversation and redraw the header",
		examples: []string{"/clear"},
	},
	{
		name: "/compact", usage: "/compact [show]", description: "Summarize old context to make room for new work",
		arguments: []helpArgument{{name: "show", description: "Show the compacted context (system + summary + recent tail) in a history-style view for debugging compaction quality."}},
		examples:  []string{"/compact", "/compact show"},
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
		name: "/export", usage: "/export [pretty|raw]", description: "Export conversations or the full transcript as HTML",
		arguments: []helpArgument{
			{name: "pretty", description: "Default: agent tabs with completed prompt/response pairs, oldest first."},
			{name: "raw", description: "Full styled transcript, including tool activity and thinking."},
		},
		examples: []string{"/export", "/export pretty", "/export raw"},
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
		name: "/interactive", usage: "/interactive [on|off]", description: "Toggle questions during normal agent work",
		arguments: []helpArgument{{name: "on|off", description: "Enable or disable questions for this agent and save the default for future sessions; omit to show the current setting."}},
		examples:  []string{"/interactive", "/interactive on", "/interactive off"},
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
		name: "/memo", usage: "/memo [text]", description: "Save a scratchpad memo or manage saved memos",
		arguments: []helpArgument{
			{name: "text", description: "Save one memo without disturbing agent work; omit it to open the manage view."},
		},
		examples: []string{"/memo check edge case where path has spaces", "/memo"},
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
		name: "/provider", usage: "/provider [name]", description: "Switch the active tab's provider (terminal only)",
		arguments: []helpArgument{{name: "name", description: "Registered provider name; omit to open the picker. Restore the provider's last-used model when available, otherwise keep the model if supported. Main-tab changes save the default; workers stay local."}},
		examples:  []string{"/provider", "/provider openai", "/provider chatgpt"},
	},
	{
		name: "/resume", usage: "/resume [session-id]", description: "Browse, rename, pin, delete, and continue saved sessions",
		arguments: []helpArgument{{name: "session-id", description: "Session to restore; omit for the picker with a transcript preview. Tab opens Rename, Pin/Unpin, and Delete; Ctrl+D opens Delete all for this workspace, including entries hidden by the filter. Deletion requires confirmation; deleting the current session starts fresh with the main agent's settings."}},
		examples:  []string{"/resume", "/resume fead69f5000000000000000000000000"},
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
		name: "/skillplan", usage: "/skillplan [off|show|create|<rough intention>]", description: "Design, review, and create a qcode skill",
		arguments: []helpArgument{
			{name: "rough intention", description: "Start the guided flow with an initial idea for the skill."},
			{name: "show", description: "Open the latest complete skill draft."},
			{name: "create", description: "Approve and write the latest draft."},
			{name: "off", description: "Leave Skill Plan mode without creating the draft."},
		},
		examples: []string{"/skillplan", "/skillplan review database migrations", "/skillplan show", "/skillplan create"},
	},
	{
		name: "/quit", usage: "/quit", description: "Save the session and exit qcode",
		examples: []string{"/quit"},
	},
	{
		name: "/statusline", usage: "/statusline [<name> on|off] [hide|show a,b] [show|reset]", description: "Choose status bar segments",
		arguments: []helpArgument{
			{name: "name on|off", description: "Show or hide one segment: remote, mode, model, think, ws, ctx, step, tok."},
			{name: "hide|show a,b", description: "Hide or show several comma-separated segments."},
			{name: "show|reset", description: "List the current choice or show every segment again."},
		},
		examples: []string{"/statusline", "/statusline tok off", "/statusline show"},
	},
	{
		name: "/theme", usage: "/theme", description: "Choose a terminal color theme with a live preview",
		examples: []string{"/theme"},
	},
	{
		name: "/todo", usage: "/todo show", description: "Review the agent's execution checklist",
		arguments: []helpArgument{
			{name: "show", description: "Open the latest todo checklist."},
		},
		examples: []string{"/todo show"},
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
		name: "/usage", usage: "/usage", description: "Show detailed token usage for the active agent",
		examples: []string{"/usage"},
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

// slashSubcommandHint describes one completable first-argument token sourced
// from a slashCommand's arguments field. name is the completion token (first
// whitespace-separated field, pipe-expanded), usage is the full argument
// syntax for display, and description explains the subcommand.
type slashSubcommandHint struct {
	name        string
	usage       string
	description string
}

// slashSuggestion is a single row for the fixed-input suggestion panel and
// the legacy popup menu. subcommand distinguishes child rows from command
// rows so renderers can apply distinct styling.
type slashSuggestion struct {
	name        string
	description string
	subcommand  bool
}

func slashSubcommandHints(command slashCommand) []slashSubcommandHint {
	var hints []slashSubcommandHint
	seen := make(map[string]struct{})
	for _, arg := range command.arguments {
		name := strings.TrimSpace(arg.name)
		if name == "" {
			continue
		}
		fields := strings.Fields(name)
		if len(fields) == 0 {
			continue
		}
		first := fields[0]
		for _, part := range strings.Split(first, "|") {
			token := strings.TrimSpace(part)
			if token == "" {
				continue
			}
			lower := strings.ToLower(token)
			if _, dup := seen[lower]; dup {
				continue
			}
			seen[lower] = struct{}{}
			hints = append(hints, slashSubcommandHint{
				name:        token,
				usage:       name,
				description: arg.description,
			})
		}
	}
	return hints
}

func isExactSubcommandToken(hints []slashSubcommandHint, token string) bool {
	for _, hint := range hints {
		if strings.EqualFold(hint.name, token) {
			return true
		}
	}
	return false
}

// matchingSlashSubcommands resolves a line to a single parent command and its
// filtered first-argument hints. It handles "/agent", "/agent ", "/agent n",
// and "/plan s" forms with a case-insensitive prefix filter on the first arg
// token. Lines with deeper args (two or more arg tokens, or an exact first
// token followed by trailing space) pass through with ok=false so callers
// hide the subcommand menu.
func matchingSlashSubcommands(line string) ([]slashSubcommandHint, slashCommand, bool) {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, "\r\n") {
		return nil, slashCommand{}, false
	}
	idx := strings.IndexAny(line, " \t")
	var cmdToken, rest string
	hasSpace := idx >= 0
	if !hasSpace {
		cmdToken = line
	} else {
		cmdToken = line[:idx]
		rest = line[idx+1:]
	}
	if cmdToken == "" || cmdToken == "/" {
		return nil, slashCommand{}, false
	}
	var parent slashCommand
	found := false
	if !hasSpace {
		lower := strings.ToLower(cmdToken)
		var matches []slashCommand
		for _, command := range slashCommands {
			if strings.HasPrefix(strings.ToLower(command.name), lower) {
				matches = append(matches, command)
			}
		}
		if len(matches) != 1 {
			return nil, slashCommand{}, false
		}
		parent, found = matches[0], true
	} else {
		if command, ok := findSlashCommand(cmdToken); ok {
			parent, found = command, true
		} else {
			lower := strings.ToLower(cmdToken)
			var matches []slashCommand
			for _, command := range slashCommands {
				if strings.HasPrefix(strings.ToLower(command.name), lower) {
					matches = append(matches, command)
				}
			}
			if len(matches) != 1 {
				return nil, slashCommand{}, false
			}
			parent, found = matches[0], true
		}
	}
	if !found || len(parent.arguments) == 0 {
		return nil, slashCommand{}, false
	}
	hints := slashSubcommandHints(parent)
	if len(hints) == 0 {
		return nil, slashCommand{}, false
	}
	if !hasSpace {
		return hints, parent, true
	}
	trimmedLeft := strings.TrimLeft(rest, " \t")
	if trimmedLeft == "" {
		return hints, parent, true
	}
	fields := strings.Fields(trimmedLeft)
	if len(fields) == 0 {
		return hints, parent, true
	}
	if len(fields) >= 2 {
		return nil, slashCommand{}, false
	}
	token := fields[0]
	if strings.HasSuffix(rest, " ") || strings.HasSuffix(rest, "\t") {
		if isExactSubcommandToken(hints, token) {
			return nil, slashCommand{}, false
		}
	}
	lowerToken := strings.ToLower(token)
	var filtered []slashSubcommandHint
	for _, hint := range hints {
		if strings.HasPrefix(strings.ToLower(hint.name), lowerToken) {
			filtered = append(filtered, hint)
		}
	}
	return filtered, parent, true
}

// slashSuggestions combines command matches and subcommand hints into panel
// rows. When the filter yields exactly one command with subcommand context,
// the parent row comes first followed by filtered subcommand rows.
func slashSuggestions(line string) []slashSuggestion {
	commands := matchingSlashCommands(line)
	if len(commands) == 1 {
		if hints, parent, ok := matchingSlashSubcommands(line); ok {
			suggestions := make([]slashSuggestion, 0, 1+len(hints))
			suggestions = append(suggestions, slashSuggestion{name: parent.name, description: parent.description})
			for _, hint := range hints {
				suggestions = append(suggestions, slashSuggestion{name: hint.name, description: hint.description, subcommand: true})
			}
			return suggestions
		}
		return []slashSuggestion{{name: commands[0].name, description: commands[0].description}}
	}
	if len(commands) > 1 {
		suggestions := make([]slashSuggestion, 0, len(commands))
		for _, command := range commands {
			suggestions = append(suggestions, slashSuggestion{name: command.name, description: command.description})
		}
		return suggestions
	}
	if hints, parent, ok := matchingSlashSubcommands(line); ok {
		suggestions := make([]slashSuggestion, 0, 1+len(hints))
		suggestions = append(suggestions, slashSuggestion{name: parent.name, description: parent.description})
		for _, hint := range hints {
			suggestions = append(suggestions, slashSuggestion{name: hint.name, description: hint.description, subcommand: true})
		}
		return suggestions
	}
	return nil
}

// completeSlashSubcommand completes a single command's first-argument token.
// "/agent n" + Tab becomes "/agent new ", "/agent" becomes "/agent ", and
// "/agent " becomes "/agent <first-hint> ". It returns false when the line is
// not in a single-command subcommand context or when no hints match.
func completeSlashSubcommand(line string, pos int) (string, int, bool) {
	if !strings.HasPrefix(line, "/") || strings.ContainsAny(line, "\r\n") {
		return line, pos, false
	}
	if pos < 0 || pos > len(line) {
		return line, pos, false
	}
	prefix := line[:pos]
	suffix := line[pos:]
	if strings.TrimSpace(suffix) != "" {
		return line, pos, false
	}
	hints, parent, ok := matchingSlashSubcommands(prefix)
	if !ok || len(hints) == 0 {
		return line, pos, false
	}
	var newPrefix string
	if strings.IndexAny(prefix, " \t") < 0 {
		newPrefix = parent.name + " "
	} else {
		newPrefix = parent.name + " " + hints[0].name + " "
	}
	newLine := newPrefix + strings.TrimLeft(suffix, " \t")
	if newLine == line {
		return line, pos, false
	}
	return newLine, len(newPrefix), true
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

func (u *UI) runBashCommand(ctx context.Context, command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		u.printSystemMessage(yellow + "Usage: /bash <cmd>" + reset)
		return
	}
	runner, ok := u.runner.(shellCommandRunner)
	if !ok {
		u.printSystemMessage(yellow + "The active agent cannot run shell commands." + reset)
		return
	}

	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if u.input != nil {
		u.input.setCancel(cancel)
		defer func() {
			u.input.setCancel(nil)
			u.updateActiveCancellation()
		}()
	}
	out := transcript.NewGroup(u.display)
	u.printMessage(out, dim+"Running shell command (Ctrl+C to cancel): "+sanitizeDiffLine(command, "<ESC>")+reset)
	output, err := runner.RunShell(commandCtx, command)
	if output != "" {
		u.printMessage(out, dim+"Shell output:"+reset+"\n"+sanitizeShellOutput(output))
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			u.printMessage(out, yellow+"Cancelled"+reset)
		} else {
			u.printMessage(out, yellow+"Shell command failed: "+sanitizeDiffLine(err.Error(), "<ESC>")+reset)
		}
		return
	}
	if output == "" {
		u.printMessage(out, dim+"Command completed with no output."+reset)
	}
}

func sanitizeShellOutput(output string) string {
	lines := strings.Split(output, "\n")
	for i := range lines {
		lines[i] = sanitizeDiffLine(lines[i], "<ESC>")
	}
	return strings.Join(lines, "\n")
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

// updateSubcommands renders a single parent command row on top with its
// filtered subcommand rows below using distinct indented styling.
func (m *slashCommandMenu) updateSubcommands(parent slashCommand, hints []slashSubcommandHint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var output strings.Builder
	for range m.visible {
		output.WriteString("\x1b[1A\r\x1b[2K")
	}
	rows := 0
	var parentLine string
	if m.color {
		parentLine = fmt.Sprintf("  %s%-8s%s %s%s%s", cyan, parent.name, reset, dim, parent.description, reset)
	} else {
		parentLine = fmt.Sprintf("  %-8s %s", parent.name, parent.description)
	}
	parentLine = wrapANSI(parentLine, m.width, "            ")
	rows += strings.Count(parentLine, "\n") + 1
	output.WriteString(parentLine)
	output.WriteByte('\n')
	for _, hint := range hints {
		var line string
		if m.color {
			line = fmt.Sprintf("    %s%s%s %s%s%s", green, hint.name, reset, dim, hint.description, reset)
		} else {
			line = fmt.Sprintf("    %s %s", hint.name, hint.description)
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

// updateForLine refreshes the popup for any slash input, showing subcommand
// rows when the filter yields exactly one command with subcommand context.
func (m *slashCommandMenu) updateForLine(line string) {
	commands := matchingSlashCommands(line)
	if len(commands) == 1 {
		if hints, parent, ok := matchingSlashSubcommands(line); ok {
			m.updateSubcommands(parent, hints)
			return
		}
		m.update(commands)
		return
	}
	if len(commands) > 1 {
		m.update(commands)
		return
	}
	if hints, parent, ok := matchingSlashSubcommands(line); ok {
		m.updateSubcommands(parent, hints)
		return
	}
	m.update(nil)
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
