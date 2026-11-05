package llm

// UsageSchemaVersion identifies the normalized presence-aware usage contract.
// Trace usage events carry this version so downstream consumers can tell
// schema-2 (pointer presence) records from legacy schema-1 (zero-means-unknown)
// records. See docs/prompt-caching.md.
const UsageSchemaVersion = 2

// Usage describes one completion, not cumulative session billing.
//
// Every field is optional: nil means unknown (unreported), while a pointer to
// zero means an explicit reported zero. Absent optional metadata must never be
// treated as a reported zero.
//
// Contract (epic #70, issue #73):
//   - InputTokens is the complete logical input, inclusive of cache reads and
//     cache writes exactly once.
//   - OutputTokens already includes reported reasoning when the protocol says
//     so (no separate addition).
//   - CacheReadTokens, CacheWriteTokens, and UncachedInputTokens are subset
//     breakdowns of InputTokens; ReasoningTokens is a subset of OutputTokens.
//     Neither reasoning nor cache counters are added to totals.
//   - JSON uses prompt_tokens/completion_tokens for the two historical fields
//     plus cache_read_tokens, cache_write_tokens, uncached_input_tokens, and
//     reasoning_tokens. Nil serializes as null, never as numeric zero.
type Usage struct {
	InputTokens         *int `json:"prompt_tokens"`
	OutputTokens        *int `json:"completion_tokens"`
	CacheReadTokens     *int `json:"cache_read_tokens"`
	CacheWriteTokens    *int `json:"cache_write_tokens"`
	UncachedInputTokens *int `json:"uncached_input_tokens"`
	ReasoningTokens     *int `json:"reasoning_tokens"`
}

// SessionUsage accumulates reported counts plus coverage bookkeeping.
// Missing retains its historical meaning: completions with no usable usage
// fields. Coverage counters count requests, never token quantities.
type SessionUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	Missing      int `json:"missing_completions"`

	// Requests counts every recorded completion, including missing ones.
	Requests int `json:"requests"`
	// Per-field request coverage: how many completions reported each field.
	InputReported     int `json:"input_reported"`
	OutputReported    int `json:"output_reported"`
	CacheReadReported int `json:"cache_read_reported"`
	CacheWriteReported int `json:"cache_write_reported"`
	// Sums of valid reported cache tokens across all completions.
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	// Cache-ratio sums include ONLY requests with both known complete input
	// and known valid cache-read counts. CacheRatioReadTokens is separate
	// from every reported cache-read sum above; it must not be mixed with
	// CacheReadTokens when computing effectiveness.
	CacheRatioReadTokens  int `json:"cache_ratio_read_tokens"`
	CacheRatioInputTokens int `json:"cache_ratio_input_tokens"`
	CacheRatioRequests    int `json:"cache_ratio_requests"`
	// LegacyUncertain marks sessions that may mix pre-schema-2 ambiguous
	// zeros with presence-aware counts (for example restored checkpoints
	// written before UsageSchemaVersion 2).
	LegacyUncertain bool `json:"legacy_uncertain"`
}

// IntPtr returns a pointer to n for explicit reported counts in tests and
// adapters. Use nil for unknown/unreported.
func IntPtr(n int) *int { return &n }

// Known accessors avoid dereferencing unknown fields.
func (u *Usage) KnownInput() (int, bool)         { return knownToken(u, func(v *Usage) *int { return v.InputTokens }) }
func (u *Usage) KnownOutput() (int, bool)        { return knownToken(u, func(v *Usage) *int { return v.OutputTokens }) }
func (u *Usage) KnownCacheRead() (int, bool)     { return knownToken(u, func(v *Usage) *int { return v.CacheReadTokens }) }
func (u *Usage) KnownCacheWrite() (int, bool)    { return knownToken(u, func(v *Usage) *int { return v.CacheWriteTokens }) }
func (u *Usage) KnownUncached() (int, bool)      { return knownToken(u, func(v *Usage) *int { return v.UncachedInputTokens }) }
func (u *Usage) KnownReasoning() (int, bool)     { return knownToken(u, func(v *Usage) *int { return v.ReasoningTokens }) }

