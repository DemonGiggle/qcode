package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/tools"
	"qcode/internal/trace"
	"qcode/internal/tui"
)

type fakeProvider struct{ calls int }

type lifecycleWriter struct {
	bytes.Buffer
	begins         int
	ends           int
	thinkingBegins int
	thinkingEnds   int
}

type diffLifecycleWriter struct {
	lifecycleWriter
	diffs []string
}

func (w *diffLifecycleWriter) DiffEnabled() bool { return true }
func (w *diffLifecycleWriter) WriteDiff(diff string) {
	w.diffs = append(w.diffs, diff)
}

func (w *lifecycleWriter) BeginResponse() { w.begins++ }
func (w *lifecycleWriter) EndResponse()   { w.ends++ }
func (w *lifecycleWriter) BeginThinking() { w.thinkingBegins++ }
func (w *lifecycleWriter) EndThinking() {
	w.thinkingEnds++
	w.WriteByte('\n')
}

type responseProvider struct{}

type thinkingProvider struct{}

type repeatingProvider struct{ calls int }

type scriptedProvider struct {
	calls    int
	sequence []llm.ToolCall
}

type observingStreamProvider struct {
	terminal     *bytes.Buffer
	afterToken   string
	afterNewline string
}

type cancelBeforeToolProvider struct {
	calls  int
	cancel context.CancelFunc
}

type cancelWithFinalResponseProvider struct {
	cancel context.CancelFunc
}

type modelProvider struct {
	requestedModel string
}

type imageProvider struct {
	requests []llm.Request
}

type skillProvider struct{ calls int }

type whitespaceToolProvider struct{ calls int }

type skillToolset struct{}

func (*skillToolset) Schemas() []llm.Tool {
	return []llm.Tool{{Name: "skill"}}
}

func (*skillToolset) EnabledSchemas() []llm.Tool {
	return []llm.Tool{{Name: "skill"}}
}

func (*skillToolset) ExecuteDetailed(_ context.Context, _ llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{Output: "skill instructions"}, nil
}

func (p *responseProvider) Name() string { return "response" }
func (p *responseProvider) Complete(_ context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "finished"})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func (p *thinkingProvider) Name() string { return "thinking" }
func (p *thinkingProvider) Complete(_ context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	onText(llm.StreamEvent{Kind: llm.StreamThinking, Text: "considering"})
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "finished"})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func (p *repeatingProvider) Name() string { return "repeating" }
func (p *repeatingProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "repeat", Name: "read", Arguments: json.RawMessage(`{"path":"same.txt"}`),
	}}}}, nil
}

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls > len(p.sequence) {
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
	}
	call := p.sequence[p.calls-1]
	call.ID = fmt.Sprintf("call-%d", p.calls)
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}}, nil
}

func (p *observingStreamProvider) Name() string { return "observing" }
func (p *observingStreamProvider) Complete(_ context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	onText(llm.StreamEvent{Kind: llm.StreamThinking, Text: "thought"})
	p.afterToken = p.terminal.String()
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "answer\nnext"})
	p.afterNewline = p.terminal.String()
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "answer\nnext", Thinking: "thought"}}, nil
}

