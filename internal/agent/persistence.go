package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/session"
)

// SavedMessage explicitly includes images, which are omitted by the wire message type.
type SavedMessage struct {
	llm.Message
	Images    []llm.Image
	ToolCalls []SavedToolCall `json:"tool_calls,omitempty"`
}

// Arguments are bytes rather than RawMessage so even a malformed provider
// tool call can be saved alongside the error it produced.
type SavedToolCall struct {
	ID, Name  string
	Arguments []byte
}

type SavedState struct {
	ProviderSession                                 string
	PendingImages                                   []llm.Image
	Provider, Model, Endpoint, System               string
	Messages                                        []SavedMessage
	LastResponse                                    string
	ContextWindow, ContextOverride, ContextMessages int
	ContextUsage                                    *llm.Usage
	Usage                                           llm.SessionUsage
	Remaining                                       int
	Known, Estimated                                bool
	AutoCompact                                     bool
	AutoCompactThreshold, MaxSteps                  int
	LearningContext, LearningSessionID              string
	LearningBudget                                  int
	Skills                                          []prompt.SkillSummary
	Tools                                           json.RawMessage
}

type persistentTools interface {
	SaveTools() json.RawMessage
	RestoreTools(json.RawMessage) error
}

func (t *managedToolset) SaveTools() json.RawMessage {
	if base, ok := t.base.(persistentTools); ok {
		return base.SaveTools()
	}
	return nil
}
func (t *managedToolset) RestoreTools(data json.RawMessage) error {
	if base, ok := t.base.(persistentTools); ok {
		return base.RestoreTools(data)
	}
	return nil
}

func (t *managedToolset) RestoreWarnings() []string {
	if base, ok := t.base.(interface{ RestoreWarnings() []string }); ok {
		return base.RestoreWarnings()
	}
	return nil
}
func (a *Agent) RestoreWarnings() []string {
	if base, ok := a.tools.(interface{ RestoreWarnings() []string }); ok {
		return base.RestoreWarnings()
	}
	return nil
}

