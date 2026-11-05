package agent

import (
	"qcode/internal/llm"
)

// Cache-session identity for epic #70 issue #81.
//
// The agent owns one random UUIDv4 cache-session identity (CacheSessionID)
// independent of provider-instance IDs such as the OpenCode Go
// x-opencode-session routing header. The base identity is persisted in
// SavedState and survives resume; only the base is stored. Each transmitted
// prompt_cache_key is a fixed-length digest of the base plus non-secret
// namespace components (provider, model, purpose) so model/provider switches
// keep the agent identity while namespacing the transmitted key. Switching
// back may reuse a namespace. The digest never covers prompt content,
// credentials, cwd, or personal identifiers.
//
// Session partitioning prioritizes isolation and a stable identity per agent.
// Separate agents never share an identity, which may reduce cross-session
// reuse and guarantees no hit-rate improvement. Existing automatic provider
// caching remains provider-owned.
//
// Lifecycle:
//   - New agents mint a fresh identity.
//   - /new (ResetSession) rotates the identity.
//   - RestoreState keeps a valid stored identity, generates once for legacy
//     saves missing the field, and regenerates safely on malformed values.
//   - RestoreFreshState never imports the stored identity.
//   - Normal turns use the chat purpose; compaction and learning use
//     separate purposes so auxiliary requests cannot share the chat key.

// ensureCacheSessionID returns the agent's base cache identity, generating
// one lazily when missing (legacy saves) or malformed. Generation uses only
// randomness; it never derives from prompt content, credentials, cwd, or
// personal identifiers.
func (a *Agent) ensureCacheSessionID() string {
	if llm.IsValidCacheSessionID(a.cacheSessionID) {
		return a.cacheSessionID
	}
	if id, err := llm.NewCacheSessionID(); err == nil {
		a.cacheSessionID = id
		return a.cacheSessionID
	}
	// Randomness unavailable: omit the key rather than deriving one from
	// request data or replaying a malformed value.
	a.cacheSessionID = ""
	return ""
}

// chatCacheKey returns the transmitted key for normal/tool turns.
func (a *Agent) chatCacheKey(provider, model string) string {
	return llm.NamespacedCacheKey(a.ensureCacheSessionID(), provider, model, llm.CachePurposeChat)
}

// compactCacheKey returns the transmitted key for compaction summaries.
func (a *Agent) compactCacheKey(provider, model string) string {
	return llm.NamespacedCacheKey(a.ensureCacheSessionID(), provider, model, llm.CachePurposeCompact)
}

// learnCacheKey returns the transmitted key for learning proposals.
func (a *Agent) learnCacheKey(provider, model string) string {
	return llm.NamespacedCacheKey(a.ensureCacheSessionID(), provider, model, llm.CachePurposeLearn)
}

// rotateCacheSessionID mints a fresh identity for /new. Failures leave the
// identity empty (key omitted) rather than reusing the old session.
func (a *Agent) rotateCacheSessionID() {
	if id, err := llm.NewCacheSessionID(); err == nil {
		a.cacheSessionID = id
		return
	}
	a.cacheSessionID = ""
}
