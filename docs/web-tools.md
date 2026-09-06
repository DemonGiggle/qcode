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

## Enabling the tools

`/tool` can enable or disable the two tools independently. Both web tools start disabled for security. Enable each one explicitly through `/tool` for the current session; `/new` and restarting qcode disable them again. Disabled tools are neither sent to the model nor executable through tool calls. Backend configuration does not enable them. One-shot mode leaves them disabled because it has no interactive opt-in.

## Limits and networking

Requests have a 20-second deadline, at most 5 redirects, a 2 MiB response limit (after HTTP decompression), and at most two concurrent web operations. Text output is capped at 64 KiB with an explicit truncation marker. The HTML tokenizer avoids building a full page DOM. Requests use normal TLS certificate validation; the provider-only TLS bypass setting does not apply to these tools.

Web tools are blocked when an active sandbox disables networking. Outside the sandbox they still accept only public destinations: loopback, private, link-local, and reserved addresses are rejected, including DNS results and redirects. They connect directly and do not use environment HTTP proxies, browser cookies, or provider credentials. Retrieved content is marked as untrusted reference data.

## Testing

Run the optional live smoke test with `QCODE_TEST_WEB_LIVE=1 go test ./internal/tools -run '^TestWebLive$' -v`. Ordinary tests use local HTTP fixtures and do not depend on search availability.
