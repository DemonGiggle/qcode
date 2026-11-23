package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type ToolCall struct {
	ID               string          `json:"id,omitempty"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

// Image is provider-neutral binary image input attached to a message.
type Image struct {
	MediaType string
	Data      []byte
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// Origin and Untrusted are local provenance metadata. Providers without a
	// native trust field receive the same information in the content envelope.
	Origin    string `json:"origin,omitempty"`
	Untrusted bool   `json:"untrusted,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	// ReasoningDetails preserves provider-specific structured reasoning when a
	// model requires it on a later tool-call turn. Thinking remains the
	// normalized, displayable stream; this field is intentionally opaque.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	Images           []Image         `json:"-"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Model string
	// Thinking is an optional user-selected level. Providers must omit thinking
	// request fields when it is empty or unsupported for the selected model.
	Thinking string
	Messages []Message
	Tools    []Tool
	// RequestOnlyInstructions carries current-request guidance outside the
	// replayable conversation. The ChatGPT adapter encodes it after history.
	RequestOnlyInstructions string
	// Temperature is optional: nil preserves the provider's model default.
	// A non-nil value, including zero, is sent only by adapters that support it.
	Temperature *float64
	// PromptCacheKey carries a session-stable namespaced cache identity for
	// the official OpenAI Responses and ChatGPT SIWC routes (see cachekey.go).
	// The agent sets it for normal, compaction, and learning requests with
	// purpose namespacing; only OpenAI and ChatGPT adapters with an official endpoint
	// emits it as prompt_cache_key. All other adapters must omit it even
	// when set. ChatGPT may retry once without any cache fields after a 400
	// cache-field rejection (safe: a 400 rejects before execution); no adapter
	// may replay a potentially billable completion.
	PromptCacheKey string
	// Responses opt-ins (issue #116).
	//
	// PreviousResponseID opts into stored history. Supply only new items in
	// Messages when using this field. If the server rejects the ID, retry
	// with full history and no ID. Sign in with ChatGPT HTTP rejects this option.
	PreviousResponseID string
	// StoreResponses opts into storage on compatible Responses routes.
	// Cache diagnostics can use a completed response ID with storage disabled.
	StoreResponses bool
	// ComparisonResponseID opts into prompt-cache diagnostics
	// (prompt_cache_options.comparison_response_id). It never changes
	// prefix matching, hit rate, or billing; it only asks the server to
	// report prompt_cache_diagnostics for this request. On a 400 mentioning
	// prompt_cache_options/comparison the provider retries once without it.
	ComparisonResponseID string
	// ReportCacheComparison records the local diagnostic outcome even when
	// there is no baseline ID. Only ComparisonResponseID affects the wire.
	ReportCacheComparison bool
	// ConversationID opts into the Conversations API container
	// (conversation: "...") for persistent server-side history. Use it
	// independently of PreviousResponseID on compatible Responses routes.
	ConversationID string
	// ResponseFormat opts into structured output (text.format). Nil preserves
	// the default free-text wire.
	ResponseFormat *ResponsesTextFormat
	// ToolChoice forwards Responses tool_choice (for example "auto",
	// "required", or {"type":"function","name":"..."}). Nil omits it.
	ToolChoice any
	// ParallelToolCalls forwards parallel_tool_calls when non-nil.
	ParallelToolCalls *bool
	// MaxOutputTokens forwards max_output_tokens when non-nil.
	MaxOutputTokens *int
	// ServiceTier forwards service_tier (for example "auto", "default",
	// "flex", "priority") when non-empty.
	ServiceTier string
	// Metadata forwards provider metadata when non-nil.
	Metadata map[string]any
	// Background forwards background:true for async handling when non-nil.
	Background *bool
	// Truncation forwards truncation ("auto"/"disabled") when non-empty.
	Truncation string
	// Include forwards Responses include (for example
	// ["reasoning.encrypted_content"]) when non-empty.
	Include []string
	// ReasoningSummary overrides the reasoning summary detail
	// ("auto"/"concise"/"detailed") when non-empty. Empty preserves the
	// thinkingFields default (auto when effort is enabled).
	ReasoningSummary string
	// Verbosity forwards text.verbosity when non-empty.
	Verbosity string
	// BuiltinTools carries passthrough Responses built-in tools
	// (for example {"type":"web_search"}). Callers must enforce a config
	// allowlist before setting; the provider only validates the type is a
	// known built-in and drops unknown entries. Empty preserves the
	// function-only wire.
	BuiltinTools []map[string]any
}

