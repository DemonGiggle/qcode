package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
)

func restoreHistoryManager(t *testing.T, original *AgentManager) *AgentManager {
	t.Helper()
	agents, next := original.SaveAgents()
	// Exercise the exact JSON shape used by the session store.
	data, err := json.Marshal(session.Snapshot{Agents: agents, NextID: next, Work: original.SaveWorkHistory()})
	if err != nil {
		t.Fatal(err)
	}
	var snap session.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	m := NewAgentManager(context.Background(), 20)
	m.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
		return New(&managerProvider{}, model, m.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
	})
	t.Cleanup(m.Shutdown)
	if err := m.RestoreAgents(snap.Agents, snap.NextID); err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreWorkHistory(snap.Work); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWorkHistorySurvivesEvictionResetClosureAndResume(t *testing.T) {
	m := newTestManager(t, 3)
	m.resultLimit = 1
	worker, _ := m.Create("model")
	old, err := m.SubmitAndWait(context.Background(), worker.ID, "authentication bug in token.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitAndWait(context.Background(), worker.ID, "unrelated CSS"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetResult(old.RequestID); err == nil {
		t.Fatal("expected cache eviction")
	}
	if err := m.Reset(worker.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(worker.ID); err != nil {
		t.Fatal(err)
	}
	found := m.SearchWork("authentication", "", 0)
	if len(found.Matches) != 1 || found.Matches[0].RequestID != old.RequestID || found.Matches[0].Available {
		t.Fatal(found)
	}
	restored := restoreHistoryManager(t, m)
	if got := restored.SearchWork("authentication", "", 0); !reflect.DeepEqual(got, found) {
		t.Fatalf("lost old work: %+v", got)
	}
	if _, err := restored.Submit(worker.ID, "question"); err == nil {
		t.Fatal("closed worker became available")
	}
	next, err := restored.SubmitAndWait(context.Background(), "main", "another task")
	if err != nil || next.RequestID != "request-3" {
		t.Fatalf("reused request identity: %+v %v", next, err)
	}
	if len(restored.SaveWorkHistory().Records) != 3 {
		t.Fatal("lost history")
	}
}

func TestHistorySearchPaginationAndRelevantExcerpts(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	for i := 0; i < 13; i++ {
		if _, err := m.SubmitAndWait(context.Background(), worker.ID, fmt.Sprintf("request %d unicode 日本語 authentication", i)); err != nil {
			t.Fatal(err)
		}
	}
	first := m.SearchWork("authentication", "", 0)
	if len(first.Matches) != 10 || first.Total != 13 || first.NextOffset == nil {
		t.Fatal(first)
	}
	last := m.SearchWork("authentication", worker.ID, *first.NextOffset)
	if len(last.Matches) != 3 || last.NextOffset != nil {
		t.Fatal(last)
	}
	if got := m.SearchWork("authentication", "missing", 0); got.Total != 0 {
		t.Fatal(got)
	}
	if got := m.SearchWork("", "", 1000); len(got.Matches) != 0 {
		t.Fatal(got)
	}
	text := strings.Repeat("日本語İ ", 300) + "authentication finding" + strings.Repeat(" after", 50)
	preview := excerpt(text, "authentication", 100)
	if !utf8.ValidString(preview) || len(preview) > 100 || !strings.Contains(preview, "authentication") {
		t.Fatal(preview)
	}
}

func TestAgentKnowledgeUsesLatestCompletedFinding(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	if _, err := m.SubmitAndWait(context.Background(), worker.ID, "first finding"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SubmitAndWait(context.Background(), worker.ID, "latest finding"); err != nil {
		t.Fatal(err)
	}
	if got := m.AgentKnowledge(worker.ID); got != "handled latest finding" {
		t.Fatalf("knowledge = %q", got)
	}
	if got := m.AgentKnowledge("missing"); got != "" {
		t.Fatalf("missing knowledge = %q", got)
	}
}

func TestMainSearchesOlderWorkThenConsultsBeforeAnswer(t *testing.T) {
	var mainContext string
	var replies []ConsultationReply
	p := consultationProvider(func(_ context.Context, req llm.Request) (llm.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Content == "Investigate authentication" {
			mainContext = req.Messages[0].Content
			return llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "consult", Name: "consult_agents", Arguments: json.RawMessage(`{"requests":[{"agent_id":"agent-1","prompt":"Explain your authentication findings"}]}`)}}}}, nil
		}
		if last.Role == "tool" && last.Name == "consult_agents" {
			if err := json.Unmarshal([]byte(last.Content), &replies); err != nil {
				return llm.Response{}, err
			}
			return llm.Response{Message: llm.Message{Role: "assistant", Content: "Answer incorporating consultation"}}, nil
		}
		return llm.Response{Message: llm.Message{Role: "assistant", Content: "Findings: " + last.Content}}, nil
	})
	m := consultationManager(t, p, 1)
	for _, task := range []string{"authentication token validation", "unrelated layout work"} {
		if _, err := m.SubmitAndWait(context.Background(), "agent-1", task); err != nil {
			t.Fatal(err)
		}
	}
	answer, err := m.SubmitAndWait(context.Background(), "main", "Investigate authentication")
	if err != nil || answer.Response != "Answer incorporating consultation" {
		t.Fatalf("%+v %v", answer, err)
	}
	if !strings.Contains(mainContext, "authentication token validation") || !strings.Contains(mainContext, "consult_agents") {
		t.Fatalf("old work was not injected: %s", mainContext)
	}
	if len(replies) != 1 || replies[0].Status != "completed" || replies[0].Response != "Findings: Explain your authentication findings" {
		t.Fatal(replies)
	}
	main, _ := m.Agent("main")
	if strings.Contains(main.messages[0].Content, "recorded work history") || main.taskContext != "" {
		t.Fatal("temporary history entered stored system prompt")
	}
}

