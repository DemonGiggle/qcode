package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/trace"
)

const maxIdenticalToolCalls = 3

type Agent struct {
	provider llm.Provider
	model    string
	tools    Toolset
	trace    *trace.Logger
	out      io.Writer
	maxSteps int
	messages []llm.Message
}

// Toolset is the complete tool boundary used by the agent loop. Production and
// demo implementations can provide the same schemas with different execution
// behavior without adding mode-specific branches to the loop.
type Toolset interface {
	Schemas() []llm.Tool
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
	if maxSteps <= 0 {
		maxSteps = 32
	}
	return &Agent{provider: provider, model: model, tools: toolset, trace: logger, out: out, maxSteps: maxSteps, messages: []llm.Message{{Role: "system", Content: prompt.System}}}
}

func (a *Agent) SetVerbose(verbose bool) { a.trace.SetVerbose(verbose) }

func (a *Agent) SetUnicode(enabled bool) { a.trace.SetUnicode(enabled) }

func (a *Agent) Run(ctx context.Context, userText string) error {
	task := a.trace.BeginTask()
	defer task.End()
	a.messages = append(a.messages, llm.Message{Role: "user", Content: userText})
	identicalToolCalls := map[string]int{}
	for step := 0; step < a.maxSteps; step++ {
		span := a.trace.Start("llm", a.provider.Name(), map[string]any{"model": a.model, "step": step + 1})
		wroteText := false
		thinking := false
		lifecycle, rendersResponses := a.out.(responseLifecycle)
		thinkingOutput, stylesThinking := a.out.(thinkingLifecycle)
		lineStreamer, keepsWaiting := a.out.(lineStreamingWriter)
		if rendersResponses {
			lifecycle.BeginResponse()
		}
		response, err := a.provider.Complete(ctx, llm.Request{Model: a.model, Messages: a.messages, Tools: a.tools.Schemas()}, func(event llm.StreamEvent) {
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
		if len(response.Message.ToolCalls) == 0 {
			return nil
		}
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
		}
	}
	return fmt.Errorf("agent stopped after %d model steps", a.maxSteps)
}

func toolMayChangeWorkspace(name string) bool {
	return name == "write" || name == "edit" || name == "shell"
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
