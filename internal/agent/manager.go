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
	DefaultMaxAgents        = 20
	DefaultPromptQueueLimit = 16
	DefaultResultLimit      = 128
	maxHandoffBytes         = 4 * 1024
	maxRosterBytes          = 2 * 1024
)

var (
	ErrManagerClosed   = errors.New("agent manager is shutting down")
	ErrUnknownAgent    = errors.New("unknown agent")
	ErrClosedAgent     = errors.New("closed agent")
	ErrPromptQueueFull = errors.New("prompt queue is full")
	ErrRequestNotFound = errors.New("prompt request not found")
	ErrRequestPending  = errors.New("prompt request is pending")
)

type Status = session.Status
type AgentSummary = session.Summary
type ManagerEvent = session.Event
type Submission = session.Submission
type PromptResult = session.PromptResult

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
	active  *promptRequest
	queue   []*promptRequest
}

type promptRequest struct {
	id           string
	targetID     string
	prompt       string
	done         chan struct{}
	result       PromptResult
	err          error
	journalIndex int
	deadline     time.Time
	finished     bool
	abortErr     error
}

// AgentManager owns independent agent lifecycles and the shared workspace
// mutation lock. Terminal presentation remains the UI's responsibility.
type AgentManager struct {
	ctx                 context.Context
	cancel              context.CancelFunc
	mu                  sync.RWMutex
	workspace           sync.Mutex
	sessions            map[string]*managedSession
	closed              map[string]struct{}
	order               []string
	nextID              int
	nextRequestID       uint64
	max                 int
	queueLimit          int
	resultLimit         int
	results             map[string]*promptRequest
	resultOrder         []string
	factory             SessionFactory
	events              chan ManagerEvent
	wg                  sync.WaitGroup
	shutdown            bool
	stopOnce            sync.Once
	consultationTimeout time.Duration
	work                []session.WorkRecord
	consultationEvents  []session.ConsultationEvent
}

