package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	if len(views) != 3 {
		t.Fatalf("expected all 3 provider-qualified rows, got %+v", views)
	}
	for _, v := range views {
		if v.Provider != "openai" {
			if v.Available || v.UnavailableReason == "" {
				t.Fatalf("missing preparation error: %+v", v)
			}
			continue
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

func TestCrossProviderRosterCreationPolicy(t *testing.T) {
	m := newAllowRosterManager(t, &allowRosterProvider{name: "openai"})
	if _, err := m.CreateMain("main-model"); err != nil {
		t.Fatal(err)
	}
	m.SetProviderFactory(func(id, name, model string, main bool, backend CreationBackend) (*Agent, error) {
		if backend.Provider != "ollama" || !backend.ResolveProfile {
			t.Fatalf("wrong target: %+v", backend)
		}
		return New(&allowRosterProvider{name: backend.Provider, capability: llm.ThinkingCapability{Adjustable: true, Levels: []string{"low", "high"}}}, model, &allowRosterToolset{}, nil, nil, 5), nil
	})
	ts := m.WrapToolset("main", &allowRosterToolset{}, true)
	call := func(args string) error {
		_, err := ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "create_agent", Arguments: json.RawMessage(args)})
		return err
	}
	if err := call(`{"provider":"ollama","model":"other"}`); err == nil {
		t.Fatal("empty roster allowed override")
	}
	m.SetAllowedAgentModels([]AllowedAgentModel{{Provider: "ollama", Model: "other", Role: "smart"}})
	for _, args := range []string{`{}`, `{"model":"other"}`, `{"provider":"ollama","model":"unlisted"}`, `{"provider":"ollama","model":"other","thinking_level":"invalid"}`} {
		if err := call(args); err == nil {
			t.Fatalf("accepted %s", args)
		}
	}
	if len(m.List()) != 1 {
		t.Fatal("invalid selection created a child")
	}
	if err := call(`{"provider":"ollama","model":"other","thinking_level":"high"}`); err != nil {
		t.Fatal(err)
	}
	rows := m.List()
	if len(rows) != 2 || rows[1].Provider != "ollama" || rows[1].Model != "other" {
		t.Fatalf("wrong child: %+v", rows)
	}
}

func TestCrossProviderRosterUsesTargetThinkingAndKeepsFailures(t *testing.T) {
	m := newAllowRosterManager(t, &allowRosterProvider{name: "openai", capability: llm.ThinkingCapability{Levels: []string{"parent"}}})
	if _, err := m.CreateMain("main"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.SetProviderResolver(func(ctx context.Context, name string, endpoint *string) (llm.Provider, string, error) {
		calls++
		if endpoint != nil {
			t.Fatal("other provider inherited endpoint")
		}
		if name == "chatgpt" {
			return nil, "", fmt.Errorf("login required")
		}
		return &allowRosterProvider{name: name, capability: llm.ThinkingCapability{Levels: []string{"target-low", "target-high"}, Default: "target-low"}}, "profile", nil
	})
	m.SetAllowedAgentModels([]AllowedAgentModel{{Provider: "ollama", Model: "one", Role: "smart"}, {Provider: "ollama", Model: "two", Role: "cheap"}, {Provider: "chatgpt", Model: "three", Role: "balanced"}})
	ts := m.WrapToolset("main", &allowRosterToolset{}, true)
	result, err := ts.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "list_allow_agent_models", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var rows []AllowedAgentView
	if err := json.Unmarshal([]byte(result.Output), &rows); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(rows) != 3 {
		t.Fatalf("calls=%d rows=%+v", calls, rows)
	}
	for _, row := range rows {
		if row.Provider == "chatgpt" {
			if row.Available || row.UnavailableReason != "login required" {
				t.Fatalf("missing error: %+v", row)
			}
		} else if !row.Available || row.ThinkingDefault != "target-low" || len(row.ThinkingLevels) != 2 {
			t.Fatalf("wrong backend capabilities: %+v", row)
		}
	}
}
