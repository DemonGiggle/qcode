// Package prompt contains every instruction sent to a language model.
// Keeping these strings together makes the agent's behavior easy to audit.
package prompt

import "strings"

const System = `You are qcode, a careful coding agent working in the user's current directory.

Use tools when they are needed to inspect or change the workspace. Before changing files, inspect the relevant code. Make focused changes, preserve unrelated work, and verify the result. Treat tool results as authoritative and do not repeat a tool call with unchanged arguments unless the workspace changed and another observation is necessary. Do not claim that a command succeeded unless its tool result says it did. Prefer search and targeted reads over dumping large files. Explain the completed result concisely.`

// SkillSummary is the model-visible portion of a workspace skill.
type SkillSummary struct {
	Name        string
	Description string
}

// SystemWithSkills adds a compact skill catalog to the base system prompt.
// Full instructions remain out of context until the model elects to load one.
func SystemWithSkills(skills []SkillSummary) string {
	if len(skills) == 0 {
		return System
	}
	var catalog strings.Builder
	catalog.WriteString("\n\nWorkspace skills are available. When a skill applies, call the skill tool with its exact name before doing that work. Loaded skill instructions are authoritative for their scope.\n")
	for _, skill := range skills {
		catalog.WriteString("- ")
		catalog.WriteString(skill.Name)
		if skill.Description != "" {
			catalog.WriteString(": ")
			catalog.WriteString(skill.Description)
		}
		catalog.WriteByte('\n')
	}
	return System + catalog.String()
}

const (
	ReadTool            = "Read a UTF-8 text file. Use the one-based offset and limit parameters for large files, continuing with the exact next offset shown in the result."
	WriteTool           = "Create or replace a UTF-8 text file, including parent directories."
	EditTool            = "Replace one exact occurrence of old_text in a UTF-8 text file."
	ListTool            = "List a directory. Results are sorted and include a trailing slash for directories."
	SearchTool          = "Search UTF-8 files under a directory with a Go regular expression."
	ShellTool           = "Run a command with the platform shell in the current working directory."
	ImageTool           = "Load a local image and attach it for visual analysis. Use this when the user asks about an image in the workspace. Supports PNG, JPEG, WEBP, and GIF."
	DirectoryAccessTool = "Ask the user to grant read/write access to an additional directory for this session. Use this before a shell command needs a path outside the approved workspace."
	SkillTool           = "Load the complete instructions for an available workspace skill. Call this before performing work covered by that skill."
)

// Tool parameter descriptions are model-visible prompts too, so they live here.
const (
	PathParameter       = "File path relative to the workspace"
	OffsetParameter     = "One-based line number to start reading from (default 1)"
	LimitParameter      = "Maximum lines to read (default 200)"
	ContentParameter    = "Complete new file content"
	OldTextParameter    = "Exact text to replace"
	NewTextParameter    = "Replacement text"
	DirectoryParameter  = "Directory path relative to the workspace; defaults to ."
	PatternParameter    = "Go regular expression"
	SearchPathParameter = "Directory or file to search; defaults to ."
	MaxResultsParameter = "Maximum matches (default 100)"
	CommandParameter    = "Shell command"
	TimeoutParameter    = "Timeout in milliseconds (default 120000)"
	AccessPathParameter = "File or directory path that must be accessible outside the approved workspace"
	SkillNameParameter  = "Exact name of an available workspace skill"
)
