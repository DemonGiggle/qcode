package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync/atomic"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

const maxIdenticalToolCalls = 3

type Agent struct {
	learningStore     learning.Store
	learningBudget    int
	learningContext   string
	learningSessionID string
	provider          llm.Provider
	model             string
	tools             Toolset
	trace             *trace.Logger
	out               io.Writer
	maxSteps          int
	messages          []llm.Message
	contextStatus     atomic.Pointer[contextStatus]
	contextWindow     int
	contextOverride   int
	contextUsage      *llm.Usage
	contextMessages   int
	system            string
}

// Toolset is the complete tool boundary used by the agent loop. Production and
// demo implementations can provide the same schemas with different execution
// behavior without adding mode-specific branches to the loop.
type Toolset interface {
	Schemas() []llm.Tool
	EnabledSchemas() []llm.Tool
	ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error)
}

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
	return &Agent{provider: provider, model: model, tools: toolset, trace: logger, out: out, maxSteps: maxSteps, system: system, messages: []llm.Message{{Role: "system", Content: system}}}
}

func (a *Agent) SetVerbose(verbose bool) { a.trace.SetVerbose(verbose) }

func (a *Agent) SetUnicode(enabled bool) { a.trace.SetUnicode(enabled) }

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
	defer a.invalidateContextUsage()
	a.system = prompt.SystemWithSkills(skills)
	if len(a.messages) > 0 && a.messages[0].Role == "system" {
		a.messages[0].Content = a.system
	}
}

// ResetSession discards conversation history while retaining the agent's
// provider, model, tools, and runtime settings.
func (a *Agent) ResetSession() {
	a.learningContext = ""
	a.learningSessionID = ""
	defer a.invalidateContextUsage()
	a.messages = []llm.Message{{Role: "system", Content: a.system}}
	if resetter, ok := a.tools.(interface{ ResetSession() }); ok {
		resetter.ResetSession()
	}
}

// ToolNames returns the names of all registered tools.
func (a *Agent) ToolNames() []string {
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
	defer a.invalidateContextUsage()
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
	defer a.publishContext()
	task := a.trace.BeginTask()
	defer task.End()
	a.messages = append(a.messages, llm.Message{Role: "user", Content: userText})
	identicalToolCalls := map[string]int{}
	for step := 0; step < a.maxSteps; step++ {
		requestMessages := a.requestMessages(ctx)
		span := a.trace.Start("llm", a.provider.Name(), map[string]any{"model": a.model, "step": step + 1})
		wroteText := false
		thinking := false
		lifecycle, rendersResponses := a.out.(responseLifecycle)
		thinkingOutput, stylesThinking := a.out.(thinkingLifecycle)
		lineStreamer, keepsWaiting := a.out.(lineStreamingWriter)
		if rendersResponses {
			lifecycle.BeginResponse()
		}
		response, err := a.provider.Complete(ctx, llm.Request{Model: a.model, Messages: requestMessages, Tools: a.tools.EnabledSchemas()}, func(event llm.StreamEvent) {
			if ctx.Err() != nil {
				return
			}
			if event.Text == "" {
				return
			}
			willRenderLine := keepsWaiting && (lineStreamer.StreamChunkCompletesLine(event.Text) || (event.Kind == llm.StreamOutput && thinking))
			if willRenderLine || !keepsWaiting && !wroteText {
				task.Suspend()
				span.Suspend()
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
		if thinking && stylesThinking {
			thinkingOutput.EndThinking()
		}
		if wroteText {
			fmt.Fprintln(a.out)
		}
		if rendersResponses {
			lifecycle.EndResponse()
		}
		span.End(err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		a.messages = append(a.messages, response.Message)
		a.contextUsage = response.Usage
		a.contextMessages = len(a.messages)
		a.RefreshContext(ctx)
		if len(response.Message.ToolCalls) == 0 {
			return nil
		}
		var loadedImages []llm.Image
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
			if skillName, ok := referencedSkill(call); ok {
				task.Suspend()
				fmt.Fprintf(a.out, "Using skill: %s\n", skillName)
				task.Resume()
			}
			arguments := compactJSON(call.Arguments)
			toolSpan := a.trace.Start("tool", call.Name, map[string]any{"arguments": arguments})
			execution, toolErr := a.tools.ExecuteDetailed(ctx, call)
			toolSpan.End(toolErr)
			if err := ctx.Err(); err != nil {
				return err
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
			if len(execution.Images) > 0 {
				loadedImages = append(loadedImages, execution.Images...)
			}
		}
		if len(loadedImages) > 0 {
			a.messages = append(a.messages, llm.Message{
				Role:    "user",
				Content: "Image data loaded by the requested tool calls.",
				Images:  loadedImages,
			})
		}
	}
	return fmt.Errorf("agent stopped after %d model steps", a.maxSteps)
}

// referencedSkill extracts a display-safe name from a valid skill-tool call.
// Tool execution remains responsible for validating the name against the
// discovered catalog.
func referencedSkill(call llm.ToolCall) (string, bool) {
	if call.Name != "skill" {
		return "", false
	}
	var arguments struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(call.Arguments, &arguments) != nil || arguments.Name == "" {
		return "", false
	}
	return arguments.Name, true
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
