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

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
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
}

// ToolResult is the normalized output returned by a tool implementation.
type ToolResult struct {
	Output string
	Diff   string
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

type Factory func(Config) (Provider, error)

type Config struct {
	BaseURL string
	APIKey  string
	HTTP    HTTPDoer
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
