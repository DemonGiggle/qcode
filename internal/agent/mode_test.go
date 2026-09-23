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

type modeSkillPlanProvider struct {
	calls int
	tools []llm.Tool
}

func (*modeSkillPlanProvider) Name() string { return "mode-skill-plan-test" }
func (p *modeSkillPlanProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	p.tools = request.Tools
	if p.calls > 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Review the draft, refine it, or create it."}}, nil
	}
	args, _ := json.Marshal(map[string]any{
		"name": "review-migrations", "location": ".qcode/skills",
		"content": "---\ndescription: Review database migrations safely.\n---\n\n# Review migrations\n\n## Validation\n\nRun migration tests.",
	})
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "skill-1", Name: "propose_skill", Arguments: args}}}}, nil
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

func TestSkillPlanModeFiltersMutationAndSavesDraft(t *testing.T) {
	provider := &modeSkillPlanProvider{}
	tools := &modeTestToolset{}
	a := New(provider, "model", tools, trace.New(io.Discard, false), io.Discard, 3)
	a.SetSkillPlanMode(true)
	seen := map[string]bool{}
	for _, schema := range a.enabledSchemas() {
		seen[schema.Name] = true
	}
	for _, name := range []string{"read", "list_agents", "ask_questions", "propose_skill"} {
		if !seen[name] {
			t.Fatalf("Skill Plan mode omitted %q from schemas: %v", name, seen)
		}
	}
	if !a.ToolEnabled("ask_questions") || !a.ToolEnabled("propose_skill") {
		t.Fatal("Skill Plan mode reported its built-in tools as disabled")
	}
	for _, name := range []string{"write", "shell", "create_agent", "propose_plan"} {
		if seen[name] {
			t.Fatalf("Skill Plan mode advertised unavailable tool %q", name)
		}
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "write"}); err == nil {
		t.Fatal("write unexpectedly succeeded in Skill Plan mode")
	}
	if tools.called != "" {
		t.Fatal("rejected tool reached underlying toolset")
	}
	if err := a.Run(context.Background(), "design a migration review skill"); err != nil {
		t.Fatal(err)
	}
	draft, ok := a.LatestSkillDraft()
	if !ok || draft.Name != "review-migrations" || draft.Location != ".qcode/skills" || !strings.Contains(draft.Content, "description:") {
		t.Fatalf("saved draft = %+v, ok=%v", draft, ok)
	}
	if !a.SkillPlanMode() || a.PlanMode() {
		t.Fatalf("mode state: skillplan=%v plan=%v", a.SkillPlanMode(), a.PlanMode())
	}
	if text, ok := a.LatestSkillDraftText(); !ok || !strings.Contains(text, ".qcode/skills/review-migrations/SKILL.md") {
		t.Fatalf("draft text = %q, ok=%v", text, ok)
	}
	data := a.checkpoint.Load()
	if data == nil {
		t.Fatal("skill draft state was not checkpointed")
	}
	restored := New(&modeSkillPlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	if err := restored.RestoreState(*data); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := restored.LatestSkillDraftParts(); !restored.SkillPlanMode() || !ok {
		t.Fatal("restored agent lost Skill Plan state")
	}
}

func TestSkillPlanModeRejectsInvalidDraft(t *testing.T) {
	a := New(&modeSkillPlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	a.SetSkillPlanMode(true)
	args, _ := json.Marshal(map[string]any{"name": "Invalid Name", "location": ".qcode/skills", "content": "instructions"})
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "propose_skill", Arguments: args}); err == nil {
		t.Fatal("invalid skill draft unexpectedly succeeded")
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
