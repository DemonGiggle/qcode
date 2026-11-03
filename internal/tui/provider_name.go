package tui

import "strings"

// providerDisplayName maps provider IDs to their commonly used brand spelling.
// Unknown/custom provider names are left untouched.
func providerDisplayName(provider string) string {
	switch strings.ToLower(provider) {
	case "ollama":
		return "Ollama"
	case "openai":
		return "OpenAI"
	case "opencode-go":
		return "OpenCode Go"
	case "chatgpt":
		return "ChatGPT"
	default:
		return provider
	}
}
