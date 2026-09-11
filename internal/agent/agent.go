package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/question"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

const maxIdenticalToolCalls = 3

// DefaultAutoCompactThreshold is the percentage of a known context window
// used before the next request is compacted.
const DefaultAutoCompactThreshold = 80

type Agent struct {
	learningStore        learning.Store
	learningBudget       int
	learningContext      string
	learningSessionID    string
	provider             llm.Provider
	model                string
	tools                Toolset
	trace                *trace.Logger
	out                  io.Writer
	maxSteps             atomic.Int64
	currentStep          atomic.Int64
	messages             []llm.Message
	stateMu              sync.RWMutex
	requestContext       func() string
	taskContext          string
	lastResponse         string
	contextStatus        atomic.Pointer[contextStatus]
	contextWindow        int
	contextOverride      int
	contextUsage         *llm.Usage
	sessionUsage         llm.SessionUsage
	contextMessages      int
	autoCompact          bool
	autoCompactThreshold int
	system               string
	endpoint             string
	selectedSkills       []prompt.SkillSummary
	pendingImages        []llm.Image
	planMode             atomic.Bool
	questioner           Questioner
	latestPlan           *Plan
	checkpoint           atomic.Pointer[[]byte]
}

// Toolset is the complete tool boundary used by the agent loop. Production and
// demo implementations can provide the same schemas with different execution
// behavior without adding mode-specific branches to the loop.
type Toolset interface {
	Schemas() []llm.Tool
	EnabledSchemas() []llm.Tool
	ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error)
}

type Question = question.Question
type Questioner = question.Questioner

type responseLifecycle interface {
	BeginResponse()
	EndResponse()
}

type thinkingLifecycle interface {
	BeginThinking()
	EndThinking()
}

type lineStreamingWriter interface {
	StreamChunkCompletesLine(string) bool
}

type diffWriter interface {
	DiffEnabled() bool
	WriteDiff(string)
}

func New(provider llm.Provider, model string, toolset Toolset, logger *trace.Logger, out io.Writer, maxSteps int) *Agent {
	return NewWithSystem(provider, model, toolset, logger, out, maxSteps, prompt.System)
}

// NewWithSystem constructs an agent with additional startup instructions, such
// as a workspace skill catalog.
func NewWithSystem(provider llm.Provider, model string, toolset Toolset, logger *trace.Logger, out io.Writer, maxSteps int, system string) *Agent {
	if maxSteps <= 0 {
		maxSteps = 32
	}
	if system == "" {
		system = prompt.System
	}
	a := &Agent{provider: provider, model: model, trace: logger, out: out, system: system, messages: []llm.Message{{Role: "system", Content: system}}, autoCompact: true, autoCompactThreshold: DefaultAutoCompactThreshold}
	a.tools = toolset
	a.maxSteps.Store(int64(maxSteps))
	a.publishContext()
	return a
}

func (a *Agent) SetVerbose(verbose bool) { a.trace.SetVerbose(verbose) }

// SetQuestioner installs the interactive host used by Plan mode's
// ask_questions tool. A nil questioner makes that tool unavailable.
func (a *Agent) SetQuestioner(questioner Questioner) {
	a.stateMu.Lock()
	a.questioner = questioner
	a.stateMu.Unlock()
}

// SetAutoCompact configures automatic compaction. Manual compaction remains
// available regardless of this setting.
func (a *Agent) SetAutoCompact(enabled bool, threshold int) {
	defer a.publishCheckpoint()
	a.autoCompact = enabled
	if threshold >= 1 && threshold <= 99 {
		a.autoCompactThreshold = threshold
	}
}

func (a *Agent) SetUnicode(enabled bool) { a.trace.SetUnicode(enabled) }

// SetTaskIndicator controls the trace-level Waiting animation. Terminal tab
// UIs can render the shared task state themselves instead.
func (a *Agent) SetTaskIndicator(enabled bool) { a.trace.SetTaskIndicator(enabled) }

// MaxSteps returns the maximum number of model turns allowed for each request.
func (a *Agent) MaxSteps() int { return int(a.maxSteps.Load()) }

