package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

const maxIdenticalToolCalls = 3

type Agent struct {
	provider llm.Provider
	model    string
	tools    *tools.Registry
	trace    *trace.Logger
	out      io.Writer
	maxSteps int
	messages []llm.Message
}

type responseLifecycle interface {
	BeginResponse()
	EndResponse()
}

func New(provider llm.Provider, model string, registry *tools.Registry, logger *trace.Logger, out io.Writer, maxSteps int) *Agent {
	if maxSteps <= 0 {
		maxSteps = 32
	}
	return &Agent{provider: provider, model: model, tools: registry, trace: logger, out: out, maxSteps: maxSteps, messages: []llm.Message{{Role: "system", Content: prompt.System}}}
}

func (a *Agent) SetVerbose(verbose bool) { a.trace.SetVerbose(verbose) }

func (a *Agent) Run(ctx context.Context, userText string) error {
	task := a.trace.BeginTask()
	defer task.End()
	a.messages = append(a.messages, llm.Message{Role: "user", Content: userText})
	lastToolCall := ""
	identicalToolCalls := 0
	for step := 0; step < a.maxSteps; step++ {
		span := a.trace.Start("llm", a.provider.Name(), map[string]any{"model": a.model, "step": step + 1})
		wroteText := false
		lifecycle, rendersResponses := a.out.(responseLifecycle)
		if rendersResponses {
			lifecycle.BeginResponse()
		}
		response, err := a.provider.Complete(ctx, llm.Request{Model: a.model, Messages: a.messages, Tools: a.tools.Schemas()}, func(text string) {
			if text == "" {
				return
			}
			if !wroteText {
				task.Suspend()
				span.Suspend()
			}
			wroteText = true
			fmt.Fprint(a.out, text)
		})
		if wroteText {
			fmt.Fprintln(a.out)
		}
		if rendersResponses {
			lifecycle.EndResponse()
		}
		span.End(err)
		if err != nil {
			return err
		}
		a.messages = append(a.messages, response.Message)
		if len(response.Message.ToolCalls) == 0 {
			return nil
		}
		for index, call := range response.Message.ToolCalls {
			task.Resume()
			fingerprint := toolFingerprint(call)
			if fingerprint == lastToolCall {
				identicalToolCalls++
			} else {
				lastToolCall = fingerprint
				identicalToolCalls = 1
			}
			if identicalToolCalls >= maxIdenticalToolCalls {
				return fmt.Errorf("agent stopped after tool %q was requested unchanged %d times; arguments=%s", call.Name, identicalToolCalls, compactJSON(call.Arguments))
			}
			if call.ID == "" {
				call.ID = fmt.Sprintf("call_%d_%d", step+1, index+1)
			}
			arguments := compactJSON(call.Arguments)
			toolSpan := a.trace.Start("tool", call.Name, map[string]any{"arguments": arguments})
			result, toolErr := a.tools.Execute(ctx, call)
			toolSpan.End(toolErr)
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
