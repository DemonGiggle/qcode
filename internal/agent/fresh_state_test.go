package agent

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/skills"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

type freshStateProvider struct {
	resumeProvider
	identity string
}

func (p *freshStateProvider) SessionIdentity() string { return p.identity }
func (p *freshStateProvider) RestoreSessionIdentity(id string) error {
	if id != "" {
		p.identity = id
	}
	return nil
}
func (*freshStateProvider) ThinkingCapability(string) llm.ThinkingCapability {
	return llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"low", "high"}}
}

func TestRestoreFreshStatePreservesSettingsAndClearsConversation(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	selection := skills.NewLazySelection(root)
	registry, err := tools.NewWithOptions(root, tools.Options{Skills: selection})
	if err != nil {
		t.Fatal(err)
	}
	toolState, _ := json.Marshal(map[string]any{
		"Disabled": map[string]bool{"web_search": false, "web_fetch": true, "shell": true},
		"Grants":   []string{root, external}, "Skills": []string{"review"},
	})
	if err := registry.RestoreTools(toolState); err != nil {
		t.Fatal(err)
	}
	provider := &freshStateProvider{identity: "old-provider-session"}
	a := NewWithSystem(provider, "current-model", registry, trace.New(io.Discard, false), io.Discard, 21, "current system")
	if err := a.SetThinking("high"); err != nil {
		t.Fatal(err)
	}
	a.SetContextWindow(19000)
	a.SetAutoCompact(false, 67)
	a.SetInteractiveAvailable(true)
	a.SetInteractiveMode(true)
	a.SetSkills([]prompt.SkillSummary{{Name: "review", Description: "Review changes"}})
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "old conversation"})
	a.lastResponse = "old answer"
	a.pendingImages = []llm.Image{{Data: []byte("old image")}}
	a.contextUsage = &llm.Usage{InputTokens: 1000, OutputTokens: 500}
	a.contextMessages = 2
	a.sessionUsage = llm.SessionUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500}
	a.learningBudget, a.learningContext, a.learningSessionID = 345, "old learning", "old-learning-session"
	a.latestPlan, a.latestSkillDraft = &Plan{Title: "old plan"}, &SkillDraft{Name: "old draft"}
	a.planMode.Store(true)
	a.skillPlanMode.Store(true)
	a.artifactVersion = 14
	a.SetEndpoint("https://current.example/v1")
	checkpoint := *a.checkpoint.Load()

	newSelection := skills.NewLazySelection(root)
	newRegistry, err := tools.NewWithOptions(root, tools.Options{Skills: newSelection})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewAgentManager(context.Background(), 4)
	defer manager.Shutdown()
	newProvider := &freshStateProvider{identity: "fresh-provider-session"}
	b := NewWithSystem(newProvider, "current-model", manager.WrapToolset("main", newRegistry, true), trace.New(io.Discard, false), io.Discard, 1, "new trusted system")
	b.SetInteractiveAvailable(true)
	if err := b.RestoreFreshState(checkpoint); err != nil {
		t.Fatal(err)
	}
	var saved SavedState
	if err := json.Unmarshal(*b.checkpoint.Load(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Provider != provider.Name() || saved.Model != "current-model" || saved.Endpoint != a.endpoint || saved.Thinking != "high" || saved.MaxSteps != 21 || saved.ContextOverride != 19000 || saved.AutoCompact || saved.AutoCompactThreshold != 67 || saved.LearningBudget != 345 || !saved.InteractiveMode || !reflect.DeepEqual(saved.Skills, a.SelectedSkills()) {
		t.Fatalf("lost settings: %+v", saved)
	}
	if len(saved.Messages) != 1 || saved.Messages[0].Role != "system" || saved.Messages[0].Content != b.system || strings.Contains(saved.Messages[0].Content, "old conversation") || !strings.Contains(saved.Messages[0].Content, "Review changes") {
		t.Fatal("fresh system conversation not prepared", saved.Messages)
	}
	if saved.PlanMode || saved.SkillPlanMode || saved.LatestPlan != nil || saved.LatestSkillDraft != nil || saved.ArtifactVersion != 0 || saved.LastResponse != "" || len(saved.PendingImages) != 0 || saved.Usage != (llm.SessionUsage{}) || saved.ContextUsage != nil || saved.ContextMessages != 0 || saved.LearningContext != "" || saved.LearningSessionID != "" || saved.ProviderSession != "fresh-provider-session" || b.restored {
		t.Fatalf("retained conversation state: %+v", saved)
	}
	var selected struct {
		Disabled       map[string]bool
		Grants, Skills []string
	}
	if err := json.Unmarshal(saved.Tools, &selected); err != nil {
		t.Fatal(err)
	}
	if b.ToolEnabled("shell") || !b.ToolEnabled("web_search") || b.ToolEnabled("web_fetch") || !reflect.DeepEqual(selected.Grants, []string{root}) || !reflect.DeepEqual(newSelection.Selected(), []string{"review"}) {
		t.Fatal("tool selections or fresh grants are wrong", selected)
	}
	if provider.identity != "old-provider-session" || len(a.messages) != 2 || a.LastResponse() != "old answer" {
		t.Fatal("preparation mutated the source agent")
	}
	if err := b.Run(context.Background(), "new task"); err != nil {
		t.Fatal(err)
	}
	if len(newProvider.request.Messages) != 2 || newProvider.request.Messages[1].Content != "new task" || newProvider.request.Thinking != "high" {
		t.Fatal("new request carried old history", newProvider.request)
	}
}

func TestRestoreFreshStateClearsStartupThinkingWhenCurrentLevelIsDefault(t *testing.T) {
	a := New(&freshStateProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	b := New(&freshStateProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	if err := b.SetThinking("high"); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreFreshState(*a.checkpoint.Load()); err != nil {
		t.Fatal(err)
	}
	if b.ThinkingLevel() != "" {
		t.Fatal("startup thinking overrode current default level")
	}
}

func TestRestoreFreshStateRejectsInvalidSettingsAndTools(t *testing.T) {
	a := New(&freshStateProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	for _, data := range []json.RawMessage{json.RawMessage(`broken`), json.RawMessage(`{"Provider":"resume-test","Model":"other","MaxSteps":4}`)} {
		if err := a.RestoreFreshState(data); err == nil {
			t.Fatal("invalid settings accepted", string(data))
		}
	}
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := New(&freshStateProvider{}, "model", registry, trace.New(io.Discard, false), io.Discard, 4)
	data := json.RawMessage(`{"Provider":"resume-test","Model":"model","MaxSteps":4,"Tools":{"Disabled":true}}`)
	if err := b.RestoreFreshState(data); err == nil {
		t.Fatal("invalid tool selections accepted")
	}
}
