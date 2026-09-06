package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/session"
)

const (
	DefaultMaxAgents = 4
	maxHandoffBytes  = 4 * 1024
	maxRosterBytes   = 2 * 1024
)

type Status = session.Status
type AgentSummary = session.Summary
type ManagerEvent = session.Event

const (
	StatusIdle               = session.StatusIdle
	StatusRunning            = session.StatusRunning
	StatusWaitingForApproval = session.StatusWaitingForApproval
	StatusCompleted          = session.StatusCompleted
	StatusFailed             = session.StatusFailed
	StatusCancelled          = session.StatusCancelled
)

type SessionFactory func(id, name, model string, main bool) (*Agent, error)

type managedSession struct {
	summary AgentSummary
	runner  *Agent
	cancel  context.CancelFunc
	started time.Time
}

// AgentManager owns independent agent lifecycles and the shared workspace
// mutation lock. Terminal presentation remains the UI's responsibility.
type AgentManager struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex
	workspace sync.Mutex
	sessions  map[string]*managedSession
	order     []string
	nextID    int
	max       int
	factory   SessionFactory
	events    chan ManagerEvent
	wg        sync.WaitGroup
	shutdown  bool
	stopOnce  sync.Once
}

func NewAgentManager(ctx context.Context, maxAgents int) *AgentManager {
	if maxAgents <= 0 {
		maxAgents = DefaultMaxAgents
	}
	managerCtx, cancel := context.WithCancel(ctx)
	return &AgentManager{
		ctx: managerCtx, cancel: cancel, max: maxAgents,
		sessions: make(map[string]*managedSession), events: make(chan ManagerEvent, 32),
	}
}

func (m *AgentManager) SetFactory(factory SessionFactory) {
	m.mu.Lock()
	m.factory = factory
	m.mu.Unlock()
}

func (m *AgentManager) Events() <-chan ManagerEvent { return m.events }

func (m *AgentManager) CreateMain(model string) (AgentSummary, error) {
	return m.create("main", "main", model, true)
}

func (m *AgentManager) Create(model string) (AgentSummary, error) {
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return AgentSummary{}, fmt.Errorf("agent manager is shutting down")
	}
	if len(m.sessions) >= m.max {
		m.mu.Unlock()
		return AgentSummary{}, fmt.Errorf("agent limit reached (%d including main)", m.max)
	}
	m.nextID++
	id := fmt.Sprintf("agent-%d", m.nextID)
	m.mu.Unlock()
	return m.create(id, id, model, false)
}

func (m *AgentManager) create(id, name, model string, main bool) (AgentSummary, error) {
	m.mu.RLock()
	factory := m.factory
	shuttingDown := m.shutdown
	m.mu.RUnlock()
	if shuttingDown {
		return AgentSummary{}, fmt.Errorf("agent manager is shutting down")
	}
	if factory == nil {
		return AgentSummary{}, fmt.Errorf("agent factory is not configured")
	}
	runner, err := factory(id, name, model, main)
	if err != nil {
		return AgentSummary{}, err
	}
	summary := AgentSummary{ID: id, Name: name, Model: model, Status: StatusIdle}
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return AgentSummary{}, fmt.Errorf("agent manager is shutting down")
	}
	if _, exists := m.sessions[id]; exists {
		m.mu.Unlock()
		return AgentSummary{}, fmt.Errorf("agent %q already exists", id)
	}
	if len(m.sessions) >= m.max {
		m.mu.Unlock()
		return AgentSummary{}, fmt.Errorf("agent limit reached (%d including main)", m.max)
	}
	m.sessions[id] = &managedSession{summary: summary, runner: runner}
	m.order = append(m.order, id)
	m.mu.Unlock()
	if main {
		runner.SetRequestContext(m.RosterContext)
	}
	return summary, nil
}

func (m *AgentManager) Agent(id string) (*Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	return s.runner, true
}

// Runner exposes a session without coupling terminal code to Agent.
func (m *AgentManager) Runner(id string) (any, bool) {
	return m.Agent(id)
}