func TestResumeInterruptsConsultationsWithoutReplayingThem(t *testing.T) {
	m := newTestManager(t, 2)
	worker, _ := m.Create("model")
	if err := m.Start(worker.ID, "block"); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.submitRequest(worker.ID, "queued question", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	restored := restoreHistoryManager(t, m)
	history := restored.SaveWorkHistory()
	if len(history.Records) != 2 || len(history.Events) != 1 {
		t.Fatal(history)
	}
	for _, record := range history.Records {
		if record.Status != "interrupted" || record.Error == "" || record.Response != "" {
			t.Fatal(record)
		}
	}
	if s, _ := restored.Summary(worker.ID); s.QueueDepth != 0 {
		t.Fatal(s)
	}
	result, err := restored.SubmitAndWait(context.Background(), worker.ID, "new explicit request")
	if err != nil || result.RequestID != "request-3" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRestoreHistoryRejectsInvalidIdentities(t *testing.T) {
	for _, bad := range []session.WorkHistory{
		{NextRequestID: 1, Records: []session.WorkRecord{{RequestID: "request-2", AgentID: "main", Status: "completed"}}},
		{NextRequestID: 1, Records: []session.WorkRecord{{RequestID: "request-1", AgentID: "agent-99", Status: "completed"}}},
		{Events: []session.ConsultationEvent{{Sequence: 2}}},
	} {
		m := newTestManager(t, 2)
		if err := m.RestoreWorkHistory(&bad); err == nil {
			t.Fatal("accepted invalid history")
		}
		if len(m.SaveWorkHistory().Records) != 0 {
			t.Fatal("partial restore")
		}
	}
}

func TestSessionStateStaysConsistentDuringAgentCreationAndWork(t *testing.T) {
	m := newTestManager(t, 3)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 30; i++ {
			worker, err := m.Create("model")
			if err != nil {
				done <- err
				return
			}
			if _, err = m.SubmitAndWait(context.Background(), worker.ID, "record findings"); err != nil {
				done <- err
				return
			}
			if err = m.Close(worker.ID); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 50; i++ {
		agents, next, work := m.SaveSessionState()
		restored := NewAgentManager(context.Background(), 3)
		restored.SetFactory(func(id, name, model string, main bool) (*Agent, error) {
			return New(&managerProvider{}, model, restored.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4), nil
		})
		err := restored.RestoreAgents(agents, next)
		if err == nil {
			err = restored.RestoreWorkHistory(work)
		}
		restored.Shutdown()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if len(m.SaveWorkHistory().Records) != 30 {
		t.Fatal("journal entries were lost")
	}
}

func TestHistoryExcerptsContributeToCompactionThreshold(t *testing.T) {
	a := New(&managerProvider{}, "model", &managerToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	a.messages = append(a.messages, llm.Message{Role: "user", Content: "question"})
	a.SetContextWindow(2000)
	if a.shouldAutoCompact() {
		t.Fatal("small conversation unexpectedly needs compaction")
	}
	a.taskContext = strings.Repeat("historical finding ", 400)
	a.publishContext()
	if !a.shouldAutoCompact() {
		t.Fatal("large historical context did not trigger compaction")
	}
}
