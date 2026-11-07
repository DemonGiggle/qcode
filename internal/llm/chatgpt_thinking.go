package llm

import (
	"embed"
	"encoding/json"
	"strings"
)

// The snapshot contains only model IDs, defaults, and supported efforts from
// https://github.com/openai/codex/blob/main/codex-rs/models-manager/models.json.
// Live account metadata overrides these bundled values after Models succeeds.
//
//go:embed chatgpt_models.json
var chatGPTCatalogFS embed.FS

var bundledChatGPTThinking = func() map[string]ThinkingCapability {
	data, err := chatGPTCatalogFS.ReadFile("chatgpt_models.json")
	if err != nil {
		panic(err)
	}
	models, err := decodeChatGPTThinking(data)
	if err != nil {
		panic(err)
	}
	return models
}()

func decodeChatGPTThinking(data []byte) (map[string]ThinkingCapability, error) {
	var catalog struct {
		Models []struct {
			Slug    string `json:"slug"`
			ID      string `json:"id"`
			Default string `json:"default_reasoning_level"`
			Levels  *[]struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	models := make(map[string]ThinkingCapability)
	for _, model := range catalog.Models {
		id := model.Slug
		if id == "" {
			id = model.ID
		}
		// Absent metadata permits the bundled fallback; an explicit empty list
		// overrides it and disables the selector.
		if id == "" || model.Levels == nil {
			continue
		}
		capability := ThinkingCapability{RequestFormat: ThinkingRequestResponsesReasoning, ReplayFormat: ThinkingReplayReasoningDetails}
		for _, level := range *model.Levels {
			effort := strings.ToLower(strings.TrimSpace(level.Effort))
			// Preserve wire-native future tiers instead of restricting the catalog
			// to today's globally known effort names.
			if effort != "" {
				capability.Levels = appendUnique(capability.Levels, effort)
			}
		}
		capability.Supported = len(capability.Levels) > 0
		capability.Adjustable = len(capability.Levels) > 1
		capability.AlwaysOn = len(capability.Levels) == 1
		if containsChoice(capability.Levels, model.Default) {
			capability.Default = model.Default
		}
		if capability.AlwaysOn {
			capability.Default = capability.Levels[0]
		}
		models[id] = capability
	}
	return models, nil
}
