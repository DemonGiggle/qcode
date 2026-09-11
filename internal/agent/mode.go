package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"qcode/internal/llm"
	"qcode/internal/prompt"
)

// ExecutionMode controls the authority available to an agent request.
type ExecutionMode string

const (
	ModeAct  ExecutionMode = "act"
	ModePlan ExecutionMode = "plan"
)

// Plan is the latest complete plan submitted by a Plan-mode agent. It is kept
// as structured data so /plan act never has to guess which assistant message
// the user meant.
type Plan struct {
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Steps         []string `json:"steps"`
	Validation    []string `json:"validation"`
	OpenQuestions []string `json:"open_questions,omitempty"`
}

func (p Plan) clone() Plan {
	p.Steps = append([]string(nil), p.Steps...)
	p.Validation = append([]string(nil), p.Validation...)
	p.OpenQuestions = append([]string(nil), p.OpenQuestions...)
	return p
}

func renderPlan(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n## Implementation steps\n", p.Title, p.Summary)
	for i, step := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	b.WriteString("\n## Validation\n")
	for _, check := range p.Validation {
		fmt.Fprintf(&b, "- %s\n", check)
	}
	if len(p.OpenQuestions) > 0 {
		b.WriteString("\n## Open questions\n")
		for _, question := range p.OpenQuestions {
			fmt.Fprintf(&b, "- %s\n", question)
		}
	}
	return strings.TrimSpace(b.String())
}

// PlanMode reports whether this agent is currently read-only.
func (a *Agent) PlanMode() bool { return a.planMode.Load() }

// SetPlanMode changes the agent's default mode for future requests. The TUI
// only exposes this while the agent is idle, so an active run cannot change
// authority midway through a request.
func (a *Agent) SetPlanMode(enabled bool) {
	a.planMode.Store(enabled)
	a.system = prompt.SystemForMode(a.selectedSkills, enabled)
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = a.system
	}
	a.invalidateContextUsage()
	a.publishCheckpoint()
}

// LatestPlan returns a copy of the executable plan, if one has been saved.
func (a *Agent) LatestPlan() (Plan, bool) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.latestPlan == nil {
		return Plan{}, false
	}
	return a.latestPlan.clone(), true
}

// LatestPlanText returns the rendered latest plan for a user-facing handoff.
func (a *Agent) LatestPlanText() (string, bool) {
	plan, ok := a.LatestPlan()
	if !ok {
		return "", false
	}
	return renderPlan(plan), true
}

// ClearLatestPlan starts a fresh planning handoff while retaining prior plan
// revisions in the conversation transcript.
func (a *Agent) ClearLatestPlan() {
	a.stateMu.Lock()
	a.latestPlan = nil
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

func (a *Agent) enabledSchemas() []llm.Tool {
	if !a.PlanMode() {
		return a.tools.EnabledSchemas()
	}
	filtered := make([]llm.Tool, 0)
	for _, tool := range a.tools.EnabledSchemas() {
		if planAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return append(filtered, proposePlanSchema())
}

func (a *Agent) executeDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	if !a.PlanMode() {
		return a.tools.ExecuteDetailed(ctx, call)
	}
	return (&modeToolset{base: a.tools, owner: a}).ExecuteDetailed(ctx, call)
}

func (a *Agent) savePlan(plan Plan) {
	a.stateMu.Lock()
	a.latestPlan = &plan
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

func proposePlanSchema() llm.Tool {
	stringArray := func(description string) map[string]any {
		return map[string]any{"type": "array", "description": description, "items": map[string]any{"type": "string"}}
	}
	return llm.Tool{Name: "propose_plan", Description: prompt.ProposePlanTool, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"title":          map[string]any{"type": "string", "description": prompt.PlanTitleParameter},
			"summary":        map[string]any{"type": "string", "description": prompt.PlanSummaryParameter},
			"steps":          stringArray(prompt.PlanStepsParameter),
			"validation":     stringArray(prompt.PlanValidationParameter),
			"open_questions": stringArray(prompt.PlanQuestionsParameter),
		},
		"required": []string{"title", "summary", "steps", "validation"},
	}}
}

func planAllowedTool(name string) bool {
	switch name {
	case "web_fetch", "web_search", "read", "list", "search", "view_image", "skill", "list_agents", "search_agent_work", "propose_plan":
		return true
	default:
		return false
	}
}

// modeToolset is the enforcement boundary for Plan mode. It forwards all
// mutable tool state to the existing registry while filtering and rejecting
// dangerous calls independently of model instructions.
type modeToolset struct {
	base  Toolset
	owner *Agent
}

