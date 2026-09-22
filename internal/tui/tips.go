package tui

import (
	"fmt"
	"strings"
	"time"
)

// tipVivid is the vivid 256-color orange-gold used for the startup tip.
// It is intentionally distinct from the dim/green/cyan/yellow banner text.
const tipVivid = "\x1b[1;38;5;220m"

// tipTexts holds every startup tip. Each entry is written to render as one or
// two wrapped lines at 80 columns once prefixed with "Tip: ".
var tipTexts = []string{
	// Commands and terminal navigation.
	"Type `/` to see matching slash commands, keep typing to filter, and press Tab to complete the first match.",
	"Run `/help` to list commands, or `/help <command>` (e.g. `/help model`) for arguments and examples.",
	"Run `/model` to search the provider catalog with Up/Down + Enter, or `/model <id> [thinking]` to set it directly.",
	"Run `/tool` to toggle tools with Space + Enter, or `/tool <name> on|off`; web tools start disabled until you enable them.",
	"Run `/skill` to pick reusable instruction bundles; only selected skills reach the model and its `skill` tool.",
	"Run `/agent new review` to create a named agent; qcode supports up to 20 tabs, including main.",
	"Switch agent tabs with Ctrl+PgUp/PgDn or Alt+,/. — each tab keeps its own draft, history, model, and tool settings.",
	"Press PgUp/PgDn to scroll history while running; scrolling pauses live output, PgDn to the bottom resumes it.",
	"Press Ctrl+C to cancel the running prompt, a picker, or a `/bash` command without exiting qcode.",
	"Keep typing while the agent works — extra prompts queue as `Queued #N` and run in FIFO order.",
	"Run `/history` to search completed prompts newest-first; Enter views one response, Esc returns to the live view.",
	"`write` and `edit` show 10-line diff previews; run `/diff` or `/diff N` to expand one up to 200 lines.",
	"Run `/plan` for read-only Plan mode, `/plan show` to review, `/plan act` to implement, `/plan off` to leave.",
	"Run `/clear` to redraw the header and see the active agent's enabled and disabled tools.",
	"Run `/bash <cmd>` to execute a shell command from the workspace in the active agent's environment.",
	"Run `/verbose` to toggle timestamped tool traces, including tool arguments, for troubleshooting.",
	"Move through your draft with Left/Right; Home/End or Ctrl+A/E jump to the line ends.",
	"Use Ctrl/Alt+Left/Right or Alt+B/F to move by word; Ctrl+W deletes the previous word.",
	"Press Up/Down at the prompt to recall earlier input and return to your unfinished draft.",
	"In pickers type to filter, move with Up/Down or PgUp/PgDn, Enter to apply, Esc to go back, Ctrl+C to cancel all.",
	"Run `/compact` to summarize old context; watch STEP, CONTEXT % left, and MODE PLAN in the status bar.",
	"Set `NO_COLOR=1` before launch to disable styling, or `QCODE_ASCII=1` for ASCII interface glyphs.",

	// Agents and queued work.
	"Run `/agent list` to browse agent status, queues, models, and recent findings; press Enter to switch tabs.",
	"Run `/agent switch <id>` to jump to a tab, or `/agent rename <id> <name>` to label its task.",
	"Run `/agent cancel <id>` to stop another agent's current prompt; `/agent close <id>` closes its tab.",
	"Each agent accepts up to 16 waiting prompts; other tabs keep working independently of its queue.",
	"Cancelling the current prompt lets the next queued prompt start; completed file changes are kept.",
	"Ask main to delegate independent tasks to other agents; all tabs share the workspace, so use clear task boundaries.",
	"Ask main to consult agents about their findings, or search earlier work, including work from closed tabs.",
	"Agent work stays searchable within the saved session after `/compact`, `/new`, or closing a tab.",
	"Agents created by main inherit its model, tools, skills, directory grants, and step limit when created.",
	"Set `--agent-timeout 90s` to change how long main waits for consultations; the deadline includes queue time.",

	// Models, context, and usage.
	"The `/model` picker offers supported thinking levels after model selection; Esc returns to the model list.",
	"In normal interactive mode, `/model` and `/maxsteps` on main save user defaults; changes in other tabs stay local.",
	"Run `/maxsteps` to see the model-turn limit, or `/maxsteps 64` to allow more turns per request.",
	"`CONTEXT` shows remaining model capacity; a `~` marks estimated usage and `unknown` means the limit is unavailable.",
	"Use `--context-window 32768` for an unknown context limit; it sets accounting capacity, not the provider's actual limit.",
	"Changing models clears a custom context-window override; check the CONTEXT display after switching.",
	"Automatic compaction starts at 80% context use when capacity is known; set `auto_compact_threshold` to change it.",
	"Use `--disable-auto-compact` to turn off automatic summaries; `/compact` remains available.",
	"`TOK I` and `O` count input and output tokens across requests for the active agent; `?` marks incomplete totals.",
	"Widen the terminal to 100 columns to see the combined token total; changing models retains session totals.",

	// Saved sessions and exports.
	"Run `/resume` to browse this workspace's saved sessions, or `/resume <session-id>` to restore one directly.",
	"Sessions autosave during interactive work; `/resume` restores tabs, drafts, models, settings, and retained output.",
	"Finish or cancel running agents before `/resume`; a session already open in another process cannot be selected.",
	"After a crash, `/resume` restores the latest checkpoint; unfinished requests are interrupted and need your direction.",
	"Run `/new` to reset the active agent's conversation, token totals, plan, web tools, and added directory grants.",
	"Run `/export` or `/export pretty` for an HTML timeline of completed prompts and responses, with a tab for each agent.",
	"Run `/export raw` for the full styled transcript, including tool activity, thinking, and diffs.",
	"Export filenames are generated automatically; the terminal saves in your workspace and the browser downloads the file.",
	"Run `/exit` or `/quit` to save and leave; one-shot and demo runs do not create saved sessions.",
	"Saved sessions follow the workspace's canonical path: symlink aliases share sessions, separate worktrees do not.",

	// Planning and skills.
	"Enter `/plan` while idle to investigate safely: Plan mode blocks file changes, shell commands, and delegation.",
	"Planning questions accept a listed choice or a custom answer; Ctrl+C cancels the planning request.",
	"Use `/plan show` to scroll through the latest submitted plan, then `/plan act` to start implementation.",
	"Run `/plan off` to leave planning without executing the plan; `/new` clears the saved plan.",
	"Create `.qcode/skills/<name>/SKILL.md` for workspace instructions, then run `/skill` to discover and select them.",
	"Put shared personal skills in `~/.qcode/skills/<name>/SKILL.md`; `.agents/skills` also works inside a workspace.",
	"Use lowercase letters, digits, hyphens, or underscores in skill names; keep each SKILL.md within 64 KiB.",
	"Start SKILL.md with a short heading or a front-matter `description:` line so the picker explains its purpose.",
	"Add `paths = [\".team/skills\"]` under `[skills]` in config.toml to discover extra skill directories.",
	"For duplicate skill names, `.qcode/skills` overrides `.agents/skills` and user skills; configured paths take priority.",
	"Run `/skill name1,name2` to select skills directly, or `/skill none` to clear the active agent's selection.",
	"Run `/skill` again after adding a skill; discovery refreshes without restarting qcode.",
	"Selected skills initially add only names and summaries to context; the agent loads full instructions when needed.",

	// Durable learning.
	"Run `/learn` after useful work to propose reusable preferences; review the preview and answer yes to save.",
	"Learning is shared across your workspaces on this device; save general preferences and procedures with clear scope.",
	"Run `/learn list` for saved record IDs, then `/learn forget <id>` to review and remove an item.",
	"Run `/learn compact` to review merges and cleanup of saved learning; approved changes create a backup.",
	"Set `context_budget = 0` under `[learning]` to stop retrieving learning while keeping saved records.",
	"`/new` keeps global learning; use `/learn forget <id>` to remove a preference you no longer want.",

	// File tools, web access, and sandboxing.
	"Name a workspace image in your prompt to inspect it with a vision model; PNG, JPEG, WEBP, and GIF are supported.",
	"For large files, ask for a line range; the read tool supports offsets and limits, and search reports matching line numbers.",
	"Enable `/tool web_search on` for public web searches; the built-in DuckDuckGo backend needs no API key.",
	"Enable `/tool web_fetch on` to read public HTTP(S) pages; it extracts text without executing JavaScript or reading PDFs.",
	"Web tools can be enabled separately per agent; `/new` disables them, and one-shot mode leaves them off.",
	"Use `--sandbox` on Linux for bubblewrap isolation; check the startup notice to confirm it is active.",
	"Enabling either web tool also allows sandbox networking; disabling both restores network isolation.",
	"When sandboxed work needs another directory, the agent can request access; review or edit the path before approving.",
	"Use `--sandbox-command-path ~/.local/bin` for trusted executables in the sandbox; repeat the flag for more directories.",
	"Sandbox command directories are mounted read-only and persist across `/new`; added writable directory grants do not.",
	"Sandbox shell calls get a fresh private `/tmp`; use workspace files for data that must survive between calls.",

	// Browser control.
	"Run `/remote` to control the same agents and queues from a browser, including a phone.",
	"Choose Pure Web only on a trusted LAN; choose Tailscale for HTTPS access from browsers on your tailnet.",
	"Remote login links are single-use and expire after three minutes; run `/remote` again for another browser.",
	"If a remote QR code does not fit, enlarge the terminal or use its clickable login heading.",
	"Use `/remote` in the terminal and confirm Close Connection to revoke all browser access.",
	"Reloading the same browser tab keeps remote access; a fresh browser session needs a new login link.",

	// Command-line usage and configuration.
	"Run `qcode --help` from your shell for startup flags, or `qcode --list-providers` for built-in provider names.",
	"Start with `qcode --cwd /path/to/project` to choose a workspace without changing your shell's directory.",
	"Pass a prompt such as `qcode \"explain this repository\"` for a one-shot answer, or pipe prompt text into qcode.",
	"In one-shot mode, answers go to stdout and activity goes to stderr; redirect them separately for scripts.",
	"Use `qcode --json-events \"your task\" 2>events.jsonl` for structured activity and per-completion token usage.",
	"Run `qcode --demo` for an offline interactive showcase with mocked tools, queued prompts, diffs, and model selection.",
	"Use `--provider` and `--model` at launch, or set `QCODE_PROVIDER` and `QCODE_MODEL` for shell defaults.",
	"Use `--base-url` or `QCODE_BASE_URL` to point qcode at your provider endpoint.",
	"Set `QCODE_API_KEY` or `OPENAI_API_KEY` for provider credentials; `--api-key` overrides both.",
	"Use `--thinking` or `QCODE_THINKING` to choose a supported reasoning level at launch.",
	"Flags override environment variables, which override config.toml settings; check these layers when a setting surprises you.",
	"Save your preferred provider, model, thinking level, and step limit in config.toml for future sessions.",
	"A config.toml beside the executable takes priority over user and system config files; unspecified settings are inherited.",
	"Run `qcode --version` to check your installed version, or `qcode update` from your shell to install the latest release.",
}