func NewAgentManager(ctx context.Context, maxAgents int) *AgentManager {
	if maxAgents <= 0 {
		maxAgents = DefaultMaxAgents
	}
	managerCtx, cancel := context.WithCancel(ctx)
	return &AgentManager{
		ctx: managerCtx, cancel: cancel, max: maxAgents,
		queueLimit: DefaultPromptQueueLimit, resultLimit: DefaultResultLimit,
		consultationTimeout: DefaultConsultationTimeout,
		sessions:            make(map[string]*managedSession), closed: make(map[string]struct{}),
		results: make(map[string]*promptRequest), events: make(chan ManagerEvent, 32),
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

// CreateInherited creates a worker and copies the parent agent's capabilities
// into it before the caller can submit work. The parent conversation and its
// main-only orchestration tools remain private to the parent.
func (m *AgentManager) CreateInherited(parentID, model string) (AgentSummary, error) {
	parent, ok := m.Agent(parentID)
	if !ok {
		return AgentSummary{}, fmt.Errorf("unknown agent %q", parentID)
	}
	if strings.TrimSpace(model) == "" {
		parentSummary, err := m.Summary(parentID)
		if err != nil {
			return AgentSummary{}, err
		}
		model = parentSummary.Model
	}
	created, err := m.Create(model)
	if err != nil {
		return AgentSummary{}, err
	}
	child, ok := m.Agent(created.ID)
	if !ok {
		return AgentSummary{}, fmt.Errorf("created agent %q is unavailable", created.ID)
	}
	if err := child.InheritCapabilitiesFrom(parent); err != nil {
		return AgentSummary{}, fmt.Errorf("inherit capabilities for %s: %w", created.ID, err)
	}
	return created, nil
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
	_, err := m.Submit(id, task)
	return err
}

// Submit appends a prompt to the target agent's FIFO and returns immediately.
func (m *AgentManager) Submit(id, task string) (Submission, error) {
	_, submission, err := m.submit(id, task)
	return submission, err
}

// SubmitAndWait appends a prompt, then waits for that prompt's full result.
// Cancelling the wait does not remove or cancel the accepted prompt.
func (m *AgentManager) SubmitAndWait(ctx context.Context, id, task string) (PromptResult, error) {
	req, _, err := m.submit(id, task)
	if err != nil {
		return PromptResult{}, err
	}
	select {
	case <-req.done:
		return req.result, req.err
	case <-ctx.Done():
		return PromptResult{}, ctx.Err()
	}
}

// GetResult returns a completed prompt result without consuming it.
func (m *AgentManager) GetResult(requestID string) (PromptResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	req, ok := m.results[requestID]
	if ok {
		return req.result, req.err
	}
	for _, s := range m.sessions {
		if s.active != nil && s.active.id == requestID {
			return PromptResult{}, ErrRequestPending
		}
		for _, queued := range s.queue {
			if queued.id == requestID {
				return PromptResult{}, ErrRequestPending
			}
		}
	}
	return PromptResult{}, ErrRequestNotFound
}

func (m *AgentManager) submit(id, task string) (*promptRequest, Submission, error) {
	return m.submitRequest(id, task, time.Time{})
}

func (m *AgentManager) submitRequest(id, task string, deadline time.Time) (*promptRequest, Submission, error) {
	if strings.TrimSpace(task) == "" {
		return nil, Submission{}, fmt.Errorf("task must not be empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return nil, Submission{}, ErrManagerClosed
	}
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return nil, Submission{}, context.DeadlineExceeded
	}
	s, ok := m.sessions[id]
	if !ok {
		if _, closed := m.closed[id]; closed {
			return nil, Submission{}, fmt.Errorf("%w %q", ErrClosedAgent, id)
		}
		return nil, Submission{}, fmt.Errorf("%w %q", ErrUnknownAgent, id)
	}
	if s.active != nil && len(s.queue) >= m.queueLimit {
		return nil, Submission{}, fmt.Errorf("%w for agent %q (limit %d)", ErrPromptQueueFull, id, m.queueLimit)
	}
	if m.nextRequestID == ^uint64(0) {
		return nil, Submission{}, fmt.Errorf("prompt request ID space exhausted")
	}
	m.nextRequestID++
	req := &promptRequest{
		id: fmt.Sprintf("request-%d", m.nextRequestID), targetID: id,
		prompt: strings.TrimSpace(task), done: make(chan struct{}),
		journalIndex: len(m.work), deadline: deadline,
	}
	m.work = append(m.work, session.WorkRecord{
		RequestID: req.id, AgentID: id, AgentName: s.summary.Name, Model: s.summary.Model,
		Prompt: req.prompt, Status: "queued", Created: time.Now().UTC(), Consultation: !deadline.IsZero(),
	})
	position := 0
	if s.active == nil {
		m.startRequestLocked(id, s, req)
	} else {
		s.queue = append(s.queue, req)
		position = len(s.queue)
		s.summary.QueueDepth = len(s.queue)
		m.emitLocked(s.summary, 0)
	}
	return req, Submission{RequestID: req.id, TargetID: id, QueuePosition: position}, nil
}

func (m *AgentManager) startRequestLocked(id string, s *managedSession, req *promptRequest) {
	var runCtx context.Context
	var cancel context.CancelFunc
	if req.deadline.IsZero() {
		runCtx, cancel = context.WithCancel(m.ctx)
	} else {
		runCtx, cancel = context.WithDeadline(m.ctx, req.deadline)
	}
	s.active = req
	s.cancel = cancel
	s.started = time.Now()
	m.work[req.journalIndex].Status = "running"
	m.work[req.journalIndex].Started = s.started.UTC()
	s.summary.Status = StatusRunning
	s.summary.QueueDepth = len(s.queue)
	s.summary.CurrentTask = truncateUTF8(req.prompt, 512)
	s.summary.ChangedFiles = nil
	s.summary.Error = ""
	runner := s.runner
	m.wg.Add(1)
	m.emitLocked(s.summary, 0)
	go func() {
		defer m.wg.Done()
		err := runner.Run(runCtx, req.prompt)
		if runCtx.Err() != nil {
			err = runCtx.Err()
		}
		cancel()
		m.finish(id, req, err, runner.LastResponse())
	}()
}

func (m *AgentManager) finish(id string, req *promptRequest, err error, outcome string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s == nil || s.active != req {
		m.mu.Unlock()
		return
	}
	if req.abortErr != nil {
		err, outcome = req.abortErr, ""
	} else if !req.deadline.IsZero() && !time.Now().Before(req.deadline) {
		err, outcome = context.DeadlineExceeded, ""
	}
	fullOutcome := outcome
	if req.finished {
		// Cancellation cannot roll back a tool action that was already in
		// flight. Retain its actual changed files without reviving a late reply.
		m.work[req.journalIndex].ChangedFiles = append([]string(nil), s.summary.ChangedFiles...)
	}
	m.completeRequestLocked(req, outcome, err, s.summary.ChangedFiles)
	s.active = nil
	s.cancel = nil
	duration := time.Since(s.started)
	if err == nil {
		s.summary.Status = StatusCompleted
		s.summary.LastOutcome = truncateUTF8(strings.TrimSpace(fullOutcome), maxHandoffBytes)
		s.summary.Error = ""
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		s.summary.Status = StatusCancelled
		s.summary.Error = ""
		if errors.Is(err, context.DeadlineExceeded) {
			s.summary.Error = "Agent consultation timed out"
		}
	} else {
		// Provider, network, and tool errors are routinely transient. Keep the
		// error for the tab and roster, but leave every agent ready for its next
		// prompt without requiring a session reset.
		s.summary.Status = StatusIdle
		s.summary.Error = truncateUTF8(err.Error(), 1024)
	}
	s.summary.QueueDepth = len(s.queue)
	m.emitLocked(s.summary, duration)
	for !m.shutdown && len(s.queue) > 0 {
		next := s.queue[0]
		s.queue = s.queue[1:]
		s.summary.QueueDepth = len(s.queue)
		if !next.deadline.IsZero() && !time.Now().Before(next.deadline) {
			m.completeRequestLocked(next, "", context.DeadlineExceeded, nil)
			continue
		}
		m.startRequestLocked(id, s, next)
		break
	}
	m.mu.Unlock()
}

func (m *AgentManager) storeResultLocked(req *promptRequest, result PromptResult, err error) {
	req.result = result
	req.err = err
	m.results[req.id] = req
	m.resultOrder = append(m.resultOrder, req.id)
	for len(m.resultOrder) > m.resultLimit {
		delete(m.results, m.resultOrder[0])
		m.resultOrder = m.resultOrder[1:]
	}
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
	m.closed[id] = struct{}{}
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
		for _, s := range m.sessions {
			for _, req := range s.queue {
				m.completeRequestLocked(req, "", context.Canceled, nil)
			}
			s.queue = nil
			s.summary.QueueDepth = 0
		}
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
		case "create_agent":
			return t.createAgent(ctx, call.Arguments)
		case "delegate_task":
			return t.delegate(ctx, call.Arguments)
		case "search_agent_work":
			return t.searchWork(call.Arguments)
		case "consult_agents":
			return t.consult(ctx, call.Arguments)
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

func (t *managedToolset) createAgent(ctx context.Context, arguments json.RawMessage) (llm.ToolResult, error) {
	var args struct {
		Model string `json:"model"`
		Task  string `json:"task"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid create_agent arguments: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return llm.ToolResult{}, err
	}
	model := strings.TrimSpace(args.Model)
	if model == "" {
		summary, err := t.manager.Summary(t.id)
		if err != nil {
			return llm.ToolResult{}, err
		}
		model = summary.Model
	}
	summary, err := t.manager.CreateInherited(t.id, model)
	if err != nil {
		return llm.ToolResult{}, err
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return llm.ToolResult{Output: fmt.Sprintf("created agent %s using model %q; no task assigned", summary.ID, summary.Model)}, nil
	}
	submission, err := t.manager.Submit(summary.ID, task)
	if err != nil {
		return llm.ToolResult{
			Output: fmt.Sprintf("created agent %s using model %q, but task assignment failed; the agent remains idle and can be retried with delegate_task", summary.ID, summary.Model),
		}, fmt.Errorf("assign task to newly created agent %s: %w", summary.ID, err)
	}
	return llm.ToolResult{Output: fmt.Sprintf("created agent %s using model %q and accepted task asynchronously; request_id=%s; queue_position=%d", summary.ID, summary.Model, submission.RequestID, submission.QueuePosition)}, nil
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
	submission, err := t.manager.Submit(args.AgentID, args.Prompt)
	if err != nil {
		return llm.ToolResult{}, err
	}
	return llm.ToolResult{Output: fmt.Sprintf("task accepted by %s; request_id=%s; queue_position=%d", args.AgentID, submission.RequestID, submission.QueuePosition)}, nil
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
		{Name: "search_agent_work", Description: prompt.SearchAgentWorkTool, Parameters: object(map[string]any{"query": stringField(prompt.AgentWorkQueryParameter), "agent_id": stringField(prompt.AgentIDParameter), "offset": map[string]any{"type": "integer", "minimum": 0}}, "query")},
		{Name: "consult_agents", Description: prompt.ConsultAgentsTool, Parameters: object(map[string]any{"requests": map[string]any{"type": "array", "minItems": 1, "maxItems": DefaultMaxAgents - 1, "items": object(map[string]any{"agent_id": stringField(prompt.AgentIDParameter), "prompt": stringField(prompt.AgentPromptParameter)}, "agent_id", "prompt")}}, "requests")},
		{Name: "list_agents", Description: prompt.ListAgentsTool, Parameters: object(nil)},
		{Name: "create_agent", Description: prompt.CreateAgentTool, Parameters: object(map[string]any{"model": stringField(prompt.AgentModelParameter), "task": stringField(prompt.AgentPromptParameter)})},
		{Name: "delegate_task", Description: prompt.DelegateTaskTool, Parameters: object(map[string]any{"agent_id": stringField(prompt.AgentIDParameter), "prompt": stringField(prompt.AgentPromptParameter)}, "agent_id", "prompt")},
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