// SetMaxSteps updates the maximum number of model turns allowed for each
// request. The new value also applies to a request that is already running at
// its next model-turn boundary.
func (a *Agent) SetMaxSteps(maxSteps int) {
	if maxSteps <= 0 {
		return
	}
	a.maxSteps.Store(int64(maxSteps))
	a.updateCheckpointMaxSteps(maxSteps)
}

// StepProgress returns the current model-turn number and the configured limit
// for the active request. The current value is zero while the agent is idle.
func (a *Agent) StepProgress() (int, int) {
	return int(a.currentStep.Load()), a.MaxSteps()
}

// SetRequestContext installs an ephemeral context source. Its result is added
// to requests without becoming part of the stored conversation.
func (a *Agent) SetRequestContext(source func() string) {
	a.stateMu.Lock()
	a.requestContext = source
	a.stateMu.Unlock()
	a.invalidateContextUsage()
}

// LastResponse returns the final assistant text from the latest run.
func (a *Agent) LastResponse() string {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.lastResponse
}

// ListModels returns the provider's currently available models.
func (a *Agent) ListModels(ctx context.Context) ([]string, error) {
	lister, ok := a.provider.(llm.ModelLister)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support model discovery", a.provider.Name())
	}
	models, err := lister.Models(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, nil
}

func (a *Agent) SetModel(model string) {
	if model != "" {
		if a.model != model {
			a.contextWindow = 0
			a.contextOverride = 0
			a.contextUsage = nil
		}
		a.model = model
		a.publishContext()
	}
}

// SetSkills replaces the user-selected skills advertised to the model.
func (a *Agent) SetSkills(skills []prompt.SkillSummary) {
	a.selectedSkills = append([]prompt.SkillSummary(nil), skills...)
	defer a.invalidateContextUsage()
	a.system = prompt.SystemForMode(skills, a.PlanMode())
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = a.system
	}
}

// SelectedSkills returns the skills currently advertised to the model.
func (a *Agent) SelectedSkills() []prompt.SkillSummary {
	return append([]prompt.SkillSummary(nil), a.selectedSkills...)
}

// InheritCapabilitiesFrom copies the user-facing capabilities of source into
// this agent. Conversation history and main-only orchestration tools are not
// copied. Tool registries carry the remaining mutable capability state,
// including tool toggles, directory grants, and selected skill names.
func (a *Agent) InheritCapabilitiesFrom(source *Agent) error {
	if source == nil {
		return fmt.Errorf("source agent must not be nil")
	}
	if sourceTools, ok := source.tools.(persistentTools); ok {
		if targetTools, targetOK := a.tools.(persistentTools); targetOK {
			if err := targetTools.RestoreTools(sourceTools.SaveTools()); err != nil {
				return err
			}
		}
	}
	a.SetSkills(source.SelectedSkills())
	a.SetMaxSteps(source.MaxSteps())
	return nil
}

// ResetSession discards conversation history while retaining the agent's
// provider, model, tools, and runtime settings.
func (a *Agent) ResetSession() {
	a.pendingImages = nil
	a.stateMu.Lock()
	a.lastResponse = ""
	a.sessionUsage = llm.SessionUsage{}
	a.stateMu.Unlock()
	a.learningContext = ""
	a.learningSessionID = ""
	a.planMode.Store(false)
	a.stateMu.Lock()
	a.latestPlan = nil
	a.stateMu.Unlock()
	defer a.invalidateContextUsage()
	a.system = prompt.SystemForMode(a.selectedSkills, false)
	a.messages = []llm.Message{{Role: "system", Content: a.system}}
	if resetter, ok := a.tools.(interface{ ResetSession() }); ok {
		resetter.ResetSession()
	}
}

