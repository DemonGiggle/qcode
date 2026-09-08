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
	// Barrier is acknowledged after earlier presentation events are drained.
	Barrier chan struct{}
}
