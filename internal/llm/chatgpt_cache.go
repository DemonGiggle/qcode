package llm

import "strings"

// Explicit caching is documented for these model generations. Keep unknown
// aliases and earlier models on implicit caching instead of guessing support.
func chatGPTSupportsCacheBreakpoints(model string) bool {
	for _, family := range []string{"gpt-5.6", "gpt-6", "gpt-6.1"} {
		if model == family || strings.HasPrefix(model, family+"-") {
			return true
		}
	}
	return false
}

// chatGPTCacheInput decorates freshly allocated wire items only. Never modify
// durable messages, replayed assistant items, or other Responses providers.
// Keep all historical boundaries stable as the conversation grows; the server
// selects cache writes and lookup boundaries within its documented limits.
func chatGPTCacheInput(instructions string, messages []Message) []any {
	items := responsesInput(messages)
	if strings.TrimSpace(instructions) != "" {
		initial := responsesMessage("developer", instructions, nil)
		markChatGPTCacheContent(initial["content"])
		items = append([]any{initial}, items...)
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue // opaque assistant replay must remain byte-for-byte intact
		}
		if item["type"] == "function_call_output" {
			text, ok := item["output"].(string)
			if ok {
				content := []map[string]any{{"type": "input_text", "text": text}}
				markChatGPTCacheContent(content)
				item["output"] = content
			}
		} else if item["role"] == "user" {
			markChatGPTCacheContent(item["content"])
		}
	}
	return items
}

func markChatGPTCacheContent(value any) {
	if content, ok := value.([]map[string]any); ok && len(content) > 0 {
		content[len(content)-1]["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
	}
}
