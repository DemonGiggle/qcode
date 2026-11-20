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
		return "", fmt.Errorf("providers.%s.type must name a built-in provider (ollama, ollama-cloud, openai, opencode-go, chatgpt or gemini)", name)
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

// validateLayer permits an instance override whose type is supplied by another
// layer. The complete merged configuration must still pass validate.
func validateLayer(path string, cfg Config) error {
	return validateConfig(path, cfg, true)
}

// inheritInstances runs once, after layers merge and before key files are read.
// It also copies the declaring directory of inherited key paths. Credentials
// are one choice: any explicit instance credential field replaces the parent's
// credential source, including an explicit empty value.
func inheritInstances(cfg *Config, keyDirs map[string]string) error {
	if cfg.Provider != "" && !cfg.HasProvider(cfg.Provider) {
		return fmt.Errorf("unknown default provider %q: select a built-in kind or configure providers.%s.type", cfg.Provider, cfg.Provider)
	}

	for name, p := range cfg.Providers {
		kind, err := cfg.ProviderKind(name)
		if err != nil {
			return err
		}
		if kind == name {
			continue
		}
		base := cfg.Providers[kind]
		if p.BaseURL == nil {
			p.BaseURL = base.BaseURL
		}
		if p.Model == nil {
			p.Model = base.Model
		}
		if p.Thinking == nil {
			p.Thinking = base.Thinking
		}
		if p.APIKey == nil && p.APIKeyPath == nil {
			p.APIKey, p.APIKeyPath = base.APIKey, base.APIKeyPath
			keyDirs[name] = keyDirs[kind]
		}
		cfg.Providers[name] = p
	}
	return validate("(merged)", *cfg)
}

// HasProvider checks a provider identity against the complete configuration.
func (c Config) HasProvider(name string) bool {
	_, err := c.ProviderKind(name)
	return err == nil
}
