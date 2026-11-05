package agent

import (
	"errors"
	"fmt"
	"strings"

	"qcode/internal/llm"
	"qcode/internal/session"
)

// Immutable view for concurrent catalog/status readers. Execution still owns the
// mutable conversation; a switch is only permitted while execution is idle.
type backendSnapshot struct {
	provider        llm.Provider
	model, thinking string
}

var ErrStaleConfiguration = errors.New("agent configuration changed")

// CreationBackend selects a backend, not a provider instance. Factories must
// construct a fresh provider (and provider session identity) for each agent.
type CreationBackend = session.CreationBackend

// ProviderSessionFactory is optional; SessionFactory remains the default path,
// including restore factories that select backends from saved agent states.
type ProviderSessionFactory func(id, name, model string, main bool, backend CreationBackend) (*Agent, error)

func (m *AgentManager) SetProviderFactory(factory ProviderSessionFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.providerFactory = factory
}

// CreateWithProvider creates an independent agent using the selected backend.
func (m *AgentManager) CreateWithProvider(model string, backend CreationBackend) (AgentSummary, error) {
	return m.createNew(model, nil, backend)
}

// BackendSwitch contains an externally prepared provider and discovered capacity.
// ContextCapacity is zero when unknown. No discovery is performed by SwitchBackend.
// Provider thinking capabilities must already be available locally.
type BackendSwitch = session.BackendSwitch

// ConfigurationRevision is the optimistic concurrency token for backend pickers.
// Legacy setters also advance it; live callers must serialize setters via Configure.
func (m *AgentManager) ConfigurationRevision(id string) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if _, closed := m.closed[id]; closed || s != nil && s.summary.Status == StatusClosing {
		return 0, fmt.Errorf("%w %q", ErrClosedAgent, id)
	}
	if s == nil {
		return 0, fmt.Errorf("%w %q", ErrUnknownAgent, id)
	}
	return s.runner.configurationRevision.Load(), nil
}

// SwitchBackend atomically replaces settings on the existing Agent, never its
// conversation or capabilities. Read the revision BEFORE external discovery;
// start, close, and configuration changes during discovery are checked here.
// Completed/failed/cancelled agents are idle for this purpose when no work remains.
func (m *AgentManager) SwitchBackend(id string, expectedRevision uint64, next BackendSwitch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return ErrManagerClosed
	}
	s := m.sessions[id]
	if _, closed := m.closed[id]; closed || s != nil && s.summary.Status == StatusClosing {
		return fmt.Errorf("%w %q", ErrClosedAgent, id)
	}
	if s == nil {
		return fmt.Errorf("%w %q", ErrUnknownAgent, id)
	}
	if s.active != nil || len(s.queue) != 0 || s.summary.Status == StatusRunning || s.summary.Status == StatusWaitingForApproval {
		return fmt.Errorf("agent %q is not idle", id)
	}
	a := s.runner
	if a.configurationRevision.Load() != expectedRevision {
		return ErrStaleConfiguration
	}
	if next.Provider == nil || strings.TrimSpace(next.Model) == "" || next.ContextCapacity < 0 {
		return fmt.Errorf("invalid backend settings")
	}
	level := strings.ToLower(strings.TrimSpace(next.Thinking))
	if level != "" {
		p, ok := next.Provider.(llm.ThinkingProvider)
		if !ok {
			return fmt.Errorf("thinking level %q is not supported by model %q", level, next.Model)
		}
		capability := p.ThinkingCapability(next.Model)
		if !capability.Adjustable || !thinkingLevelAllowed(capability, level) {
			return fmt.Errorf("thinking level %q is not supported by model %q", level, next.Model)
		}
	}
	a.provider, a.endpoint, a.model, a.thinking = next.Provider, next.Endpoint, next.Model, level
	a.contextWindow, a.contextOverride = next.ContextCapacity, 0
	a.contextUsage, a.contextMessages = nil, 0
	a.invalidatePreparedCache()
	a.configurationRevision.Add(1)
	s.summary.Model = next.Model
	a.publishContext()
	a.publishCheckpoint()
	// Refresh presentation without reporting task completion. A plain status
	// event would make handleAgentEvent print the idle/completed status (e.g.
	// "idle") into the transcript; DirectoryChanged refreshes tab/status bars
	// while skipping completion output, matching Reset.
	select {
	case m.events <- ManagerEvent{Agent: cloneSummary(s.summary), DirectoryChanged: true}:
	default:
	}
	return nil
}

// Backend returns only the non-secret identity needed to create an independent
// agent. It is safe to sample while the parent is running.
func (m *AgentManager) Backend(id string) (CreationBackend, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if s == nil || s.summary.Status == StatusClosing {
		return CreationBackend{}, fmt.Errorf("agent %q is unavailable", id)
	}
	return CreationBackend{Provider: s.runner.provider.Name(), Endpoint: s.runner.endpoint}, nil
}
