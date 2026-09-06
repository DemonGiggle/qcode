// Package demo provides a self-contained, deliberately paced qcode session.
// It never contacts a model or executes a real tool.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qcode/internal/llm"
)

const (
	// Model is a display-only model name used by the demo session.
	Model = "scripted-demo"
	// ContextWindow is a fake capacity for the scripted model. Token counts
	// stay real, so this only supplies the status bar's denominator and lets
	// the displayed percentage move as the demo conversation grows.
	ContextWindow  = 8192
	demoModelCount = 240
	defaultDelay   = 1500 * time.Millisecond
)

// Session contains both mocked boundaries needed by the normal agent loop.
type Session struct {
	Provider *Provider
	Tools    *Toolset
}

// New creates a demo session from the production tool schemas. This makes the
// scenario automatically exercise newly registered tools without executing
// their production handlers.
func New(schemas []llm.Tool) *Session {
	return newSession(schemas, defaultDelay)
}

func newSession(schemas []llm.Tool, delay time.Duration) *Session {
	copied := append([]llm.Tool(nil), schemas...)
	return &Session{
		Provider: &Provider{delay: delay},
		Tools:    &Toolset{schemas: copied, delay: delay},
	}
}

// Provider returns one scripted batch containing every advertised tool, then
// a final response after their mocked results have been added to the history.
type Provider struct {
	delay time.Duration
}

var _ llm.ContextWindowProvider = (*Provider)(nil)

func (p *Provider) Name() string { return "demo" }

// ContextWindow reports the scripted model's fake capacity so the status bar
// can divide real token counts by a known limit. It never waits or contacts a
// network, because context discovery runs before the first completion.
func (p *Provider) ContextWindow(context.Context, string) (int, error) {
	return ContextWindow, nil
}

func (p *Provider) Models(ctx context.Context) ([]string, error) {
	if err := wait(ctx, p.delay); err != nil {
		return nil, err
	}
	models := make([]string, 0, demoModelCount+1)
	models = append(models, Model)
	for index := 1; index <= demoModelCount; index++ {
		models = append(models, fmt.Sprintf("demo-coder-%03d", index))
	}
	return models, nil
}

func (p *Provider) Complete(ctx context.Context, request llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	if err := wait(ctx, p.delay); err != nil {
		return llm.Response{}, err
	}
	if toolResultsSinceLastUser(request.Messages) == 0 && len(request.Tools) > 0 {
		if onText != nil {
			onText(llm.StreamEvent{Kind: llm.StreamThinking, Text: "Demo mode: planning a safe, mocked tour of every available tool.\n"})
		}
		calls := make([]llm.ToolCall, 0, len(request.Tools))
		for index, schema := range request.Tools {
			calls = append(calls, llm.ToolCall{
				ID:        fmt.Sprintf("demo_%d_%s", index+1, schema.Name),
				Name:      schema.Name,
				Arguments: demoArguments(schema.Name),
			})
		}
		return llm.Response{Message: llm.Message{
			Role:      "assistant",
			Thinking:  "Demo mode is exercising every available tool with mocked arguments.",
			ToolCalls: calls,
		}}, nil
	}

	message := fmt.Sprintf(`## Demo report

The mocked LLM connection succeeded and all %d available tools ran. Use Page Up and Page Down to revisit the colored output.

| Feature | Demonstration | Safety |
| :--- | :---: | ---: |
| LLM connection | Mocked with delay | Offline |
| Tool calls | All %d schemas | Mocked |
| Cancellation | Every phase | Ctrl+C |
| Code diffs | Write and edit | No files changed |
| Markdown tables | Aligned output | Width bounded |
| Model search | %d fake models | No API call |
| History paging | Colored content | PgUp / PgDn |
| Completion time | Whole seconds | Deterministic |`, len(request.Tools), len(request.Tools), demoModelCount+1)
	if onText != nil {
		onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: message})
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: message}}, nil
}

func toolResultsSinceLastUser(messages []llm.Message) int {
	count := 0
	for index := len(messages) - 1; index >= 0; index-- {
		switch messages[index].Role {
		case "user":
			return count
		case "tool":
			count++
		}
	}
	return count
}

