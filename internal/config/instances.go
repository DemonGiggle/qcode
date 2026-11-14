package config

import "fmt"

// ProviderKind separates a configured instance's identity from its wire protocol.
// Instances refer directly to built-in kinds; chains and cycles are unsupported.
func (c Config) ProviderKind(name string) (string, error) {
	profile, exists := c.Providers[name]
	if validProvider(name) {
		if profile.Type != nil && *profile.Type != name {
			return "", fmt.Errorf("providers.%s.type cannot change a built-in provider's kind", name)
		}
		return name, nil
	}
	if !validInstanceName(name) {
		return "", fmt.Errorf("invalid provider instance %q: use 1-64 lowercase letters, digits, hyphens or underscores; custom is reserved", name)
	}
	if !exists || profile.Type == nil || !validProvider(*profile.Type) {
		return "", fmt.Errorf("providers.%s.type must name a built-in provider (ollama, openai, opencode-go, chatgpt or gemini)", name)
	}
	return *profile.Type, nil
}

func validInstanceName(name string) bool {
	if name == "custom" || len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
