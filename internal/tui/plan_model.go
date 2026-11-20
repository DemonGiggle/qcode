package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"qcode/internal/llm"
	"qcode/internal/session"
)

// handlePlanActWithModelSelection lets the user pick a provider, model, and
// thinking level before implementing the approved plan. Cancelling any
// selector keeps the agent in Plan mode without starting implementation.
func (u *UI) handlePlanActWithModelSelection(ctx context.Context) {
	controller, ok := u.runner.(planController)
	if !ok {
		u.printSystemMessage(yellow + "Plan mode is unavailable." + reset)
		return
	}
	if _, exists := controller.LatestPlanText(); !exists {
		u.printSystemMessage(yellow + "No complete plan is available. Ask qcode to submit a plan first." + reset)
		return
	}
	if u.manager == nil {
		u.printSystemMessage(yellow + "Plan execution requires the interactive agent manager." + reset)
		return
	}
	if !u.activeAgentConfigurable() {
		return
	}
	if !u.selectPlanBackendAndCommit(ctx) {
		return
	}
	u.handlePlanCommand(ctx, []string{"/plan", "act"})
}

// selectPlanBackendAndCommit runs provider/model/thinking selectors and
// commits the chosen backend. It reports whether the caller should proceed
// with plan implementation. Any cancellation or failure leaves Plan mode
// active and returns false.
func (u *UI) selectPlanBackendAndCommit(ctx context.Context) bool {
	if manager, ok := u.manager.(backendController); ok && u.providerFactory != nil {
		return u.selectPlanProviderAndCommit(ctx, manager)
	}
	return u.selectPlanModelAndCommit(ctx)
}

