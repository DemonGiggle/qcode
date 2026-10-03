package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

// Synthetic fixture reproducing a file read followed by thinking that quotes it.
const paymentTestText = "Credit card:\n- Number: 4111 1111 1111 1111\n- CVV: 123\n- Thru: 04/29"

type paymentProvider struct {
	requests []llm.Request
	fail     bool
}

func (*paymentProvider) Name() string { return "payment-test" }
func (p *paymentProvider) Complete(_ context.Context, r llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	p.requests = append(p.requests, r)
	if len(p.requests) == 1 {
		return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "read-1", Name: "read", Arguments: json.RawMessage(`{"path":"AGENT.md"}`)}}}}, nil
	}
	for _, b := range []byte(paymentTestText) {
		stream(llm.StreamEvent{Kind: llm.StreamThinking, Text: string(b)})
	}
	if p.fail {
		return llm.Response{}, errors.New("provider failed: CVV: 123, Thru: 04/29, card number: 4111111111111111")
	}
	for _, b := range []byte(paymentTestText) {
		stream(llm.StreamEvent{Text: string(b)})
	}
	replay, _ := json.Marshal([]map[string]any{{"type": "reasoning", "text": paymentTestText}})
	return llm.Response{Message: llm.Message{Role: "assistant", Content: paymentTestText, Thinking: paymentTestText, ReasoningDetails: replay}}, nil
}

func TestReadPaymentFileFiltersThinkingAndSavedCopies(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENT.md"), []byte(paymentTestText), 0600); err != nil {
		t.Fatal(err)
	}
	for _, jsonEvents := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			registry, err := tools.New(root)
			if err != nil {
				t.Fatal(err)
			}
			provider := &paymentProvider{fail: fail}
			var logs bytes.Buffer
			out := &lifecycleWriter{}
			a := New(provider, "model", registry, trace.New(&logs, jsonEvents), out, 3)
			// Exercise the built-in default without exact-value registration.
			err = a.Run(context.Background(), "Read AGENT.md")
			if (err != nil) != fail {
				t.Fatalf("unexpected run failure: %v", err)
			}
			for _, output := range []string{logs.String(), out.String()} {
				for _, sensitive := range []string{"4111 1111 1111 1111", "4111111111111111", "CVV: 123", "04/29"} {
					if strings.Contains(output, sensitive) {
						t.Fatal("payment data leaked through a local output boundary")
					}
				}
			}
			if out.thinkingBegins != 1 || out.thinkingEnds != 1 || strings.Count(out.String(), "[REDACTED]") < 3 {
				t.Fatal("thinking output was not exercised and redacted")
			}
			if len(provider.requests) != 2 {
				t.Fatal("file read did not reach the next provider turn")
			}
			found := false
			for _, message := range provider.requests[1].Messages {
				if message.Role == "tool" {
					var observation struct{ Content string }
					if err := json.Unmarshal([]byte(message.Content), &observation); err != nil {
						t.Fatal(err)
					}
					found = strings.Contains(observation.Content, "4111 1111 1111 1111") && strings.Contains(observation.Content, "CVV: 123") && strings.Contains(observation.Content, "Thru: 04/29")
				}
			}
			if !found {
				t.Fatal("local filtering changed the live tool result")
			}
			if !fail {
				if a.LastResponse() != paymentTestText {
					t.Fatal("live provider response was changed")
				}
				checkpoint, err := FilterSavedState(nil, *a.checkpoint.Load())
				if err != nil {
					t.Fatal(err)
				}
				var state SavedState
				if err := json.Unmarshal(checkpoint, &state); err != nil {
					t.Fatal(err)
				}
				for _, message := range state.Messages {
					for _, text := range []string{message.Content, message.Thinking, string(message.ReasoningDetails)} {
						if strings.Contains(text, "4111") || strings.Contains(text, "123") || strings.Contains(text, "04/29") {
							t.Fatal("payment data survived in a decoded saved message")
						}
					}
				}
			}
			if jsonEvents {
				for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
					if !json.Valid([]byte(line)) {
						t.Fatal("payment filtering broke JSON events")
					}
				}
			}
		}
	}
}
