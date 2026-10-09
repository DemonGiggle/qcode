# Extending providers

Providers implement the small `llm.Provider` interface in [`internal/llm/types.go`](../internal/llm/types.go): `Name()` and `Complete(ctx, request, onText)`. Add an implementation in `internal/llm` and register its `func(Config) (Provider, error)` factory in `init`:

```go
func init() {
    Register("my-provider", newMyProvider)
}
```

Normalize provider messages, tool calls, image inputs, streamed output and
thinking, and usage into the shared types. A nil usage value means accounting
is unavailable. Preserve any required reasoning replay data in
`Message.ReasoningDetails`; `Message.Thinking` supplies displayable text.
Honor context cancellation and the configured HTTP client/TLS policy.

Implement optional capabilities when supported: `ModelLister` for `/model`,
`ThinkingProvider` for exact-model thinking choices, `ContextWindowProvider`
for capacity discovery, and `ModelValidator` for early validation. Providers
without these capabilities can still complete requests, but their corresponding
UI features may be unavailable.

Registration makes the provider available to `--provider` and
`--list-providers`; update the CLI help, example configuration, and
[provider guide](providers.md) with its setup. Use fixture-based HTTP tests
for payload encoding, stream parsing, usage, errors, and cancellation, then
run `go test ./internal/llm ./internal/agent ./cmd/qcode`.
