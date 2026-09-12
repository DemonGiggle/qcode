package llm

import "strings"

const (
	ThinkingRequestNone            = "none"
	ThinkingRequestReasoningEffort = "reasoning_effort"
	ThinkingRequestObject          = "thinking"
	ThinkingReplayNone             = "none"
	ThinkingReplayReasoningContent = "reasoning_content"
	ThinkingReplayReasoningDetails = "reasoning_details"
)

// openCodeGoThinkingCatalog is deliberately exact-match only. OpenCode Go
// model names can look alike while accepting different request fields; an
// unknown model must therefore receive no optional thinking fields.
var openCodeGoThinkingCatalog = map[string]ThinkingCapability{
	// OpenCode Go currently exposes deepseek-flash as an alias of V4.1 Flash.
	// V4.1 accepts these named aliases (as well as a numeric 1–100 budget,
	// which qcode intentionally does not expose as a selector value).
	"deepseek-flash": {
		Supported: true, Adjustable: true, Levels: []string{"low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"deepseek-v4.1-flash": {
		Supported: true, Adjustable: true, Levels: []string{"low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"deepseek-v4-flash": {
		Supported: true, Adjustable: true, Levels: []string{"off", "low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"deepseek-v4-flash-vision-exp": {
		Supported: true, Adjustable: true, Levels: []string{"off", "low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"deepseek-v4-pro": {
		Supported: true, Adjustable: true, Levels: []string{"off", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"glm-5": {
		Supported: true, Adjustable: true, Levels: []string{"off", "on"}, Default: "on",
		RequestFormat: ThinkingRequestObject, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"glm-5.1": {
		Supported: true, Adjustable: true, Levels: []string{"off", "on"}, Default: "on",
		RequestFormat: ThinkingRequestObject, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"glm-5.2": {
		Supported: true, Adjustable: true, Levels: []string{"high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"glm-5.3": {
		Supported: true, Adjustable: true, Levels: []string{"low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"glm-5.3-flash": {
		Supported: true, Adjustable: true, Levels: []string{"low", "high", "max"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"kimi-k2.5": {
		Supported: true, Adjustable: true, Levels: []string{"off", "on"}, Default: "on",
		RequestFormat: ThinkingRequestObject, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"kimi-k2.6": {
		Supported: true, Adjustable: true, Levels: []string{"off", "on"}, Default: "on",
		RequestFormat: ThinkingRequestObject, ReplayFormat: ThinkingReplayReasoningContent,
	},
	// Kimi K3 only advertises the max reasoning tier, so present it as an
	// always-on state instead of a selector with a single invalid alternative.
	"kimi-k3": {
		Supported: true, AlwaysOn: true, Levels: []string{"max"}, Default: "max",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"hy3": {
		Supported: true, Adjustable: true, Levels: []string{"none", "low", "high"}, Default: "none",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"hy3-preview": {
		Supported: true, Adjustable: true, Levels: []string{"none", "low", "high"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
	"hy4-preview": {
		Supported: true, Adjustable: true, Levels: []string{"none", "high"}, Default: "high",
		RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayReasoningContent,
	},
}

func (p *openAIProvider) ThinkingCapability(model string) ThinkingCapability {
	if p.name != "opencode-go" || p.baseURL != openCodeGoBaseURL {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	capability, ok := openCodeGoThinkingCatalog[model]
	if !ok {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	capability.Levels = append([]string(nil), capability.Levels...)
	return capability
}

func validThinkingLevel(capability ThinkingCapability, level string) bool {
	for _, candidate := range capability.Levels {
		if level == candidate {
			return true
		}
	}
	return false
}

// thinkingFields resolves the single optional request encoding. Off is a
// thinking-object setting even for models that use reasoning_effort for their
// enabled tiers; this prevents sending mutually exclusive fields together.
func thinkingFields(capability ThinkingCapability, level string) map[string]any {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" || !capability.Adjustable || !validThinkingLevel(capability, level) {
		return nil
	}
	if level == "off" {
		return map[string]any{"thinking": map[string]any{"type": "disabled"}}
	}
	if capability.RequestFormat == ThinkingRequestObject {
		return map[string]any{"thinking": map[string]any{"type": "enabled"}}
	}
	if capability.RequestFormat == ThinkingRequestReasoningEffort {
		return map[string]any{"reasoning_effort": level}
	}
	return nil
}
