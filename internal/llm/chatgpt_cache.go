package llm

import (
	"strconv"
	"strings"
)

// Explicit caching follows the documented old/new generation split: GPT-5.6
// and later use explicit breakpoints plus implicit lookup, earlier models
// stay on implicit caching. Compare as a version so future minors/majors
// (gpt-6.3, gpt-7, ...) automatically inherit the new-model rule instead of
// needing another allowlist entry. Models that advertise the new generation
// but reject cache fields (for example gpt-6.1-terra) are healed by the
// once-only fallback in doCodex that retries without any prompt_cache_*
// fields on a 400 cache-field rejection.
func chatGPTSupportsCacheBreakpoints(model string) bool {
	major, minor, ok := parseGPTVersion(model)
	if !ok {
		return false
	}
	if major != 5 {
		return major > 5
	}
	return minor >= 6
}

// parseGPTVersion extracts the (major, minor) generation from a model slug
// such as "gpt-6.2-sol", "gpt-5.6-codex", or "gpt-6-astra". The variant suffix
// after "-" is not part of the version. Missing minor means .0 (so "gpt-5"
// is 5.0 and "gpt-6" is 6.0). Anything that is not a gpt-<digits>[.<digits>]
// slug reports ok=false so unknown aliases stay on the conservative path.
func parseGPTVersion(model string) (major, minor int, ok bool) {
	s := strings.ToLower(strings.TrimSpace(model))
	if !strings.HasPrefix(s, "gpt-") {
		return 0, 0, false
	}
	rest := s[len("gpt-"):]
	i := 0
	for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(rest[:i])
	if err != nil {
		return 0, 0, false
	}
	rest = rest[i:]
	minor = 0
	if strings.HasPrefix(rest, ".") {
		rest = rest[1:]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			return 0, 0, false
		}
		minor, err = strconv.Atoi(rest[:j])
		if err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
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
