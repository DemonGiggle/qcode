package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/prompt"
)

// Todo limits keep model-generated checklists small enough to resend every
// turn without crowding context, while still covering realistic task lists.
const (
	MaxTodoItems      = 20
	MaxTodoTitleRunes = 120
	MaxTodoIDRunes    = 32
)

// TodoStatus is the lifecycle state of one checklist item. Multiple items may
// be doing at once; the model flips states as work progresses.
type TodoStatus string

const (
	TodoStatusTodo  TodoStatus = "todo"
	TodoStatusDoing TodoStatus = "doing"
	TodoStatusDone  TodoStatus = "done"
)

// TodoItem is one checklist row. IDs are model-chosen stable references so
// later updates can target an item even when titles are edited.
type TodoItem struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Status TodoStatus `json:"status"`
}

// Todo is the per-agent execution checklist. It is owned by one agent tab,
// never shared with sub-agents, and never shares state with /plan.
type Todo struct {
	Items []TodoItem `json:"items"`
}

func (t Todo) clone() Todo {
	items := make([]TodoItem, len(t.Items))
	copy(items, t.Items)
	return Todo{Items: items}
}

func validTodoID(id string) bool {
	if id == "" || utf8.RuneCountInString(id) > MaxTodoIDRunes {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validateTodoID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("todo id must not be empty")
	}
	if utf8.RuneCountInString(id) > MaxTodoIDRunes {
		return "", fmt.Errorf("todo id must be at most %d characters", MaxTodoIDRunes)
	}
	if !validTodoID(id) {
		return "", fmt.Errorf("todo id must contain 1-%d lowercase letters, digits, hyphens, or underscores", MaxTodoIDRunes)
	}
	return id, nil
}

func validateTodoTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", fmt.Errorf("todo title must not be empty")
	}
	if !utf8.ValidString(title) {
		return "", fmt.Errorf("todo title must be valid UTF-8")
	}
	if utf8.RuneCountInString(title) > MaxTodoTitleRunes {
		return "", fmt.Errorf("todo title must be at most %d characters", MaxTodoTitleRunes)
	}
	return title, nil
}

func validateTodoStatus(status string) (TodoStatus, error) {
	switch TodoStatus(strings.TrimSpace(status)) {
	case TodoStatusTodo:
		return TodoStatusTodo, nil
	case TodoStatusDoing:
		return TodoStatusDoing, nil
	case TodoStatusDone:
		return TodoStatusDone, nil
	default:
		return "", fmt.Errorf("todo status must be one of todo, doing, or done")
	}
}

func (t *Todo) find(id string) int {
	for i := range t.Items {
		if t.Items[i].ID == id {
			return i
		}
	}
	return -1
}

// renderTodo produces the read-only checklist shown in the transcript and by
// /todo show. It reuses Markdown checkboxes so the plan pager renders it
// without viewport changes.
func renderTodo(t Todo) string {
	var b strings.Builder
	done := 0
	for _, item := range t.Items {
		if item.Status == TodoStatusDone {
			done++
		}
	}
	fmt.Fprintf(&b, "# Todo (%d/%d done)\n", done, len(t.Items))
	for _, item := range t.Items {
		marker := " "
		switch item.Status {
		case TodoStatusDoing:
			marker = "-"
		case TodoStatusDone:
			marker = "x"
		}
		fmt.Fprintf(&b, "\n- [%s] %s (%s)", marker, item.Title, item.ID)
	}
	return b.String()
}

// LatestTodo returns a copy of the agent's execution checklist, if one exists.
func (a *Agent) LatestTodo() (Todo, bool) {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	if a.latestTodo == nil {
		return Todo{}, false
	}
	return a.latestTodo.clone(), true
}

// LatestTodoText returns the rendered checklist for user-facing handoffs.
func (a *Agent) LatestTodoText() (string, bool) {
	todo, ok := a.LatestTodo()
	if !ok {
		return "", false
	}
	return renderTodo(todo), true
}

// TodoVersion reports how many todo mutations this agent has applied. It is
// informational only; ordering across saves rides on the checkpoint payload.
func (a *Agent) TodoVersion() uint64 {
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	return a.todoVersion
}

// ClearTodo drops the execution checklist, e.g. on /new. Plan state is left
// untouched.
func (a *Agent) ClearTodo() {
	a.stateMu.Lock()
	a.todoVersion++
	a.latestTodo = nil
	a.stateMu.Unlock()
	a.publishCheckpoint()
}

func (a *Agent) applyTodo(next Todo) string {
	a.stateMu.Lock()
	a.todoVersion++
	a.artifactVersion++
	cloned := next.clone()
	a.latestTodo = &cloned
	text := renderTodo(cloned)
	a.stateMu.Unlock()
	a.publishCheckpoint()
	return text
}

