package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
)

func orchestrationSteeringFixture(t *testing.T) (*AgentManager, *steeringProvider) {
	t.Helper()
	provider := &steeringProvider{requests: make(chan llm.Request, 8), replies: make(chan llm.Response, 8)}
	return consultationManager(t, provider, 1), provider
}

func submitWorkerSteer(t *testing.T, manager *AgentManager, taskID, text string) session.Submission {
	t.Helper()
	submission, err := manager.SubmitPrompt(session.PromptSubmission{
		AgentID: "agent-1", ObservedTaskID: taskID, Prompt: text,
		Intent: session.IntentSteer, Source: "browser", Actor: "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func waitQueuedConsultation(t *testing.T, manager *AgentManager, prompt string) session.QueuedPrompt {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		for _, item := range manager.QueuedPrompts("agent-1") {
			if item.Prompt == prompt && item.Source == "consultation" {
				return item
			}
		}
		select {
		case <-manager.Events():
		case <-deadline.C:
			t.Fatal("consultation was not queued")
		}
	}
}

func waitOrchestrationResult(t *testing.T, manager *AgentManager, requestID string) (PromptResult, error) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		result, err := manager.GetResult(requestID)
		if !errors.Is(err, ErrRequestPending) {
			return result, err
		}
		select {
		case <-manager.Events():
		case <-deadline.C:
			t.Fatalf("request %s did not finish", requestID)
		}
	}
}

func expireConsultationRequest(t *testing.T, manager *AgentManager, requestID string) {
	t.Helper()
	manager.mu.RLock()
	worker := manager.sessions["agent-1"]
	request := worker.active
	if request == nil || request.id != requestID {
		request = nil
		for _, queued := range worker.queue {
			if queued.id == requestID {
				request = queued
				break
			}
		}
	}
	valid := request != nil && !request.finished && !request.deadline.IsZero() && manager.work[request.journalIndex].Consultation
	manager.mu.RUnlock()
	if !valid {
		t.Fatal("expected an unfinished consultation request")
	}
	// Existing consultation tests exercise the actual timeout timer. Trigger its
	// termination path here after steering is accepted, without timing races.
	manager.abortRequest(request, context.DeadlineExceeded)
}

func TestBusyConsultationToolDisclosesOnlyItsDeliveredSteering(t *testing.T) {
	manager, provider := orchestrationSteeringFixture(t)
	original, err := manager.Submit("agent-1", "existing task")
	if err != nil {
		t.Fatal(err)
	}
	nextSteeringRequest(t, provider)

	main, _ := manager.Agent("main")
	type toolOutcome struct {
		result llm.ToolResult
		err    error
	}
	done := make(chan toolOutcome, 1)
	go func() {
		result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{
			Name: "consult_agents", Arguments: json.RawMessage(`{"requests":[{"agent_id":"agent-1","prompt":"consultation question"}]}`),
		})
		done <- toolOutcome{result, err}
	}()
	queued := waitQueuedConsultation(t, manager, "consultation question")
	if queued.Actor != "main" || queued.RequestID == original.RequestID {
		t.Fatalf("consultation did not remain separate FIFO work: %+v", queued)
	}

	// Steer the earlier task while the consultation waits behind it. Its
	// delivered instruction must not be attributed to the consultation reply.
	earlier := submitWorkerSteer(t, manager, original.RequestID, "adjust existing work")
	provider.replies <- finalSteeringResponse("earlier answer")
	request := nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "adjust existing work" {
		t.Fatalf("earlier steering missing from model request: %q", got)
	}
	provider.replies <- finalSteeringResponse("earlier steered answer")
	request = nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "consultation question" {
		t.Fatalf("consultation did not advance in FIFO order: %q", got)
	}

	obsolete := submitWorkerSteer(t, manager, queued.RequestID, "obsolete consultation instruction")
	latest := submitWorkerSteer(t, manager, queued.RequestID, "revise consultation answer")
	provider.replies <- finalSteeringResponse("answer before steering")
	request = nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "revise consultation answer" {
		t.Fatalf("latest consultation steering missing from model request: %q", got)
	}
	provider.replies <- finalSteeringResponse("revised consultation answer")
	outcome := receive(t, done)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	var replies []ConsultationReply
	if err := json.Unmarshal([]byte(outcome.result.Output), &replies); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(outcome.result.Output, `"user_steering"`) || len(replies) != 1 {
		t.Fatalf("tool response did not disclose steering: %s", outcome.result.Output)
	}
	reply := replies[0]
	if reply.AgentID != "agent-1" || reply.RequestID != queued.RequestID || reply.Status != "completed" || reply.Response != "revised consultation answer" || reply.Error != "" || len(reply.Steers) != 1 {
		t.Fatalf("unexpected consultation reply: %+v", reply)
	}
	steer := reply.Steers[0]
	if steer.ID != latest.RequestID || steer.ID == earlier.RequestID || steer.ParentTaskID != queued.RequestID || steer.State != session.InputDelivered || steer.Text != "revise consultation answer" || steer.Source != "browser" || steer.Actor != "alice@example.com" || steer.Replaces != obsolete.RequestID || steer.Created.IsZero() || steer.Updated.Before(steer.Created) {
		t.Fatalf("steering identity, lifecycle, or attribution lost: %+v", steer)
	}
	records := manager.WorkRecords()
	if len(records) != 2 || len(records[0].Steers) != 1 || records[0].Steers[0].State != session.InputDelivered || len(records[1].Steers) != 2 || records[1].Steers[0].State != session.InputSuperseded || records[1].Steers[1] != steer {
		t.Fatalf("consultation disclosure differs from its work record: %+v", records)
	}
}

