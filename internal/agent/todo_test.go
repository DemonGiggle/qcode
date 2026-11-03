package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/redaction"
	"qcode/internal/trace"
)

func todoTestAgent() *Agent {
	return New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 4)
}

func mustTodoAdd(t *testing.T, a *Agent, id, title string) string {
	t.Helper()
	result, err := a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_add",
		Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q,"title":%q}`, id, title)),
	})
	if err != nil {
		t.Fatalf("todo_add %q: %v", id, err)
	}
	if !strings.Contains(result.Output, id) || !strings.Contains(result.Output, title) {
		t.Fatalf("todo_add output missing item: %q", result.Output)
	}
	if result.EndTurn {
		t.Fatal("todo_add must not end the turn")
	}
	return result.Output
}

func TestTodoAddUpdateDoneFlow(t *testing.T) {
	a := todoTestAgent()
	mustTodoAdd(t, a, "setup", "Set up workspace")
	mustTodoAdd(t, a, "impl", "Implement changes")

	result, err := a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_update",
		Arguments: json.RawMessage(`{"id":"impl","status":"doing"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "- [-] Implement changes (impl)") {
		t.Fatalf("doing marker missing: %q", result.Output)
	}

	result, err = a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_done",
		Arguments: json.RawMessage(`{"id":"setup"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "- [x] Set up workspace (setup)") {
		t.Fatalf("done marker missing: %q", result.Output)
	}

	text, ok := a.LatestTodoText()
	if !ok {
		t.Fatal("todo list went missing")
	}
	if !strings.Contains(text, "# Todo (1/2 done)") || !strings.Contains(text, "- [-] Implement changes (impl)") {
		t.Fatalf("checklist = %q", text)
	}
	if _, ok := a.LatestPlan(); ok {
		t.Fatal("todo writes leaked into plan state")
	}
}

func TestTodoSnapshotExposesPanelData(t *testing.T) {
	a := todoTestAgent()
	if _, _, _, _, ok := a.TodoSnapshot(); ok {
		t.Fatal("empty snapshot reported a list")
	}
	mustTodoAdd(t, a, "setup", "Set up workspace")
	mustTodoAdd(t, a, "impl", "Implement changes")
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_update",
		Arguments: json.RawMessage(`{"id":"impl","status":"doing"}`),
	}); err != nil {
		t.Fatal(err)
	}
	ids, titles, statuses, firstVersion, ok := a.TodoSnapshot()
	if !ok {
		t.Fatal("snapshot went missing")
	}
	if len(ids) != 2 || ids[0] != "setup" || ids[1] != "impl" {
		t.Fatalf("ids = %q", ids)
	}
	if len(titles) != 2 || titles[0] != "Set up workspace" || titles[1] != "Implement changes" {
		t.Fatalf("titles = %q", titles)
	}
	if len(statuses) != 2 || statuses[0] != "todo" || statuses[1] != "doing" {
		t.Fatalf("statuses = %q", statuses)
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_done",
		Arguments: json.RawMessage(`{"id":"setup"}`),
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, secondVersion, ok := a.TodoSnapshot()
	if !ok || secondVersion != firstVersion+1 {
		t.Fatalf("version did not advance: %d -> %d, ok=%v", firstVersion, secondVersion, ok)
	}
	a.ClearTodo()
	if _, _, _, _, ok := a.TodoSnapshot(); ok {
		t.Fatal("cleared snapshot still reported a list")
	}
}

func TestTodoUpdateRenamesTitles(t *testing.T) {
	a := todoTestAgent()
	mustTodoAdd(t, a, "item", "Old title")
	result, err := a.executeDetailed(context.Background(), llm.ToolCall{
		Name:      "todo_update",
		Arguments: json.RawMessage(`{"id":"item","title":"New title","status":"doing"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "New title") || strings.Contains(result.Output, "Old title") {
		t.Fatalf("rename output = %q", result.Output)
	}
}

func TestTodoValidation(t *testing.T) {
	a := todoTestAgent()
	cases := []struct {
		name string
		args string
	}{
		{"empty id", `{"id":"","title":"T"}`},
		{"bad id chars", `{"id":"Has Space","title":"T"}`},
		{"bad id case", `{"id":"UPPER","title":"T"}`},
		{"empty title", `{"id":"a","title":"  "}`},
		{"title too long", fmt.Sprintf(`{"id":"a","title":%q}`, strings.Repeat("x", MaxTodoTitleRunes+1))},
		{"malformed", `{"id":`},
	}
	for _, tc := range cases {
		if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_add", Arguments: json.RawMessage(tc.args)}); err == nil {
			t.Fatalf("%s was accepted", tc.name)
		}
	}

	mustTodoAdd(t, a, "dup", "Original")
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_add", Arguments: json.RawMessage(`{"id":"dup","title":"Again"}`)}); err == nil {
		t.Fatal("duplicate id was accepted")
	}
	updateCases := []struct {
		name string
		args string
	}{
		{"missing id", `{"status":"doing"}`},
		{"unknown id", `{"id":"ghost","status":"doing"}`},
		{"bad status", `{"id":"dup","status":"finished"}`},
		{"neither field", `{"id":"dup"}`},
		{"no-op", `{"id":"dup","status":"todo"}`},
	}
	for _, tc := range updateCases {
		if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_update", Arguments: json.RawMessage(tc.args)}); err == nil {
			t.Fatalf("update %s was accepted", tc.name)
		}
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_done", Arguments: json.RawMessage(`{"id":"ghost"}`)}); err == nil {
		t.Fatal("todo_done accepted an unknown id")
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_done", Arguments: json.RawMessage(`{"id":"dup"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_done", Arguments: json.RawMessage(`{"id":"dup"}`)}); err == nil {
		t.Fatal("todo_done accepted an already-done item")
	}
}

