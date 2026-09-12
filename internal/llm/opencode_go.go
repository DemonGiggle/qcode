package llm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const openCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

// OpenCode Go's /models endpoint includes models on Chat Completions,
// Responses, and Anthropic Messages routes. qcode currently implements only
// Chat Completions, so hide known incompatible entries from the picker and
// reject direct use before it can incur a request. Source: OpenCode Go model
// table, checked 2026-09-12.
var openCodeGoNonChatModels = map[string]string{
	"gpt-5.6-luna":               "Responses",
	"grok-4.6":                   "Responses",
	"minimax-m2.5":               "Anthropic Messages",
	"minimax-m2.7":               "Anthropic Messages",
	"minimax-m3":                 "Anthropic Messages",
	"muse-spark-1.2-contributor": "Responses",
	"muse-spark-1.3-contributor": "Responses",
	"qwen3.5-plus":               "Anthropic Messages",
	"qwen3.6-plus":               "Anthropic Messages",
	"qwen3.7-max":                "Anthropic Messages",
	"qwen3.7-plus":               "Anthropic Messages",
	"qwen3.8-flash":              "Anthropic Messages",
	"qwen3.8-max":                "Anthropic Messages",
}

func init() { Register("opencode-go", newOpenCodeGo) }

// newOpenCodeGo configures OpenCode Go's OpenAI-compatible Chat Completions
// endpoint. OpenCode asks third-party agents to identify themselves, so these
// requests use a narrow qcode-specific User-Agent instead of a browser value.
func newOpenCodeGo(config Config) (Provider, error) {
	sessionID, err := newOpenCodeSessionID()
	if err != nil {
		return nil, fmt.Errorf("create OpenCode Go session ID: %w", err)
	}
	return newOpenAICompatible(config, openCodeGoBaseURL, "opencode-go", "qcode", sessionID)
}

func (p *openAIProvider) ValidateModel(model string) error {
	if p.name != "opencode-go" {
		return nil
	}
	if endpoint, unsupported := openCodeGoNonChatModels[model]; unsupported {
		return fmt.Errorf("OpenCode Go model %q requires the %s endpoint, which qcode does not support yet", model, endpoint)
	}
	return nil
}

// newOpenCodeSessionID returns a UUIDv4 which is stable for the lifetime of a
// provider instance. OpenCode Go uses it to keep related requests on the same
// backend and requires the x-opencode-session header to be present.
func newOpenCodeSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	hexID := hex.EncodeToString(id[:])
	return hexID[0:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:32], nil
}
