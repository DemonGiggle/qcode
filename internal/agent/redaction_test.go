package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/redaction"
	"qcode/internal/trace"
)

const redactionTestSecret = "synthetic-runtime-credential"

func TestFilterSavedAgentDecodedArgumentsAndReplay(t *testing.T) {
	secret := redactionTestSecret
	p, _ := redaction.New(redaction.Config{}, []string{secret})
	image := llm.Image{MediaType: "image/png", Data: []byte(secret)}
	arguments := []byte(`{"command":"echo ` + secret + `","password":"short"}`)
	state := SavedState{Provider: "test", Model: "model", ProviderSession: "session-id", PendingImages: []llm.Image{image}, LastResponse: secret, System: secret, LearningContext: secret,
		Messages: []SavedMessage{{Message: llm.Message{Role: "assistant", Content: secret, Thinking: secret, ReasoningDetails: json.RawMessage(`[{"type":"message","id":"replay-id","content":[{"type":"output_text","text":"` + secret + `"}]},{"type":"function_call","call_id":"call-id","name":"shell","arguments":"{\"password\":\"short\"}"},{"type":"reasoning","encrypted_content":"` + secret + `","signature":"` + secret + `"}]`)}, Images: []llm.Image{image}, ToolCalls: []SavedToolCall{{ID: "call-id", Name: "shell", Arguments: arguments}}}},
		Tools:    json.RawMessage(`{"grants":["/operational/` + secret + `"]}`), LatestPlan: &Plan{Summary: secret, Steps: []string{secret}}, LatestSkillDraft: &SkillDraft{Name: "skill", Location: "local", Content: secret}}
	data, _ := json.Marshal(state)
	before := append([]byte(nil), data...)
	filtered, err := FilterSavedState(p, data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("changed original checkpoint")
	}
	var got SavedState
	if json.Unmarshal(filtered, &got) != nil {
		t.Fatal("invalid checkpoint")
	}
	if got.LastResponse != redaction.Marker || got.Messages[0].Content != redaction.Marker || got.Messages[0].Thinking != redaction.Marker || got.LatestPlan.Summary != redaction.Marker || got.LatestSkillDraft.Content != redaction.Marker {
		t.Fatal("text leaked")
	}
	if strings.Contains(string(got.Messages[0].ToolCalls[0].Arguments), secret) || !json.Valid(got.Messages[0].ToolCalls[0].Arguments) {
		t.Fatal("byte-backed arguments leaked or broke")
	}
	if !reflect.DeepEqual(got.PendingImages, state.PendingImages) || !reflect.DeepEqual(got.Messages[0].Images, state.Messages[0].Images) || !bytes.Equal(got.Tools, state.Tools) {
		t.Fatal("operational grants or images changed")
	}
	var replay []map[string]any
	json.Unmarshal(got.Messages[0].ReasoningDetails, &replay)
	if replay[2]["encrypted_content"] != secret || replay[2]["signature"] != secret || replay[0]["id"] != "replay-id" || strings.Contains(replay[1]["arguments"].(string), "short") {
		t.Fatal(replay)
	}
	content := replay[0]["content"].([]any)[0].(map[string]any)
	if content["text"] != redaction.Marker {
		t.Fatal("duplicated replay text leaked")
	}
	twice, err := FilterSavedState(p, filtered)
	if err != nil || !bytes.Equal(filtered, twice) {
		t.Fatal("snapshot filter not idempotent")
	}
}

type redactionProvider struct {
	requests []llm.Request
	fail     bool
}

func (*redactionProvider) Name() string { return "redaction-test" }
func (p *redactionProvider) Complete(_ context.Context, r llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, r)
	if p.fail {
		for _, b := range []byte(redactionTestSecret) {
			stream(llm.StreamEvent{Text: string(b)})
		}
		return llm.Response{}, errors.New("provider failed: " + redactionTestSecret)
	}
	if len(p.requests) == 1 {
		for _, b := range []byte("reasoning " + redactionTestSecret) {
			stream(llm.StreamEvent{Kind: llm.StreamThinking, Text: string(b)})
		}
		for _, b := range []byte("answer " + redactionTestSecret + "\r\n") {
			stream(llm.StreamEvent{Text: string(b)})
		}
		return llm.Response{Message: llm.Message{Role: "assistant", Content: redactionTestSecret, Thinking: redactionTestSecret, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo ` + redactionTestSecret + `"}`)}}}}, nil
	}
	stream(llm.StreamEvent{Text: redactionTestSecret})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: redactionTestSecret}}, nil
}

type redactionToolset struct {
	managerToolset
	executed llm.ToolCall
}

func (t *redactionToolset) ExecuteDetailed(_ context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	t.executed = call
	return llm.ToolResult{Output: redactionTestSecret, Diff: "--- a/file\n+++ b/file\n+" + redactionTestSecret + "\n"}, nil
}
func (t *redactionToolset) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	r, e := t.ExecuteDetailed(ctx, call)
	return r.Output, e
}

func TestLocalSinksLeaveProviderAndToolsRaw(t *testing.T) {
	policy, _ := redaction.New(redaction.Config{}, []string{redactionTestSecret})
	for _, jsonEvents := range []bool{false, true} {
		provider := &redactionProvider{}
		toolset := &redactionToolset{}
		var logs bytes.Buffer
		out := &diffLifecycleWriter{}
		logger := trace.New(&logs, jsonEvents)
		a := New(provider, "model", toolset, logger, out, 3)
		a.SetRedaction(policy)
		if err := a.Run(context.Background(), "prompt "+redactionTestSecret); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(logs.String(), redactionTestSecret) || strings.Contains(out.String(), redactionTestSecret) || strings.Contains(strings.Join(out.diffs, "\n"), redactionTestSecret) {
			t.Fatalf("local sink leaked: %q %q", logs.String(), out.String())
		}
		if len(provider.requests) != 2 || !strings.Contains(provider.requests[0].Messages[1].Content, redactionTestSecret) || !strings.Contains(string(toolset.executed.Arguments), redactionTestSecret) || a.LastResponse() != redactionTestSecret {
			t.Fatal("live runtime was redacted")
		}
		found := false
		for _, m := range provider.requests[1].Messages {
			if m.Role == "tool" && strings.Contains(m.Content, redactionTestSecret) {
				found = true
			}
		}
		if !found {
			t.Fatal("tool result changed before provider request")
		}
		if jsonEvents {
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatal("invalid JSON event")
				}
			}
		}
	}
}
func TestPartialStreamFlushOnProviderFailure(t *testing.T) {
	policy, _ := redaction.New(redaction.Config{}, []string{redactionTestSecret})
	var output, logs bytes.Buffer
	a := New(&redactionProvider{fail: true}, "model", &managerToolset{}, trace.New(&logs, false), &output, 2)
	a.SetRedaction(policy)
	if err := a.Run(context.Background(), "prompt"); err == nil {
		t.Fatal("missing failure")
	}
	if output.String() != redaction.Marker+"\n" || strings.Contains(logs.String(), redactionTestSecret) {
		t.Fatal("error stream leaked")
	}
	// Disabling a local sink must never stop the learning detector.
	disabled := false
	p, _ := redaction.New(redaction.Config{Persistence: &disabled}, nil)
	if !p.ContainsSecret("password=short") {
		t.Fatal("learning detection disabled")
	}
}
