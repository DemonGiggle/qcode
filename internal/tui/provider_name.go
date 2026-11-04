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

// providerPickerLabels returns the status-line display names for the registry
// IDs in names, preserving order so the picker can show the same spelling as
// the status bar.
func providerPickerLabels(names []string) []string {
	labels := make([]string, len(names))
	for i, name := range names {
		labels[i] = providerDisplayName(name)
	}
	return labels
}

// resolveProviderID maps user input to a registry ID. It accepts the registry
// ID (exact, then case-insensitive) and the status-line display name
// (case-insensitive) so the picker and typed names stay in sync.
func resolveProviderID(input string, names []string) (string, bool) {
	for _, name := range names {
		if input == name {
			return name, true
		}
	}
	lower := strings.ToLower(input)
	for _, name := range names {
		if strings.ToLower(name) == lower {
			return name, true
		}
	}
	for _, name := range names {
		if strings.ToLower(providerDisplayName(name)) == lower {
			return name, true
		}
	}
	return "", false
}