type Response struct {
	Message Message
	Usage   *Usage
	// ResponseID is the completed Responses id (resp_...) when reported.
	// It seeds the next PreviousResponseID or ComparisonResponseID for
	// opt-in stateful or diagnostics flows. Empty when unreported.
	ResponseID string
	// CacheDiagnostics is the parsed prompt_cache_diagnostics for this
	// completion, if the server reported it. Nil means unreported
	// (diagnostics not requested, model without support, or no record).
	// Diagnostic counts can differ from usage counts; billing always follows
	// Usage, never diagnostics.
	CacheDiagnostics *ResponsesCacheDiagnostics
	// ComparisonStatus describes what the client sent on this Responses call.
	ComparisonStatus CacheComparisonStatus
}

// CacheComparisonStatus records the request path independently of any
// provider-reported diagnostics. Empty leaves ordinary requests quiet.
type CacheComparisonStatus string

const (
	CacheComparisonNoBaseline       CacheComparisonStatus = "no_baseline"
	CacheComparisonRequested        CacheComparisonStatus = "requested"
	CacheComparisonRetriedWithoutID CacheComparisonStatus = "retried_without_comparison"
)

func (r Request) comparisonStatus() CacheComparisonStatus {
	if r.ComparisonResponseID != "" {
		return CacheComparisonRequested
	}
	if r.ReportCacheComparison {
		return CacheComparisonNoBaseline
	}
	return ""
}

// Summary renders local observations separately from the server's type and reason.
func (s CacheComparisonStatus) Summary(diag *ResponsesCacheDiagnostics) string {
	var observation string
	switch s {
	case CacheComparisonNoBaseline:
		observation = "skipped (no previous completed response ID)"
	case CacheComparisonRetriedWithoutID:
		observation = "retried without comparison after a cache-field rejection"
	case CacheComparisonRequested:
		if diag == nil {
			return "requested, but the provider returned no usable diagnostics"
		}
	}
	if diag == nil {
		return observation
	}
	if observation != "" {
		observation += ". Provider reported "
	}
	observation += fmt.Sprintf("%q", diag.Type)
	if diag.Reason != "" {
		observation += fmt.Sprintf(" (%q)", diag.Reason)
	}
	return observation
}

// ResponsesCacheDiagnostics is the parsed prompt_cache_diagnostics object
// (Responses API, GPT-5.6+). See
// https://developers.openai.com/api/docs/guides/prompt-caching/diagnostics
type ResponsesCacheDiagnostics struct {
	// Type is cache_hit, cache_miss, comparison_response_not_found, or
	// unavailable.
	Type string `json:"type"`
	// Reason is the miss classifier (for example tools_changed,
	// instructions_changed, model_changed). Empty for hits and for
	// not_found/unavailable results.
	Reason string `json:"reason,omitempty"`
	// ComparisonReusableTokens is the raw token count of the comparison
	// response's reusable prefix, when reported. Nil means unreported.
	ComparisonReusableTokens *int `json:"comparison_reusable_tokens,omitempty"`
	// CacheMissedTokens estimates how many reusable tokens were not reused.
	// Nil means unreported.
	CacheMissedTokens *int `json:"cache_missed_tokens,omitempty"`
}

// SuggestedFix returns the documented fix for the miss reason, or "" when
// unknown. It extends the "Fix a cache miss" table (with an extra
// instructions_changed entry for 6.2+ top-level instructions) so trace
// output can point at the actionable change without re-reading docs.
func (d *ResponsesCacheDiagnostics) SuggestedFix() string {
	if d == nil {
		return ""
	}
	return ResponsesDiagnosticsFix(d.Reason)
}

// ResponsesDiagnosticsFix maps a prompt_cache_diagnostics reason to its
// documented fix. Unknown reasons return "".
func ResponsesDiagnosticsFix(reason string) string {
	switch reason {
	case "model_changed":
		return "Use the same model for requests intended to share a cached prefix."
	case "prompt_cache_key_changed":
		return "Keep a stable prompt_cache_key within each accounting group; omit the key unless separate cache accounting is needed."
	case "service_tier_changed":
		return "Keep the service tier consistent for requests expected to share a prefix."
	case "tools_changed":
		return "Keep tool definitions and ordering stable; use tool_choice:none or allowed_tools instead of rewriting the tool list."
	case "text_format_changed":
		return "Keep top-level text.format stable; append a format update mid-conversation on supported GPT-6+ models."
	case "reasoning_effort_changed":
		return "On supported GPT-6+ models append a configuration_update input item to change effort mid-conversation; otherwise keep reasoning.effort consistent."
	case "verbosity_changed":
		return "Keep text.verbosity consistent across requests intended to share a prefix."
	case "context_compacted":
		return "Preserve stable instructions and let later turns build on the compacted context; fewer input tokens can still save money despite lower reuse."
	case "input_changed":
		return "Preserve earlier messages and tool results and append new turns; keep changing content after the reusable prefix."
	case "instructions_changed":
		return "Keep durable instructions byte-identical at the start of input; send per-request guidance via top-level instructions (6.2+) so it does not poison the prefix."
	default:
		return ""
	}
}