func TestTodoItemLimit(t *testing.T) {
	a := todoTestAgent()
	for i := 0; i < MaxTodoItems; i++ {
		mustTodoAdd(t, a, fmt.Sprintf("item-%d", i), fmt.Sprintf("Task %d", i))
	}
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_add", Arguments: json.RawMessage(`{"id":"extra","title":"Too many"}`)}); err == nil {
		t.Fatalf("accepted item %d", MaxTodoItems+1)
	}
}

func TestTodoBlockedInPlanAndSkillPlanModes(t *testing.T) {
	a := todoTestAgent()
	a.SetPlanMode(true)
	for _, name := range []string{"todo_add", "todo_update", "todo_done"} {
		if hasTool(a.enabledSchemas(), name) {
			t.Fatalf("Plan mode advertised %q", name)
		}
		if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: name, Arguments: json.RawMessage(`{"id":"x"}`)}); err == nil {
			t.Fatalf("%q succeeded in Plan mode", name)
		}
		if a.ToolEnabled(name) {
			t.Fatalf("%q reported enabled in Plan mode", name)
		}
	}
	a.SetPlanMode(false)
	a.SetSkillPlanMode(true)
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_add", Arguments: json.RawMessage(`{"id":"x","title":"T"}`)}); err == nil {
		t.Fatal("todo_add succeeded in Skill Plan mode")
	}
	a.SetSkillPlanMode(false)
	mustTodoAdd(t, a, "back", "Back in normal mode")
}

func TestTodoToolsAdvertisedInNormalMode(t *testing.T) {
	a := todoTestAgent()
	for _, name := range []string{"todo_add", "todo_update", "todo_done"} {
		if !hasTool(a.enabledSchemas(), name) {
			t.Fatalf("normal mode omitted %q", name)
		}
		if !a.ToolEnabled(name) {
			t.Fatalf("%q is not enabled", name)
		}
	}
	a.ToggleTool("todo_add", false)
	if !a.ToolEnabled("todo_add") {
		t.Fatal("todo tools must not be toggleable")
	}
}

func TestTodoPersistsAcrossCheckpoint(t *testing.T) {
	a := todoTestAgent()
	mustTodoAdd(t, a, "one", "First task")
	if _, err := a.executeDetailed(context.Background(), llm.ToolCall{Name: "todo_update", Arguments: json.RawMessage(`{"id":"one","status":"doing"}`)}); err != nil {
		t.Fatal(err)
	}
	before, _ := a.LatestTodoText()
	data := *a.checkpoint.Load()

	restored := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	if err := restored.RestoreState(data); err != nil {
		t.Fatal(err)
	}
	after, ok := restored.LatestTodoText()
	if !ok || after != before {
		t.Fatalf("restored checklist = %q, want %q", after, before)
	}
	if _, ok := restored.LatestPlan(); ok {
		t.Fatal("restore leaked todo into plan state")
	}
	restored.ClearTodo()
	if _, ok := restored.LatestTodo(); ok {
		t.Fatal("ClearTodo kept the list")
	}

	fresh := New(&modePlanProvider{}, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 4)
	freshData := *a.checkpoint.Load()
	var saved SavedState
	if err := json.Unmarshal(freshData, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.LatestTodo == nil {
		t.Fatal("checkpoint omitted the todo list")
	}
	if err := fresh.RestoreFreshState(freshData); err != nil {
		t.Fatal(err)
	}
	if _, ok := fresh.LatestTodo(); ok {
		t.Fatal("fresh state retained the todo list")
	}

	a.ResetSession()
	if _, ok := a.LatestTodo(); ok {
		t.Fatal("/new kept the todo list")
	}
	if _, ok := a.LatestPlan(); ok {
		t.Fatal("unexpected plan state")
	}
}

func TestTodoRedactedInCheckpoint(t *testing.T) {
	secret := "todo-checklist-secret"
	p, _ := redaction.New(redaction.Config{}, []string{secret})
	state := SavedState{Provider: "test", Model: "model", LatestTodo: &Todo{Items: []TodoItem{{ID: "item", Title: secret, Status: TodoStatusDoing}}}}
	data, _ := json.Marshal(state)
	filtered, err := FilterSavedState(p, data)
	if err != nil {
		t.Fatal(err)
	}
	var got SavedState
	if err := json.Unmarshal(filtered, &got); err != nil {
		t.Fatal(err)
	}
	if got.LatestTodo == nil || strings.Contains(got.LatestTodo.Items[0].Title, secret) {
		t.Fatalf("todo title leaked: %+v", got.LatestTodo)
	}
}