func (u *UI) selectPlanProviderAndCommit(ctx context.Context, manager backendController) bool {
	u.screenMu.Lock()
	id, oldProvider, oldModel, runner := u.activeAgent, u.provider, u.model, u.runner
	writer := u.runtimePreferences
	u.screenMu.Unlock()
	revision, err := manager.ConfigurationRevision(id)
	if err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return false
	}
	oldThinking := ""
	if thinking, ok := runner.(thinkingRunner); ok {
		oldThinking = thinking.ThinkingLevel()
	}
	names := u.availableProviderNames()
	isAvailable := func(name string) bool { return u.isProviderSelectable(name, oldProvider) }
	labels, disabled := u.providerLabelsWithAvailability(names, isAvailable)
	visible := min(12, max(3, u.height-6))
	for {
		u.input.setRaw(true)
		u.beginRawSelector()
		selectedDisplay, accepted, err := selectOptionWithDisabled(u.input, u.themedSelectorWriter(), labels, disabled, u.providerLabel(oldProvider), visible, u.width, ColorEnabled(u.out), "provider", false)
		u.input.setRaw(false)
		u.endRawSelector()
		if err != nil {
			u.printSystemMessage(yellow + "Unable to select provider: " + err.Error() + reset)
			return false
		}
		if !accepted {
			u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
			return false
		}
		resolved, ok := u.resolveProviderID(stripProviderSetupSuffix(selectedDisplay), names)
		if !ok {
			u.printSystemMessage(yellow + "Unknown provider: " + sanitizeDiffLine(selectedDisplay, "<ESC>") + ". Supported: " + strings.Join(labels, ", ") + reset)
			return false
		}
		if !isAvailable(resolved) {
			if message := u.providerSetupHintMessage(resolved); message != "" {
				u.printSystemMessage(yellow + sanitizeDiffLine(message, "<ESC>") + reset)
			} else {
				u.printSystemMessage(yellow + "Provider " + u.providerLabel(resolved) + " needs setup first." + reset)
			}
			continue
		}
		name := resolved
		fetchCtx, cancel := context.WithCancel(ctx)
		if u.input != nil {
			u.input.setCancel(cancel)
		}
		u.showTransientStatus(dim + "Preparing provider... Ctrl+C to cancel" + reset)
		lookupCtx, stopLookup := context.WithTimeout(fetchCtx, 30*time.Second)
		provider, endpoint, err := u.providerFactory(lookupCtx, name)
		var models []string
		if err == nil {
			if provider == nil || provider.Name() != name {
				err = fmt.Errorf("provider factory returned an incompatible backend")
			} else if lister, ok := provider.(llm.ModelLister); ok {
				models, err = lister.Models(lookupCtx)
			} else {
				err = fmt.Errorf("provider %s does not support model discovery", u.providerLabel(name))
			}
		}
		stopLookup()
		u.clearTransientStatus()
		if err != nil {
			u.input.setCancel(nil)
			cancel()
			if errors.Is(err, context.Canceled) {
				u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
			} else {
				u.printSystemMessage(yellow + "Unable to switch provider: " + err.Error() + reset)
			}
			return false
		}
		if len(models) == 0 {
			u.input.setCancel(nil)
			cancel()
			u.printSystemMessage(yellow + "The provider returned no models; staying in Plan mode." + reset)
			return false
		}
		// Associate each provider with its last-used model so switching back
		// restores it when still available.
		u.rememberProviderModel(oldProvider, oldModel, oldThinking)
		initial := ""
		if name == oldProvider {
			initial = oldModel
		} else if rememberedModel, _, ok := u.rememberedProviderModel(name); ok && containsOption(models, rememberedModel) {
			initial = rememberedModel
		} else if containsOption(models, oldModel) {
			initial = oldModel
		}
		candidate := &providerModelRunner{provider: provider, models: models}
		model, level, accepted, back, selectErr := u.selectPlanModelThinking(candidate, initial, visible)
		if selectErr != nil {
			u.input.setCancel(nil)
			cancel()
			u.printSystemMessage(yellow + "Unable to select model: " + selectErr.Error() + reset)
			return false
		}
		if back {
			u.input.setCancel(nil)
			cancel()
			continue
		}
		if !accepted {
			u.input.setCancel(nil)
			cancel()
			u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
			return false
		}
		if validator, ok := provider.(llm.ModelValidator); ok {
			if err := validator.ValidateModel(model); err != nil {
				u.input.setCancel(nil)
				cancel()
				u.printSystemMessage(yellow + err.Error() + reset)
				return false
			}
		}
		capacity := 0
		if tracker, ok := provider.(llm.ContextWindowProvider); ok {
			contextCtx, stop := context.WithTimeout(fetchCtx, 2*time.Second)
			if value, err := tracker.ContextWindow(contextCtx, model); err == nil && value > 0 {
				capacity = value
			}
			stop()
		}
		if err := fetchCtx.Err(); err != nil {
			u.input.setCancel(nil)
			cancel()
			u.printSystemMessage(yellow + "Provider/model selection cancelled or timed out; staying in Plan mode." + reset)
			return false
		}
		if err := u.commitProvider(manager, id, revision, session.BackendSwitch{Provider: provider, Endpoint: endpoint, Model: model, Thinking: level, ContextCapacity: capacity}); err != nil {
			u.input.setCancel(nil)
			cancel()
			u.printSystemMessage(yellow + "Unable to switch provider: " + err.Error() + reset)
			return false
		}
		u.input.setCancel(nil)
		cancel()
		u.persistProviderPreference(writer, id, name, model, level)
		u.signalPresentation()
		u.drawStatusBar()
		u.printSystemMessage(green + "Provider: " + u.providerLabel(name) + interfaceGlyph(u.unicode, " · model: ", " | model: ") + sanitizeDiffLine(model, "<ESC>") + reset)
		if oldThinking != "" && level == "" {
			u.printSystemMessage(dim + "Thinking reset to the target provider/model default." + reset)
		}
		u.printSystemMessage(yellow + "History kept. This provider uses a different backend, so the next request may fail — if it does, run /new to start fresh." + reset)
		return true
	}
}

