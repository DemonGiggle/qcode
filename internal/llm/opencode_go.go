package llm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const openCodeGoBaseURL = "https://opencode.ai/zen/go/v1"

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
