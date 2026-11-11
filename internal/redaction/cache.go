package redaction

import "sync"

// LineCache memoizes the pure per-segment redaction applied to complete
// logical lines. Stream PEM state, overflow handling, and line splitting stay
// on the existing code paths; only Policy.line (which is a pure function of
// the input plus the immutable pattern set and the current literal set) is
// memoized. A cache must not outlive one snapshot: literals can grow via
// RegisterSecrets, so entries are versioned by the literal registry
// generation and the whole map is dropped when it changes. Callers create one
// cache per snapshot via Policy.WithLineCache and share the derived policy
// across every Text/Copy/JSON call in that snapshot.
type LineCache struct {
	mu  sync.Mutex
	by  map[string]string
	gen uint64
}

// WithLineCache returns a shallow copy of the policy that memoizes line
// filtering in a fresh per-snapshot cache. The receiver is never mutated, so
// the original policy remains safe for concurrent use. A nil receiver selects
// the same built-in fallback as Text/Redact.
func (p *Policy) WithLineCache() *Policy {
	base := effective(p)
	cp := *base
	cp.cache = &LineCache{}
	return &cp
}

// Cached reuses an existing per-snapshot cache when present and creates a
// fresh one otherwise, so nested snapshot helpers share a single cache for
// the whole snapshot while direct callers still get per-call dedupe.
func (p *Policy) Cached() *Policy {
	if p != nil && p.cache != nil {
		return p
	}
	return p.WithLineCache()
}

func (c *LineCache) getOrCompute(p *Policy, s string) string {
	lit := p.literals
	lit.mu.RLock()
	gen := lit.gen
	values := lit.values
	lit.mu.RUnlock()
	c.mu.Lock()
	if c.by == nil {
		c.by = make(map[string]string)
		c.gen = gen
	}
	if c.gen != gen {
		c.by = make(map[string]string)
		c.gen = gen
	}
	if v, ok := c.by[s]; ok {
		c.mu.Unlock()
		return v
	}
	c.mu.Unlock()
	v := p.lineWithLiterals(s, values)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		// Literals changed while computing; the value was derived from a
		// stale set, so return it without storing to avoid leaking a new
		// secret through a stale hit later.
		return v
	}
	// Re-check under lock in case another caller filled the same input.
	if existing, ok := c.by[s]; ok {
		return existing
	}
	c.by[s] = v
	return v
}