// publishCheckpoint runs only on the agent's state-owning goroutine. Readers
// receive immutable JSON and never access the live conversation.
func (a *Agent) publishCheckpoint() {
	s := SavedState{Provider: a.provider.Name(), Model: a.model, Endpoint: a.endpoint, System: a.system,
		ContextWindow: a.contextWindow, ContextOverride: a.contextOverride, ContextMessages: a.contextMessages,
		ContextUsage: a.contextUsage, AutoCompact: a.autoCompact, AutoCompactThreshold: a.autoCompactThreshold,
		MaxSteps: a.maxSteps, LearningContext: a.learningContext, LearningSessionID: a.learningSessionID,
		LearningBudget: a.learningBudget, Skills: a.selectedSkills, PendingImages: a.pendingImages}
	a.stateMu.RLock()
	s.LastResponse, s.Usage = a.lastResponse, a.sessionUsage
	a.stateMu.RUnlock()
	if c := a.contextStatus.Load(); c != nil {
		s.Remaining, s.Known, s.Estimated = c.remaining, c.known, c.estimated
	}
	for _, m := range a.messages {
		saved := SavedMessage{Message: m, Images: m.Images}
		for _, call := range m.ToolCalls {
			saved.ToolCalls = append(saved.ToolCalls, SavedToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		s.Messages = append(s.Messages, saved)
	}
	if t, ok := a.tools.(persistentTools); ok {
		s.Tools = t.SaveTools()
	}
	if p, ok := a.provider.(interface{ SessionIdentity() string }); ok {
		s.ProviderSession = p.SessionIdentity()
	}
	data, err := json.Marshal(s)
	if err == nil {
		a.checkpoint.Store(&data)
	}
}

func (a *Agent) SetEndpoint(endpoint string) { a.endpoint = endpoint; a.publishCheckpoint() }

func (a *Agent) RestoreState(data json.RawMessage) error {
	var s SavedState
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s.Provider != a.provider.Name() || s.Model != a.model || s.Model == "" || len(s.Messages) == 0 || s.Messages[0].Role != "system" || s.MaxSteps < 1 || s.Remaining < 0 || s.Remaining > 100 {
		return fmt.Errorf("incompatible agent state")
	}
	if p, ok := a.provider.(interface{ RestoreSessionIdentity(string) error }); ok {
		if err := p.RestoreSessionIdentity(s.ProviderSession); err != nil {
			return err
		}
	}
	if t, ok := a.tools.(persistentTools); ok && len(s.Tools) > 0 {
		if err := t.RestoreTools(s.Tools); err != nil {
			return err
		}
	}
	a.messages = nil
	for _, m := range s.Messages {
		m.Message.Images = m.Images
		m.Message.ToolCalls = nil
		for _, call := range m.ToolCalls {
			m.Message.ToolCalls = append(m.Message.ToolCalls, llm.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		a.messages = append(a.messages, m.Message)
	}
	a.endpoint, a.system, a.lastResponse = s.Endpoint, s.System, s.LastResponse
	a.contextWindow, a.contextOverride, a.contextMessages = s.ContextWindow, s.ContextOverride, s.ContextMessages
	a.contextUsage, a.sessionUsage = s.ContextUsage, s.Usage
	a.autoCompact, a.autoCompactThreshold, a.maxSteps = s.AutoCompact, s.AutoCompactThreshold, s.MaxSteps
	a.learningContext, a.learningSessionID, a.learningBudget = s.LearningContext, s.LearningSessionID, s.LearningBudget
	a.selectedSkills = s.Skills
	a.pendingImages = s.PendingImages
	a.contextStatus.Store(&contextStatus{remaining: s.Remaining, known: s.Known, estimated: s.Estimated})
	a.publishCheckpoint()
	return nil
}

// Repair unfinished calls only when the user elects to run another turn.
func (a *Agent) repairInterruptedCalls() {
	var repaired []llm.Message
	for i, m := range a.messages {
		if m.Role == "tool" {
			continue
		}
		repaired = append(repaired, m)
		for _, call := range m.ToolCalls {
			result := llm.Message{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: "Interrupted before a result was recorded; execution outcome is unknown. Inspect current state before retrying."}
			for j := i + 1; j < len(a.messages) && a.messages[j].Role == "tool"; j++ {
				if a.messages[j].ToolCallID == call.ID {
					result = a.messages[j]
					break
				}
			}
			repaired = append(repaired, result)
		}
	}
	a.messages = repaired
	if len(a.pendingImages) > 0 {
		a.messages = append(a.messages, llm.Message{Role: "user", Content: "Image data loaded by the requested tool calls.", Images: a.pendingImages})
		a.pendingImages = nil
	}
}

func (m *AgentManager) SaveAgents() ([]session.SavedAgent, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []session.SavedAgent
	for _, id := range m.order {
		s := m.sessions[id]
		if data := s.runner.checkpoint.Load(); data != nil {
			summary := cloneSummary(s.summary)
			var identity struct{ Model string }
			_ = json.Unmarshal(*data, &identity)
			summary.Model = identity.Model
			result = append(result, session.SavedAgent{Summary: summary, State: append(json.RawMessage(nil), (*data)...)})
		}
	}
	return result, m.nextID
}

// FlushEvents requires an active presentation consumer and an idle manager.
// The UI calls it before saving a session it is about to leave.
func (m *AgentManager) FlushEvents() {
	done := make(chan struct{})
	m.events <- session.Event{Barrier: done}
	<-done
}

// RestoreAgents is used on a detached manager before any tasks or event readers start.
func (m *AgentManager) RestoreAgents(saved []session.SavedAgent, nextID int) error {
	if nextID < 0 {
		return fmt.Errorf("invalid next agent ID")
	}
	seen := map[string]bool{}
	for _, item := range saved {
		if item.Summary.ID == "" || seen[item.Summary.ID] {
			return fmt.Errorf("invalid agent IDs")
		}
		seen[item.Summary.ID] = true
		if item.Summary.ID != "main" {
			n, err := strconv.Atoi(strings.TrimPrefix(item.Summary.ID, "agent-"))
			if err != nil || n < 1 || n > nextID || item.Summary.ID != fmt.Sprintf("agent-%d", n) {
				return fmt.Errorf("invalid saved agent ID")
			}
		}
	}
	if !seen["main"] || len(saved) > m.max {
		return fmt.Errorf("invalid saved agent roster")
	}
	for _, item := range saved {
		if _, err := m.create(item.Summary.ID, item.Summary.Name, item.Summary.Model, item.Summary.ID == "main"); err != nil {
			return err
		}
		s := m.sessions[item.Summary.ID]
		if err := s.runner.RestoreState(item.State); err != nil {
			return err
		}
		s.summary = item.Summary
		if s.summary.Status == StatusRunning || s.summary.Status == StatusWaitingForApproval {
			s.summary.Status = StatusCancelled
			s.summary.Error = "Interrupted when the previous process ended"
		}
		s.summary.QueueDepth = 0
	}
	m.nextID = nextID
	return nil
}