func (p *fakeProvider) Name() string { return "fake" }
func (p *fakeProvider) Complete(_ context.Context, request llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{"path":"result.txt","content":"done"}`)}}}}, nil
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != "tool" {
		panic("missing tool result")
	}
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "finished"})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "finished"}}, nil
}

func (p *cancelBeforeToolProvider) Name() string { return "cancel-before-tool" }
func (p *cancelBeforeToolProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls > 1 {
		panic("agent requested another model turn after cancellation")
	}
	p.cancel()
	return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "cancelled", Name: "write", Arguments: json.RawMessage(`{"path":"result.txt","content":"nope"}`),
	}}}}, nil
}

func (p *cancelWithFinalResponseProvider) Name() string { return "cancel-with-final-response" }
func (p *cancelWithFinalResponseProvider) Complete(_ context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	p.cancel()
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "stale output"})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "stale output"}}, nil
}

func (p *modelProvider) Name() string { return "models" }
func (p *modelProvider) Models(context.Context) ([]string, error) {
	return []string{"zeta", "alpha"}, nil
}
func (p *modelProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.requestedModel = request.Model
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func (*imageProvider) Name() string { return "image" }
func (p *imageProvider) Complete(_ context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	request.Messages = append([]llm.Message(nil), request.Messages...)
	p.requests = append(p.requests, request)
	if len(p.requests) == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
			{ID: "image-1", Name: "view_image", Arguments: json.RawMessage(`{"path":"screen.png"}`)},
			{ID: "list-1", Name: "list", Arguments: json.RawMessage(`{"path":"."}`)},
		}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "seen"}}, nil
}

func (p *skillProvider) Name() string { return "skill" }
func (p *skillProvider) Complete(_ context.Context, _ llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			Name: "skill", Arguments: json.RawMessage(`{"name":"review"}`),
		}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func (p *whitespaceToolProvider) Name() string { return "whitespace-tool" }
func (p *whitespaceToolProvider) Complete(_ context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: "\n\n"})
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
			Name: "skill", Arguments: json.RawMessage(`{"name":"review"}`),
		}}}}, nil
	}
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "done"}}, nil
}

func TestAgentEmitsActivityWhenSkillIsLoaded(t *testing.T) {
	provider := &skillProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", &skillToolset{}, trace.New(&events, false), &output, 2)
	if err := runner.Run(context.Background(), "review this"); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "" {
		t.Fatalf("assistant output = %q", got)
	}
	if !strings.Contains(events.String(), "Loaded skill review") {
		t.Fatalf("activity = %q", events.String())
	}
	if !strings.Contains(events.String(), "skill instructions") {
		t.Fatalf("tool output preview = %q", events.String())
	}
}

func TestAgentDropsWhitespaceOnlyProviderPaddingBeforeToolActivity(t *testing.T) {
	provider := &whitespaceToolProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", &skillToolset{}, trace.New(&events, false), &output, 2)
	if err := runner.Run(context.Background(), "review this"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(events.String(), "\n\n") {
		t.Fatalf("provider padding created a blank activity row: %q", events.String())
	}
}

func TestAgentMarksResponseBoundaries(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &responseProvider{}
	var output lifecycleWriter
	var events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 1)
	if err := runner.Run(context.Background(), "respond"); err != nil {
		t.Fatal(err)
	}
	if output.begins != 1 || output.ends != 1 {
		t.Fatalf("boundaries = %d/%d", output.begins, output.ends)
	}
	if output.String() != "finished\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAgentResetSessionDiscardsConversationHistory(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &responseProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 1)
	if err := runner.Run(context.Background(), "remember this"); err != nil {
		t.Fatal(err)
	}
	if len(runner.messages) != 3 {
		t.Fatalf("message count before reset = %d, want 3", len(runner.messages))
	}

	runner.ToggleTool("web_fetch", true)
	runner.ToggleTool("web_search", true)
	runner.ResetSession()
	for _, name := range []string{"web_fetch", "web_search"} {
		if runner.ToolEnabled(name) {
			t.Fatalf("%s remained enabled after a new session", name)
		}
	}

	if len(runner.messages) != 1 || runner.messages[0].Role != "system" || runner.messages[0].Content != prompt.System {
		t.Fatalf("messages after reset = %#v, want only the system prompt", runner.messages)
	}
}

func TestAgentAddsLoadedImageAsUserInput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "screen.png"), []byte("\x89PNG\r\n\x1a\nimage-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	provider := &imageProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "vision", registry, trace.New(&events, false), &output, 2)

	if err := runner.Run(context.Background(), "describe screen.png"); err != nil {
		t.Fatal(err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	messages := provider.requests[1].Messages
	if len(messages) != 6 || messages[3].Role != "tool" || messages[4].Role != "tool" || messages[5].Role != "user" || len(messages[5].Images) != 1 {
		t.Fatalf("second request messages = %#v", messages)
	}
	if messages[5].Images[0].MediaType != "image/png" {
		t.Fatalf("image = %#v", messages[5].Images[0])
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "Viewed screen.png") || strings.Contains(message.Content, "Listed .") {
			t.Fatalf("activity leaked into model context: %#v", messages)
		}
	}
}

func TestAgentListsAndChangesProviderModel(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &modelProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "old", registry, trace.New(&events, false), &output, 1)
	models, err := runner.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "alpha,zeta" {
		t.Fatalf("models = %v", models)
	}
	runner.SetModel("alpha")
	if err := runner.Run(context.Background(), "use it"); err != nil {
		t.Fatal(err)
	}
	if provider.requestedModel != "alpha" {
		t.Fatalf("requested model = %q", provider.requestedModel)
	}
}

func TestAgentMarksThinkingBoundaries(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var output lifecycleWriter
	var events bytes.Buffer
	runner := New(&thinkingProvider{}, "test", registry, trace.New(&events, false), &output, 1)
	if err := runner.Run(context.Background(), "respond"); err != nil {
		t.Fatal(err)
	}
	if output.thinkingBegins != 1 || output.thinkingEnds != 1 {
		t.Fatalf("thinking boundaries = %d/%d", output.thinkingBegins, output.thinkingEnds)
	}
	if output.String() != "considering\nfinished\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestAgentKeepsWaitingUntilBufferedOutputIsVisible(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var terminal bytes.Buffer
	provider := &observingStreamProvider{terminal: &terminal}
	logger := trace.NewAnimated(&terminal, false)
	logger.SetVerbose(false)
	output := tui.NewMarkdownWriter(&terminal, false, 80)
	runner := New(provider, "test", registry, logger, output, 1)
	if err := runner.Run(context.Background(), "respond"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(provider.afterToken, "Waiting (") || strings.Contains(provider.afterToken, "thought") {
		t.Fatalf("after partial token = %q", provider.afterToken)
	}
	first := strings.LastIndex(provider.afterNewline, "answer\n")
	waiting := strings.LastIndex(provider.afterNewline, "Waiting (")
	if first < 0 || waiting < first {
		t.Fatalf("waiting was not repainted below streamed line: %q", provider.afterNewline)
	}
	if got := terminal.String(); !strings.Contains(got, "thought\n") || !strings.Contains(got, "answer\n") || !strings.Contains(got, "next\n") {
		t.Fatalf("final output = %q", got)
	}
}

func TestAgentRunsToolsUntilFinalResponse(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 4)
	if err := runner.Run(context.Background(), "create it"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if output.String() != "finished\n" {
		t.Fatalf("output = %q", output.String())
	}
	for _, expected := range []string{"start llm fake", "start tool write"} {
		if !bytes.Contains(events.Bytes(), []byte(expected)) {
			t.Errorf("events missing %q:\n%s", expected, events.String())
		}
	}
	if bytes.Contains(events.Bytes(), []byte("end llm")) || bytes.Contains(events.Bytes(), []byte("end tool")) {
		t.Errorf("events contain separate end entries:\n%s", events.String())
	}
}

func TestAgentStopsAfterCancelledTool(t *testing.T) {
	root := t.TempDir()
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelBeforeToolProvider{cancel: cancel}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 4)
	err = runner.Run(ctx, "cancel it")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context cancellation", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	if _, statErr := os.Stat(filepath.Join(root, "result.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled tool created a file: %v", statErr)
	}
}

func TestAgentTreatsCancelledProviderResponseAsCancellation(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	provider := &cancelWithFinalResponseProvider{cancel: cancel}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 1)
	err = runner.Run(ctx, "cancel it")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context cancellation", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output after cancellation = %q", output.String())
	}
}

func TestAgentRendersInteractiveFileDiff(t *testing.T) {
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{}
	var output diffLifecycleWriter
	var events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 4)
	if err := runner.Run(context.Background(), "create it"); err != nil {
		t.Fatal(err)
	}
	if len(output.diffs) != 1 || !strings.Contains(output.diffs[0], "+done") {
		t.Fatalf("diffs = %#v", output.diffs)
	}
	if output.String() != "finished\n" {
		t.Fatalf("response output = %q", output.String())
	}
}

func TestAgentStopsRepeatedIdenticalToolCallsEarly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	provider := &repeatingProvider{}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 32)
	err = runner.Run(context.Background(), "repeat forever")
	if err == nil || !strings.Contains(err.Error(), `tool "read" was requested unchanged 3 times`) {
		t.Fatalf("error = %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if bytes.Count(events.Bytes(), []byte("start tool read")) != 2 {
		t.Fatalf("tool events:\n%s", events.String())
	}
}

func TestAgentDetectsIdenticalCallsAcrossReadOnlyInterleaving(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	read := llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"same.txt"}`)}
	list := llm.ToolCall{Name: "list", Arguments: json.RawMessage(`{"path":"."}`)}
	provider := &scriptedProvider{sequence: []llm.ToolCall{read, list, read, list, read}}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 16)
	err = runner.Run(context.Background(), "loop")
	if err == nil || !strings.Contains(err.Error(), `tool "read" was requested unchanged 3 times`) {
		t.Fatalf("error = %v", err)
	}
	if bytes.Count(events.Bytes(), []byte("start tool read")) != 2 || bytes.Count(events.Bytes(), []byte("start tool list")) != 2 {
		t.Fatalf("tool events:\n%s", events.String())
	}
}

func TestAgentAllowsRepeatedReadAfterWorkspaceChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.New(root)
	if err != nil {
		t.Fatal(err)
	}
	read := llm.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"same.txt"}`)}
	write := llm.ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"same.txt","content":"changed"}`)}
	provider := &scriptedProvider{sequence: []llm.ToolCall{read, read, write, read, read}}
	var output, events bytes.Buffer
	runner := New(provider, "test", registry, trace.New(&events, false), &output, 16)
	if err := runner.Run(context.Background(), "inspect, change, inspect"); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 6 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
}
