package llm

import (
	"fmt"
	"strings"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// Google's OpenAI-compatible /models endpoint returns IDs but no capability
// flags. The official embeddings documentation identifies this family and
// legacy IDs as embedding-only; exclude those IDs without claiming capabilities
// for the remaining catalog entries. See:
// https://ai.google.dev/gemini-api/docs/openai#list-models
// https://ai.google.dev/gemini-api/docs/embeddings
func isKnownGeminiEmbeddingModel(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	modelID = strings.TrimPrefix(modelID, "models/")
	return strings.HasPrefix(modelID, "gemini-embedding-") || modelID == "embedding-001" || modelID == "text-embedding-004"
}

func init() { Register("gemini", newGemini) }

// Gemini uses Google's OpenAI-compatible Chat Completions endpoint, but has
// model-specific reasoning controls and needs tool-call thought signatures
// replayed in Google's extension field.
func newGemini(config Config) (Provider, error) {
	provider, err := newOpenAICompatible(config, geminiBaseURL, "gemini", "", "")
	if err != nil {
		return nil, err
	}
	p := provider.(*openAIProvider)
	p.modelMeta = config.ModelMeta
	return p, nil
}

var geminiThinkingCatalog = map[string]ThinkingCapability{
	// Gemini 2.5 Flash can disable reasoning. Gemini 2.5 Pro does not.
	"gemini-2.5-flash":              {Supported: true, Adjustable: true, Levels: []string{"none", "minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-2.5-flash-lite":         {Supported: true, Adjustable: true, Levels: []string{"none", "minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-2.5-pro":                {Supported: true, Adjustable: true, Levels: []string{"minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-3-flash-preview":        {Supported: true, Adjustable: true, Levels: []string{"minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-3-pro-preview":          {Supported: true, Adjustable: true, Levels: []string{"minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-3.1-flash-lite-preview": {Supported: true, Adjustable: true, Levels: []string{"minimal", "low", "medium", "high"}, RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	// Gemini 4 uses thinking_level (low/medium/high, plus xhigh for Hydrogen).
	// MINIMAL is not supported on any Gemini 4 model: the API rejects it with
	// 400 INVALID_ARGUMENT, so it must not be offered. Defaults follow Google:
	// Carbon/Argon default to medium, Hydrogen defaults to high.
	"gemini-4-carbon":   {Supported: true, Adjustable: true, Levels: []string{"low", "medium", "high"}, Default: "medium", RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-4-argon":    {Supported: true, Adjustable: true, Levels: []string{"low", "medium", "high"}, Default: "medium", RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
	"gemini-4-hydrogen": {Supported: true, Adjustable: true, Levels: []string{"low", "medium", "high", "xhigh"}, Default: "high", RequestFormat: ThinkingRequestReasoningEffort, ReplayFormat: ThinkingReplayNone},
}

func (p *openAIProvider) geminiThinkingCapability(model string) ThinkingCapability {
	if p.name != "gemini" || p.baseURL == "" {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}

	// Only exact model IDs with locally documented Gemini effort mappings may
	// expose thinking controls. Models.dev metadata can narrow these choices,
	// but must never create controls for an unrecognized model or add levels
	// outside Google's documented mapping for that exact model.
	capability, ok := geminiThinkingCatalog[model]
	if !ok {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	if p.modelMeta == nil || p.modelMeta.Provider != "gemini" {
		capability.Levels = append([]string(nil), capability.Levels...)
		return capability
	}

	options, exists := p.modelMeta.Models[model]
	if !exists || len(options.Effort) == 0 {
		// Metadata may be absent when loading failed or did not report this
		// model. Retain the conservative, exact-model built-in mapping rather
		// than disabling known-safe controls or guessing new ones.
		capability.Levels = append([]string(nil), capability.Levels...)
		return capability
	}

	levels := make([]string, 0, len(options.Effort))
	for _, level := range options.Effort {
		if containsChoice(capability.Levels, level) {
			levels = appendUnique(levels, level)
		}
	}
	if len(levels) == 0 {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	capability.Levels = levels
	capability.Adjustable = len(levels) > 1
	capability.AlwaysOn = len(levels) == 1
	if !containsChoice(levels, capability.Default) {
		capability.Default = ""
	}
	return capability
}

func (p *openAIProvider) geminiValidateThinking(model, level string) error {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return nil
	}
	if !validThinkingLevel(p.geminiThinkingCapability(model), level) {
		return fmt.Errorf("thinking level %q is not supported by model %q", level, model)
	}
	return nil
}

var _ ThinkingProvider = (*openAIProvider)(nil)
var _ ModelLister = (*openAIProvider)(nil)
var _ ContextWindowProvider = (*openAIProvider)(nil)
