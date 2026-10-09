package agent

import (
	"time"

	"qcode/internal/redaction"
	"qcode/internal/session"
)

// trackProgress owns a task indicator unless an outer operation already does.
// Compaction can run both independently and inside a normal agent request.
func (a *Agent) trackProgress(summary string) func() {
	a.stateMu.Lock()
	previous := a.progress
	task := a.progressTask
	owned := task == nil
	if owned {
		task = a.trace.BeginTask()
		a.progressTask = task
	}
	a.stateMu.Unlock()
	a.reportProgress(summary)
	if summary != "" {
		task.Resume()
	}
	return func() {
		if owned {
			task.End()
			a.stateMu.Lock()
			a.progressTask = nil
			a.stateMu.Unlock()
		}
		a.publishProgress(previous)
	}
}

func (a *Agent) reportProgress(summary string) {
	if summary == "" {
		a.publishProgress(nil)
		return
	}
	a.publishProgress(&session.Progress{
		Summary: a.redaction.Text(redaction.Terminal, summary), StartedAt: time.Now().UTC(),
	})
}

func (a *Agent) publishProgress(progress *session.Progress) {
	a.stateMu.Lock()
	a.progress = progress
	task, recorder := a.progressTask, a.progressRecorder
	a.stateMu.Unlock()
	if task != nil {
		if progress == nil {
			task.Suspend()
		}
		task.SetProgress(progress)
	}
	if recorder != nil {
		recorder(progress)
	}
}

func (m *AgentManager) setProgress(id string, progress *session.Progress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[id]
	if s == nil || s.active == nil {
		return
	}
	s.summary.Progress = progress
	m.emitLocked(s.summary, 0)
}