func (t *modeToolset) Schemas() []llm.Tool {
	base := t.base.Schemas()
	if !t.owner.PlanMode() {
		return base
	}
	filtered := make([]llm.Tool, 0, len(base)+1)
	for _, tool := range base {
		if planAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return append(filtered, proposePlanSchema())
}

func (t *modeToolset) EnabledSchemas() []llm.Tool {
	base := t.base.EnabledSchemas()
	if !t.owner.PlanMode() {
		return base
	}
	filtered := make([]llm.Tool, 0, len(base)+1)
	for _, tool := range base {
		if planAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return append(filtered, proposePlanSchema())
}

func (t *modeToolset) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	if t.owner.PlanMode() {
		if call.Name == "propose_plan" {
			var plan Plan
			if err := json.Unmarshal(call.Arguments, &plan); err != nil {
				return llm.ToolResult{}, fmt.Errorf("invalid plan: %w", err)
			}
			plan.Title = strings.TrimSpace(plan.Title)
			plan.Summary = strings.TrimSpace(plan.Summary)
			if plan.Title == "" || plan.Summary == "" || len(plan.Steps) == 0 || len(plan.Validation) == 0 {
				return llm.ToolResult{}, fmt.Errorf("plan requires a title, summary, at least one step, and at least one validation check")
			}
			for i := range plan.Steps {
				plan.Steps[i] = strings.TrimSpace(plan.Steps[i])
			}
			for i := range plan.Validation {
				plan.Validation[i] = strings.TrimSpace(plan.Validation[i])
			}
			for i := range plan.OpenQuestions {
				plan.OpenQuestions[i] = strings.TrimSpace(plan.OpenQuestions[i])
			}
			planText := renderPlan(plan)
			t.owner.savePlan(plan)
			return llm.ToolResult{Output: planText, EndTurn: true}, nil
		}
		if !planAllowedTool(call.Name) {
			return llm.ToolResult{}, fmt.Errorf("tool %q is unavailable in Plan mode; Plan mode is read-only", call.Name)
		}
	}
	return t.base.ExecuteDetailed(ctx, call)
}

func (t *modeToolset) ResetSession() {
	if resetter, ok := t.base.(interface{ ResetSession() }); ok {
		resetter.ResetSession()
	}
}
func (t *modeToolset) ToolNames() []string {
	names := make([]string, 0)
	for _, name := range toolNames(t.base) {
		if !t.owner.PlanMode() || planAllowedTool(name) {
			names = append(names, name)
		}
	}
	if t.owner.PlanMode() {
		names = append(names, "propose_plan")
	}
	return names
}
func toolNames(t Toolset) []string {
	if named, ok := t.(interface{ ToolNames() []string }); ok {
		return named.ToolNames()
	}
	result := make([]string, 0, len(t.Schemas()))
	for _, schema := range t.Schemas() {
		result = append(result, schema.Name)
	}
	return result
}
func (t *modeToolset) ToggleTool(name string, enabled bool) {
	if t.owner.PlanMode() && !planAllowedTool(name) {
		return
	}
	if configurable, ok := t.base.(interface{ ToggleTool(string, bool) }); ok {
		configurable.ToggleTool(name, enabled)
		return
	}
	if configurable, ok := t.base.(interface {
		EnableTool(string)
		DisableTool(string)
	}); ok {
		if enabled {
			configurable.EnableTool(name)
		} else {
			configurable.DisableTool(name)
		}
	}
}
func (t *modeToolset) ToolEnabled(name string) bool {
	if t.owner.PlanMode() && !planAllowedTool(name) {
		return false
	}
	if configurable, ok := t.base.(interface{ ToolEnabled(string) bool }); ok {
		return configurable.ToolEnabled(name)
	}
	for _, schema := range t.base.EnabledSchemas() {
		if schema.Name == name {
			return true
		}
	}
	return false
}
func (t *modeToolset) SaveTools() json.RawMessage {
	if persistent, ok := t.base.(persistentTools); ok {
		return persistent.SaveTools()
	}
	return nil
}
func (t *modeToolset) RestoreTools(data json.RawMessage) error {
	if persistent, ok := t.base.(persistentTools); ok {
		return persistent.RestoreTools(data)
	}
	return nil
}
func (t *modeToolset) RestoreWarnings() []string {
	if warnings, ok := t.base.(interface{ RestoreWarnings() []string }); ok {
		return warnings.RestoreWarnings()
	}
	return nil
}
func (t *modeToolset) TaskContext(text string) string {
	if contextual, ok := t.base.(interface{ TaskContext(string) string }); ok {
		return contextual.TaskContext(text)
	}
	return ""
}
func (t *modeToolset) WorkspaceState(ctx context.Context) map[string]string {
	if tracker, ok := t.base.(interface {
		WorkspaceState(context.Context) map[string]string
	}); ok {
		return tracker.WorkspaceState(ctx)
	}
	return nil
}
