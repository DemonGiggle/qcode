# Skills

Skills let you attach focused, reusable instructions to a session. Place a `SKILL.md` in `~/.qcode/skills/<name>/` for your user account, or `.qcode/skills/<name>/` / `.agents/skills/<name>/` for one workspace.

## Discovery and precedence

qcode scans three directories in ascending precedence (later overrides earlier on name collisions):

| Precedence | Location | Scope |
|---|---|---|
| Lowest | `~/.qcode/skills/` | User-level |
| Middle | `<workspace>/.agents/skills/` | Workspace |
| Highest | `<workspace>/.qcode/skills/` | Workspace (overrides) |

A skill is a directory whose name matches `[a-z0-9-_]{1,64}` containing a `SKILL.md` file. The file must be a regular file (not a symlink escape) ≤ 64 KiB. The description is parsed from YAML front-matter (`description:` field) or the first heading/line, truncated to 160 chars.

## Selection and lazy loading

Use `/skill` to choose skills from a checkbox list showing each name and short description. Only selected skills are shared with the model or available to its `skill` tool.

Skill bodies never enter the prompt unless the model calls the `skill` tool—this keeps context small. The system prompt includes a compact catalog of selected skill names and descriptions; full instructions are loaded on demand.

Workspace-local `.qcode/skills` takes precedence over `.agents/skills`, which takes precedence over user-level skills. Like `/model` and `/tool`, `/skill` affects only the active agent tab.

## Security

Symlink resolution and path checks prevent skill files from escaping their directory. The catalog is frozen at startup (new skills require restart), but selection can change live via the TUI.
