package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"qcode/internal/llm"
)

// sample is one recorded turn. Only metadata, normalized usage, and
// monotonic durations are stored: never prompts, responses, tool args,
// credentials, user paths, raw payloads, or cache keys.
type sample struct {
	SchemaVersion int        `json:"schema_version"`
	GitRevision   string     `json:"git_revision"`
	Scenario      string     `json:"scenario"`
	Variant       string     `json:"variant"`
	Repetition    int        `json:"repetition"`
	Turn          int        `json:"turn"`
	TurnLabel     string     `json:"turn_label"` // cold = first request, warm = repeated request (order labels only)
	Provider      string     `json:"provider"`
	Model         string     `json:"model"`
	Protocol      string     `json:"protocol"`
	Settings      settings   `json:"settings"`
	ElapsedMs     float64    `json:"elapsed_ms"`
	Status        string     `json:"status"` // ok | error
	ErrorCategory string     `json:"error_category,omitempty"`
	HTTPStatus    int        `json:"http_status,omitempty"`
	Usage         *llm.Usage `json:"usage"`
	// ProviderReportedCost is nullable. Current adapters report no cost, so
	// null is the correct value; never estimate token-price costs here.
	ProviderReportedCost *reportedCost `json:"provider_reported_cost"`
}

type settings struct {
	Temperature *float64 `json:"temperature,omitempty"`
	Thinking    string   `json:"thinking,omitempty"`
}

type reportedCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Source   string  `json:"source"`
}

type benchRunner struct {
	scenarios []scenario
	opts      options
	protocol  string

	newProvider func() (llm.Provider, error)
	now         func() time.Time
}

func turnLabel(turn int) string {
	if turn == 0 {
		return "cold"
	}
	return "warm"
}

func protocolFor(provider, model string) string {
	switch provider {
	case "openai":
		return "chat_completions"
	case "chatgpt":
		return "responses"
	case "ollama":
		return "ollama_chat"
	case "opencode-go":
		switch llm.OpenCodeGoRoute(model) {
		case "responses":
			return "responses"
		case "messages":
			return "anthropic_messages"
		default:
			return "chat_completions"
		}
	default:
		return "unknown"
	}
}

// cacheIdentity returns a fresh routing/cache identity for one repetition.
// Adapters receive it via Request.PromptCacheKey (currently inert by policy)
// and, for opencode-go, via a fresh provider instance session ID. The
// identity itself is never written to output.
func cacheIdentity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("rep-%d", time.Now().UnixNano())
	}
	return "rep-" + hex.EncodeToString(b[:])
}

func (r *benchRunner) run(ctx context.Context, out io.Writer) (int, error) {
	w := bufio.NewWriter(out)
	defer w.Flush()
	wrote := 0
	var firstErr error
	for rep := 0; rep < r.opts.reps; rep++ {
		provider, err := r.newProvider()
		if err != nil {
			return wrote, err
		}
		identity := cacheIdentity()
		for _, sc := range r.scenarios {
			reqs := sc.turns(r.opts.model)
			for turn, req := range reqs {
				req.Model = r.opts.model
				req.PromptCacheKey = identity
				s := r.executeOnce(ctx, provider, sc.name, rep, turn, req)
				data, err := json.Marshal(s)
				if err != nil {
					return wrote, err
				}
				data = append(data, '\n')
				if _, err := w.Write(data); err != nil {
					return wrote, err
				}
				wrote++
				if s.Status != "ok" && firstErr == nil {
					firstErr = errors.New("one or more samples failed; see output for error_category")
				}
			}
		}
	}
	if err := w.Flush(); err != nil {
		return wrote, err
	}
	return wrote, firstErr
}

