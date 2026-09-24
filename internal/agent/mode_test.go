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

type interactiveQuestionProvider struct {
	calls    int
	messages []llm.Message
}

type batchQuestionProvider struct {
	calls    int
	messages []llm.Message
}

func (*batchQuestionProvider) Name() string { return "batch-question-test" }
func (p *batchQuestionProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	p.messages = request.Messages
	if p.calls == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "ask-1", Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"Which flow?"}]}`)},
			{ID: "read-1", Name: "read", Arguments: json.RawMessage(`{"path":"unrelated.go"}`)},
		}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func (*interactiveQuestionProvider) Name() string { return "interactive-question-test" }
func (p *interactiveQuestionProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	p.messages = request.Messages
	if p.calls == 1 {
		args := json.RawMessage(`{"questions":[{"question":"Which flow?","options":["Login","OAuth"]}]}`)
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "ask_questions", Arguments: args}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "Continue with OAuth"}}, nil
}

func TestInteractiveQuestionsGateBudgetAndUserAnswer(t *testing.T) {
	provider := &interactiveQuestionProvider{}
	a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 5)
	call := llm.ToolCall{Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"Which flow?"}]}`)}
	if _, err := a.executeDetailed(context.Background(), call); err == nil {
		t.Fatal("question bypassed disabled toggle")
	}
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	if !a.ToolEnabled("ask_questions") || !hasTool(a.enabledSchemas(), "ask_questions") {
		t.Fatal("enabled interactive question tool is hidden")
	}
	a.SetQuestioner(func(_ context.Context, questions []Question) ([]string, error) {
		return []string{"OAuth"}, nil
	})
	if err := a.Run(context.Background(), "fix auth"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 || len(provider.messages) < 4 || provider.messages[len(provider.messages)-1].Role != "user" || !strings.Contains(provider.messages[len(provider.messages)-1].Content, "OAuth") {
		t.Fatalf("answer did not enter user context: calls=%d messages=%+v", provider.calls, provider.messages)
	}
	if _, err := a.executeDetailed(context.Background(), call); err == nil || !strings.Contains(err.Error(), "already asked") {
		t.Fatalf("duplicate question error = %v", err)
	}
	for _, text := range []string{"Which service?", "Which file?"} {
		args, _ := json.Marshal(map[string]any{"questions": []map[string]any{{"question": text}}})
		if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "ask_questions", Arguments: args}); err != nil {
			t.Fatal(err)
		}
	}
	if hasTool(a.enabledSchemas(), "ask_questions") {
		t.Fatal("question tool remained visible after budget exhausted")
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"One more?"}]}`)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("budget boundary error = %v", err)
	}
	a.SetInteractiveMode(false)
	if hasTool(a.enabledSchemas(), "ask_questions") {
		t.Fatal("disabled question tool remained visible")
	}
}

func TestInteractiveQuestionsUnavailableWithoutTerminal(t *testing.T) {
	a := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	a.SetInteractiveAvailable(false)
	a.SetInteractiveMode(true)
	if hasTool(a.enabledSchemas(), "ask_questions") || !strings.Contains(a.system, "Interactive questions are unavailable") {
		t.Fatal("non-interactive agent offered questions")
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"Where?"}]}`)}); err == nil {
		t.Fatal("non-interactive question call was accepted")
	}
	a.SetPlanMode(true)
	if !hasTool(a.enabledSchemas(), "ask_questions") {
		t.Fatal("Plan mode lost its existing question tool")
	}
}

func TestInteractiveQuestionsAcceptOptionObjects(t *testing.T) {
	a := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	a.SetQuestioner(func(_ context.Context, questions []Question) ([]string, error) {
		if len(questions) != 1 || len(questions[0].Options) != 2 ||
			questions[0].Options[0] != "Colored lines are missed" || questions[0].Options[1] != "Cursor codes appear" ||
			questions[0].OptionDescriptions[0] != "The gag count stays zero" {
			t.Fatalf("options were not normalized to labels: %+v", questions)
		}
		return []string{questions[0].Options[0]}, nil
	})
	call := llm.ToolCall{Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"What fails?","options":[{"label":"Colored lines are missed","description":"The gag count stays zero"},"Cursor codes appear"]}]}`)}
	result, err := a.executeDetailed(context.Background(), call)
	if err != nil || !strings.Contains(result.UserAnswer, "Colored lines are missed") {
		t.Fatalf("object options were rejected: result=%+v err=%v", result, err)
	}
}

func TestInteractiveQuestionsRejectOptionWithoutLabel(t *testing.T) {
	a := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 2)
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	call := llm.ToolCall{Name: "ask_questions", Arguments: json.RawMessage(`{"questions":[{"question":"What fails?","options":[{"description":"No label"},"Other"]}]}`)}
	if _, err := a.executeDetailed(context.Background(), call); err == nil || !strings.Contains(err.Error(), "empty option") {
		t.Fatalf("missing option label error = %v", err)
	}
}

func TestInteractiveAnswerFollowsAllToolResults(t *testing.T) {
	provider := &batchQuestionProvider{}
	tools := &modeTestToolset{}
	a := New(provider, "model", tools, trace.New(io.Discard, false), io.Discard, 3)
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	a.SetQuestioner(func(context.Context, []Question) ([]string, error) { return []string{"OAuth"}, nil })
	if err := a.Run(context.Background(), "investigate auth"); err != nil {
		t.Fatal(err)
	}
	if tools.called != "" {
		t.Fatal("workspace tool ran before the user answer was processed")
	}
	messages := provider.messages
	if len(messages) < 5 || messages[len(messages)-3].Role != "tool" || messages[len(messages)-2].Role != "tool" || messages[len(messages)-1].Role != "user" {
		t.Fatalf("assistant tool-call pairing was broken: %+v", messages)
	}
}

func hasTool(schemas []llm.Tool, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
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