// startupTipIndex is chosen once per process launch so the header tip stays
// stable across /clear redraws but varies between restarts.
var startupTipIndex = pickStartupTipIndex()

func pickStartupTipIndex() int {
	if len(tipTexts) == 0 {
		return 0
	}
	return int(time.Now().UnixNano()%int64(len(tipTexts))) % len(tipTexts)
}

// startupTip returns the single random tip selected for this process.
func startupTip() string {
	if len(tipTexts) == 0 {
		return ""
	}
	index := startupTipIndex % len(tipTexts)
	if index < 0 {
		index = 0
	}
	return tipTexts[index]
}

// formatTip renders the startup tip for the given width. The result is at
// most two visual lines and uses vivid styling only when color is enabled.
func formatTip(width int, colorEnabled bool) string {
	tip := startupTip()
	if tip == "" {
		return ""
	}
	line := "Tip: " + tip
	if width > 0 {
		line = wrapANSI(line, width, "  ")
		lines := strings.Split(line, "\n")
		if len(lines) > 2 {
			lines = lines[:2]
		}
		line = strings.Join(lines, "\n")
	}
	if !colorEnabled {
		return line
	}
	return tipVivid + line + reset
}

// printTip prints the startup tip below the Tools enabled/disabled block.
func (u *UI) printTip() {
	color := false
	if u.out != nil {
		color = ColorEnabled(u.out)
	}
	formatted := formatTip(u.width, color)
	if formatted == "" {
		return
	}
	fmt.Fprintln(u.display, formatted)
	fmt.Fprintln(u.display)
}
