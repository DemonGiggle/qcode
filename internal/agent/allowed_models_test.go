package agent

import (
	"context"
	"encoding/json"
	"testing"

	"qcode/internal/llm"
)

type allowRosterProvider struct {
	name       string
	capability llm.ThinkingCapability
}

func (p *allowRosterProvider) Name() string { return p.name }
func (p *allowRosterProvider) Complete(ctx context.Context, req llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	return llm.Response{}, nil
}
func (p *allowRosterProvider) ThinkingCapability(model string) llm.ThinkingCapability {
	return p.capability
}

type allowRosterToolset struct{}

func (t *allowRosterToolset) Schemas() []llm.Tool { return nil }
func (t *allowRosterToolset) EnabledSchemas() []llm.Tool {
	return nil
}
func (t *allowRosterToolset) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func newAllowRosterManager(t *testing.T, provider *allowRosterProvider) *AgentManager {
	t.Helper()
	m := NewAgentManager(context.Background(), 5)
	t.Cleanup(m.Shutdown)
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(provider, model, &allowRosterToolset{}, nil, nil, 5), nil
	})
	return m
}

func TestListAllowAgentModelsFiltersProviderAndEnriches(t *testing.T) {
	provider := &allowRosterProvider{
		name: "openai",
		capability: llm.ThinkingCapability{
			Supported:  true,
			Adjustable: true,
			Levels:     []string{"low", "high"},
			Default:    "high",
		},
	}
	m := newAllowRosterManager(t, provider)
	m.SetAllowedAgentModels([]AllowedAgentModel{
		{Provider: "openai", Model: "gpt-5", Role: "smart"},
		{Provider: "openai", Model: "mini", Role: "cheap"},
		{Provider: "ollama", Model: "other", Role: "balanced"},
	})
	if _, err := m.CreateMain("gpt-5"); err != nil {
		t.Fatalf("create main: %v", err)
	}
	ts := m.WrapToolset("main", &allowRosterToolset{}, true)
	res, err := ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "list_allow_agent_models", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	var views []AllowedAgentView
	if err := json.Unmarshal([]byte(res.Output), &views); err != nil {
		t.Fatalf("unmarshal %q: %v", res.Output, err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 openai rows, got %+v", views)
	}
	for _, v := range views {
		if v.Provider != "openai" {
			t.Fatalf("unfiltered provider: %+v", v)
		}
		if len(v.ThinkingLevels) != 2 || v.ThinkingDefault != "high" {
			t.Fatalf("thinking not enriched: %+v", v)
		}
	}
	// Role filter.
	res, err = ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "list_allow_agent_models", Arguments: json.RawMessage(`{"role":"smart"}`)})
	if err != nil {
		t.Fatalf("role filter: %v", err)
	}
	views = nil
	if err := json.Unmarshal([]byte(res.Output), &views); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(views) != 1 || views[0].Role != "smart" {
		t.Fatalf("role filter got %+v", views)
	}
	// Bad role rejected.
	if _, err := ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "list_allow_agent_models", Arguments: json.RawMessage(`{"role":"genius"}`)}); err == nil {
		t.Fatal("bad role must fail")
	}
}

func TestListAllowAgentModelsEmptyRoster(t *testing.T) {
	provider := &allowRosterProvider{name: "openai"}
	m := newAllowRosterManager(t, provider)
	if _, err := m.CreateMain("gpt-5"); err != nil {
		t.Fatalf("create main: %v", err)
	}
	ts := m.WrapToolset("main", &allowRosterToolset{}, true)
	res, err := ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "list_allow_agent_models", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("tool: %v", err)
	}
	if res.Output != "[]" {
		t.Fatalf("empty roster must return [], got %q", res.Output)
	}
}

func TestListAllowAgentModelsMainOnly(t *testing.T) {
	provider := &allowRosterProvider{name: "openai"}
	m := newAllowRosterManager(t, provider)
	m.SetAllowedAgentModels([]AllowedAgentModel{{Provider: "openai", Model: "gpt-5", Role: "smart"}})
	if _, err := m.CreateMain("gpt-5"); err != nil {
		t.Fatalf("create main: %v", err)
	}
	// Sub-agent toolset must not expose the tool.
	hasTool := false
	for _, tool := range m.WrapToolset("main", &allowRosterToolset{}, false).Schemas() {
		if tool.Name == "list_allow_agent_models" {
			hasTool = true
		}
	}
	if hasTool {
		t.Fatal("list_allow_agent_models must be main-only")
	}
	// Main toolset must expose it alongside create_agent.
	hasTool = false
	for _, tool := range m.WrapToolset("main", &allowRosterToolset{}, true).Schemas() {
		if tool.Name == "list_allow_agent_models" {
			hasTool = true
		}
	}
	if !hasTool {
		t.Fatal("main toolset missing list_allow_agent_models")
	}
}
