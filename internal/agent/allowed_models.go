package agent

import (
	"sort"
	"strings"

	"qcode/internal/config"
)

// AllowedAgentModel reuses the persisted roster shape. Provider qualifies the
// model, Role is smart | balanced | cheap. Thinking is never stored; main
// decides thinking_level at create_agent time from live capability data.
type AllowedAgentModel = config.AllowedAgentModel

// AllowedAgentView is the tool-facing row enriched with live thinking data.
type AllowedAgentView struct {
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	Provider          string   `json:"provider"`
	Model             string   `json:"model"`
	Role              string   `json:"role"`
	ThinkingLevels    []string `json:"thinking_levels"`
	ThinkingDefault   string   `json:"thinking_default"`
}

func normalizeAllowedRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

func validAllowedRole(role string) bool {
	switch role {
	case "smart", "balanced", "cheap":
		return true
	}
	return false
}

func normalizeAllowedModels(entries []AllowedAgentModel) []AllowedAgentModel {
	byKey := map[string]AllowedAgentModel{}
	for _, raw := range entries {
		provider := strings.TrimSpace(raw.Provider)
		model := strings.TrimSpace(raw.Model)
		role := normalizeAllowedRole(raw.Role)
		if provider == "" || model == "" {
			continue
		}
		if !validAllowedRole(role) {
			continue
		}
		key := provider + "\x00" + model
		byKey[key] = AllowedAgentModel{Provider: provider, Model: model, Role: role}
	}
	out := make([]AllowedAgentModel, 0, len(byKey))
	for _, entry := range byKey {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Role < out[j].Role
	})
	if len(out) == 0 {
		return []AllowedAgentModel{}
	}
	return out
}

// SetAllowedAgentModels replaces the in-memory roster used by
// list_allow_agent_models. Empty makes children use main's current provider/model.
func (m *AgentManager) SetAllowedAgentModels(entries []AllowedAgentModel) {
	normalized := normalizeAllowedModels(entries)
	m.mu.Lock()
	if len(normalized) == 0 && entries != nil {
		m.allowedModels = []AllowedAgentModel{}
	} else if len(normalized) == 0 {
		m.allowedModels = nil
	} else {
		m.allowedModels = append([]AllowedAgentModel(nil), normalized...)
	}
	m.mu.Unlock()
}

// AllowedAgentModels returns a copy of the in-memory roster.
func (m *AgentManager) AllowedAgentModels() []AllowedAgentModel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.allowedModels == nil {
		return nil
	}
	out := append([]AllowedAgentModel(nil), m.allowedModels...)
	if len(out) == 0 {
		return []AllowedAgentModel{}
	}
	return out
}