// Toolset exposes real schemas and returns realistic but side-effect-free
// results. The delay is context-aware so Ctrl+C can cancel every tool phase.
type Toolset struct {
	schemas []llm.Tool
	delay   time.Duration
}

func (t *Toolset) Schemas() []llm.Tool {
	return append([]llm.Tool(nil), t.schemas...)
}

// EnabledSchemas returns all schemas in demo mode (tools are never disabled).
func (t *Toolset) EnabledSchemas() []llm.Tool {
	return t.Schemas()
}

func (t *Toolset) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	if err := wait(ctx, t.delay); err != nil {
		return llm.ToolResult{}, err
	}
	if call.Name == "read" {
		return llm.ToolResult{}, fmt.Errorf("mocked read failed: file is unavailable")
	}
	return demoResult(call), nil
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func demoArguments(name string) json.RawMessage {
	values := map[string]string{
		"list_agents":      `{}`,
		"delegate_task":    `{"agent_id":"agent-1","prompt":"Review the mocked demo workspace"}`,
		"get_agent_result": `{"agent_id":"agent-1"}`,
		"web_fetch":        `{"url":"https://example.com"}`,
		"web_search":       `{"query":"Go documentation","max_results":5}`,
		"read":             `{"path":"README.md","offset":1,"limit":20}`,
		"write":            `{"path":"demo/greeter.go","content":"package demo\n\nfunc Greeting(name string) string {\n\treturn \"Hello, \" + name\n}\n"}`,
		"edit":             `{"path":"demo/greeter.go","old_text":"return \"Hello, \" + name","new_text":"return \"Hello, \" + name + \"!\""}`,
		"list":             `{"path":"."}`,
		"search":           `{"pattern":"TODO|FIXME","path":".","max_results":20}`,
		"shell":            `{"command":"go test ./...","timeout_ms":120000}`,
		"view_image":       `{"path":"screenshot.png"}`,
	}
	if value, ok := values[name]; ok {
		return json.RawMessage(value)
	}
	return json.RawMessage(`{}`)
}

func demoResult(call llm.ToolCall) llm.ToolResult {
	switch call.Name {
	case "web_fetch":
		return llm.ToolResult{Output: "Source: https://example.com\n[Untrusted web content]\nExample page (mocked; no network request)."}
	case "web_search":
		return llm.ToolResult{Output: "[Untrusted web search results]\n1. Go documentation\nhttps://go.dev/doc/\nMocked search result; no network request."}
	case "read":
		return llm.ToolResult{Output: "     1\t# qcode demo\n     2\tThis content is generated by the mocked read tool."}
	case "write":
		return llm.ToolResult{
			Output:       "wrote 76 bytes to demo/greeter.go (mocked; workspace unchanged)",
			ChangedFiles: []string{"demo/greeter.go"},
			Diff: strings.Join([]string{
				"--- /dev/null",
				"+++ b/demo/greeter.go",
				"@@ -0,0 +1,5 @@",
				"+package demo",
				"+",
				"+func Greeting(name string) string {",
				`+	return "Hello, " + name`,
				"+}",
			}, "\n"),
		}
	case "edit":
		return llm.ToolResult{
			Output:       "edited demo/greeter.go (mocked; workspace unchanged)",
			ChangedFiles: []string{"demo/greeter.go"},
			Diff: strings.Join([]string{
				"--- a/demo/greeter.go",
				"+++ b/demo/greeter.go",
				"@@ -1,5 +1,5 @@",
				" package demo",
				" ",
				" func Greeting(name string) string {",
				`-	return "Hello, " + name`,
				`+	return "Hello, " + name + "!"`,
				" }",
			}, "\n"),
		}
	case "list":
		return llm.ToolResult{Output: "README.md\ncmd/\ninternal/\n(mocked listing)"}
	case "search":
		return llm.ToolResult{Output: "internal/demo/demo.go:1:// Mocked search result"}
	case "shell":
		return llm.ToolResult{Output: "ok\tqcode/internal/demo\t0.001s\n(mocked command; nothing was executed)"}
	case "view_image":
		return llm.ToolResult{Output: "Loaded screenshot.png (mocked image; no file was read)."}
	default:
		return llm.ToolResult{Output: fmt.Sprintf("%s completed with mocked arguments %s", call.Name, strings.TrimSpace(string(call.Arguments)))}
	}
}
