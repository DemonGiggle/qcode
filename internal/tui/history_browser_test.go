package tui

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"qcode/internal/session"
)

func historyRecord(id, agentID, prompt, response, status string, consultation bool, minute int) session.WorkRecord {
	return session.WorkRecord{
		RequestID: id, AgentID: agentID, Prompt: prompt, Response: response,
		Status: status, Consultation: consultation,
		Finished: time.Date(2026, time.September, 21, 10, minute, 0, 0, time.UTC),
	}
}

func TestCompletedAgentHistoryFiltersAndOrders(t *testing.T) {
	records := []session.WorkRecord{
		historyRecord("request-1", "main", "old 日本語 prompt", "old answer", "completed", false, 1),
		historyRecord("request-2", "agent-1", "other agent", "answer", "completed", false, 2),
		historyRecord("request-3", "main", "failed prompt", "", "failed", false, 3),
		historyRecord("request-4", "main", "internal consultation", "answer", "completed", true, 4),
		historyRecord("request-5", "main", "new prompt", "new answer", "completed", false, 5),
	}

	got := completedAgentHistory(records, "main")
	if len(got) != 2 || got[0].RequestID != "request-5" || got[1].RequestID != "request-1" {
		t.Fatalf("completed history = %+v", got)
	}
}

func TestHistoryBrowserSearchesAndOpensResponse(t *testing.T) {
	records := []session.WorkRecord{
		historyRecord("request-2", "main", "Second prompt", "**Second answer**", "completed", false, 2),
		historyRecord("request-1", "main", "First prompt", "First answer", "completed", false, 1),
	}
	input := strings.NewReader("second\r" + selectorPageDown + "q")
	var output bytes.Buffer

	if err := showHistoryBrowser(input, &output, records, "main", 80, 8, false, true); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	if !strings.Contains(got, "Search: second") || !strings.Contains(got, "Prompt") || !strings.Contains(got, "Second answer") {
		t.Fatalf("browser output = %q", got)
	}
}

func TestHistoryBrowserSearchAcceptsUTF8(t *testing.T) {
	browser := newHistoryBrowser([]session.WorkRecord{
		historyRecord("request-1", "main", "修正登入流程", "完成", "completed", false, 1),
		historyRecord("request-2", "main", "other prompt", "done", "completed", false, 2),
	})
	for _, value := range []byte("登入") {
		browser.addSearchKey(string([]byte{value}))
	}
	if browser.query != "登入" || len(browser.matches) != 1 || browser.records[browser.matches[0]].RequestID != "request-1" {
		t.Fatalf("query = %q, matches = %v", browser.query, browser.matches)
	}
}

func TestHistoryDetailReturnsToPreservedSearch(t *testing.T) {
	records := []session.WorkRecord{
		historyRecord("request-2", "main", "needle two", strings.Repeat("line\n", 20), "completed", false, 2),
		historyRecord("request-1", "main", "needle one", "answer", "completed", false, 1),
	}
	input := strings.NewReader("needle" + arrowDownSequence + "\r" + selectorPageDown + string([]byte{127, ctrlC}))
	var output bytes.Buffer

	if err := showHistoryBrowser(input, &output, records, "main", 60, 6, false, true); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "Search: needle") < 2 {
		t.Fatalf("list state was not restored: %q", output.String())
	}
}

func TestHistoryDetailRendersOnlyPromptAndFinalResponse(t *testing.T) {
	record := historyRecord("request-1", "main", "Explain `x`", "# Result\n\n- done", "completed", false, 1)
	lines := strings.Join(historyDetailLines(record, 40, false, true), "\n")
	for _, want := range []string{"Prompt", "Explain `x`", "Response", "Result", "done"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("missing %q in %q", want, lines)
		}
	}
	if strings.Contains(lines, record.RequestID) || strings.Contains(lines, "Completed in") {
		t.Fatalf("detail included transcript metadata: %q", lines)
	}
}

func TestHistoryDetailHeadingsDifferFromActiveTabColor(t *testing.T) {
	record := historyRecord("request-1", "main", "Prompt text", "Response text", "completed", false, 1)
	lines := historyDetailLines(record, 80, true, true)
	if lines[0] != bold+yellow+"Prompt"+reset {
		t.Fatalf("prompt heading = %q", lines[0])
	}
	wantResponse := bold + green + "Response" + reset
	found := false
	for _, line := range lines {
		if line == wantResponse {
			found = true
		}
		if line == bold+cyan+"Prompt"+reset || line == bold+cyan+"Response"+reset {
			t.Fatalf("history heading reused active-tab color: %q", line)
		}
	}
	if !found {
		t.Fatalf("response heading not found in %q", lines)
	}
}

func TestHistoryDetailLeavesBlankRowBelowTabs(t *testing.T) {
	pager := planPager{lines: []string{"Prompt", "text", "", "Response", "answer"}}
	var output bytes.Buffer
	renderHistoryDetail(&output, pager, 1, 1, 80, 7, false, true)

	wantPrefix := "\x1b[2;1H\x1b[2K\x1b[0m\x1b[3;1H\x1b[2KHistory item 1/1"
	if !strings.HasPrefix(output.String(), wantPrefix) {
		t.Fatalf("history detail is not separated from tabs: %q", output.String())
	}
	if visible := historyDetailVisible(7); visible != 5 {
		t.Fatalf("visible content rows = %d, want 5", visible)
	}
}

type historyBrowserManager struct {
	agentController
	records []session.WorkRecord
}

func (m *historyBrowserManager) WorkRecords() []session.WorkRecord {
	return append([]session.WorkRecord(nil), m.records...)
}

func (*historyBrowserManager) Summary(id string) (session.Summary, error) {
	return session.Summary{ID: id, Name: "Main", Status: session.StatusIdle}, nil
}

func (m *historyBrowserManager) List() []session.Summary {
	summary, _ := m.Summary("main")
	return []session.Summary{summary}
}

func TestShowHistoryDoesNotChangeConversationHistory(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "history-browser")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	input := newInterruptReader(nil)
	input.data <- ctrlC
	display := newHistoryWriter(io.Discard)
	display.AddLine("existing conversation")
	u := &UI{
		fixedInput: true, input: input, out: out, width: 80, height: 12, unicode: true,
		inputLabel: inputPrompt, activeAgent: "main", drafts: map[string]string{}, views: map[string]*agentView{},
		manager: &historyBrowserManager{records: []session.WorkRecord{
			historyRecord("request-1", "main", "A prompt", "A response", "completed", false, 1),
		}},
		display: display,
	}

	u.showHistory(context.Background())
	got := strings.Join(display.Lines(), "\n")
	if !strings.Contains(got, "existing conversation") || strings.Contains(got, "A prompt") || strings.Contains(got, "A response") {
		t.Fatalf("conversation history changed: %q", got)
	}
}
