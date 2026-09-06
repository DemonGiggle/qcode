# Context usage

The status bar shows `CONTEXT 73% left`, using the latest completion's input and output token counts (not cumulative billed usage). OpenAI and OpenCode Go request streamed usage; Ollama uses `prompt_eval_count` and `eval_count`. Cached input remains part of the context. The value refreshes after each run and after `/new`, `/model`, `/skill`, or `/tool`.

## Window limits

OpenAI and OpenCode Go limits come from a bundled [Models.dev catalog](https://models.dev/) (snapshot: 2026-09-06). Ollama discovers the loaded model's allocated window through [`/api/ps`](https://docs.ollama.com/api/ps), which may be unavailable before the first completion. Unknown limits show `CONTEXT unknown`.

For custom endpoints or unlisted models, supply `--context-window 32768` or `context_window = 32768` in `config.toml`. The flag takes precedence. This only sets the display denominator; it does not change provider limits. Switching models clears the override.

## Estimates

A `~` prefix marks an estimate when usage is unavailable, the session is new, or messages/tool definitions changed since the last measured response. Estimates use serialized text size and may be inaccurate for images and model-specific tokenization. Percentages are clamped to 0–100%; this indicator does not compact history or reserve room for the next response.

## References

Usage parsing follows the [OpenAI Chat API](https://developers.openai.com/api/reference/resources/chat) and [Ollama usage fields](https://docs.ollama.com/api/usage).
