package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"qcode/internal/session"
	"qcode/internal/trace"
)

func TestLatestPromptDeliveryAndQueueTransitions(t *testing.T) {
	m, p := steeringFixture(t, 8, &steeringTools{})
	if got := m.LatestPrompt("main"); got != "" || m.LatestPrompt("missing") != "" {
		t.Fatalf("new agent prompt = %q", got)
	}
	full := strings.Repeat("界", 600) + "\nsecond line"
	task, err := m.Submit("main", full)
	if err != nil {
		t.Fatal(err)
	}
	nextSteeringRequest(t, p)
	if got := m.LatestPrompt("main"); got != full {
		t.Fatal("started prompt was truncated")
	}
	steeringSubmit(t, m, task.RequestID, "superseded steer")
	steer := steeringSubmit(t, m, task.RequestID, "delivered\nsteering 界")
	queued, err := m.Submit("main", "queued prompt")
	if err != nil || queued.QueuePosition != 1 {
		t.Fatalf("queue submission = %+v, %v", queued, err)
	}
	removed, err := m.Submit("main", "removed prompt")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CancelInput("main", removed.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitPrompt(session.PromptSubmission{AgentID: "main", ObservedTaskID: "stale", Intent: session.IntentSteer, Prompt: "rejected"}); !errors.Is(err, ErrStaleTask) {
		t.Fatalf("stale submission = %v", err)
	}
	if got := m.LatestPrompt("main"); got != full {
		t.Fatal("pending, cancelled, or rejected input replaced the prompt")
	}
	p.replies <- finalSteeringResponse("first draft")
	nextSteeringRequest(t, p)
	if got := m.LatestPrompt("main"); got != "delivered\nsteering 界" {
		t.Fatalf("delivered steering prompt = %q", got)
	}
	var delivered bool
	for _, event := range m.SteeringEvents(0) {
		if event.Input.ID == steer.RequestID && event.Input.State == session.InputDelivered {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("steering prompt changed before delivery")
	}
	p.replies <- finalSteeringResponse("done")
	nextSteeringRequest(t, p)
	if got := m.LatestPrompt("main"); got != "queued prompt" {
		t.Fatalf("next started prompt = %q", got)
	}
	p.replies <- finalSteeringResponse("queue done")
	waitSteeringResult(t, m, queued.RequestID)
	if got := m.LatestPrompt("main"); got != "queued prompt" {
		t.Fatal("completion cleared the prompt")
	}
}

func TestLatestPromptRetentionResetAndTabIsolation(t *testing.T) {
	m := newTestManager(t, 2)
	worker, err := m.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitAndWait(context.Background(), "main", "main prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitAndWait(context.Background(), worker.ID, "fail"); err == nil {
		t.Fatal("expected provider failure")
	}
	if m.LatestPrompt("main") != "main prompt" || m.LatestPrompt(worker.ID) != "fail" {
		t.Fatal("failure or another tab replaced the prompt")
	}
	if err := m.Start(worker.ID, "block"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reset(worker.ID); err == nil || m.LatestPrompt(worker.ID) != "block" {
		t.Fatal("rejected reset cleared the prompt")
	}
	if err := m.Cancel(worker.ID); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, m, worker.ID, StatusCancelled)
	if m.LatestPrompt(worker.ID) != "block" {
		t.Fatal("cancellation cleared the prompt")
	}
	if _, err := m.SubmitAndWait(context.Background(), "main", "/command"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitCompact("main"); err != nil {
		t.Fatal(err)
	}
	waitManagerStatus(t, m, "main", StatusCompleted)
	if m.LatestPrompt("main") != "main prompt" {
		t.Fatal("slash command or manual compaction replaced the prompt")
	}
	if err := m.Reset("main"); err != nil {
		t.Fatal(err)
	}
	if m.LatestPrompt("main") != "" || m.LatestPrompt(worker.ID) != "block" {
		t.Fatal("reset did not clear only the active agent's prompt")
	}
}

func TestLatestPromptPersistenceAndOlderSaves(t *testing.T) {
	m := newTestManager(t, 2)
	full := strings.Repeat("long prompt 界\n", 100)
	if _, err := m.SubmitAndWait(context.Background(), "main", full); err != nil {
		t.Fatal(err)
	}
	saved, next := m.SaveAgents()
	if len(saved) != 1 || saved[0].LatestPrompt != strings.TrimSpace(full) {
		t.Fatal("save lost the full prompt")
	}
	for _, older := range []bool{false, true} {
		data, err := json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		var copy []session.SavedAgent
		if err := json.Unmarshal(data, &copy); err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(full)
		if older {
			copy[0].LatestPrompt = ""
			want = ""
			data, _ := json.Marshal(copy)
			if strings.Contains(string(data), "LatestPrompt") {
				t.Fatal("empty prompt field should be optional")
			}
		}
		restored := NewAgentManager(context.Background(), 2)
		restored.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
			return New(&managerProvider{}, model, &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4), nil
		})
		if err := restored.RestoreAgents(copy, next); err != nil {
			t.Fatal(err)
		}
		if got := restored.LatestPrompt("main"); got != want {
			t.Fatalf("restored prompt = %q, want %q", got, want)
		}
		restored.Shutdown()
	}
}
