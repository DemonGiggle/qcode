package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"qcode/internal/chatgptoauth"
	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/trace"
	"qcode/internal/trust"
)

type chatGPTWireRequest struct {
	Input        []json.RawMessage `json:"input"`
	Instructions json.RawMessage   `json:"instructions"`
	Tools        json.RawMessage   `json:"tools"`
	Key          string            `json:"prompt_cache_key"`
}

type chatGPTTestTransport struct {
	requests []chatGPTWireRequest
}

func (p *chatGPTTestTransport) Do(r *http.Request) (*http.Response, error) {
	var req chatGPTWireRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	p.requests = append(p.requests, req)
	output := `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]`
	if len(p.requests) == 1 {
		output = `[{"type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"c1","name":"skill","arguments":"{}"}]`
	}
	body := `data: {"type":"response.completed","response":{"output":` + output + "}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestChatGPTWirePrefixAcrossToolAndUserTurns(t *testing.T) {
	tokens := filepath.Join(t.TempDir(), "oauth.json")
	if err := chatgptoauth.Save(tokens, chatgptoauth.Tokens{ClientID: "oaiapp_test", ExtAgentHostID: "urn:uuid:test", AccessToken: "test", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	transport := &chatGPTTestTransport{}
	p, err := llm.New("chatgpt", llm.Config{InstanceName: "subscription", HTTP: transport, ChatGPTOAuthPath: tokens})
	if err != nil {
		t.Fatal(err)
	}
	toolset := &context79Toolset{cwd: "/project", work: "work-1"}
	a := NewWithSystem(p, "gpt-6.2-sol", toolset, trace.New(io.Discard, false), io.Discard, 4, "durable instructions")
	store := &counting79Store{items: []learning.Learning{{Topic: "first", Content: "learning-one"}}}
	a.SetLearning(store, 1200)
	a.SetInteractiveMode(true)
	a.SetInteractiveAvailable(true)
	a.SetRequestContext(func() string {
		if len(transport.requests) == 0 {
			return "roster-1"
		}
		return "roster-2"
	})
	if err := a.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	store.items = []learning.Learning{{Topic: "second", Content: "learning-two"}}
	if err := a.Run(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	toolset.work = ""
	store.items = nil
	if err := a.Run(context.Background(), "third"); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("got %d requests", len(transport.requests))
	}
	for i, r := range transport.requests {
		first := transport.requests[0]
		if r.Key == "" || r.Key != first.Key || !bytes.Equal(r.Tools, first.Tools) {
			t.Fatalf("request %d changed the cache key or tools", i)
		}
		var instructions string
		if err := json.Unmarshal(r.Instructions, &instructions); err != nil {
			t.Fatalf("request %d instructions: %v", i, err)
		}
		if !strings.Contains(instructions, "Interactive questions remaining for this request: 3.") {
			t.Fatalf("request %d lost the question budget: %q", i, instructions)
		}
		if strings.Contains(instructions, "learning-one") != (i < 2) ||
			strings.Contains(instructions, "learning-two") != (i == 2) ||
			strings.Contains(instructions, `"origin":"user_approved_learning","trust":"untrusted"`) != (i < 3) {
			t.Fatalf("request %d included the wrong learning selection: %q", i, instructions)
		}
		if i > 0 {
			prev := transport.requests[i-1].Input
			if len(r.Input) <= len(prev) || !reflect.DeepEqual(r.Input[:len(prev)], prev) {
				t.Fatalf("request %d changed an earlier wire item: %s", i, r.Input)
			}
		}
	}
	first := transport.requests[0].Input
	if len(first) != 3 || !bytes.Contains(first[0], []byte(`"role":"developer"`)) || !bytes.Contains(first[0], []byte("Interactive questions are enabled.")) || !bytes.Contains(first[2], []byte(`"role":"user"`)) {
		t.Fatalf("unexpected initial prefix: %s", first)
	}
	var contextMessage struct {
		Role    string
		Content []struct{ Text string }
	}
	if err := json.Unmarshal(first[1], &contextMessage); err != nil {
		t.Fatal(err)
	}
	if contextMessage.Role != "developer" || len(contextMessage.Content) != 1 {
		t.Fatalf("unexpected context message: %s", first[1])
	}
	for _, want := range []string{"/project", trust.Wrap("request_context", "roster-1"), trust.Wrap("work_history", "work-1")} {
		if !strings.Contains(contextMessage.Content[0].Text, want) {
			t.Fatalf("missing %q in context message", want)
		}
	}
	toolTurn := transport.requests[1].Input[len(first):]
	if len(toolTurn) != 4 || !bytes.Contains(toolTurn[0], []byte("opaque")) || !bytes.Contains(toolTurn[1], []byte(`"type":"function_call"`)) || !bytes.Contains(toolTurn[2], []byte(`"type":"function_call_output"`)) || !bytes.Contains(toolTurn[3], []byte("roster-2")) {
		t.Fatalf("unexpected tool continuation: %s", toolTurn)
	}
	last := transport.requests[3].Input
	all, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"work-1", "/project", "roster-1", "roster-2", "no current work history excerpts"} {
		if bytes.Count(all, []byte(want)) != 1 {
			t.Fatalf("expected %q exactly once in %s", want, all)
		}
	}
	for _, marker := range []string{"learning-one", "learning-two", "Interactive questions remaining"} {
		if bytes.Contains(all, []byte(marker)) {
			t.Fatalf("request-only text entered conversation history: %s", marker)
		}
	}
}

func TestChatGPTContextRebuiltWhenHistoryChanges(t *testing.T) {
	for _, action := range []string{"reset", "restore", "compact"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			p := &captureAgentProvider{name: "chatgpt"}
			a := newCacheTestAgent(p)
			value := "original roster"
			a.SetRequestContext(func() string { return value })
			for _, question := range []string{"one", "two", "three"} {
				if err := a.Run(ctx, question); err != nil {
					t.Fatal(err)
				}
			}
			if len(a.chatGPTContext.updates) == 0 {
				t.Fatal("missing accepted context")
			}
			switch action {
			case "reset":
				a.ResetSession()
			case "restore":
				if err := a.RestoreState(*a.checkpoint.Load()); err != nil {
					t.Fatal(err)
				}
			case "compact":
				// Eight durable messages before and after compaction: a
				// summary now replaces the first user message.
				a.messages = append(a.messages, llm.Message{Role: "user", Content: "four"})
				a.SetContextWindow(1)
				before := len(a.messages)
				prep := a.prepareNormalRequest(ctx, "")
				if !a.maybeAutoCompactForPrepared(ctx, prep.Estimate) || len(a.messages) != before {
					t.Fatal("equal-length compaction did not require a new preparation")
				}
				a.SetAutoCompact(false, 80)
			}
			value = "fresh roster"
			if err := a.Run(ctx, "continue"); err != nil {
				t.Fatal(err)
			}
			msgs := p.requests[len(p.requests)-1].Messages
			if msgs[len(msgs)-2].Role != "developer" || !strings.Contains(msgs[len(msgs)-2].Content, value) {
				t.Fatal("fresh context missing from rebuilt history")
			}
			for _, m := range msgs {
				if strings.Contains(m.Content, "original roster") {
					t.Fatal("stale context survived history replacement")
				}
			}
		})
	}
}