func (a *Agent) todoAdd(arguments json.RawMessage) (llm.ToolResult, error) {
	var request struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(arguments, &request); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid todo_add: %w", err)
	}
	id, err := validateTodoID(request.ID)
	if err != nil {
		return llm.ToolResult{}, err
	}
	title, err := validateTodoTitle(request.Title)
	if err != nil {
		return llm.ToolResult{}, err
	}
	a.stateMu.Lock()
	var current Todo
	if a.latestTodo != nil {
		current = *a.latestTodo
	}
	if current.find(id) >= 0 {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q already exists", id)
	}
	if len(current.Items) >= MaxTodoItems {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo list holds at most %d items", MaxTodoItems)
	}
	current.Items = append(current.Items, TodoItem{ID: id, Title: title, Status: TodoStatusTodo})
	a.stateMu.Unlock()
	text := a.applyTodo(current)
	return llm.ToolResult{Output: text}, nil
}

func (a *Agent) todoUpdate(arguments json.RawMessage) (llm.ToolResult, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &raw); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid todo_update: %w", err)
	}
	idRaw, ok := raw["id"]
	if !ok {
		return llm.ToolResult{}, fmt.Errorf("todo_update requires an id")
	}
	var id string
	if err := json.Unmarshal(idRaw, &id); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid todo_update: %w", err)
	}
	id, err := validateTodoID(id)
	if err != nil {
		return llm.ToolResult{}, err
	}
	_, hasTitle := raw["title"]
	_, hasStatus := raw["status"]
	if !hasTitle && !hasStatus {
		return llm.ToolResult{}, fmt.Errorf("todo_update requires a title and/or a status")
	}
	var title string
	if hasTitle {
		var value string
		if err := json.Unmarshal(raw["title"], &value); err != nil {
			return llm.ToolResult{}, fmt.Errorf("invalid todo_update title: %w", err)
		}
		title, err = validateTodoTitle(value)
		if err != nil {
			return llm.ToolResult{}, err
		}
	}
	var status TodoStatus
	if hasStatus {
		var value string
		if err := json.Unmarshal(raw["status"], &value); err != nil {
			return llm.ToolResult{}, fmt.Errorf("invalid todo_update status: %w", err)
		}
		status, err = validateTodoStatus(value)
		if err != nil {
			return llm.ToolResult{}, err
		}
	}
	a.stateMu.Lock()
	if a.latestTodo == nil {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q does not exist", id)
	}
	current := *a.latestTodo
	index := current.find(id)
	if index < 0 {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q does not exist", id)
	}
	changed := false
	if hasTitle && current.Items[index].Title != title {
		current.Items[index].Title = title
		changed = true
	}
	if hasStatus && current.Items[index].Status != status {
		current.Items[index].Status = status
		changed = true
	}
	if !changed {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q is already up to date", id)
	}
	a.stateMu.Unlock()
	text := a.applyTodo(current)
	return llm.ToolResult{Output: text}, nil
}

func (a *Agent) todoDone(arguments json.RawMessage) (llm.ToolResult, error) {
	var request struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(arguments, &request); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid todo_done: %w", err)
	}
	id, err := validateTodoID(request.ID)
	if err != nil {
		return llm.ToolResult{}, err
	}
	a.stateMu.Lock()
	if a.latestTodo == nil {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q does not exist", id)
	}
	current := *a.latestTodo
	index := current.find(id)
	if index < 0 {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q does not exist", id)
	}
	if current.Items[index].Status == TodoStatusDone {
		a.stateMu.Unlock()
		return llm.ToolResult{}, fmt.Errorf("todo %q is already done", id)
	}
	current.Items[index].Status = TodoStatusDone
	a.stateMu.Unlock()
	text := a.applyTodo(current)
	return llm.ToolResult{Output: text}, nil
}

// isTodoTool reports whether name is one of the agent-owned execution
// checklist tools. They are always enabled in normal mode, never toggleable,
// and unavailable in Plan/Skill Plan modes.
func isTodoTool(name string) bool {
	switch name {
	case "todo_add", "todo_update", "todo_done":
		return true
	default:
		return false
	}
}

func todoAddSchema() llm.Tool {
	return llm.Tool{Name: "todo_add", Description: prompt.TodoAddTool, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":    map[string]any{"type": "string", "description": prompt.TodoIDParameter},
			"title": map[string]any{"type": "string", "description": prompt.TodoTitleParameter},
		},
		"required": []string{"id", "title"},
	}}
}

func todoUpdateSchema() llm.Tool {
	return llm.Tool{Name: "todo_update", Description: prompt.TodoUpdateTool, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id":     map[string]any{"type": "string", "description": prompt.TodoIDParameter},
			"title":  map[string]any{"type": "string", "description": prompt.TodoTitleParameter},
			"status": map[string]any{"type": "string", "enum": []string{"todo", "doing", "done"}, "description": prompt.TodoStatusParameter},
		},
		"required": []string{"id"},
	}}
}

func todoDoneSchema() llm.Tool {
	return llm.Tool{Name: "todo_done", Description: prompt.TodoDoneTool, Parameters: map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"id": map[string]any{"type": "string", "description": prompt.TodoIDParameter},
		},
		"required": []string{"id"},
	}}
}
