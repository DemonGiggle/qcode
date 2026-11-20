package llm

import (
	"strconv"
	"strings"
)

// Codex parity is implicit caching only: no explicit breakpoints, no
// prompt_cache_options. Cache reuse comes from a stable prompt_cache_key
// plus an exact reusable prefix (leading developer instructions, stable
// tools, append-only history). Explicit breakpoint markers are deferred;
// version-compare below is retained for future explicit-mode evidence and
// is not consulted by the current wire (see buildCodexBody).
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

// chatGPTCacheInput builds the Codex-parity input: leading developer
// instructions followed by the plain Responses encoding of history. It
// allocates fresh wire items only and never modifies durable messages,
// replayed assistant items, or other Responses providers. No explicit
// prompt_cache_breakpoint markers are added; the server selects implicit
// writes and lookup boundaries within its documented limits.
func chatGPTCacheInput(instructions string, messages []Message) []any {
	items := responsesInput(messages)
	if strings.TrimSpace(instructions) != "" {
		initial := responsesMessage("developer", instructions, nil)
		items = append([]any{initial}, items...)
	}
	return items
}

// markChatGPTCacheContent is deferred explicit-mode tooling (see above).
// Retained so future explicit breakpoints, if re-enabled with per-model
// evidence, have a single marking helper. Currently unused by the wire.
func markChatGPTCacheContent(value any) {
	if content, ok := value.([]map[string]any); ok && len(content) > 0 {
		content[len(content)-1]["prompt_cache_breakpoint"] = map[string]any{"mode": "explicit"}
	}
}