func (r *benchRunner) executeOnce(ctx context.Context, provider llm.Provider, scenarioName string, rep, turn int, req llm.Request) sample {
	s := sample{
		SchemaVersion: schemaVersion,
		GitRevision:   r.opts.gitRev,
		Scenario:      scenarioName,
		Variant:       r.opts.variant,
		Repetition:    rep,
		Turn:          turn,
		TurnLabel:     turnLabel(turn),
		Provider:      r.opts.provider,
		Model:         r.opts.model,
		Protocol:      r.protocol,
		ElapsedMs:     0,
		Status:        "ok",
		Usage:         nil,
		// ProviderReportedCost stays nil: verified adapter semantics report
		// no cost today, and price estimation is explicitly out of scope.
		ProviderReportedCost: nil,
	}
	// Repetition is supplied by the caller so each repetition can reuse one
	// provider instance (fresh routing identity) across its turns.
	timeout := r.opts.timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := r.now()
	resp, err := provider.Complete(callCtx, req, nil)
	elapsed := r.now().Sub(start)
	s.ElapsedMs = float64(elapsed) / float64(time.Millisecond)
	if err != nil {
		s.Status = "error"
		s.ErrorCategory, s.HTTPStatus = classifyError(err)
		s.Usage = nil
		return sanitizeSample(s)
	}
	s.Usage = llm.NormalizeUsage(resp.Usage)
	return sanitizeSample(s)
}

// sanitizeSample enforces privacy: usage only carries numeric presence
// fields, and no free-text payloads survive. Defensive: drop any usage that
// somehow carries non-numeric content (impossible via llm.Usage) and ensure
// git revision / variant labels carry no path-like secrets.
func sanitizeSample(s sample) sample {
	s.Variant = sanitizeLabel(s.Variant)
	s.GitRevision = sanitizeLabel(s.GitRevision)
	return s
}

func sanitizeLabel(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 128 {
		v = v[:128]
	}
	return v
}

func classifyError(err error) (string, int) {
	if err == nil {
		return "", 0
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", 0
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", 0
	}
	msg := err.Error()
	// Sanitized categories only: never propagate raw bodies or URLs.
	switch {
	case strings.Contains(msg, " 429"):
		return "http_429", 429
	case strings.Contains(msg, " 400"):
		return "http_400", 400
	case strings.Contains(msg, " 401") || strings.Contains(msg, " 403"):
		return "http_auth", 0
	case strings.Contains(msg, " 5"):
		return "http_5xx", 0
	case strings.Contains(msg, "provider returned"):
		return "http_error", 0
	case strings.Contains(msg, "decode") || strings.Contains(msg, "unexpected") || strings.Contains(msg, "invalid"):
		return "decode", 0
	default:
		return "network", 0
	}
}

// --- Comparison ---

type compareSummary struct {
	Before string `json:"before"`
	After  string `json:"after"`
	// matched identity (must agree or comparison is rejected)
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	Protocol  string        `json:"protocol"`
	Settings  settings      `json:"settings"`
	Scenarios []scenarioCmp `json:"scenarios"`
	Warnings  []string      `json:"warnings"`
}

type scenarioCmp struct {
	Scenario string     `json:"scenario"`
	Before   groupStats `json:"before"`
	After    groupStats `json:"after"`
	// Paired cached-input share over eligible requests only (weighted by
	// input tokens). Missing metrics are never treated as zero.
	CachedShareBefore *float64 `json:"cached_share_before"`
	CachedShareAfter  *float64 `json:"cached_share_after"`
	CoverageBefore    float64  `json:"coverage_before"`
	CoverageAfter     float64  `json:"coverage_after"`
	OutputTokenDelta  *float64 `json:"output_token_delta_after_minus_before"`
	LatencyDeltaMs    *float64 `json:"latency_p50_delta_after_minus_before"`
	Note              string   `json:"note,omitempty"`
}

type groupStats struct {
	Samples      int       `json:"samples"`
	Failed       int       `json:"failed"`
	FirstTurn    turnStats `json:"first_turn"`
	RepeatedTurn turnStats `json:"repeated_turn"`
}

type turnStats struct {
	Samples  int      `json:"samples"`
	Failed   int      `json:"failed"`
	P50Ms    *float64 `json:"p50_ms,omitempty"`
	P90Ms    *float64 `json:"p90_ms,omitempty"`
	MeanOut  *float64 `json:"mean_output_tokens,omitempty"`
	Eligible int      `json:"cache_eligible_requests"`
	Coverage float64  `json:"cache_coverage"`
}

