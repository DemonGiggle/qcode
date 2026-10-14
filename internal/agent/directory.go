package agent

import (
	"context"
	"fmt"
)

// Directory support is optional so providers and test toolsets need not
// implement filesystem capabilities just to satisfy Toolset.
type directoryTools interface {
	WorkingDirectory() string
	ValidateWorkingDirectory(context.Context, string) (string, error)
	ChangeWorkingDirectory(context.Context, string) (string, error)
}

func (a *Agent) WorkingDirectory() string {
	if tools, ok := a.tools.(interface{ WorkingDirectory() string }); ok {
		return tools.WorkingDirectory()
	}
	return ""
}

func (a *Agent) directoryChanged() {
	// Invalidating the estimate also publishes the new tool-state checkpoint.
	a.invalidateContextUsage()
	if a.directoryRecorder != nil {
		a.directoryRecorder(a.WorkingDirectory())
	}
}

func (t *managedToolset) WorkingDirectory() string {
	if tools, ok := t.base.(interface{ WorkingDirectory() string }); ok {
		return tools.WorkingDirectory()
	}
	return ""
}

func (t *managedToolset) ValidateWorkingDirectory(ctx context.Context, path string) (string, error) {
	if tools, ok := t.base.(directoryTools); ok {
		return tools.ValidateWorkingDirectory(ctx, path)
	}
	return "", fmt.Errorf("working directories are unavailable for this toolset")
}

func (t *managedToolset) ChangeWorkingDirectory(ctx context.Context, path string) (string, error) {
	if tools, ok := t.base.(directoryTools); ok {
		return tools.ChangeWorkingDirectory(ctx, path)
	}
	return "", fmt.Errorf("working directories are unavailable for this toolset")
}

func (t *modeToolset) WorkingDirectory() string {
	if tools, ok := t.base.(interface{ WorkingDirectory() string }); ok {
		return tools.WorkingDirectory()
	}
	return ""
}

func (t *modeToolset) ValidateWorkingDirectory(ctx context.Context, path string) (string, error) {
	if tools, ok := t.base.(directoryTools); ok {
		return tools.ValidateWorkingDirectory(ctx, path)
	}
	return "", fmt.Errorf("working directories are unavailable for this toolset")
}

func (t *modeToolset) ChangeWorkingDirectory(ctx context.Context, path string) (string, error) {
	if !t.owner.modeAllowedTool("change_directory") {
		return "", fmt.Errorf("change_directory is unavailable in the current mode")
	}
	if tools, ok := t.base.(directoryTools); ok {
		return tools.ChangeWorkingDirectory(ctx, path)
	}
	return "", fmt.Errorf("working directories are unavailable for this toolset")
}

func (m *AgentManager) directoryChanged(id, cwd string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		s.summary.Cwd = cwd
		select {
		case m.events <- ManagerEvent{Agent: cloneSummary(s.summary), DirectoryChanged: true}:
		default:
		}
	}
}
