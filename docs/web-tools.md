# Web tools

`web_fetch(url)` retrieves public HTTP(S) pages as readable text. It supports UTF-8 HTML and text, strips scripts and styles, and retains source URL metadata. It does not execute JavaScript or convert PDFs or other binary documents. `web_search(query, max_results)` returns titles, URLs, and snippets; the default is 5 results and the supported range is 1–10.

Both tools run inside qcode without a browser, helper process, or local search service.

## Backends

DuckDuckGo HTML search is the first supported backend and needs no API key. Its page markup or blocking behavior can change; challenges and unrecognized pages return errors rather than being presented as empty search results. There is no automatic fallback to another backend.

Select the backend in `config.toml`:

```toml
[web_search]
backend = "duckduckgo"
```

Omitting the setting defaults to `duckduckgo`. Unknown backend names fail at startup. Backend changes require restarting qcode; there is no command-line, environment-variable, model argument, or slash-command override.

## Tool defaults and toggles

Both web tools start enabled outside sandbox mode and disabled when a sandbox is active, in both interactive and one-shot runs. `/tool` can enable or disable them independently for the current agent; for example, `/tool web_fetch off` disables fetching. `/new` and a fresh launch restore these defaults. Resuming a saved session restores its tool selections, and helpers created by main inherit its selections. Disabled tools are neither sent to the model nor executable through tool calls. The `[web_search].backend` setting selects the search backend independently of tool enablement.

## Limits and networking

Requests have a 20-second deadline, at most 5 redirects, a 2 MiB response limit (after HTTP decompression), and at most two concurrent web operations. Text output is capped at 64 KiB with an explicit truncation marker. The HTML tokenizer avoids building a full page DOM. Requests use normal TLS certificate validation unless `--danger-skip-tls-verify` (or `danger_skip_tls_verify = true`) is set.

In sandbox mode, fresh sessions block networking for native web tools and sandboxed shell commands. Enabling either web tool allows networking for both, including shell commands such as `curl` or package downloads. Disabling only one leaves networking available while the other is enabled; disabling both or using `/new` restores isolation. Resumed sessions and helpers with an enabled web tool also allow sandbox networking.

The defaults follow the effective sandbox state after configuration, CLI overrides, and startup checks: `sandbox = true` and `--sandbox` match, `--sandbox=false` overrides configuration, and continuing without a sandbox after a startup failure uses enabled web defaults.

The web tools accept only public destinations: loopback, private, link-local, and reserved addresses are rejected, including DNS results and redirects. They connect directly and do not use environment HTTP proxies, browser cookies, or provider credentials. Retrieved content is wrapped as untrusted reference data with tool origin metadata. Page text, snippets, links, and encoded instructions cannot authorize tool actions. A detected instruction attempt causes a visible warning and requires user approval before later calls that can access data, change state, or send data; see the [trust model](trust-model.md) for scope and limitations.

## Testing

Run the optional live smoke test with `QCODE_TEST_WEB_LIVE=1 go test ./internal/tools -run '^TestWebLive$' -v`. Ordinary tests use local HTTP fixtures and do not depend on search availability.