func runCompare(pathA, pathB string, stdout, stderr io.Writer) error {
	a, err := loadSamples(pathA)
	if err != nil {
		return err
	}
	b, err := loadSamples(pathB)
	if err != nil {
		return err
	}
	if len(a) == 0 || len(b) == 0 {
		return errors.New("comparison requires non-empty JSONL inputs (refusing to compare empty runs)")
	}
	sum, err := compare(a, b, pathA, pathB)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(data))
	for _, w := range sum.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	return nil
}

func loadSamples(path string) ([]sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sample
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	for scan.Scan() {
		line++
		text := strings.TrimSpace(scan.Text())
		if text == "" {
			continue
		}
		var s sample
		if err := json.Unmarshal([]byte(text), &s); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, line, err)
		}
		out = append(out, s)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func identityOf(s sample) string {
	settingsKey, _ := json.Marshal(s.Settings)
	return s.Provider + "\x00" + s.Model + "\x00" + s.Protocol + "\x00" + string(settingsKey)
}

func compare(a, b []sample, pathA, pathB string) (compareSummary, error) {
	idA := identityOf(a[0])
	idB := identityOf(b[0])
	for _, s := range a[1:] {
		if identityOf(s) != idA {
			return compareSummary{}, errors.New("before file mixes provider/model/protocol/settings; refusing comparison")
		}
	}
	for _, s := range b[1:] {
		if identityOf(s) != idB {
			return compareSummary{}, errors.New("after file mixes provider/model/protocol/settings; refusing comparison")
		}
	}
	if idA != idB {
		return compareSummary{}, fmt.Errorf("mismatched runs: before %q vs after %q (provider/model/protocol/settings must match)", idA, idB)
	}
	scenA := scenarioSet(a)
	scenB := scenarioSet(b)
	if !equalSets(scenA, scenB) {
		return compareSummary{}, fmt.Errorf("mismatched scenarios: before %v vs after %v", sortedKeys(scenA), sortedKeys(scenB))
	}
	sum := compareSummary{
		Before: pathA, After: pathB,
		Provider: a[0].Provider, Model: a[0].Model, Protocol: a[0].Protocol,
		Settings: a[0].Settings,
	}
	// Incomplete samples: any scenario/rep/turn grid missing or failed.
	for _, name := range sortedKeys(scenA) {
		ga := filterScenario(a, name)
		gb := filterScenario(b, name)
		cmp := scenarioCmp{Scenario: name, Before: summarize(ga), After: summarize(gb)}
		shareA, covA := cachedShare(ga)
		shareB, covB := cachedShare(gb)
		cmp.CachedShareBefore = shareA
		cmp.CachedShareAfter = shareB
		cmp.CoverageBefore = covA
		cmp.CoverageAfter = covB
		cmp.OutputTokenDelta = diffMeanOut(ga, gb)
		cmp.LatencyDeltaMs = diffP50(ga, gb)
		failed := cmp.Before.Failed + cmp.After.Failed
		if failed > 0 {
			w := fmt.Sprintf("scenario %s has %d failed sample(s); included in totals, excluded from latency/usage means", name, failed)
			sum.Warnings = append(sum.Warnings, w)
			cmp.Note = w
		}
		if cmp.Before.Samples != cmp.After.Samples {
			w := fmt.Sprintf("scenario %s sample count differs (%d vs %d); comparison is incomplete", name, cmp.Before.Samples, cmp.After.Samples)
			sum.Warnings = append(sum.Warnings, w)
			if cmp.Note != "" {
				cmp.Note += "; " + w
			} else {
				cmp.Note = w
			}
		}
		sum.Scenarios = append(sum.Scenarios, cmp)
	}
	return sum, nil
}

func scenarioSet(samples []sample) map[string]bool {
	m := map[string]bool{}
	for _, s := range samples {
		m[s.Scenario] = true
	}
	return m
}

func filterScenario(samples []sample, name string) []sample {
	var out []sample
	for _, s := range samples {
		if s.Scenario == name {
			out = append(out, s)
		}
	}
	return out
}

