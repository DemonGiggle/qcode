package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

const learningProposal = `{"changes":[{"kind":"add","id":"","topic":"Go testing","content":"For Go packages, run focused tests before the full suite.","tags":["go"]}]}`
const learningSource = "0123456789abcdef0123456789abcdef"

type learningProvider struct {
	requests []llm.Request
	response string
}

func (p *learningProvider) Name() string { return "learning-test" }
func (p *learningProvider) Complete(ctx context.Context, req llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	p.requests = append(p.requests, req)
	return llm.Response{Message: llm.Message{Role: "assistant", Content: p.response}}, nil
}
func newLearningAgent(t *testing.T) (*Agent, *learning.FileStore, *learningProvider) {
	t.Helper()
	registry, err := tools.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := &learningProvider{response: learningProposal}
	a := New(p, "test", registry, trace.New(io.Discard, false), io.Discard, 3)
	store := learning.New(filepath.Join(t.TempDir(), "learning"), nil)
	a.SetLearning(store, 1200)
	return a, store, p
}
func seedLearning(t *testing.T, store *learning.FileStore) {
	t.Helper()
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := learning.NewPlan(snapshot, []learning.Draft{{Kind: "add", Topic: "Go testing", Content: "For Go packages, run focused tests before the full suite.", Tags: []string{"go"}}}, learningSource, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
}
func TestLearningApprovalAndExtractionIsolation(t *testing.T) {
	for _, approved := range []bool{false, true} {
		a, store, p := newLearningAgent(t)
		a.messages = append(a.messages, llm.Message{Role: "user", Content: "Always run Go tests. api_key = abcdef1234567890"}, llm.Message{Role: "assistant", Content: "I ran focused Go tests.", Thinking: "private reasoning"}, llm.Message{Role: "tool", Content: "private raw output", ToolCallID: "x"})
		historyLen := len(a.messages)
		reviews := 0
		result, err := a.Learn(context.Background(), "", func(ctx context.Context, review []learning.Change) (bool, error) {
			reviews++
			if len(review) != 1 || review[0].Before != nil || review[0].After == nil || review[0].After.Topic != "Go testing" {
				t.Fatal(review)
			}
			snapshot, err := store.Snapshot(ctx)
			if err != nil || len(snapshot.Items) != 0 {
				t.Fatal("wrote before review")
			}
			return approved, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if reviews != 1 || len(p.requests) != 1 || len(a.messages) != historyLen {
			t.Fatal("unexpected learning side effects")
		}
		req := p.requests[0]
		if len(req.Tools) != 0 || req.Messages[0].Content != prompt.LearningExtract {
			t.Fatal("extraction must be a separate tool-free request")
		}
		input := req.Messages[1].Content
		for _, forbidden := range []string{"abcdef1234567890", "private reasoning", "private raw output", "tool_call_id"} {
			if strings.Contains(input, forbidden) {
				t.Fatalf("leaked %s", forbidden)
			}
		}
		snapshot, err := store.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if approved && len(snapshot.Items) != 1 || !approved && len(snapshot.Items) != 0 {
			t.Fatal("approval was not enforced")
		}
		if !approved && !strings.Contains(result, "cancelled") {
			t.Fatal(result)
		}
	}
}
func TestLearningRejectsInvalidOrUnavailableApproval(t *testing.T) {
	for _, response := range []string{
		"not JSON", `{"changes":[{"kind":"add","topic":"secret","content":"password = abcdef1234567890","tags":[]}]}`,
		`{"changes":[{"kind":"add","scope":"workspace","topic":"bad","content":"bad","tags":[]}]}`,
	} {
		a, store, p := newLearningAgent(t)
		p.response = response
		a.messages = append(a.messages, llm.Message{Role: "user", Content: "Remember Go tests"})
		_, err := a.Learn(context.Background(), "", func(context.Context, []learning.Change) (bool, error) {
			t.Fatal("invalid proposal reached approval")
			return true, nil
		})
		if err == nil {
			t.Fatal("accepted invalid proposal")
		}
		snapshot, _ := store.Snapshot(context.Background())
		if len(snapshot.Items) != 0 {
			t.Fatal("stored invalid learning")
		}
	}
	a, _, p := newLearningAgent(t)
	if _, err := a.Learn(context.Background(), "", nil); err == nil {
		t.Fatal("allowed changes without approver")
	}
	for _, args := range []string{"global", "workspace", "forget", "list extra", "extract"} {
		if _, err := a.Learn(context.Background(), args, nil); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	if len(p.requests) != 0 {
		t.Fatal("invalid command invoked model")
	}
}
func TestLearningCancellationAndStaleApproval(t *testing.T) {
	a, store, _ := newLearningAgent(t)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "Remember Go testing"})
	ctx, cancel := context.WithCancel(context.Background())
	_, err := a.Learn(ctx, "", func(context.Context, []learning.Change) (bool, error) { cancel(); return true, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error %v", err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if len(snapshot.Items) != 0 {
		t.Fatal("saved after cancellation")
	}
	_, err = a.Learn(context.Background(), "", func(context.Context, []learning.Change) (bool, error) { seedLearning(t, store); return true, nil })
	if !errors.Is(err, learning.ErrConflict) {
		t.Fatalf("stale approval error %v", err)
	}
	snapshot, _ = store.Snapshot(context.Background())
	if len(snapshot.Items) != 1 {
		t.Fatal("stale proposal changed store")
	}
}
func TestLearningListForgetAndCompact(t *testing.T) {
	a, store, p := newLearningAgent(t)
	seedLearning(t, store)
	snapshot, _ := store.Snapshot(context.Background())
	item := snapshot.Items[0]
	result, err := a.Learn(context.Background(), "list", nil)
	if err != nil || !strings.Contains(result, item.ID) || strings.Contains(result, `"version"`) || len(p.requests) != 0 {
		t.Fatalf("list %s %v", result, err)
	}
	if _, err := a.Learn(context.Background(), "forget "+item.ID, func(context.Context, []learning.Change) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	still, _ := store.Snapshot(context.Background())
	if len(still.Items) != 1 {
		t.Fatal("unapproved deletion")
	}
	proposal, _ := json.Marshal(map[string]any{"changes": []learning.Draft{{Kind: "update", ID: item.ID, Topic: item.Topic, Content: "For Go tests, begin with the changed package.", Tags: item.Tags}}})
	p.response = string(proposal)
	result, err = a.Learn(context.Background(), "compact", func(_ context.Context, review []learning.Change) (bool, error) {
		if len(review) != 1 || review[0].Before == nil || review[0].After == nil || review[0].Before.Content != item.Content || !strings.Contains(review[0].After.Content, "begin with the changed package") {
			t.Fatal("incomplete review")
		}
		return true, nil
	})
	if err != nil || !strings.Contains(result, "Compaction backup:") {
		t.Fatalf("compact %s %v", result, err)
	}
	if p.requests[0].Messages[0].Content != prompt.LearningCompact || strings.Contains(p.requests[0].Messages[1].Content, `"session"`) {
		t.Fatal("incorrect compaction input")
	}
	if _, err := a.Learn(context.Background(), "forget "+item.ID, func(context.Context, []learning.Change) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	empty, _ := store.Snapshot(context.Background())
	if len(empty.Items) != 0 {
		t.Fatal("approved deletion did not apply")
	}
	if len(p.requests) != 1 {
		t.Fatal("list or forget called the model")
	}
}
func TestLearningRetrievalIsEphemeralAndResettable(t *testing.T) {
	a, store, p := newLearningAgent(t)
	seedLearning(t, store)
	p.response = "Done"
	if err := a.Run(context.Background(), "Go testing"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.requests[0].Messages[0].Content, prompt.LearningReference) {
		t.Fatal("missing relevant learning")
	}
	if learning.EstimatedTokens(a.learningContext) > 1200 {
		t.Fatal("budget exceeded")
	}
	for _, m := range a.messages {
		if strings.Contains(m.Content, prompt.LearningReference) {
			t.Fatal("learning persisted in history")
		}
	}
	if err := a.Run(context.Background(), "Go testing"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(p.requests[1].Messages[0].Content, prompt.LearningReference) != 1 {
		t.Fatal("learning accumulated")
	}
	if err := a.Run(context.Background(), "unrelated weather question"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.requests[2].Messages[0].Content, prompt.LearningReference) {
		t.Fatal("irrelevant learning retained")
	}
	a.learningSessionID = learningSource
	a.ResetSession()
	if a.learningContext != "" || a.learningSessionID != "" {
		t.Fatal("new session retained learning context or provenance")
	}
	snapshot, _ := store.Snapshot(context.Background())
	if len(snapshot.Items) != 1 {
		t.Fatal("new session deleted durable learning")
	}
	if err := a.Run(context.Background(), "Go testing"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.requests[3].Messages[0].Content, prompt.LearningReference) {
		t.Fatal("durable learning unavailable after reset")
	}
	a.SetLearning(store, 0)
	if err := a.Run(context.Background(), "Go testing"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.requests[4].Messages[0].Content, prompt.LearningReference) {
		t.Fatal("zero budget did not disable retrieval")
	}
}

func TestNormalConversationDoesNotCreateLearning(t *testing.T) {
	a, store, p := newLearningAgent(t)
	p.response = "I will remember your preference for Go tests."
	if err := a.Run(context.Background(), "Remember my preference for Go tests"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Items) != 0 {
		t.Fatal("normal conversation saved learning without /learn")
	}
	if len(p.requests) != 1 {
		t.Fatal("automatic extraction requested")
	}
}

type oversizedLearningProvider struct{}

func (oversizedLearningProvider) Name() string { return "oversized" }
func (oversizedLearningProvider) Complete(ctx context.Context, _ llm.Request, onText llm.StreamCallback) (llm.Response, error) {
	onText(llm.StreamEvent{Kind: llm.StreamOutput, Text: strings.Repeat("x", learning.MaxProposalBytes+1)})
	return llm.Response{}, ctx.Err()
}
func TestOversizedLearningStreamCancelsBeforeReview(t *testing.T) {
	a, store, _ := newLearningAgent(t)
	a.provider = oversizedLearningProvider{}
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "Remember Go testing"})
	_, err := a.Learn(context.Background(), "", func(context.Context, []learning.Change) (bool, error) {
		t.Fatal("oversized proposal reached review")
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds 64 KiB") {
		t.Fatalf("got %v", err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if len(snapshot.Items) != 0 {
		t.Fatal("oversized proposal saved")
	}
}
