package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/prompt"
)

// ExecutionMode controls the authority available to an agent request.
type ExecutionMode string

const (
	ModeAct       ExecutionMode = "act"
	ModePlan      ExecutionMode = "plan"
	ModeSkillPlan ExecutionMode = "skillplan"
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

// SkillDraft is the complete, reviewable artifact produced in Skill Plan
// mode. Location is one of qcode's built-in skill roots.
type SkillDraft struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Content  string `json:"content"`
}

func (d SkillDraft) clone() SkillDraft { return d }

func renderSkillDraft(d SkillDraft) string {
	path := strings.TrimSuffix(d.Location, "/") + "/" + d.Name + "/SKILL.md"
	return fmt.Sprintf("# Skill draft: %s\n\nTarget: `%s`\n\n```markdown\n%s\n```", d.Name, path, strings.TrimSpace(d.Content))
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
	if enabled {
		a.skillPlanMode.Store(false)
		a.stateMu.Lock()
		a.skillPlanDecisionPending = false
		a.stateMu.Unlock()
	}
	if !enabled {
		a.stateMu.Lock()
		a.planDecisionPending = false
		a.stateMu.Unlock()
	}
	a.system = prompt.SystemForModes(a.selectedSkills, enabled, a.SkillPlanMode())
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = a.system
	}
	a.invalidateContextUsage()
	a.publishCheckpoint()
}

// SkillPlanMode reports whether this agent is designing a qcode skill in the
// read-only guided workflow.
func (a *Agent) SkillPlanMode() bool { return a.skillPlanMode.Load() }

// SetSkillPlanMode changes the default mode for future requests. Skill Plan
// and implementation Plan modes are mutually exclusive.
func (a *Agent) SetSkillPlanMode(enabled bool) {
	a.skillPlanMode.Store(enabled)
	if enabled {
		a.planMode.Store(false)
		a.stateMu.Lock()
		a.planDecisionPending = false
		a.stateMu.Unlock()
	} else {
		a.stateMu.Lock()
		a.skillPlanDecisionPending = false
		a.stateMu.Unlock()
	}
	a.system = prompt.SystemForModes(a.selectedSkills, a.PlanMode(), enabled)
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = a.system
	}
	a.invalidateContextUsage()
	a.publishCheckpoint()
}

// LatestSkillDraft returns a copy of the most recently submitted draft.
func (a *Agent) LatestSkillDraft() (SkillDraft, bool) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.latestSkillDraft == nil {
		return SkillDraft{}, false
	}
	return a.latestSkillDraft.clone(), true
}

// LatestSkillDraftParts exposes the draft through primitive return values so
// UI packages can consume it without depending on Agent's concrete type.
func (a *Agent) LatestSkillDraftParts() (name, location, content string, ok bool) {
	draft, ok := a.LatestSkillDraft()
	return draft.Name, draft.Location, draft.Content, ok
}

// LatestSkillDraftText returns a reviewable rendering of the current draft.
func (a *Agent) LatestSkillDraftText() (string, bool) {
	draft, ok := a.LatestSkillDraft()
	if !ok {
		return "", false
	}
	return renderSkillDraft(draft), true
}