func (m *AgentManager) List() []AgentSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]AgentSummary, 0, len(m.order))
	for _, id := range m.order {
		if s := m.sessions[id]; s != nil {
			result = append(result, cloneSummary(s.summary))
		}
	}
	return result
}

func (m *AgentManager) Summary(id string) (AgentSummary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return AgentSummary{}, fmt.Errorf("unknown agent %q", id)
	}
	return cloneSummary(s.summary), nil
}

func (m *AgentManager) Rename(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("agent name must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("unknown agent %q", id)
	}
	s.summary.Name = truncateUTF8(name, 64)
	return nil
}

func (m *AgentManager) UpdateModel(id, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("unknown agent %q", id)
	}
	if s.summary.Status == StatusRunning || s.summary.Status == StatusWaitingForApproval {
		return fmt.Errorf("agent %q is %s", id, s.summary.Status)
	}
	s.summary.Model = model
	return nil
}

func (m *AgentManager) Reset(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown agent %q", id)
	}
	if s.summary.Status == StatusRunning || s.summary.Status == StatusWaitingForApproval {
		m.mu.Unlock()
		return fmt.Errorf("agent %q is %s", id, s.summary.Status)
	}
	s.summary.Status = StatusIdle
	s.summary.CurrentTask = ""
	s.summary.LastOutcome = ""
	s.summary.ChangedFiles = nil
	s.summary.Error = ""
	runner := s.runner
	m.mu.Unlock()
	runner.ResetSession()
	return nil
}

func (m *AgentManager) Start(id, task string) error {
	if strings.TrimSpace(task) == "" {
		return fmt.Errorf("task must not be empty")
	}
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return fmt.Errorf("agent manager is shutting down")
	}
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown agent %q", id)
	}
	switch s.summary.Status {
	case StatusRunning, StatusWaitingForApproval:
		status := s.summary.Status
		m.mu.Unlock()
		return fmt.Errorf("agent %q is %s", id, status)
	}
	runCtx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.started = time.Now()
	s.summary.Status = StatusRunning
	s.summary.CurrentTask = truncateUTF8(task, 512)
	s.summary.ChangedFiles = nil
	s.summary.Error = ""
	runner := s.runner
	m.wg.Add(1)
	m.emitLocked(s.summary, 0)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		err := runner.Run(runCtx, task)
		cancel()
		m.finish(id, err, runner.LastResponse())
	}()
	return nil
}

func (m *AgentManager) finish(id string, err error, outcome string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil {
		m.mu.Unlock()
		return
	}
	s.cancel = nil
	duration := time.Since(s.started)
	if err == nil {
		s.summary.Status = StatusCompleted
		s.summary.LastOutcome = truncateUTF8(strings.TrimSpace(outcome), maxHandoffBytes)
		s.summary.Error = ""
	} else if errors.Is(err, context.Canceled) {
		s.summary.Status = StatusCancelled
		s.summary.Error = ""
	} else {
		// Provider, network, and tool errors are routinely transient. Keep the
		// error for the tab and roster, but leave every agent ready for its next
		// prompt without requiring a session reset.
		s.summary.Status = StatusIdle
		s.summary.Error = truncateUTF8(err.Error(), 1024)
	}
	m.emitLocked(s.summary, duration)
	m.mu.Unlock()
}

func (m *AgentManager) Cancel(id string) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("unknown agent %q", id)
	}
	cancel := s.cancel
	status := s.summary.Status
	m.mu.RUnlock()
	if cancel == nil || status != StatusRunning && status != StatusWaitingForApproval {
		return fmt.Errorf("agent %q is not running", id)
	}
	cancel()
	return nil
}

