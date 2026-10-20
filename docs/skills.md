# Skills

Skills let you attach focused, reusable instructions to a session. Place a `SKILL.md` in `~/.qcode/skills/<name>/` for your user account, or `.qcode/skills/<name>/` / `.agents/skills/<name>/` for one workspace.

To create or update a skill, see the [skill specification](skill-spec.md).
For a guided flow that turns a rough intention into a reviewed skill, use
[`/skillplan`](skillplan.md).

## Official release skills

The repository maintains a collection of ready-to-use skills in
[`docs/skills/`](skills/). Users are encouraged to enable any official skill
that helps with their task; skill selection is optional, and qcode loads a
selected skill's full instructions only when the agent requests them.

The release collection is optional and is not in the built-in discovery roots.
From a qcode source checkout, add it to your user configuration before launch,
then select a skill with `/skill`:

```toml
[skills]
paths = ["/absolute/path/to/qcode/docs/skills"]
```

Alternatively, copy a chosen skill directory into `.qcode/skills/` or
`~/.qcode/skills/`. Use `autoload_paths` instead of `paths` only when you want
every skill in that directory selected at startup. Release executables do not
bundle these Markdown files.

For explicit stopping conditions during code investigations and fixes, select
[`bounded-verification`](skills/bounded-verification/SKILL.md). It requires each
additional verification step to resolve a fact that could change the answer,
while preserving task scope and required checks. It provides model guidance;
`/maxsteps` separately limits model turns.

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
Use `skills.autoload_paths` to discover and enable every skill in a directory
when a new run starts. It does not need to appear in `skills.paths` too.
Autoload directories are searched after `skills.paths` and win on duplicate
skill names. For example:

```toml
[skills]
autoload_paths = [".team/skills", "~/.config/my-skills"]
```

A skill is a directory whose name matches `[a-z0-9-_]{1,64}` containing a regular `SKILL.md` file. The file must be within its skill root and ≤ 64 KiB. qcode reads a simple `description:` line from an initial front-matter block, or the first non-empty heading/line, for a one-line summary truncated to 160 bytes. See the [skill specification](skill-spec.md) for the authoring format.

## Selection and lazy loading

Use `/skill` to choose skills from a checkbox list showing each name and short description. The command first shows every built-in and configured location it checks. Only selected skills are shared with the model or available to its `skill` tool.

Skill discovery runs when `/skill` is used, and at startup when
`skills.autoload_paths` is set. It reads a small prefix of each `SKILL.md` to
obtain its description. Skill bodies never enter the prompt unless the model
calls the `skill` tool—this keeps context small. The system prompt includes a
compact catalog of selected skill names and descriptions; full instructions
are loaded on demand.

Each `skill` result starts with the skill name, absolute resolved `SKILL.md`
path, and base directory, followed by the original Markdown unchanged. This
applies to both manually selected and autoloaded skills. Optional companion
files such as references, templates, images, or databases are read only when
needed. Bundled references are relative to that base directory unless the
skill explicitly specifies another base: `foo.db` in a skill based at
`/opt/skills/query` means `/opt/skills/query/foo.db`. The agent passes absolute
paths to existing tools, so references still work after a directory change.
Loading a skill does not change the working directory or automatically read
adjacent files. `/skillplan` creates only the reviewed `SKILL.md`; supporting
files must already exist or be created separately.

Workspace-local `.qcode/skills` takes precedence over `.agents/skills`, which takes precedence over user-level skills. Like `/model` and `/tool`, `/skill` affects only the active agent tab.

## Security

Symlink resolution and path checks prevent skill files from escaping their directory. `/skill` rediscovers the catalog when invoked, so newly added skills appear without a restart.

Loading a skill does not grant directory access or command-execution
permission. Companion files use the existing sandbox approval flow, enabled
tools, and execution-mode restrictions.
