package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Session-stable cache key purposes for epic #70 issue #81.
//
// Normal model turns use CachePurposeChat. Compaction summaries and
// learning proposals use separate purposes so auxiliary requests can never
// share the chat accounting group. Purposes are non-secret namespace
// components only; they never carry prompt content, credentials, paths, or
// personal identifiers.
const (
	CachePurposeChat    = "chat"
	CachePurposeCompact = "compact"
	CachePurposeLearn   = "learn"
)

// NewCacheSessionID returns a random UUIDv4 cache-session identity. The
// identity is independent of provider-instance IDs (such as the OpenCode Go
// x-opencode-session routing header) and carries no secrets.
func NewCacheSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	hexID := hex.EncodeToString(id[:])
	return hexID[0:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:32], nil
}

// IsValidCacheSessionID reports whether id is a UUID-shaped cache identity.
func IsValidCacheSessionID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	data, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(data) != 16 {
		return false
	}
	return true
}

// NamespacedCacheKey derives the transmitted prompt_cache_key from the
// persisted base identity plus non-secret namespace components (provider,
// model, purpose). The result is a fixed-length 64-character hex digest so
// the transmitted key stays within the documented API limit regardless of
// model ID length. Only the base identity is persisted; the digest is
// recomputed per request so provider/model switches keep the agent identity
// while switching back may reuse a namespace.
//
// The digest never covers prompt content, credentials, working directory,
// or personal identifiers — only the random base identity and the three
// namespace strings.
func NamespacedCacheKey(baseID, provider, model, purpose string) string {
	if baseID == "" {
		return ""
	}
	h := sha256.New()
	for _, s := range []string{baseID, provider, model, purpose} {
		fmt.Fprintf(h, "%d\x00%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}
