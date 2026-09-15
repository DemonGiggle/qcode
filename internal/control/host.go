package control

import (
	"context"
	"sync"
	"time"

	"qcode/internal/agent"
	"qcode/internal/session"
)

const legacyEventBuffer = 32

type Snapshot struct {
	Sequence     uint64            `json:"sequence"`
	Agents       []session.Summary `json:"agents"`
	Interactions []Interaction     `json:"interactions,omitempty"`
}

// Host is the single in-process application runtime shared by every control
// surface. AgentManager remains responsible for agent execution; Host adds
// transport-neutral snapshots, broadcast events, and human interactions.
type Host struct {
	manager      *agent.AgentManager
	events       *eventBus
	interactions *InteractionBroker
	legacyEvents chan session.Event
	done         chan struct{}
	shutdownOnce sync.Once
}

func NewHost(ctx context.Context, maxAgents int) *Host {
	if ctx == nil {
		ctx = context.Background()
	}
	manager := agent.NewAgentManager(ctx, maxAgents)
	host := &Host{
		manager:      manager,
		events:       newEventBus(defaultEventHistory),
		legacyEvents: make(chan session.Event, legacyEventBuffer),
		done:         make(chan struct{}),
	}
	host.interactions = newInteractionBroker(
		func(interaction Interaction) {
			host.events.publish(Event{Type: EventInteractionRequested, Interaction: &interaction})
		},
		func(resolution Resolution) {
			host.events.publish(Event{Type: EventInteractionResolved, Resolution: &resolution})
		},
	)
	go host.forwardEvents()
	return host
}

func (h *Host) forwardEvents() {
	defer close(h.done)
	defer close(h.legacyEvents)
	defer h.events.close()
	for event := range h.manager.Events() {
		if event.Barrier != nil {
			// Barriers belong to the one local presentation consumer. They are
			// deliberately excluded from the broadcast transport contract.
			h.legacyEvents <- event
			continue
		}
		eventType := EventAgentChanged
		if event.Consultation {
			eventType = EventConsultationChanged
		}
		summary := event.Agent
		h.events.publish(Event{
			Type: eventType, Agent: &summary, Duration: event.Duration,
			Consultation: event.Consultation, Time: time.Now().UTC(),
		})
		select {
		case h.legacyEvents <- event:
		default:
		}
	}
}

// Events preserves the current TUI presentation contract. New adapters should
// use Subscribe so each client receives an independent cursor.
func (h *Host) Events() <-chan session.Event { return h.legacyEvents }

func (h *Host) Subscribe(ctx context.Context, after uint64) *Subscription {
	return h.events.subscribe(ctx, after)
}

// Snapshot is intentionally sampled after the event cursor. Concurrent state
// changes may therefore be replayed twice, but cannot be missed by a client
// that subscribes after Sequence; applying agent snapshots is idempotent.
func (h *Host) Snapshot() Snapshot {
	sequence := h.events.sequence()
	return Snapshot{Sequence: sequence, Agents: h.List(), Interactions: h.interactions.Pending()}
}

func (h *Host) Interactions() *InteractionBroker { return h.interactions }

func (h *Host) BeginInteraction(interaction session.Interaction) (session.InteractionWaiter, error) {
	return h.interactions.Begin(interaction)
}

func (h *Host) ResolveInteraction(resolution session.Resolution) error {
	return h.interactions.Resolve(resolution)
}

func (h *Host) PendingInteractions() []session.Interaction { return h.interactions.Pending() }

func (h *Host) SetFactory(factory agent.SessionFactory) { h.manager.SetFactory(factory) }

func (h *Host) SetConsultationTimeout(timeout time.Duration) error {
	return h.manager.SetConsultationTimeout(timeout)
}

func (h *Host) CreateMain(model string) (session.Summary, error) { return h.manager.CreateMain(model) }
func (h *Host) Create(model string) (session.Summary, error)     { return h.manager.Create(model) }
func (h *Host) List() []session.Summary                          { return h.manager.List() }
func (h *Host) Summary(id string) (session.Summary, error)       { return h.manager.Summary(id) }
func (h *Host) Agent(id string) (*agent.Agent, bool)             { return h.manager.Agent(id) }
func (h *Host) Runner(id string) (any, bool)                     { return h.manager.Runner(id) }

func (h *Host) Start(id, task string) error { return h.manager.Start(id, task) }
func (h *Host) Submit(id, task string) (session.Submission, error) {
	return h.manager.Submit(id, task)
}
func (h *Host) SubmitAndWait(ctx context.Context, id, task string) (session.PromptResult, error) {
	return h.manager.SubmitAndWait(ctx, id, task)
}
func (h *Host) GetResult(requestID string) (session.PromptResult, error) {
	return h.manager.GetResult(requestID)
}

func (h *Host) Rename(id, name string) error       { return h.manager.Rename(id, name) }
func (h *Host) Cancel(id string) error             { return h.manager.Cancel(id) }
func (h *Host) Close(id string) error              { return h.manager.Close(id) }
func (h *Host) Reset(id string) error              { return h.manager.Reset(id) }
func (h *Host) UpdateModel(id, model string) error { return h.manager.UpdateModel(id, model) }
func (h *Host) SetWaitingForApproval(id string, waiting bool) {
	h.manager.SetWaitingForApproval(id, waiting)
}

func (h *Host) WrapToolset(id string, tools agent.Toolset, main bool) agent.Toolset {
	return h.manager.WrapToolset(id, tools, main)
}
func (h *Host) AgentKnowledge(id string) string { return h.manager.AgentKnowledge(id) }

func (h *Host) SaveAgents() ([]session.SavedAgent, int) { return h.manager.SaveAgents() }
func (h *Host) SaveSessionState() ([]session.SavedAgent, int, *session.WorkHistory) {
	return h.manager.SaveSessionState()
}
func (h *Host) SaveWorkHistory() *session.WorkHistory { return h.manager.SaveWorkHistory() }
func (h *Host) RestoreAgents(saved []session.SavedAgent, nextID int) error {
	return h.manager.RestoreAgents(saved, nextID)
}
func (h *Host) RestoreWorkHistory(saved *session.WorkHistory) error {
	return h.manager.RestoreWorkHistory(saved)
}
func (h *Host) ConsultationEvents(after uint64) []session.ConsultationEvent {
	return h.manager.ConsultationEvents(after)
}
func (h *Host) FlushEvents() { h.manager.FlushEvents() }

func (h *Host) Shutdown() {
	h.shutdownOnce.Do(func() {
		h.manager.Shutdown()
		<-h.done
	})
}
