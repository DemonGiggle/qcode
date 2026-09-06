package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Image is provider-neutral binary image input attached to a message.
type Image struct {
	MediaType string
	Data      []byte
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Images     []Image    `json:"-"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Model       string
	Messages    []Message
	Tools       []Tool
	Temperature float64
}

type Response struct {
	Message Message
	Usage   *Usage
}

// ToolResult is the normalized output returned by a tool implementation.
type ToolResult struct {
	Output       string
	Diff         string
	Images       []Image
	ChangedFiles []string
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

type Factory func(Config) (Provider, error)

type Config struct {
	BaseURL            string
	APIKey             string
	HTTP               HTTPDoer
	InsecureSkipVerify bool
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

// Usage describes one completion, not cumulative session billing.
type Usage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
}

// ContextWindowProvider reports the active model's context capacity, or zero
// when unknown. Discovery must not prevent a completion from succeeding.
type ContextWindowProvider interface {
	ContextWindow(context.Context, string) (int, error)
}
