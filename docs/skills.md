# Skills

Skills let you attach focused, reusable instructions to a session. Place a `SKILL.md` in `~/.qcode/skills/<name>/` for your user account, or `.qcode/skills/<name>/` / `.agents/skills/<name>/` for one workspace.

To create or update a skill, see the [skill specification](skill-spec.md).

## Official release skills

The repository maintains a collection of ready-to-use skills in
[`docs/skills/`](skills/). Users are encouraged to enable any official skill
that helps with their task; skill selection is optional, and qcode loads a
selected skill's full instructions only when the agent requests them.

The release collection is optional and is not automatically enabled by qcode.
Browse the [release skill collection](skills/) to see what is available, then
decide whether a skill is useful for your work.

## Discovery and precedence

qcode scans three directories in ascending precedence (later overrides earlier on name collisions):

| Precedence | Location | Scope |
|---|---|---|
| Lowest | `~/.qcode/skills/` | User-level |
| Middle | `<workspace>/.agents/skills/` | Workspace |
| Highest | `<workspace>/.qcode/skills/` | Workspace (overrides) |

Additional skill directories can be configured with `skills.paths` in
`config.toml`. They are searched after the built-in locations, in the order
listed, so a custom skill with the same name overrides an earlier one. Relative
paths are based at the selected workspace and `~` expands to the user's home.

A skill is a directory whose name matches `[a-z0-9-_]{1,64}` containing a regular `SKILL.md` file. The file must be within its skill root and ≤ 64 KiB. qcode reads a simple `description:` line from an initial front-matter block, or the first non-empty heading/line, for a one-line summary truncated to 160 bytes. See the [skill specification](skill-spec.md) for the authoring format.

## Selection and lazy loading

Use `/skill` to choose skills from a checkbox list showing each name and short description. The command first shows every built-in and configured location it checks. Only selected skills are shared with the model or available to its `skill` tool.

Skill discovery runs only when `/skill` is used. It reads a small prefix of each `SKILL.md` to obtain its description. Skill bodies never enter the prompt unless the model calls the `skill` tool—this keeps context small. The system prompt includes a compact catalog of selected skill names and descriptions; full instructions are loaded on demand.

Workspace-local `.qcode/skills` takes precedence over `.agents/skills`, which takes precedence over user-level skills. Like `/model` and `/tool`, `/skill` affects only the active agent tab.

## Security

Symlink resolution and path checks prevent skill files from escaping their directory. `/skill` rediscovers the catalog when invoked, so newly added skills appear without a restart.
