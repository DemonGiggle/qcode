package agent

import (
	"context"
	"errors"
	"fmt"
	"qcode/internal/session"
	"strings"
	"time"
)

var ErrStaleTask = errors.New("observed task is no longer accepting steering")
var ErrSteeringUnavailable = errors.New("steering is unavailable during manual compaction")

type taskInput struct {
	manager    *AgentManager
	request    *promptRequest
	toolCallID string
	sealed     bool
}

// take arbitrates completion against acceptance under the same manager lock.
func (c *taskInput) take(final bool) *session.SteeringInput {
	if c == nil {
		return nil
	}
	m := c.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	work := &m.work[c.request.journalIndex]
	for i := range work.Steers {
		if work.Steers[i].State == session.InputPending {
			m.steerStateLocked(c.request, i, session.InputReplanning)
			item := work.Steers[i]
			return &item
		}
	}
	if final {
		c.sealed = true
	}
	return nil
}
func (c *taskInput) delivered(id string) {
	m := c.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, item := range m.work[c.request.journalIndex].Steers {
		if item.ID == id && item.State == session.InputReplanning {
			m.steerStateLocked(c.request, i, session.InputDelivered)
			return
		}
	}
}
func (m *AgentManager) steerStateLocked(req *promptRequest, index int, state session.InputState) {
	item := &m.work[req.journalIndex].Steers[index]
	item.State = state
	item.Updated = time.Now().UTC()
	m.steeringEvents = append(m.steeringEvents, session.SteeringEvent{Sequence: uint64(len(m.steeringEvents) + 1), AgentID: req.targetID, Input: *item})
	if s := m.sessions[req.targetID]; s != nil {
		if state == session.InputDelivered && !strings.HasPrefix(item.Text, "/") {
			s.latestPrompt = item.Text
		}
		m.emitLocked(s.summary, 0)
	}
}
func (m *AgentManager) cancelSteersLocked(req *promptRequest) {
	for i, item := range m.work[req.journalIndex].Steers {
		if item.State == session.InputPending || item.State == session.InputReplanning {
			m.steerStateLocked(req, i, session.InputCancelled)
		}
	}
}
func (m *AgentManager) SubmitPrompt(input session.PromptSubmission) (session.Submission, error) {
	if input.Intent != session.IntentAutomatic && input.Intent != session.IntentQueue && input.Intent != session.IntentSteer {
		return session.Submission{}, errors.New("invalid submission intent")
	}
	if strings.TrimSpace(input.Prompt) == "" {
		return session.Submission{}, errors.New("task must not be empty")
	}
	m.mu.Lock()
	s := m.sessions[input.AgentID]
	if m.shutdown {
		m.mu.Unlock()
		return session.Submission{}, ErrManagerClosed
	}
	if s == nil {
		m.mu.Unlock()
		return session.Submission{}, ErrUnknownAgent
	}
	steer := input.Intent == session.IntentSteer || input.Intent == session.IntentAutomatic && input.ObservedTaskID != ""
	if !steer || s.active == nil && input.ObservedTaskID == "" {
		if input.Intent == session.IntentAutomatic && s.active != nil {
			m.mu.Unlock()
			return session.Submission{}, ErrStaleTask
		}
		_, sub, err := m.submitRequestLocked(input.AgentID, strings.TrimSpace(input.Prompt), time.Time{}, false, input.Source, input.Actor)
		m.mu.Unlock()
		sub.Intent = session.IntentQueue
		sub.State = session.InputPending
		if sub.QueuePosition == 0 {
			sub.Intent = session.IntentAutomatic
			sub.State = session.InputRunning
		}
		return sub, err
	}
	req := s.active
	if req == nil || req.id != input.ObservedTaskID || req.input.sealed || req.finished {
		m.mu.Unlock()
		return session.Submission{}, ErrStaleTask
	}
	if req.compact {
		m.mu.Unlock()
		return session.Submission{}, ErrSteeringUnavailable
	}
	if m.nextRequestID == ^uint64(0) {
		m.mu.Unlock()
		return session.Submission{}, errors.New("prompt ID space exhausted")
	}
	m.nextRequestID++
	item := session.SteeringInput{ID: fmt.Sprintf("request-%d", m.nextRequestID), ParentTaskID: req.id, Text: strings.TrimSpace(input.Prompt), Source: input.Source, Actor: input.Actor, State: session.InputPending, Created: time.Now().UTC()}
	work := &m.work[req.journalIndex]
	for i := range work.Steers {
		if work.Steers[i].State == session.InputPending {
			item.Replaces = work.Steers[i].ID
			work.Steers[i].ReplacedBy = item.ID
			m.steerStateLocked(req, i, session.InputSuperseded)
		}
	}
	work.Steers = append(work.Steers, item)
	m.steerStateLocked(req, len(work.Steers)-1, session.InputPending)
	if m.onSteer != nil {
		m.onSteer(input.AgentID, req.id)
	}
	m.mu.Unlock()
	return session.Submission{RequestID: item.ID, TargetID: input.AgentID, Intent: session.IntentSteer, State: item.State, ParentTaskID: req.id}, nil
}
func (m *AgentManager) PendingInputs(id string) []session.QueuedPrompt {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[id]
	if s == nil {
		return nil
	}
	var items []session.QueuedPrompt
	if s.active != nil {
		for _, item := range m.work[s.active.journalIndex].Steers {
			if item.State == session.InputPending || item.State == session.InputReplanning {
				items = append(items, session.QueuedPrompt{RequestID: item.ID, Prompt: item.Text, Intent: session.IntentSteer, State: item.State, Source: item.Source, Actor: item.Actor})
			}
		}
	}
	for _, req := range s.queue {
		items = append(items, session.QueuedPrompt{RequestID: req.id, Prompt: req.prompt, Intent: session.IntentQueue, State: session.InputPending, Source: req.source, Actor: req.actor})
	}
	return items
}
func (m *AgentManager) CancelInput(id, inputID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrUnknownAgent
	}
	if s.active != nil {
		for i, item := range m.work[s.active.journalIndex].Steers {
			if item.ID == inputID {
				if item.State != session.InputPending {
					return ErrRequestPending
				}
				m.steerStateLocked(s.active, i, session.InputCancelled)
				return nil
			}
		}
	}
	for i, req := range s.queue {
		if req.id == inputID {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.summary.QueueDepth = len(s.queue)
			m.completeRequestLocked(req, "", context.Canceled, nil)
			m.emitLocked(s.summary, 0)
			return nil
		}
	}
	return ErrRequestNotFound
}
func (m *AgentManager) SteeringEvents(after uint64) []session.SteeringEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var events []session.SteeringEvent
	for _, event := range m.steeringEvents {
		if event.Sequence > after {
			events = append(events, event)
		}
	}
	return events
}

// InteractionScope orders opening an interaction against steering acceptance.
func (m *AgentManager) InteractionScope(id string, begin func(string, string) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.active == nil {
		return begin("", "")
	}
	for _, item := range m.work[s.active.journalIndex].Steers {
		if item.State == session.InputPending || item.State == session.InputReplanning {
			return session.ErrInteractionWithdrawn
		}
	}
	return begin(s.active.id, s.active.input.toolCallID)
}
func (m *AgentManager) SetSteeringObserver(observer func(string, string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSteer = observer
}

func (c *taskInput) setToolCall(id string) {
	if c == nil {
		return
	}
	c.manager.mu.Lock()
	c.toolCallID = id
	c.manager.mu.Unlock()
}
func (m *AgentManager) CancelTask(id, observedID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil {
		return ErrUnknownAgent
	}
	if s.active == nil || s.active.id != observedID || s.cancel == nil {
		return ErrStaleTask
	}
	s.cancel()
	return nil
}