func knownToken(u *Usage, pick func(*Usage) *int) (int, bool) {
	if u == nil {
		return 0, false
	}
	p := pick(u)
	if p == nil {
		return 0, false
	}
	return *p, true
}

// IsAbsent reports nil/all-unknown usage: no usable fields.
func (u *Usage) IsAbsent() bool {
	if u == nil {
		return true
	}
	return u.InputTokens == nil &&
		u.OutputTokens == nil &&
		u.CacheReadTokens == nil &&
		u.CacheWriteTokens == nil &&
		u.UncachedInputTokens == nil &&
		u.ReasoningTokens == nil
}

// Total returns the per-request total: the sum of known input and known
// output components (partial subtotal when either side is missing). Cache and
// reasoning breakdowns are subsets and are never added.
func (u *Usage) Total() int {
	total := 0
	if v, ok := u.KnownInput(); ok {
		total += v
	}
	if v, ok := u.KnownOutput(); ok {
		total += v
	}
	return total
}

// validField reports whether a reported token count is usable: non-negative.
// Negative wire values become unknown for that field.
func validField(p *int) (*int, bool) {
	if p == nil {
		return nil, false
	}
	if *p < 0 {
		return nil, false
	}
	return p, true
}

// NormalizeUsage validates and sanitizes one completion's usage without
// failing the completion. It returns nil for absent usage. Rules:
//   - Negative values become unknown for that field.
//   - Reasoning exceeding known output becomes unknown.
//   - If cache reads or writes individually exceed known inclusive input, or
//     their known sum (including uncached input when known) exceeds it, the
//     inconsistent breakdown fields are discarded (set unknown) while valid
//     input/output totals are retained.
//   - Never fails: inconsistent optional metadata cannot break text
//     generation.
func NormalizeUsage(u *Usage) *Usage {
	if u == nil || u.IsAbsent() {
		// Still need to scrub negatives: an all-negative record is absent.
		if u == nil {
			return nil
		}
		out := &Usage{}
		changed := false
		if _, ok := validField(u.InputTokens); ok {
			out.InputTokens = u.InputTokens
			changed = true
		}
		if _, ok := validField(u.OutputTokens); ok {
			out.OutputTokens = u.OutputTokens
			changed = true
		}
		if _, ok := validField(u.CacheReadTokens); ok {
			out.CacheReadTokens = u.CacheReadTokens
			changed = true
		}
		if _, ok := validField(u.CacheWriteTokens); ok {
			out.CacheWriteTokens = u.CacheWriteTokens
			changed = true
		}
		if _, ok := validField(u.UncachedInputTokens); ok {
			out.UncachedInputTokens = u.UncachedInputTokens
			changed = true
		}
		if _, ok := validField(u.ReasoningTokens); ok {
			out.ReasoningTokens = u.ReasoningTokens
			changed = true
		}
		if !changed || out.IsAbsent() {
			return nil
		}
		return finishNormalize(out)
	}
	out := &Usage{}
	if p, ok := validField(u.InputTokens); ok {
		out.InputTokens = p
	}
	if p, ok := validField(u.OutputTokens); ok {
		out.OutputTokens = p
	}
	if p, ok := validField(u.CacheReadTokens); ok {
		out.CacheReadTokens = p
	}
	if p, ok := validField(u.CacheWriteTokens); ok {
		out.CacheWriteTokens = p
	}
	if p, ok := validField(u.UncachedInputTokens); ok {
		out.UncachedInputTokens = p
	}
	if p, ok := validField(u.ReasoningTokens); ok {
		out.ReasoningTokens = p
	}
	if out.IsAbsent() {
		return nil
	}
	return finishNormalize(out)
}

