// Package session defines the shared, implementation-neutral contract between
// multi-agent orchestration and terminal presentation.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrInteractionWithdrawn = errors.New("interaction withdrawn by user steering")

type InteractionKind string

const (
	InteractionDirectoryApproval InteractionKind = "directory_approval"
	InteractionQuestions         InteractionKind = "questions"
	InteractionPlanDecision      InteractionKind = "plan_decision"
	InteractionSkillPlanDecision InteractionKind = "skill_plan_decision"
	InteractionLearningApproval  InteractionKind = "learning_approval"
)

type Interaction struct {
	TaskID     string          `json:"task_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	Kind       InteractionKind `json:"kind"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Resolution struct {
	Withdrawn     bool            `json:"withdrawn,omitempty"`
	InteractionID string          `json:"interaction_id"`
	Value         json.RawMessage `json:"value,omitempty"`
	ResolvedBy    string          `json:"resolved_by,omitempty"`
}

type InteractionWaiter interface {
	InteractionInfo() Interaction
	Wait(context.Context) (Resolution, error)
}

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
	ActiveTaskID string    `json:"active_task_id,omitempty"`
	Progress     *Progress `json:"progress,omitempty"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	Status       Status    `json:"status"`
	QueueDepth   int       `json:"queue_depth,omitempty"`
	CurrentTask  string    `json:"current_task,omitempty"`
	LastOutcome  string    `json:"last_outcome,omitempty"`
	ChangedFiles []string  `json:"changed_files,omitempty"`
	Error        string    `json:"error,omitempty"`
}

// Progress describes the current model operation, independently of task status.
// Summary contains safe presentation text, never tool arguments or output.
type Progress struct {
	Summary   string    `json:"summary"`
	StartedAt time.Time `json:"started_at"`
}

// Elapsed measures time in the current operation, rounded down to seconds.
func (p Progress) Elapsed(now time.Time) time.Duration {
	if p.StartedAt.IsZero() || now.Before(p.StartedAt) {
		return 0
	}
	return now.Sub(p.StartedAt).Truncate(time.Second)
}

// Submission identifies an accepted prompt and its position behind the
// currently running prompt. QueuePosition is zero when it starts immediately.
type Submission struct {
	Intent        SubmissionIntent `json:"intent,omitempty"`
	State         InputState       `json:"state,omitempty"`
	ParentTaskID  string           `json:"parent_task_id,omitempty"`
	RequestID     string           `json:"request_id"`
	TargetID      string           `json:"target_id"`
	QueuePosition int              `json:"queue_position"`
}

// QueuedPrompt is a pending request in an agent's FIFO queue.
type QueuedPrompt struct {
	Actor     string           `json:"actor,omitempty"`
	Intent    SubmissionIntent `json:"intent,omitempty"`
	State     InputState       `json:"state,omitempty"`
	Source    string           `json:"source,omitempty"`
	RequestID string           `json:"request_id"`
	Prompt    string           `json:"prompt"`
}

// PromptResult is the complete, untruncated result of one submitted prompt.
type PromptResult struct {
	RequestID string `json:"request_id"`
	TargetID  string `json:"target_id"`
	Response  string `json:"response,omitempty"`
}

// WorkActivity is a concise, user-facing tool event recorded for one request.
// Its summary matches the activity text shown in the terminal, not tool output.
type WorkActivity struct {
	Time    time.Time `json:"time"`
	Action  string    `json:"action"`
	Summary string    `json:"summary"`
	Success bool      `json:"success"`
}

const MaxWorkActivities = 256

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
	Source              string          `json:"source,omitempty"`
	Actor               string          `json:"actor,omitempty"`
	Steers              []SteeringInput `json:"steers,omitempty"`
	RequestID           string          `json:"request_id"`
	AgentID             string          `json:"agent_id"`
	AgentName           string          `json:"agent_name"`
	Model               string          `json:"model"`
	Prompt              string          `json:"prompt"`
	Response            string          `json:"response,omitempty"`
	ChangedFiles        []string        `json:"changed_files,omitempty"`
	Activities          []WorkActivity  `json:"activities,omitempty"`
	ActivitiesTruncated bool            `json:"activities_truncated,omitempty"`
	Status              string          `json:"status"`
	Error               string          `json:"error,omitempty"`
	Created             time.Time       `json:"created"`
	Started             time.Time       `json:"started,omitempty"`
	Finished            time.Time       `json:"finished,omitempty"`
	Consultation        bool            `json:"consultation,omitempty"`
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
	NextRequestID  uint64
	Records        []WorkRecord
	SteeringEvents []SteeringEvent `json:"steering_events,omitempty"`
	Events         []ConsultationEvent
}

// SubmissionIntent separates task FIFO work from input to the observed task.
type SubmissionIntent string

const (
	IntentAutomatic SubmissionIntent = "automatic"
	IntentSteer     SubmissionIntent = "steer"
	IntentQueue     SubmissionIntent = "queue"
)

type InputState string

const (
	InputRunning     InputState = "running"
	InputPending     InputState = "pending"
	InputReplanning  InputState = "replanning"
	InputDelivered   InputState = "delivered"
	InputSuperseded  InputState = "superseded"
	InputCancelled   InputState = "cancelled"
	InputInterrupted InputState = "interrupted"
)

type PromptSubmission struct {
	AgentID        string           `json:"agent_id"`
	ObservedTaskID string           `json:"observed_task_id,omitempty"`
	Prompt         string           `json:"prompt"`
	Intent         SubmissionIntent `json:"intent"`
	Source         string           `json:"source,omitempty"`
	Actor          string           `json:"actor,omitempty"`
}
type SteeringInput struct {
	ID           string     `json:"id"`
	ParentTaskID string     `json:"parent_task_id"`
	Text         string     `json:"text"`
	Source       string     `json:"source,omitempty"`
	Actor        string     `json:"actor,omitempty"`
	State        InputState `json:"state"`
	Created      time.Time  `json:"created"`
	Updated      time.Time  `json:"updated"`
	Replaces     string     `json:"replaces,omitempty"`
	ReplacedBy   string     `json:"replaced_by,omitempty"`
}
type SteeringEvent struct {
	Sequence uint64        `json:"sequence"`
	AgentID  string        `json:"agent_id"`
	Input    SteeringInput `json:"input"`
}