// ToolNames returns the names of all registered tools.
func (a *Agent) ToolNames() []string {
	if a.PlanMode() {
		names := make([]string, 0)
		for _, tool := range a.tools.Schemas() {
			if planAllowedTool(tool.Name) {
				names = append(names, tool.Name)
			}
		}
		return append(names, "ask_questions", "propose_plan")
	}
	if configurable, ok := a.tools.(interface{ ToolNames() []string }); ok {
		return configurable.ToolNames()
	}
	if registry, ok := a.tools.(*tools.Registry); ok {
		return registry.ToolNames()
	}
	names := make([]string, 0)
	for _, tool := range a.tools.Schemas() {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

// ToggleTool enables or disables a tool by name.
func (a *Agent) ToggleTool(name string, enabled bool) {
	if a.PlanMode() && !planAllowedTool(name) {
		return
	}
	defer a.invalidateContextUsage()
	if configurable, ok := a.tools.(interface{ ToggleTool(string, bool) }); ok {
		configurable.ToggleTool(name, enabled)
		return
	}
	if registry, ok := a.tools.(*tools.Registry); ok {
		if enabled {
			registry.EnableTool(name)
		} else {
			registry.DisableTool(name)
		}
	}
}

// ToolEnabled reports whether a tool is currently enabled.
func (a *Agent) ToolEnabled(name string) bool {
	if a.PlanMode() && !planAllowedTool(name) {
		return false
	}
	if configurable, ok := a.tools.(interface{ ToolEnabled(string) bool }); ok {
		return configurable.ToolEnabled(name)
	}
	if registry, ok := a.tools.(*tools.Registry); ok {
		return registry.IsToolEnabled(name)
	}
	for _, tool := range a.tools.EnabledSchemas() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (a *Agent) Run(ctx context.Context, userText string) error {
	if contextual, ok := a.tools.(interface{ TaskContext(string) string }); ok {
		nextContext := contextual.TaskContext(userText)
		if nextContext != a.taskContext {
			a.contextUsage = nil // Previous provider usage excludes the new history excerpts.
		}
		a.taskContext = nextContext
	}
	defer func() { a.taskContext = "" }()
	a.currentStep.Store(0)
	a.repairInterruptedCalls()
	a.stateMu.Lock()
	a.lastResponse = ""
	a.stateMu.Unlock()
	defer func() {
		a.currentStep.Store(0)
		a.publishContext()
	}()
	task := a.trace.BeginTask()
	defer task.End()
	if a.shouldAutoCompact() {
		fmt.Fprintln(a.out, "Compacting conversation to make room for the next request...")
		if _, err := a.Compact(ctx); err != nil {
			fmt.Fprintln(a.out, "Conversation compaction failed:", err)
		} else {
			fmt.Fprintln(a.out, "Conversation compacted.")
		}
	}
	a.messages = append(a.messages, llm.Message{Role: "user", Content: userText})
	a.publishContext()
	identicalToolCalls := map[string]int{}
	for step := 0; step < a.MaxSteps(); step++ {
		a.currentStep.Store(int64(step + 1))
		requestMessages := a.requestMessages(ctx)
		a.publishContext()
		span := a.trace.Start("llm", a.provider.Name(), map[string]any{"model": a.model, "step": step + 1})
		wroteText := false
		visibleText := false
		separatedActivity := false
		thinking := false
		lifecycle, rendersResponses := a.out.(responseLifecycle)
		thinkingOutput, stylesThinking := a.out.(thinkingLifecycle)
		lineStreamer, keepsWaiting := a.out.(lineStreamingWriter)
		if rendersResponses {
			lifecycle.BeginResponse()
		}
		response, err := a.provider.Complete(ctx, llm.Request{Model: a.model, Messages: requestMessages, Tools: a.enabledSchemas()}, func(event llm.StreamEvent) {
			if ctx.Err() != nil {
				return
			}
			if event.Text == "" {
				return
			}
			// Providers sometimes emit whitespace-only content while preparing a
			// tool call. Do not turn that protocol padding into blank transcript
			// rows before the next activity event.
			if !visibleText && strings.TrimSpace(event.Text) == "" {
				return
			}
			visibleText = true
			willRenderLine := keepsWaiting && (lineStreamer.StreamChunkCompletesLine(event.Text) || (event.Kind == llm.StreamOutput && thinking))
			if willRenderLine || !keepsWaiting && !wroteText {
				task.Suspend()
				span.Suspend()
			}
			if !separatedActivity && (willRenderLine || !keepsWaiting) {
				a.trace.SeparateActivity()
				separatedActivity = true
			}
			if event.Kind == llm.StreamThinking && !thinking {
				if stylesThinking {
					thinkingOutput.BeginThinking()
				}
				thinking = true
			} else if event.Kind == llm.StreamOutput && thinking {
				if stylesThinking {
					thinkingOutput.EndThinking()
				} else {
					fmt.Fprintln(a.out)
				}
				thinking = false
			}
			wroteText = true
			fmt.Fprint(a.out, event.Text)
			if willRenderLine {
				task.Resume()
			}
		})
		if keepsWaiting && wroteText {
			task.Suspend()
			span.Suspend()
		}
		if wroteText && !separatedActivity {
			a.trace.SeparateActivity()
			separatedActivity = true
		}
		if thinking && stylesThinking {
			thinkingOutput.EndThinking()
		}
		if wroteText {
			fmt.Fprintln(a.out)
		}
		if rendersResponses {
			lifecycle.EndResponse()
		}
		a.recordUsage(response.Usage)
		span.End(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		for index := range response.Message.ToolCalls {
			if response.Message.ToolCalls[index].ID == "" {
				response.Message.ToolCalls[index].ID = fmt.Sprintf("call_%d_%d", step+1, index+1)
			}
		}
		a.messages = append(a.messages, response.Message)
		a.contextUsage = response.Usage
		a.contextMessages = len(a.messages)
		a.RefreshContext(ctx)
		if len(response.Message.ToolCalls) == 0 {
			a.stateMu.Lock()
			a.lastResponse = response.Message.Content
			a.stateMu.Unlock()
			return nil
		}
		a.pendingImages = nil
		endTurn := false
		endTurnResponse := ""
		for index, call := range response.Message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return err
			}
			task.Resume()
			fingerprint := toolFingerprint(call)
			identicalToolCalls[fingerprint]++
			if identicalToolCalls[fingerprint] >= maxIdenticalToolCalls {
				return fmt.Errorf("agent stopped after tool %q was requested unchanged %d times; arguments=%s", call.Name, identicalToolCalls[fingerprint], compactJSON(call.Arguments))
			}
			if call.ID == "" {
				call.ID = fmt.Sprintf("call_%d_%d", step+1, index+1)
			}
			arguments := compactJSON(call.Arguments)
			activity := a.trace.StartActivity(toolActivity(call))
			toolSpan := a.trace.Start("tool", call.Name, map[string]any{"arguments": arguments})
			execution, toolErr := a.executeDetailed(ctx, call)
			toolSpan.End(toolErr)
			activity.EndWithOutput(toolErr, execution.Output)
			if err := ctx.Err(); err != nil {
				return err
			}
			if errors.Is(toolErr, context.Canceled) {
				return toolErr
			}
			if toolErr == nil && toolMayChangeWorkspace(call.Name) {
				currentCount := identicalToolCalls[fingerprint]
				identicalToolCalls = map[string]int{fingerprint: currentCount}
			}
			if renderer, ok := a.out.(diffWriter); ok && renderer.DiffEnabled() && execution.Diff != "" {
				task.Suspend()
				renderer.WriteDiff(execution.Diff)
				task.Resume()
			}
			result := execution.Output
			if toolErr != nil {
				if result != "" {
					result += "\n"
				}
				result += "ERROR: " + toolErr.Error()
			}
			a.messages = append(a.messages, llm.Message{Role: "tool", Content: result, Name: call.Name, ToolCallID: call.ID})
			if toolErr == nil && execution.EndTurn {
				endTurn = true
				endTurnResponse = execution.Output
			}
			if len(execution.Images) > 0 {
				a.pendingImages = append(a.pendingImages, execution.Images...)
			}
			a.publishContext()
		}
		if len(a.pendingImages) > 0 {
			a.messages = append(a.messages, llm.Message{
				Role:    "user",
				Content: "Image data loaded by the requested tool calls.",
				Images:  a.pendingImages,
			})
			a.pendingImages = nil
		}
		if endTurn {
			a.stateMu.Lock()
			a.lastResponse = strings.TrimSpace(endTurnResponse)
			a.stateMu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("agent stopped after %d model steps", a.MaxSteps())
}

func toolMayChangeWorkspace(name string) bool {
	return name == "write" || name == "edit" || name == "shell" || name == "request_directory_access"
}

func toolFingerprint(call llm.ToolCall) string {
	var value any
	if json.Unmarshal(call.Arguments, &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			return call.Name + "\x00" + string(canonical)
		}
	}
	return call.Name + "\x00" + compactJSON(call.Arguments)
}

func compactJSON(value json.RawMessage) string {
	var out bytes.Buffer
	if json.Compact(&out, value) == nil {
		return out.String()
	}
	return string(value)
}