// ClearLatestSkillDraft starts a fresh guided skill-design flow.
func (a *Agent) ClearLatestSkillDraft() {
	a.stateMu.Lock()
	a.latestSkillDraft = nil
	a.skillPlanDecisionPending = false
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

func (a *Agent) saveSkillDraft(draft SkillDraft) {
	a.stateMu.Lock()
	a.latestSkillDraft = &draft
	a.skillPlanDecisionPending = true
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

// TakeSkillPlanDecision returns the latest draft once when the interactive
// host should ask whether to create it or remain in Skill Plan mode.
func (a *Agent) TakeSkillPlanDecision() (string, bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.skillPlanDecisionPending || a.latestSkillDraft == nil {
		return "", false
	}
	a.skillPlanDecisionPending = false
	return renderSkillDraft(*a.latestSkillDraft), true
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
	a.planDecisionPending = false
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

// TakePlanDecision returns the latest plan once when the interactive host
// should ask whether to implement it or remain in Plan mode.
func (a *Agent) TakePlanDecision() (string, bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if !a.planDecisionPending || a.latestPlan == nil {
		return "", false
	}
	a.planDecisionPending = false
	return renderPlan(*a.latestPlan), true
}

func (a *Agent) enabledSchemas() []llm.Tool {
	if !a.PlanMode() && !a.SkillPlanMode() {
		return a.tools.EnabledSchemas()
	}
	filtered := make([]llm.Tool, 0)
	for _, tool := range a.tools.EnabledSchemas() {
		if a.modeAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	filtered = append(filtered, askQuestionsSchema())
	if a.PlanMode() {
		return append(filtered, proposePlanSchema())
	}
	return append(filtered, proposeSkillSchema())
}

func (a *Agent) executeDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	if !a.PlanMode() && !a.SkillPlanMode() {
		return a.tools.ExecuteDetailed(ctx, call)
	}
	return (&modeToolset{base: a.tools, owner: a}).ExecuteDetailed(ctx, call)
}

func (a *Agent) savePlan(plan Plan) {
	a.stateMu.Lock()
	a.latestPlan = &plan
	a.planDecisionPending = true
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

func proposeSkillSchema() llm.Tool {
	return llm.Tool{Name: "propose_skill", Description: prompt.ProposeSkillTool, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"name":     map[string]any{"type": "string", "description": prompt.SkillDraftNameParameter},
			"location": map[string]any{"type": "string", "enum": []string{"~/.qcode/skills", ".agents/skills", ".qcode/skills"}, "description": prompt.SkillLocationParameter},
			"content":  map[string]any{"type": "string", "description": prompt.SkillContentParameter},
		},
		"required": []string{"name", "location", "content"},
	}}
}

func askQuestionsSchema() llm.Tool {
	question := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"question": map[string]any{"type": "string", "description": prompt.QuestionTextParameter},
			"options": map[string]any{
				"type":        "array",
				"minItems":    2,
				"maxItems":    6,
				"items":       map[string]any{"type": "string"},
				"description": prompt.QuestionOptionsParameter,
			},
		},
		"required": []string{"question"},
	}
	return llm.Tool{Name: "ask_questions", Description: prompt.AskQuestionsTool, Parameters: map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"questions": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": question}},
		"required":             []string{"questions"},
	}}
}

