package agent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

type modeTestToolset struct {
	called string
}

func (*modeTestToolset) Schemas() []llm.Tool {
	return []llm.Tool{{Name: "read"}, {Name: "write"}, {Name: "shell"}, {Name: "list_agents"}, {Name: "create_agent"}}
}
func (*modeTestToolset) EnabledSchemas() []llm.Tool { return (&modeTestToolset{}).Schemas() }
func (t *modeTestToolset) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	t.called = "called"
	return llm.ToolResult{Output: "ok"}, nil
}

type modePlanProvider struct {
	tools []llm.Tool
}

func (*modePlanProvider) Name() string { return "mode-test" }
func (p *modePlanProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.tools = request.Tools
	args, _ := json.Marshal(map[string]any{
		"title": "Add caching", "summary": "Cache the lookup result.",
		"steps": []string{"Add the cache abstraction."}, "validation": []string{"Run the cache tests."},
	})
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "propose_plan", Arguments: args}}}}, nil
}

func TestPlanModeFiltersAndRejectsMutationTools(t *testing.T) {
	tools := &modeTestToolset{}
	a := New(&modePlanProvider{}, "model", tools, trace.New(io.Discard, false), io.Discard, 2)
	a.SetPlanMode(true)
	seen := map[string]bool{}
	for _, schema := range a.enabledSchemas() {
		seen[schema.Name] = true
	}
	for _, name := range []string{"read", "list_agents", "ask_questions", "propose_plan"} {
		if !seen[name] {
			t.Fatalf("Plan mode omitted %q from schemas: %v", name, seen)
		}
	}
	for _, name := range []string{"write", "shell", "create_agent"} {
		if seen[name] {
			t.Fatalf("Plan mode advertised mutation tool %q", name)
		}
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "write"}); err == nil {
		t.Fatal("write unexpectedly succeeded in Plan mode")
	}
	if tools.called != "" {
		t.Fatal("rejected tool reached underlying toolset")
	}
}

type modeQuestionProvider struct {
	calls      int
	answerSeen string
}

func (p *modeQuestionProvider) Name() string { return "mode-question-test" }
func (p *modeQuestionProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		args, _ := json.Marshal(map[string]any{
			"questions": []map[string]any{{"question": "Which store?", "options": []string{"SQLite", "Postgres"}}},
		})
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "question-1", Name: "ask_questions", Arguments: args}}}}, nil
	}
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == "ask_questions" {
			p.answerSeen = message.Content
		}
	}
	args, _ := json.Marshal(map[string]any{
		"title": "Choose storage", "summary": "Use the selected storage backend.",
		"steps": []string{"Configure the selected backend."}, "validation": []string{"Run storage tests."},
	})
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "plan-1", Name: "propose_plan", Arguments: args}}}}, nil
}

func TestPlanModeBlocksForQuestionsAndContinues(t *testing.T) {
	provider := &modeQuestionProvider{}
	a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.SetPlanMode(true)
	asked := false
	a.SetQuestioner(func(_ context.Context, questions []Question) ([]string, error) {
		asked = true
		if len(questions) != 1 || questions[0].Text != "Which store?" || len(questions[0].Options) != 2 {
			t.Fatalf("questions = %+v", questions)
		}
		return []string{"SQLite"}, nil
	})

	if err := a.Run(context.Background(), "plan this with decisions"); err != nil {
		t.Fatal(err)
	}
	if !asked || provider.calls != 2 || !strings.Contains(provider.answerSeen, "SQLite") {
		t.Fatalf("question flow asked=%v calls=%d answer=%q", asked, provider.calls, provider.answerSeen)
	}
	if _, ok := a.LatestPlan(); !ok {
		t.Fatal("question flow did not reach plan submission")
	}
}

func TestPlanModeSavesSubmittedPlan(t *testing.T) {
	provider := &modePlanProvider{}
	a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	a.SetPlanMode(true)
	if err := a.Run(context.Background(), "plan this"); err != nil {
		t.Fatal(err)
	}
	plan, ok := a.LatestPlan()
	if !ok || plan.Title != "Add caching" || len(plan.Steps) != 1 {
		t.Fatalf("saved plan = %+v, ok=%v", plan, ok)
	}
	if !a.PlanMode() {
		t.Fatal("planning request changed mode")
	}
	if planText, ready := a.TakePlanDecision(); !ready || !strings.Contains(planText, "Add caching") {
		t.Fatalf("plan decision = %q, ready=%v", planText, ready)
	}
	if _, ready := a.TakePlanDecision(); ready {
		t.Fatal("plan decision was returned more than once")
	}
	seen := false
	for _, schema := range provider.tools {
		if schema.Name == "propose_plan" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("provider did not receive propose_plan schema")
	}
	data := a.checkpoint.Load()
	if data == nil {
		t.Fatal("plan state was not checkpointed")
	}
	restored := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	if err := restored.RestoreState(*data); err != nil {
		t.Fatal(err)
	}
	if !restored.PlanMode() {
		t.Fatal("restored agent lost Plan mode")
	}
	if text, ok := restored.LatestPlanText(); !ok || text == "" {
		t.Fatal("restored agent lost latest plan")
	}
}
