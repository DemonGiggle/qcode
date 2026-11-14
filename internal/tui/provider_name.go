package tui

import "strings"

// providerNeedsSetupSuffix marks providers that cannot be selected until
// credentials are configured. It is appended to the picker label so an
// unconfigured backend is visible but clearly disabled.
const providerNeedsSetupSuffix = " (Need Setup First)"

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
	case "gemini":
		return "Gemini"
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

// providerPickerLabelsWithAvailability returns picker labels where unavailable
// providers carry providerNeedsSetupSuffix. The disabled map is keyed by the
// returned label. A nil available func means every provider is available.
// Callers should treat the active provider as available so the current row
// stays selectable.
func providerPickerLabelsWithAvailability(names []string, available func(string) bool) ([]string, map[string]bool) {
	labels := make([]string, len(names))
	disabled := make(map[string]bool, len(names))
	for i, name := range names {
		label := providerDisplayName(name)
		if available != nil && !available(name) {
			label += providerNeedsSetupSuffix
			disabled[label] = true
		}
		labels[i] = label
	}
	return labels, disabled
}

// stripProviderSetupSuffix removes the needs-setup marker so a picker label
// resolves back to its provider ID.
func stripProviderSetupSuffix(label string) string {
	return strings.TrimSuffix(label, providerNeedsSetupSuffix)
}

// resolveProviderID maps user input to a registry ID. It accepts the registry
// ID (exact, then case-insensitive) and the status-line display name
// (case-insensitive) so the picker and typed names stay in sync.
func resolveProviderID(input string, names []string) (string, bool) {
	input = stripProviderSetupSuffix(strings.TrimSpace(input))
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