func (a *Agent) askQuestions(ctx context.Context, arguments json.RawMessage) (llm.ToolResult, error) {
	var request struct {
		Questions []struct {
			Text    string   `json:"question"`
			Options []string `json:"options,omitempty"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(arguments, &request); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid questions: %w", err)
	}
	if len(request.Questions) == 0 || len(request.Questions) > 8 {
		return llm.ToolResult{}, fmt.Errorf("ask_questions requires 1-8 questions")
	}
	questions := make([]Question, len(request.Questions))
	for i, item := range request.Questions {
		questions[i] = Question{Text: strings.TrimSpace(item.Text), AllowCustom: true}
		if questions[i].Text == "" {
			return llm.ToolResult{}, fmt.Errorf("question %d must not be empty", i+1)
		}
		for _, option := range item.Options {
			option = strings.TrimSpace(option)
			if option == "" {
				return llm.ToolResult{}, fmt.Errorf("question %d has an empty option", i+1)
			}
			questions[i].Options = append(questions[i].Options, option)
		}
		if len(questions[i].Options) == 1 || len(questions[i].Options) > 6 {
			return llm.ToolResult{}, fmt.Errorf("question %d must have either no options or 2-6 options", i+1)
		}
	}

	a.stateMu.RLock()
	questioner := a.questioner
	a.stateMu.RUnlock()
	if questioner == nil {
		return llm.ToolResult{}, fmt.Errorf("ask_questions requires an interactive terminal")
	}
	answers, err := questioner(ctx, questions)
	if err != nil {
		return llm.ToolResult{}, err
	}
	if len(answers) != len(questions) {
		return llm.ToolResult{}, fmt.Errorf("questionnaire returned %d answers for %d questions", len(answers), len(questions))
	}
	data, err := json.Marshal(struct {
		Answers []string `json:"answers"`
	}{Answers: answers})
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Output: string(data)}, nil
}

func planAllowedTool(name string) bool {
	switch name {
	case "web_fetch", "web_search", "read", "list", "search", "view_image", "skill", "list_agents", "search_agent_work", "ask_questions", "propose_plan":
		return true
	default:
		return false
	}
}

func skillPlanAllowedTool(name string) bool {
	switch name {
	case "web_fetch", "web_search", "read", "list", "search", "view_image", "skill", "list_agents", "search_agent_work", "ask_questions", "propose_skill":
		return true
	default:
		return false
	}
}

func (a *Agent) modeAllowedTool(name string) bool {
	if a.PlanMode() {
		return planAllowedTool(name)
	}
	if a.SkillPlanMode() {
		return skillPlanAllowedTool(name)
	}
	return true
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
	if !t.owner.PlanMode() && !t.owner.SkillPlanMode() {
		return base
	}
	filtered := make([]llm.Tool, 0, len(base)+2)
	for _, tool := range base {
		if t.owner.modeAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	filtered = append(filtered, askQuestionsSchema())
	if t.owner.PlanMode() {
		return append(filtered, proposePlanSchema())
	}
	return append(filtered, proposeSkillSchema())
}

func (t *modeToolset) EnabledSchemas() []llm.Tool {
	base := t.base.EnabledSchemas()
	if !t.owner.PlanMode() && !t.owner.SkillPlanMode() {
		return base
	}
	filtered := make([]llm.Tool, 0, len(base)+2)
	for _, tool := range base {
		if t.owner.modeAllowedTool(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	filtered = append(filtered, askQuestionsSchema())
	if t.owner.PlanMode() {
		return append(filtered, proposePlanSchema())
	}
	return append(filtered, proposeSkillSchema())
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
		if call.Name == "ask_questions" {
			return t.owner.askQuestions(ctx, call.Arguments)
		}
		if !planAllowedTool(call.Name) {
			return llm.ToolResult{}, fmt.Errorf("tool %q is unavailable in Plan mode; Plan mode is read-only", call.Name)
		}
	}
	if t.owner.SkillPlanMode() {
		if call.Name == "propose_skill" {
			var draft SkillDraft
			if err := json.Unmarshal(call.Arguments, &draft); err != nil {
				return llm.ToolResult{}, fmt.Errorf("invalid skill draft: %w", err)
			}
			draft.Name = strings.TrimSpace(draft.Name)
			draft.Location = strings.TrimSpace(draft.Location)
			draft.Content = strings.TrimSpace(draft.Content)
			if err := validateSkillDraft(draft); err != nil {
				return llm.ToolResult{}, err
			}
			t.owner.saveSkillDraft(draft)
			return llm.ToolResult{Output: renderSkillDraft(draft) + "\n\nDraft saved for review. Do not create files until the user explicitly approves it with /skillplan create.", EndTurn: true}, nil
		}
		if call.Name == "ask_questions" {
			return t.owner.askQuestions(ctx, call.Arguments)
		}
		if !skillPlanAllowedTool(call.Name) {
			return llm.ToolResult{}, fmt.Errorf("tool %q is unavailable in Skill Plan mode; Skill Plan mode is read-only", call.Name)
		}
	}
	return t.base.ExecuteDetailed(ctx, call)
}

func validateSkillDraft(draft SkillDraft) error {
	if draft.Name == "" || len(draft.Name) > 64 {
		return fmt.Errorf("skill name must contain 1-64 lowercase letters, digits, hyphens, or underscores")
	}
	for _, r := range draft.Name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("skill name must contain 1-64 lowercase letters, digits, hyphens, or underscores")
		}
	}
	switch draft.Location {
	case "~/.qcode/skills", ".agents/skills", ".qcode/skills":
	default:
		return fmt.Errorf("skill location must be ~/.qcode/skills, .agents/skills, or .qcode/skills")
	}
	if draft.Content == "" {
		return fmt.Errorf("skill content must not be empty")
	}
	if !utf8.ValidString(draft.Content) {
		return fmt.Errorf("skill content must be valid UTF-8")
	}
	if len(draft.Content) > 64*1024 {
		return fmt.Errorf("skill content exceeds qcode's 64 KiB limit")
	}
	return nil
}

func (t *modeToolset) ResetSession() {
	if resetter, ok := t.base.(interface{ ResetSession() }); ok {
		resetter.ResetSession()
	}
}
func (t *modeToolset) ToolNames() []string {
	names := make([]string, 0)
	for _, name := range toolNames(t.base) {
		if t.owner.modeAllowedTool(name) {
			names = append(names, name)
		}
	}
	if t.owner.PlanMode() {
		names = append(names, "ask_questions", "propose_plan")
	} else if t.owner.SkillPlanMode() {
		names = append(names, "ask_questions", "propose_skill")
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
	if (t.owner.PlanMode() || t.owner.SkillPlanMode()) && !t.owner.modeAllowedTool(name) {
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
	if (t.owner.PlanMode() && (name == "ask_questions" || name == "propose_plan")) ||
		(t.owner.SkillPlanMode() && (name == "ask_questions" || name == "propose_skill")) {
		return true
	}
	if (t.owner.PlanMode() || t.owner.SkillPlanMode()) && !t.owner.modeAllowedTool(name) {
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
