package llm

const openCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

func init() { Register("opencode-go", newOpenCodeGo) }

// newOpenCodeGo configures OpenCode Go's OpenAI-compatible Chat Completions
// endpoint. OpenCode asks third-party agents to identify themselves, so these
// requests use a narrow qcode-specific User-Agent instead of a browser value.
func newOpenCodeGo(config Config) (Provider, error) {
	return newOpenAICompatible(config, openCodeGoBaseURL, "opencode-go", "qcode")
}