func (m *AgentManager) Close(id string) error {
	if id == "main" {
		return fmt.Errorf("main agent cannot be closed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("unknown agent %q", id)
	}
	if s.summary.Status == StatusRunning || s.summary.Status == StatusWaitingForApproval {
		return fmt.Errorf("agent %q is %s", id, s.summary.Status)
	}
	delete(m.sessions, id)
	for i, candidate := range m.order {
		if candidate == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

func (m *AgentManager) SetWaitingForApproval(id string, waiting bool) {
	m.mu.Lock()
	if s := m.sessions[id]; s != nil {
		if waiting && s.summary.Status == StatusRunning {
			s.summary.Status = StatusWaitingForApproval
			m.emitLocked(s.summary, 0)
		} else if !waiting && s.summary.Status == StatusWaitingForApproval {
			s.summary.Status = StatusRunning
			m.emitLocked(s.summary, 0)
		}
	}
	m.mu.Unlock()
}

func (m *AgentManager) AddChangedFiles(id string, files ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return
	}
	seen := make(map[string]bool, len(s.summary.ChangedFiles))
	for _, path := range s.summary.ChangedFiles {
		seen[path] = true
	}
	for _, path := range files {
		if path = strings.TrimSpace(path); path != "" && !seen[path] {
			s.summary.ChangedFiles = append(s.summary.ChangedFiles, path)
			seen[path] = true
		}
	}
	sort.Strings(s.summary.ChangedFiles)
}

func (m *AgentManager) RosterContext() string {
	list := m.List()
	var lines []string
	for _, item := range list {
		if item.ID == "main" {
			continue
		}
		line := fmt.Sprintf("- %s name=%q model=%q status=%s", item.ID, truncateUTF8(item.Name, 40), truncateUTF8(item.Model, 60), item.Status)
		if item.CurrentTask != "" {
			line += " task=" + strconvQuote(truncateUTF8(item.CurrentTask, 80))
		}
		if item.LastOutcome != "" {
			line += " outcome=" + strconvQuote(truncateUTF8(item.LastOutcome, 100))
		}
		if len(item.ChangedFiles) > 0 {
			line += " files=" + truncateUTF8(strings.Join(item.ChangedFiles, ","), 80)
		}
		if item.Error != "" {
			line += " error=" + strconvQuote(truncateUTF8(item.Error, 80))
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	text := prompt.AgentRosterReference + strings.Join(lines, "\n")
	return truncateUTF8(text, maxRosterBytes)
}

func (m *AgentManager) WrapToolset(id string, base Toolset, main bool) Toolset {
	return &managedToolset{manager: m, id: id, base: base, main: main}
}

func (m *AgentManager) emitLocked(summary AgentSummary, duration time.Duration) {
	select {
	case m.events <- ManagerEvent{Agent: cloneSummary(summary), Duration: duration}:
	default:
	}
}

func (m *AgentManager) Shutdown() {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.shutdown = true
		m.mu.Unlock()
		m.cancel()
		m.wg.Wait()
		close(m.events)
	})
}

type managedToolset struct {
	manager *AgentManager
	id      string
	base    Toolset
	main    bool
}

func (t *managedToolset) ResetSession() {
	if resetter, ok := t.base.(interface{ ResetSession() }); ok {
		resetter.ResetSession()
	}
}

func (t *managedToolset) ToolNames() []string {
	if configurable, ok := t.base.(interface{ ToolNames() []string }); ok {
		return configurable.ToolNames()
	}
	tools := t.base.Schemas()
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	return names
}

func (t *managedToolset) ToggleTool(name string, enabled bool) {
	if configurable, ok := t.base.(interface {
		EnableTool(string)
		DisableTool(string)
	}); ok {
		if enabled {
			configurable.EnableTool(name)
		} else {
			configurable.DisableTool(name)
		}
	}
}

func (t *managedToolset) ToolEnabled(name string) bool {
	if configurable, ok := t.base.(interface{ IsToolEnabled(string) bool }); ok {
		return configurable.IsToolEnabled(name)
	}
	for _, tool := range t.base.EnabledSchemas() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func (t *managedToolset) Schemas() []llm.Tool {
	tools := t.base.Schemas()
	if t.main {
		tools = append(tools, managerSchemas()...)
	}
	return tools
}

func (t *managedToolset) EnabledSchemas() []llm.Tool {
	tools := t.base.EnabledSchemas()
	if t.main {
		tools = append(tools, managerSchemas()...)
	}
	return tools
}

func (t *managedToolset) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	if t.main {
		switch call.Name {
		case "list_agents":
			return t.listAgents()
		case "delegate_task":
			return t.delegate(ctx, call.Arguments)
		case "get_agent_result":
			return t.result(call.Arguments)
		}
	}
	mutating := call.Name == "write" || call.Name == "edit" || call.Name == "shell"
	if mutating {
		t.manager.workspace.Lock()
		defer t.manager.workspace.Unlock()
	}
	var before map[string]string
	tracker, tracksWorkspace := t.base.(interface {
		WorkspaceState(context.Context) map[string]string
	})
	if call.Name == "shell" && tracksWorkspace {
		before = tracker.WorkspaceState(ctx)
	}
	result, err := t.base.ExecuteDetailed(ctx, call)
	if call.Name == "shell" && tracksWorkspace {
		result.ChangedFiles = append(result.ChangedFiles, changedStateFiles(before, tracker.WorkspaceState(ctx))...)
	}
	if len(result.ChangedFiles) > 0 {
		t.manager.AddChangedFiles(t.id, result.ChangedFiles...)
	}
	return result, err
}

func changedStateFiles(before, after map[string]string) []string {
	if before == nil || after == nil {
		return nil
	}
	seen := make(map[string]bool, len(before)+len(after))
	for path := range before {
		seen[path] = true
	}
	for path := range after {
		seen[path] = true
	}
	changed := make([]string, 0)
	for path := range seen {
		if before[path] != after[path] {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	return changed
}

func (t *managedToolset) listAgents() (llm.ToolResult, error) {
	list := t.manager.List()
	for i := range list {
		list[i].LastOutcome = ""
		list[i].ChangedFiles = nil
		list[i].Error = truncateUTF8(list[i].Error, 160)
	}
	data, _ := json.Marshal(list)
	return llm.ToolResult{Output: string(data)}, nil
}

func (t *managedToolset) delegate(ctx context.Context, arguments json.RawMessage) (llm.ToolResult, error) {
	var args struct {
		AgentID string `json:"agent_id"`
		Prompt  string `json:"prompt"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid delegate_task arguments: %w", err)
	}
	if args.AgentID == "main" {
		return llm.ToolResult{}, fmt.Errorf("main agent cannot delegate to itself")
	}
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	if err := t.manager.Start(args.AgentID, args.Prompt); err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Output: fmt.Sprintf("task accepted by %s", args.AgentID)}, nil
}

func (t *managedToolset) result(arguments json.RawMessage) (llm.ToolResult, error) {
	var args struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid get_agent_result arguments: %w", err)
	}
	if args.AgentID == "main" {
		return llm.ToolResult{}, fmt.Errorf("main agent has no external handoff")
	}
	summary, err := t.manager.Summary(args.AgentID)
	if err != nil {
		return llm.ToolResult{}, err
	}
	data, _ := json.Marshal(summary)
	return llm.ToolResult{Output: string(data)}, nil
}

func managerSchemas() []llm.Tool {
	stringField := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	object := func(properties map[string]any, required ...string) map[string]any {
		if properties == nil {
			properties = map[string]any{}
		}
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	return []llm.Tool{
		{Name: "list_agents", Description: prompt.ListAgentsTool, Parameters: object(nil)},
		{Name: "delegate_task", Description: prompt.DelegateTaskTool, Parameters: object(map[string]any{"agent_id": stringField(prompt.AgentIDParameter), "prompt": stringField(prompt.AgentPromptParameter)}, "agent_id", "prompt")},
		{Name: "get_agent_result", Description: prompt.GetAgentResultTool, Parameters: object(map[string]any{"agent_id": stringField(prompt.AgentIDParameter)}, "agent_id")},
	}
}

func cloneSummary(summary AgentSummary) AgentSummary {
	summary.ChangedFiles = append([]string(nil), summary.ChangedFiles...)
	return summary
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	if end > 3 {
		return value[:end-3] + "..."
	}
	return value[:end]
}

func strconvQuote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
