package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
)

func waitDelegationCallback(t *testing.T, manager *AgentManager, timeout time.Duration) session.WorkRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, record := range manager.WorkRecords() {
			if record.Source == delegationCallbackSource && record.AgentID == "main" {
				return record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("delegation callback was not queued for main")
	return session.WorkRecord{}
}

func TestDelegationCallbackDeliveredOnSuccess(t *testing.T) {
	manager := newTestManager(t, 3)
	worker, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	main, _ := manager.Agent("main")
	result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{Name: "delegate_task", Arguments: json.RawMessage(`{"agent_id":"`+worker.ID+`","prompt":"success task"}`)})
	if err != nil || !result.EndTurn {
		t.Fatalf("delegate = %+v, %v", result, err)
	}
	callback := waitDelegationCallback(t, manager, 3*time.Second)
	if callback.Actor != worker.ID {
		t.Fatalf("callback actor = %q, want %q", callback.Actor, worker.ID)
	}
	if !strings.Contains(callback.Prompt, "Sub-agent callback") || !strings.Contains(callback.Prompt, worker.ID) || !strings.Contains(callback.Prompt, "completed") {
		t.Fatalf("callback prompt missing framing: %q", callback.Prompt)
	}
	if _, err := waitOrchestrationResult(t, manager, callback.RequestID); err != nil {
		t.Fatal(err)
	}
	events := manager.DelegationEvents(0)
	if len(events) != 1 || events[0].Status != "completed" || events[0].AgentID != worker.ID {
		t.Fatalf("delegation events = %+v", events)
	}
}

func TestDelegationCallbackDeliveredOnFailure(t *testing.T) {
	manager := newTestManager(t, 3)
	worker, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	// "fail" makes the test provider return an error.
	submission, err := manager.SubmitPrompt(session.PromptSubmission{AgentID: worker.ID, Prompt: "fail", Intent: session.IntentQueue, Source: delegationSource, Actor: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitOrchestrationResult(t, manager, submission.RequestID); err == nil {
		t.Fatal("expected delegated failure")
	}
	callback := waitDelegationCallback(t, manager, 3*time.Second)
	if !strings.Contains(callback.Prompt, "failed") {
		t.Fatalf("failure callback missing status: %q", callback.Prompt)
	}
	if _, err := waitOrchestrationResult(t, manager, callback.RequestID); err != nil {
		t.Fatal(err)
	}
	events := manager.DelegationEvents(0)
	if len(events) != 1 || events[0].Status != "failed" {
		t.Fatalf("delegation failure event = %+v", events)
	}
}

func TestDelegationCallbackQueuedWhileMainBusyAndCoalesced(t *testing.T) {
	manager := newTestManager(t, 4)
	// Occupy main with a blocking task so callbacks queue behind it.
	blocking, err := manager.Submit("main", "block")
	if err != nil {
		t.Fatal(err)
	}
	worker1, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	worker2, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SubmitPrompt(session.PromptSubmission{AgentID: worker1.ID, Prompt: "first", Intent: session.IntentQueue, Source: delegationSource, Actor: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SubmitPrompt(session.PromptSubmission{AgentID: worker2.ID, Prompt: "second", Intent: session.IntentQueue, Source: delegationSource, Actor: "main"}); err != nil {
		t.Fatal(err)
	}
	// Wait for both workers to finish; main is still blocked so callbacks must queue.
	deadline := time.Now().Add(3 * time.Second)
	for {
		events := manager.DelegationEvents(0)
		if len(events) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 delegation events, got %+v", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
	queued := manager.QueuedPrompts("main")
	callbacks := 0
	for _, item := range queued {
		// QueuedPrompts does not expose Source; inspect the journal instead.
		_ = item
	}
	records := manager.WorkRecords()
	for _, record := range records {
		if record.Source == delegationCallbackSource {
			callbacks++
			if record.Status != "queued" && record.Status != "running" {
				t.Fatalf("busy-main callback should stay queued/running, got %+v", record)
			}
		}
	}
	if callbacks != 1 {
		t.Fatalf("burst completions should coalesce into one pending callback, got %d: %+v", callbacks, records)
	}
	if len(queued) == 0 {
		t.Fatalf("expected queued callback behind blocking main task")
	}
	// Unblock main and ensure the coalesced callback runs.
	if err := manager.Cancel("main"); err != nil {
		t.Fatal(err)
	}
	if _, err := waitOrchestrationResult(t, manager, blocking.RequestID); err == nil {
		t.Fatal("expected blocking task to cancel")
	}
	callback := waitDelegationCallback(t, manager, 3*time.Second)
	if !strings.Contains(callback.Prompt, worker1.ID) || !strings.Contains(callback.Prompt, worker2.ID) {
		t.Fatalf("coalesced callback should mention both sub-agents: %q", callback.Prompt)
	}
}

func TestDelegationCallbackBoundedAndPersisted(t *testing.T) {
	manager := newTestManager(t, 2)
	worker, err := manager.Create("worker-model")
	if err != nil {
		t.Fatal(err)
	}
	submission, err := manager.SubmitPrompt(session.PromptSubmission{AgentID: worker.ID, Prompt: "long", Intent: session.IntentQueue, Source: delegationSource, Actor: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitOrchestrationResult(t, manager, submission.RequestID); err != nil {
		t.Fatal(err)
	}
	callback := waitDelegationCallback(t, manager, 3*time.Second)
	if len(callback.Prompt) > 2*maxHandoffBytes {
		t.Fatalf("callback prompt exceeds bound: %d", len(callback.Prompt))
	}
	saved := manager.SaveWorkHistory()
	if len(saved.Delegations) != 1 {
		t.Fatalf("delegations not persisted: %+v", saved.Delegations)
	}
	restored := restoreHistoryManager(t, manager)
	if got := restored.SaveWorkHistory().Delegations; len(got) != 1 || got[0] != saved.Delegations[0] {
		t.Fatalf("delegation event lost on resume: %+v", got)
	}
}