// selectPlanModelThinking always prompts for a model and then for a thinking
// level when the backend supports it. Back reports Esc at the model step so
// the caller can return to the provider picker.
func (u *UI) selectPlanModelThinking(runner *providerModelRunner, initial string, visible int) (string, string, bool, bool, error) {
	query := ""
	current := initial
	for {
		u.input.setRaw(true)
		u.beginRawSelector()
		model, result, nextQuery, err := selectModelWithQuery(u.input, u.themedSelectorWriter(), runner.models, current, query, visible, u.width, ColorEnabled(u.out))
		u.input.setRaw(false)
		u.endRawSelector()
		query = nextQuery
		if err != nil || result != selectorAccepted {
			return "", "", false, result == selectorBack, err
		}
		current = model
		level, accepted, back, err := u.selectThinkingLevelWithBack(runner, model, visible)
		if back {
			continue
		}
		return model, level, accepted, false, err
	}
}

func (u *UI) selectPlanModelAndCommit(ctx context.Context) bool {
	runner, ok := u.runner.(modelRunner)
	if !ok {
		u.printSystemMessage(yellow + "Model selection is unavailable." + reset)
		return false
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	u.input.setCancel(cancel)
	u.showTransientStatus(dim + "Waiting for models...  Ctrl+C to cancel" + reset)
	models, err := runner.ListModels(fetchCtx)
	u.input.setCancel(nil)
	cancel()
	u.clearTransientStatus()
	if errors.Is(err, context.Canceled) {
		u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
		return false
	}
	if err != nil {
		u.printSystemMessage(yellow + "Unable to list models: " + err.Error() + reset)
		return false
	}
	if len(models) == 0 {
		u.printSystemMessage(dim + "The provider returned no models; staying in Plan mode." + reset)
		return false
	}
	visible := min(12, max(3, u.height-6))
	modelQuery := ""
	var selected, level string
	for {
		u.input.setRaw(true)
		u.beginRawSelector()
		var result selectorResult
		var selectErr error
		selected, result, modelQuery, selectErr = selectModelWithQuery(u.input, u.themedSelectorWriter(), models, u.currentModel(), modelQuery, visible, u.width, ColorEnabled(u.out))
		u.input.setRaw(false)
		u.endRawSelector()
		if selectErr != nil {
			u.printSystemMessage(yellow + "Unable to select model: " + selectErr.Error() + reset)
			return false
		}
		if result != selectorAccepted {
			u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
			return false
		}
		var selectedThinking, back bool
		level, selectedThinking, back, err = u.selectThinkingLevelWithBack(runner, selected, visible)
		if err != nil {
			u.printSystemMessage(yellow + "Unable to select thinking level: " + err.Error() + reset)
			return false
		}
		if back {
			continue
		}
		if !selectedThinking {
			u.printSystemMessage(dim + "Provider/model selection cancelled; staying in Plan mode." + reset)
			return false
		}
		break
	}
	u.modelChangeMu.Lock()
	defer u.modelChangeMu.Unlock()
	if !u.configureActive(func() error {
		runner.SetModel(selected)
		if thinking, ok := runner.(thinkingRunner); ok {
			return thinking.SetThinking(level)
		}
		return nil
	}) {
		return false
	}
	if u.manager != nil {
		if err := u.manager.UpdateModel(u.activeAgent, selected); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
			return false
		}
		u.screenMu.Lock()
		if view := u.views[u.activeAgent]; view != nil {
			view.model = selected
		}
		u.screenMu.Unlock()
	}
	if tracker, ok := u.runner.(contextRunner); ok {
		if !u.configureActive(func() error { tracker.RefreshContext(ctx); return nil }) {
			return false
		}
	}
	oldModel, provider := u.model, u.provider
	u.screenMu.Lock()
	u.model = selected
	u.screenMu.Unlock()
	u.persistModelPreference(provider, selected, level)
	u.drawStatusBar()
	message := green + "Model: " + sanitizeDiffLine(selected, "<ESC>")
	if level != "" {
		message += interfaceGlyph(u.unicode, " · thinking: ", " | thinking: ") + sanitizeDiffLine(level, "<ESC>")
	}
	u.printSystemMessage(message + reset)
	if openCodeGoBackendChanged(u.providerKind(provider), oldModel, selected) {
		u.printSystemMessage(yellow + "History kept. This model uses a different backend than the previous one, so the next request may fail — if it does, run /new to start fresh." + reset)
	}
	return true
}
