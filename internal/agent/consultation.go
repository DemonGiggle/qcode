package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
)

// DefaultConsultationTimeout includes time spent behind an agent's existing work.
const DefaultConsultationTimeout = 5 * time.Minute

type ConsultationRequest struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

type ConsultationReply struct {
	AgentID   string `json:"agent_id"`
	RequestID string `json:"request_id,omitempty"`
	Status    string `json:"status"`
	Response  string `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (m *AgentManager) SetConsultationTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("agent timeout must be positive")
	}
	m.mu.Lock()
	m.consultationTimeout = timeout
	m.mu.Unlock()
	return nil
}

// Consult dispatches the entire batch before waiting. Errors belong to individual
// replies; only caller cancellation or manager shutdown fails the overall call.
func (m *AgentManager) Consult(ctx context.Context, requests []ConsultationRequest) ([]ConsultationReply, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(requests) == 0 || len(requests) > DefaultMaxAgents-1 {
		return nil, fmt.Errorf("consultation requires between 1 and %d requests", DefaultMaxAgents-1)
	}
	seen := map[string]bool{}
	for _, request := range requests {
		if request.AgentID == "" || request.AgentID == "main" || strings.TrimSpace(request.Prompt) == "" || seen[request.AgentID] {
			return nil, fmt.Errorf("consultation requires distinct non-main agent IDs and non-empty prompts")
		}
		seen[request.AgentID] = true
	}
	m.mu.Lock()
	if m.shutdown {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	timeout := m.consultationTimeout
	// External callers may consult as well as the running main agent. Register
	// with Shutdown before spawning waiters or publishing events.
	m.wg.Add(1)
	m.mu.Unlock()
	defer m.wg.Done()
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := waitCtx.Deadline()
	pending := make([]*promptRequest, len(requests))
	replies := make([]ConsultationReply, len(requests))
	for i, request := range requests {
		replies[i].AgentID = request.AgentID
		var err error
		if err = waitCtx.Err(); err == nil {
			pending[i], _, err = m.submitRequest(request.AgentID, request.Prompt, deadline)
		}
		if err != nil {
			replies[i].Status, replies[i].Error = requestStatus(err), err.Error()
			m.mu.Lock()
			m.recordConsultationEventLocked(session.ConsultationEvent{AgentID: request.AgentID, Status: replies[i].Status, Error: err.Error()})
			m.mu.Unlock()
		}
	}
	var waiters sync.WaitGroup
	for i, req := range pending {
		if req == nil {
			continue
		}
		waiters.Add(1)
		go func(i int, req *promptRequest) {
			defer waiters.Done()
			select {
			case <-req.done:
			case <-waitCtx.Done():
				m.abortRequest(req, waitCtx.Err())
			case <-m.ctx.Done():
				m.abortRequest(req, context.Canceled)
			}
			// Completion, expiry, and cancellation all close done exactly once.
			<-req.done
			replies[i].RequestID = req.id
			replies[i].Status = requestStatus(req.err)
			if req.err != nil {
				replies[i].Error = req.err.Error()
			} else {
				replies[i].Response = truncateUTF8(req.result.Response, maxHandoffBytes)
			}
		}(i, req)
	}
	waiters.Wait()
	if ctx.Err() != nil {
		return replies, ctx.Err()
	}
	if m.ctx.Err() != nil {
		return replies, ErrManagerClosed
	}
	return replies, nil
}

// abortRequest never cancels whichever other request happens to be active in
// this agent. An uncooperative running provider keeps its slot until it exits;
// its late output cannot overwrite the expired request's recorded outcome.
func (m *AgentManager) abortRequest(req *promptRequest, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.finished {
		return
	}
	s := m.sessions[req.targetID]
	if s == nil {
		return
	}
	if s.active == req {
		req.abortErr = err
		m.completeRequestLocked(req, "", err, s.summary.ChangedFiles)
		s.cancel()
		return
	}
	for i, queued := range s.queue {
		if queued == req {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			s.summary.QueueDepth = len(s.queue)
			m.completeRequestLocked(req, "", err, nil)
			m.emitLocked(s.summary, 0)
			return
		}
	}
}

func requestStatus(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "failed"
	}
}

func (m *AgentManager) completeRequestLocked(req *promptRequest, response string, err error, files []string) {
	if req.finished {
		return
	}
	req.finished = true
	work := &m.work[req.journalIndex]
	work.Status, work.Finished = requestStatus(err), time.Now().UTC()
	work.ChangedFiles = append([]string(nil), files...)
	if err == nil {
		work.Response = response
	} else {
		response = "" // A failed or cancelled request has no usable final reply.
		work.Error = err.Error()
	}
	m.storeResultLocked(req, PromptResult{RequestID: req.id, TargetID: req.targetID, Response: response}, err)
	if work.Consultation {
		m.recordConsultationEventLocked(session.ConsultationEvent{
			AgentID: work.AgentID, RequestID: work.RequestID, Status: work.Status,
			Error: work.Error, Elapsed: work.Finished.Sub(work.Created),
		})
	}
	close(req.done)
}

func (m *AgentManager) recordConsultationEventLocked(event session.ConsultationEvent) {
	event.Sequence = uint64(len(m.consultationEvents)) + 1
	event.Time = time.Now().UTC()
	m.consultationEvents = append(m.consultationEvents, event)
	// The channel is only a wakeup. Consumers replay the durable log after
	// every event, so saturation cannot lose consultation outcomes.
	select {
	case m.events <- ManagerEvent{Consultation: true}:
	default:
	}
}

func (m *AgentManager) ConsultationEvents(after uint64) []session.ConsultationEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if after >= uint64(len(m.consultationEvents)) {
		return nil
	}
	return append([]session.ConsultationEvent(nil), m.consultationEvents[after:]...)
}

func (t *managedToolset) consult(ctx context.Context, arguments json.RawMessage) (llm.ToolResult, error) {
	var args struct {
		Requests []ConsultationRequest `json:"requests"`
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return llm.ToolResult{}, fmt.Errorf("invalid consult_agents arguments: %w", err)
	}
	replies, err := t.manager.Consult(ctx, args.Requests)
	if err != nil {
		return llm.ToolResult{}, err
	}
	data, _ := json.Marshal(replies)
	return llm.ToolResult{Output: string(data)}, nil
}