func summarize(samples []sample) groupStats {
	g := groupStats{Samples: len(samples)}
	var first, repeated []sample
	for _, s := range samples {
		if s.Status != "ok" {
			g.Failed++
		}
		if s.Turn == 0 {
			first = append(first, s)
		} else {
			repeated = append(repeated, s)
		}
	}
	g.FirstTurn = summarizeTurns(first)
	g.RepeatedTurn = summarizeTurns(repeated)
	return g
}

func summarizeTurns(samples []sample) turnStats {
	ts := turnStats{Samples: len(samples)}
	var lat []float64
	var outs []float64
	for _, s := range samples {
		if s.Status != "ok" {
			ts.Failed++
			continue
		}
		lat = append(lat, s.ElapsedMs)
		if s.Usage != nil {
			if v, ok := s.Usage.KnownOutput(); ok {
				outs = append(outs, float64(v))
			}
			if _, okIn := s.Usage.KnownInput(); okIn {
				if _, okRead := s.Usage.KnownCacheRead(); okRead {
					ts.Eligible++
				}
			}
		}
	}
	ok := len(samples) - ts.Failed
	if ok > 0 {
		ts.Coverage = float64(ts.Eligible) / float64(ok)
	}
	if len(lat) > 0 {
		p50 := percentile(lat, 50)
		p90 := percentile(lat, 90)
		ts.P50Ms = &p50
		ts.P90Ms = &p90
	}
	if len(outs) > 0 {
		m := mean(outs)
		ts.MeanOut = &m
	}
	return ts
}

// cachedShare computes the input-weighted cached-input share over eligible
// requests only: sum(cache_read)/sum(input) where both are known and valid.
// Missing cache metrics are never treated as zero.
func cachedShare(samples []sample) (*float64, float64) {
	var num, den int
	eligible, ok := 0, 0
	for _, s := range samples {
		if s.Status != "ok" || s.Usage == nil {
			continue
		}
		ok++
		in, okIn := s.Usage.KnownInput()
		read, okRead := s.Usage.KnownCacheRead()
		if !okIn || !okRead {
			continue
		}
		nu := llm.NormalizeUsage(s.Usage)
		if nu == nil {
			continue
		}
		in2, okIn2 := nu.KnownInput()
		read2, okRead2 := nu.KnownCacheRead()
		if !okIn2 || !okRead2 || in2 <= 0 {
			continue
		}
		eligible++
		num += read2
		den += in2
		_ = in
		_ = read
	}
	coverage := 0.0
	if ok > 0 {
		coverage = float64(eligible) / float64(ok)
	}
	if eligible == 0 || den == 0 {
		return nil, coverage
	}
	share := float64(num) / float64(den)
	if share < 0 {
		share = 0
	}
	if share > 1 {
		share = 1
	}
	return &share, coverage
}

func mean(xs []float64) float64 {
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func percentile(xs []float64, p float64) float64 {
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	if len(cp) == 1 {
		return cp[0]
	}
	rank := (p / 100) * float64(len(cp)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return cp[lo]
	}
	frac := rank - float64(lo)
	return cp[lo]*(1-frac) + cp[hi]*frac
}

func okOutputs(samples []sample) []float64 {
	var outs []float64
	for _, s := range samples {
		if s.Status != "ok" || s.Usage == nil {
			continue
		}
		if v, ok := s.Usage.KnownOutput(); ok {
			outs = append(outs, float64(v))
		}
	}
	return outs
}

func okLatencies(samples []sample) []float64 {
	var out []float64
	for _, s := range samples {
		if s.Status == "ok" {
			out = append(out, s.ElapsedMs)
		}
	}
	return out
}

func diffMeanOut(a, b []sample) *float64 {
	oa, ob := okOutputs(a), okOutputs(b)
	if len(oa) == 0 || len(ob) == 0 {
		return nil
	}
	d := mean(ob) - mean(oa)
	return &d
}

func diffP50(a, b []sample) *float64 {
	la, lb := okLatencies(a), okLatencies(b)
	if len(la) == 0 || len(lb) == 0 {
		return nil
	}
	d := percentile(lb, 50) - percentile(la, 50)
	return &d
}

func equalSets(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