// ResponsesTextFormat carries a structured-output text.format override.
type ResponsesTextFormat struct {
	// Type is "json_schema", "json_object", or "text".
	Type   string
	Name   string
	Schema map[string]any
	Strict *bool
}

// ToolResult is the normalized output returned by a tool implementation.
type ToolResult struct {
	Output string
	// UserAnswer is direct input collected by an interactive tool. The agent
	// records it as a user message after satisfying the tool-call protocol.
	UserAnswer   string
	Diff         string
	Images       []Image
	ChangedFiles []string
	// EndTurn tells the agent loop that the tool completed the user's request
	// for this model turn. It is used by fire-and-forget orchestration tools so
	// the model cannot immediately start polling the background task.
	EndTurn bool
}

type StreamKind uint8

const (
	StreamOutput StreamKind = iota
	StreamThinking
)

type StreamEvent struct {
	Kind StreamKind
	Text string
}

type StreamCallback func(StreamEvent)

type Provider interface {
	Name() string
	Complete(ctx context.Context, request Request, onText StreamCallback) (Response, error)
}

// ModelLister is an optional provider capability used by interactive clients.
type ModelLister interface {
	Models(context.Context) ([]string, error)
}

// ThinkingCapability describes model-specific thinking behavior without
// exposing provider wire details to the UI or agent loop.
type ThinkingCapability struct {
	Supported     bool
	Adjustable    bool
	AlwaysOn      bool
	Levels        []string
	Default       string
	RequestFormat string
	ReplayFormat  string
}

// ThinkingProvider is optional. A provider that does not implement it has no
// user-selectable thinking policy.
type ThinkingProvider interface {
	ThinkingCapability(model string) ThinkingCapability
}

// ModelMetadataProvider optionally discovers thinking controls for one selected
// model. A failed lookup leaves the provider's default behavior available.
type ModelMetadataProvider interface {
	RefreshModelMetadata(context.Context, string)
}

// ModelValidator is optional. It lets a provider reject a known model that is
// advertised by a shared catalog but requires an endpoint qcode does not yet
// implement.
type ModelValidator interface {
	ValidateModel(model string) error
}

type Factory func(Config) (Provider, error)

type Config struct {
	// InstanceName is the configured identity; empty uses the built-in kind.
	InstanceName string
	// RegisterSecrets optionally registers credentials with the shared output
	// redaction policy. The callback must be concurrency-safe.
	RegisterSecrets func(...string)
	BaseURL         string
	APIKey          string
	HTTP            HTTPDoer
	// ChatGPTOAuthPath points at the JSON file written by ChatGPT sign-in
	// in `qcode setup`. Empty selects the default path. Only used by the chatgpt
	// provider; other providers ignore it.
	ChatGPTOAuthPath   string
	InsecureSkipVerify bool
	// ModelMeta supplies exact-model reasoning choices from Models.dev. Request
	// and replay formats remain in qcode's provider adapters.
	ModelMeta *ProviderModelMeta
}

type HTTPDoer interface {
	Do(*httpRequest) (*httpResponse, error)
}

// Aliases keep the public provider seam small while allowing tests to inject clients.
// They are implemented by *http.Client through the adapter in http.go.
type httpRequest = requestAlias
type httpResponse = responseAlias

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

func New(name string, config Config) (Provider, error) {
	registryMu.RLock()
	factory, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (available: %v)", name, Names())
	}
	return factory(config)
}

func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ContextWindowProvider reports the active model's context capacity, or zero
// when unknown. Discovery must not prevent a completion from succeeding.
type ContextWindowProvider interface {
	ContextWindow(context.Context, string) (int, error)
}

// Kind returns a provider's protocol kind independently of its configured name.
func Kind(p Provider) string {
	if typed, ok := p.(interface{ Kind() string }); ok {
		return typed.Kind()
	}
	return p.Name()
}
