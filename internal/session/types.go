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
	CurrentTask  string   `json:"current_task,omitempty"`
	LastOutcome  string   `json:"last_outcome,omitempty"`
	ChangedFiles []string `json:"changed_files,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type Event struct {
	Agent    Summary
	Duration time.Duration
}