func TestActiveConsultationTimeoutRecordsPendingSteerCancellation(t *testing.T) {
	manager, provider := orchestrationSteeringFixture(t)
	done := asyncConsult(manager, context.Background(), ConsultationRequest{"agent-1", "consultation question"})
	nextSteeringRequest(t, provider)
	summary, err := manager.Summary("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	delivered := submitWorkerSteer(t, manager, summary.ActiveTaskID, "instruction already delivered")
	provider.replies <- finalSteeringResponse("answer before steering")
	nextSteeringRequest(t, provider)
	pending := submitWorkerSteer(t, manager, summary.ActiveTaskID, "instruction still pending")
	followup, err := manager.Submit("agent-1", "unrelated FIFO followup")
	if err != nil || followup.QueuePosition != 1 {
		t.Fatalf("followup was not queued: %+v %v", followup, err)
	}
	events := manager.SteeringEvents(0)
	if len(events) == 0 {
		t.Fatal("accepted steering has no lifecycle events")
	}
	cursor := events[len(events)-1].Sequence
	expireConsultationRequest(t, manager, summary.ActiveTaskID)

	outcome := receive(t, done)
	if outcome.err != nil || len(outcome.replies) != 1 {
		t.Fatalf("consultation expiry failed the batch: %+v", outcome)
	}
	reply := outcome.replies[0]
	if reply.Status != "timed_out" || reply.Error != context.DeadlineExceeded.Error() || reply.Response != "" || reply.RequestID != summary.ActiveTaskID || len(reply.Steers) != 1 || reply.Steers[0].ID != delivered.RequestID || reply.Steers[0].State != session.InputDelivered {
		t.Fatalf("timeout lost delivered steering or disclosed undelivered input: %+v", reply)
	}
	cancellations := manager.SteeringEvents(cursor)
	if len(cancellations) != 1 || cancellations[0].AgentID != "agent-1" || cancellations[0].Input.ID != pending.RequestID || cancellations[0].Input.ParentTaskID != reply.RequestID || cancellations[0].Input.State != session.InputCancelled {
		t.Fatalf("pending steering cancellation was not journaled: %+v", cancellations)
	}
	saved := manager.SaveWorkHistory()
	if len(saved.Records) != 2 {
		t.Fatalf("timeout lost consultation or queued work: %+v", saved.Records)
	}
	work := saved.Records[0]
	if work.RequestID != reply.RequestID || work.Status != "timed_out" || work.Error != reply.Error || work.Finished.IsZero() || len(work.Steers) != 2 || work.Steers[0].State != session.InputDelivered || work.Steers[1] != cancellations[0].Input {
		t.Fatalf("steering cancellation has no persisted timeout outcome: %+v", work)
	}
	if len(saved.Events) != 1 || saved.Events[0].RequestID != work.RequestID || saved.Events[0].Status != "timed_out" || saved.Events[0].Error != work.Error || len(saved.SteeringEvents) == 0 || saved.SteeringEvents[len(saved.SteeringEvents)-1] != cancellations[0] {
		t.Fatalf("timeout or steering event missing from saved history: %+v", saved)
	}
	if _, err := manager.GetResult(reply.RequestID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request outcome did not retain deadline error: %v", err)
	}
	request := nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "unrelated FIFO followup" {
		t.Fatalf("pending steer transferred to another task: %q", got)
	}
	provider.replies <- finalSteeringResponse("followup answer")
	if _, err := waitOrchestrationResult(t, manager, followup.RequestID); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedConsultationTimeoutPreservesRunningTaskSteering(t *testing.T) {
	manager, provider := orchestrationSteeringFixture(t)
	original, err := manager.Submit("agent-1", "unrelated running task")
	if err != nil {
		t.Fatal(err)
	}
	nextSteeringRequest(t, provider)
	pending := submitWorkerSteer(t, manager, original.RequestID, "keep steering the original task")
	done := asyncConsult(manager, context.Background(), ConsultationRequest{"agent-1", "expired consultation"})
	queued := waitQueuedConsultation(t, manager, "expired consultation")
	followup, err := manager.Submit("agent-1", "unrelated FIFO followup")
	if err != nil || followup.QueuePosition != 2 {
		t.Fatalf("followup was not behind consultation: %+v %v", followup, err)
	}
	events := manager.SteeringEvents(0)
	if len(events) == 0 {
		t.Fatal("accepted steering has no lifecycle events")
	}
	cursor := events[len(events)-1].Sequence
	expireConsultationRequest(t, manager, queued.RequestID)
	outcome := receive(t, done)
	if outcome.err != nil || len(outcome.replies) != 1 || outcome.replies[0].RequestID != queued.RequestID || outcome.replies[0].Status != "timed_out" || outcome.replies[0].Error != context.DeadlineExceeded.Error() || len(outcome.replies[0].Steers) != 0 {
		t.Fatalf("unexpected queued consultation outcome: %+v", outcome)
	}
	summary, err := manager.Summary("agent-1")
	if err != nil || summary.ActiveTaskID != original.RequestID || summary.Status != session.StatusRunning || summary.QueueDepth != 1 {
		t.Fatalf("queued timeout affected unrelated work: %+v %v", summary, err)
	}
	inputs := manager.PendingInputs("agent-1")
	if len(inputs) != 2 || inputs[0].RequestID != pending.RequestID || inputs[0].State != session.InputPending || inputs[1].RequestID != followup.RequestID || len(manager.SteeringEvents(cursor)) != 0 {
		t.Fatalf("queued timeout changed unrelated pending input: %+v", inputs)
	}
	work := manager.WorkRecords()
	if len(work) != 3 || work[0].Status != "running" || len(work[0].Steers) != 1 || work[0].Steers[0].State != session.InputPending || work[1].Status != "timed_out" || len(work[1].Steers) != 0 {
		t.Fatalf("queued timeout changed steering ownership: %+v", work)
	}
	provider.replies <- finalSteeringResponse("answer before steering")
	request := nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "keep steering the original task" {
		t.Fatalf("surviving steer was not delivered: %q", got)
	}
	provider.replies <- finalSteeringResponse("steered original answer")
	request = nextSteeringRequest(t, provider)
	if got := request.Messages[len(request.Messages)-1].Content; got != "unrelated FIFO followup" {
		t.Fatalf("expired consultation ran or followup was lost: %q", got)
	}
	provider.replies <- finalSteeringResponse("followup answer")
	if _, err := waitOrchestrationResult(t, manager, followup.RequestID); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationToolsPersistAttributionAndRemainSearchable(t *testing.T) {
	for _, tool := range []string{"delegate_task", "create_agent"} {
		t.Run(tool, func(t *testing.T) {
			manager := newTestManager(t, 2)
			const task = "investigate cobalt token validation"
			arguments := map[string]string{"task": task}
			if tool == "delegate_task" {
				worker, err := manager.Create("worker-model")
				if err != nil {
					t.Fatal(err)
				}
				arguments = map[string]string{"agent_id": worker.ID, "prompt": task}
			}
			data, err := json.Marshal(arguments)
			if err != nil {
				t.Fatal(err)
			}
			main, _ := manager.Agent("main")
			result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{Name: tool, Arguments: data})
			if err != nil || !result.EndTurn {
				t.Fatalf("delegation was not accepted asynchronously: %+v %v", result, err)
			}
			records := manager.WorkRecords()
			if len(records) != 1 || records[0].Source != "delegation" || records[0].Actor != "main" || records[0].AgentID != "agent-1" || records[0].Prompt != task || !strings.Contains(result.Output, "request_id="+records[0].RequestID) {
				t.Fatalf("delegation attribution missing from journal: %+v", records)
			}
			requestID := records[0].RequestID
			answer, err := waitOrchestrationResult(t, manager, requestID)
			if err != nil {
				t.Fatal(err)
			}
			restored := restoreHistoryManager(t, manager)
			for _, candidate := range []struct {
				name    string
				manager *AgentManager
			}{{"live", manager}, {"resumed", restored}} {
				t.Run(candidate.name, func(t *testing.T) {
					records := candidate.manager.WorkRecords()
					if len(records) != 1 || records[0].RequestID != requestID || records[0].Source != "delegation" || records[0].Actor != "main" || records[0].Status != "completed" {
						t.Fatalf("delegation attribution did not survive persistence: %+v", records)
					}
					main, _ := candidate.manager.Agent("main")
					result, err := main.tools.ExecuteDetailed(context.Background(), llm.ToolCall{
						Name: "search_agent_work", Arguments: json.RawMessage(`{"query":"cobalt","agent_id":"agent-1","offset":0}`),
					})
					if err != nil {
						t.Fatal(err)
					}
					var found WorkSearch
					if err := json.Unmarshal([]byte(result.Output), &found); err != nil {
						t.Fatal(err)
					}
					if found.Total != 1 || len(found.Matches) != 1 || found.Matches[0].RequestID != requestID || found.Matches[0].AgentID != "agent-1" || found.Matches[0].Status != "completed" || found.Matches[0].Task != task || found.Matches[0].Findings != answer.Response {
						t.Fatalf("delegated work was not found through search tool: %+v", found)
					}
				})
			}
		})
	}
}
