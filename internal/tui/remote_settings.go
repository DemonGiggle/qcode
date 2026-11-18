package tui

import (
	"context"
	"fmt"
	"slices"

	"qcode/internal/prompt"
	"qcode/internal/session"
	"qcode/internal/transcript"
)

type RemoteAgentSettings struct {
	Model    string          `json:"model,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	Tools    map[string]bool `json:"tools,omitempty"`
	Skills   []string        `json:"skills,omitempty"`
}

type idleAgentConfigurer interface {
	ConfigurationRevision(string) (uint64, error)
	ConfigureIdle(string, uint64, func() error) error
}

// ConfigureRemoteAgent applies one settings change to the browser's target,
// without injecting keystrokes or selecting a different terminal tab.
func (u *UI) ConfigureRemoteAgent(ctx context.Context, id string, settings RemoteAgentSettings) error {
	u.screenMu.Lock()
	manager, view := u.manager, u.views[id]
	loader, bodyLoader, writer := u.skillCatalogLoader, u.skillBodyLoader, u.runtimePreferences
	catalog := append([]prompt.SkillSummary(nil), u.skills...)
	var onSkills skillChangeFunc
	var provider string
	if view != nil {
		onSkills, provider = view.onSkills, view.provider
	}
	u.screenMu.Unlock()

	configurer, ok := manager.(idleAgentConfigurer)
	if !ok || view == nil {
		return fmt.Errorf("agent %q is unavailable", id)
	}
	revision, err := configurer.ConfigurationRevision(id)
	if err != nil {
		return err
	}
	summary, err := remoteAgentSummary(manager, id)
	if err != nil {
		return err
	}
	if summary.Status == session.StatusRunning || summary.Status == session.StatusWaitingForApproval {
		return fmt.Errorf("agent %q is not idle", id)
	}
	runner, ok := manager.Runner(id)
	if !ok {
		return fmt.Errorf("agent %q is unavailable", id)
	}

	switch {
	case settings.Model != "" && settings.Tools == nil && settings.Skills == nil:
		model, ok := runner.(modelRunner)
		if !ok {
			return fmt.Errorf("model selection is unavailable")
		}
		thinking, _ := runner.(thinkingRunner)
		models, err := model.ListModels(ctx)
		if err != nil {
			return err
		}
		if !slices.Contains(models, settings.Model) {
			return fmt.Errorf("unknown model %q", settings.Model)
		}
		if settings.Thinking != "" && (thinking == nil || !slices.Contains(thinking.ThinkingCapabilityFor(settings.Model).Levels, settings.Thinking)) {
			return fmt.Errorf("unsupported thinking level %q", settings.Thinking)
		}
		u.modelChangeMu.Lock()
		defer u.modelChangeMu.Unlock()
		return u.applyRemoteSettings(ctx, manager, view, configurer, revision, func() error {
			oldLevel := ""
			if thinking != nil {
				oldLevel = thinking.ThinkingLevel()
			}
			model.SetModel(settings.Model)
			if thinking != nil {
				if err := thinking.SetThinking(settings.Thinking); err != nil {
					model.SetModel(summary.Model)
					_ = thinking.SetThinking(oldLevel)
					return err
				}
			}
			if tracker, ok := runner.(contextRunner); ok {
				tracker.RefreshContext(ctx)
			}
			return nil
		}, func() error {
			u.screenMu.Lock()
			view.model = settings.Model
			if u.activeAgent == id {
				u.model = settings.Model
			}
			u.screenMu.Unlock()
			u.rememberProviderModel(provider, settings.Model, settings.Thinking)
			if openCodeGoBackendChanged(u.providerKind(provider), summary.Model, settings.Model) {
				fmt.Fprintln(transcript.NewGroup(view.display), "The selected model uses a different OpenCode Go backend. If the next request fails, run /new to start fresh.")
			}
			if id == "main" && writer != nil {
				return writer.PersistModel(provider, settings.Model, settings.Thinking)
			}
			return nil
		})

	case settings.Model == "" && settings.Thinking == "" && len(settings.Tools) > 0 && settings.Skills == nil:
		tools, ok := runner.(toolRunner)
		if !ok {
			return fmt.Errorf("tool selection is unavailable")
		}
		return u.applyRemoteSettings(ctx, manager, view, configurer, revision, func() error {
			names := tools.ToolNames()
			for name := range settings.Tools {
				if !slices.Contains(names, name) {
					return fmt.Errorf("unknown tool %q", name)
				}
			}
			for name, enabled := range settings.Tools {
				if tools.ToolEnabled(name) != enabled {
					tools.ToggleTool(name, enabled)
				}
			}
			return nil
		}, nil)

	case settings.Model == "" && settings.Thinking == "" && settings.Tools == nil && settings.Skills != nil:
		skills, ok := runner.(skillRunner)
		if !ok {
			return fmt.Errorf("skill selection is unavailable")
		}
		if loader != nil {
			catalog, err = loader()
			if err != nil {
				return err
			}
		}
		names, always, summaries, err := resolveSkillSelection(catalog, settings.Skills)
		if err != nil {
			return err
		}
		var bodies map[string]string
		if len(always) > 0 {
			if bodyLoader == nil {
				return fmt.Errorf("skill body loader is unavailable")
			}
			bodies, err = bodyLoader(always)
			if err != nil {
				return err
			}
		}
		return u.applyRemoteSettings(ctx, manager, view, configurer, revision, func() error {
			if onSkills != nil {
				onSkills(names, always)
			}
			setRunnerSkills(skills, summaries, bodies)
			return nil
		}, func() error {
			if id == "main" && writer != nil {
				return writer.PersistSelectedSkills(names, always)
			}
			return nil
		})
	}

	return fmt.Errorf("provide one model, tool, or skill setting")
}

func (u *UI) applyRemoteSettings(ctx context.Context, manager agentController, view *agentView, configurer idleAgentConfigurer, revision uint64, apply, persist func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep a resumed session from replacing the target during the change.
	if p := u.persistence; p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
	}
	u.screenMu.Lock()
	current := u.manager == manager && u.views[view.id] == view
	u.screenMu.Unlock()
	if !current {
		return fmt.Errorf("the live session changed")
	}
	if err := configurer.ConfigureIdle(view.id, revision, apply); err != nil {
		return err
	}
	var err error
	if persist != nil {
		err = persist()
	}
	u.requestSessionSave()
	u.drawStatusBar()
	u.signalPresentation()
	if err != nil {
		return fmt.Errorf("settings applied, but could not save preferences: %w", err)
	}
	return nil
}
