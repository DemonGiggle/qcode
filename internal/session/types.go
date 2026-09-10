// Package session defines the shared, implementation-neutral contract between
// multi-agent orchestration and terminal presentation.
package session

import "time"

type Status string

const (
	StatusIdle               Status = "idle"
	StatusRunning            Status = "running"
	StatusWaitingForApproval Status = "waiting-for-approval"
	StatusCompleted          Status = "completed"
	StatusFailed             Status = "failed"
	StatusCancelled          Status = "cancelled"
)

type Summary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Model        string   `json:"model"`
	Status       Status   `json:"status"`
	QueueDepth   int      `json:"queue_depth,omitempty"`
	CurrentTask  string   `json:"current_task,omitempty"`
	LastOutcome  string   `json:"last_outcome,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// Submission identifies an accepted prompt and its position behind the
// currently running prompt. QueuePosition is zero when it starts immediately.
type Submission struct {
	RequestID     string `json:"request_id"`
	TargetID      string `json:"target_id"`
	QueuePosition int    `json:"queue_position"`
}

// PromptResult is the complete, untruncated result of one submitted prompt.
type PromptResult struct {
	RequestID string `json:"request_id"`
	TargetID  string `json:"target_id"`
	Response  string `json:"response,omitempty"`
}

type Event struct {
	Agent    Summary
	Duration time.Duration
	// Consultation wakes consumers to read the durable consultation event log.
	Consultation bool
	// Barrier is acknowledged after earlier presentation events are drained.
	Barrier chan struct{}
}

// WorkRecord retains one request independently of conversation compaction,
// result-cache eviction, and closing an agent tab.
type WorkRecord struct {
	RequestID    string    `json:"request_id"`
	AgentID      string    `json:"agent_id"`
	AgentName    string    `json:"agent_name"`
	Model        string    `json:"model"`
	Prompt       string    `json:"prompt"`
	Response     string    `json:"response,omitempty"`
	ChangedFiles []string  `json:"changed_files,omitempty"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	Created      time.Time `json:"created"`
	Started      time.Time `json:"started,omitempty"`
	Finished     time.Time `json:"finished,omitempty"`
	Consultation bool      `json:"consultation,omitempty"`
}

type ConsultationEvent struct {
	Sequence  uint64        `json:"sequence"`
	AgentID   string        `json:"agent_id"`
	RequestID string        `json:"request_id,omitempty"`
	Status    string        `json:"status"`
	Error     string        `json:"error,omitempty"`
	Time      time.Time     `json:"time"`
	Elapsed   time.Duration `json:"elapsed_ns"`
}

type WorkHistory struct {
	NextRequestID uint64
	Records       []WorkRecord
	Events        []ConsultationEvent
}
