# Interactive questions

Interactive questions let qcode pause normal work to ask one focused question
when resolving an ambiguity would otherwise require several broad searches or
reads. The agent should inspect facts that are cheap to find directly.

Interactive questions are off by default. Use `/interactive on` for the active
agent, `/interactive off` to disable them, or `/interactive` to show the current
setting. To enable them for new terminal sessions, add this to `config.toml`:

```toml
interactive = true
```

The active tab shows `[MODE INTERACTIVE]` in the status bar, or `[MODE INT]`
when space is tight. Plan and Skill Plan modes show their own mode labels and
retain their existing question behavior. Newly inherited agents copy their
parent's setting; `/new` keeps the toggle, and saved sessions restore it.

The agent may ask one question at a time, with suggested choices or a custom
answer. It can ask at most three distinct questions per submitted prompt. Your
answer becomes user context in that agent's conversation. It is not saved to
global learning automatically; `/learn` retains its review and approval flow.

If a question arrives while you are typing, qcode saves your unfinished prompt
and restores it after the answer. Questions for other tabs wait there; switching
away from a displayed question suspends it so other tabs remain usable. Ctrl+C
cancels the request. The browser remote can also answer a pending question.

One-shot prompts, piped input, non-TTY runs, and `--json-events` never wait for
answers. The question tool is unavailable in those runs, and the model proceeds
using the context and tools it has.
