# Extending providers

Providers implement the small `llm.Provider` interface in [`internal/llm/types.go`](../internal/llm/types.go). Add an implementation and register its factory in `init`:

```go
func init() {
    Register("my-provider", newMyProvider)
}
```

The agent loop only sees normalized messages, streamed text, and tool calls, so provider-specific wire formats remain isolated. Add its name to the CLI help after registration.