func finishNormalize(out *Usage) *Usage {
	// Reasoning is a subset of output; discard when it exceeds output.
	if out.ReasoningTokens != nil && out.OutputTokens != nil {
		if *out.ReasoningTokens > *out.OutputTokens {
			out.ReasoningTokens = nil
		}
	}
	// Cache breakdowns are subsets of inclusive input.
	if out.InputTokens != nil {
		in := *out.InputTokens
		if out.CacheReadTokens != nil && *out.CacheReadTokens > in {
			out.CacheReadTokens = nil
		}
		if out.CacheWriteTokens != nil && *out.CacheWriteTokens > in {
			out.CacheWriteTokens = nil
		}
		if out.UncachedInputTokens != nil && *out.UncachedInputTokens > in {
			out.UncachedInputTokens = nil
		}
		sum := 0
		parts := 0
		if out.CacheReadTokens != nil {
			sum += *out.CacheReadTokens
			parts++
		}
		if out.CacheWriteTokens != nil {
			sum += *out.CacheWriteTokens
			parts++
		}
		if out.UncachedInputTokens != nil {
			sum += *out.UncachedInputTokens
			parts++
		}
		if parts > 1 && sum > in {
			out.CacheReadTokens = nil
			out.CacheWriteTokens = nil
			out.UncachedInputTokens = nil
		}
	}
	if out.IsAbsent() {
		return nil
	}
	return out
}

// NewUsage builds a normalized per-request Usage from presence-aware fields.
// Negative or inconsistent values are sanitized per NormalizeUsage; a fully
// unknown record yields nil (absent usage).
func NewUsage(input, output, cacheRead, cacheWrite, uncached, reasoning *int) *Usage {
	return NormalizeUsage(&Usage{
		InputTokens:         input,
		OutputTokens:        output,
		CacheReadTokens:     cacheRead,
		CacheWriteTokens:    cacheWrite,
		UncachedInputTokens: uncached,
		ReasoningTokens:     reasoning,
	})
}

// Add records one completion into session totals. The input is normalized
// first; absent usage increments Missing. Totals are sums of known components
// only, so TotalTokens is a partial subtotal when either side is missing.
func (s *SessionUsage) Add(u *Usage) {
	s.Requests++
	n := NormalizeUsage(u)
	if n == nil {
		s.Missing++
		return
	}
	if v, ok := n.KnownInput(); ok {
		s.InputTokens += v
		s.InputReported++
	}
	if v, ok := n.KnownOutput(); ok {
		s.OutputTokens += v
		s.OutputReported++
	}
	s.TotalTokens = s.InputTokens + s.OutputTokens
	if v, ok := n.KnownCacheRead(); ok {
		s.CacheReadTokens += v
		s.CacheReadReported++
	}
	if v, ok := n.KnownCacheWrite(); ok {
		s.CacheWriteTokens += v
		s.CacheWriteReported++
	}
	// Ratio sums only for requests with BOTH known input and known cache-read.
	if in, okIn := n.KnownInput(); okIn {
		if cr, okCr := n.KnownCacheRead(); okCr {
			s.CacheRatioInputTokens += in
			s.CacheRatioReadTokens += cr
			s.CacheRatioRequests++
		}
	}
}

// CacheHitRatio returns the session cache-hit ratio: ratio-read / ratio-input
// over ratio-eligible requests only. The second return is false when the ratio
// is unavailable (no eligible requests or zero denominator). The result can
// never be negative or exceed 1 for normalized data.
func (s SessionUsage) CacheHitRatio() (float64, bool) {
	if s.CacheRatioRequests == 0 || s.CacheRatioInputTokens <= 0 || s.CacheRatioReadTokens < 0 {
		return 0, false
	}
	if s.CacheRatioReadTokens > s.CacheRatioInputTokens {
		return 0, false
	}
	return float64(s.CacheRatioReadTokens) / float64(s.CacheRatioInputTokens), true
}

// MigrateLegacySessionUsage marks restored pre-schema-2 sessions whose
// coverage counters are empty but totals are nonzero as uncertain: legacy
// zeros cannot be told apart from explicit reported zeros.
func MigrateLegacySessionUsage(s SessionUsage) SessionUsage {
	if s.Requests == 0 && (s.InputTokens != 0 || s.OutputTokens != 0 || s.TotalTokens != 0 || s.Missing != 0) {
		s.LegacyUncertain = true
	}
	return s
}
