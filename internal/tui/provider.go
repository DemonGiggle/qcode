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

// ProviderFactory prepares a fresh backend using the application's connection
// resolver. It is installed only for normal, non-demo terminal sessions.
type ProviderFactory func(context.Context, string) (llm.Provider, string, error)

// ProviderAvailability reports whether a provider can be selected without
// additional setup. The hint describes the setup step when unavailable, such
// as the missing API key or login command.
type ProviderAvailability func(name string) (available bool, hint string)

type backendController interface {
	ConfigurationRevision(string) (uint64, error)
	SwitchBackend(string, uint64, session.BackendSwitch) error
}

func (u *UI) SetProviderFactory(factory ProviderFactory) { u.providerFactory = factory }

// SetProviderAvailabilityChecker installs the lightweight setup check used to
// disable unconfigured providers in the /provider picker. A nil checker means
// every provider is selectable, preserving demo and test behavior.
func (u *UI) SetProviderAvailabilityChecker(checker ProviderAvailability) {
	u.providerAvailable = checker
}

func (u *UI) providerSetupStatus(name string) (bool, string) {
	if u.providerAvailable == nil {
		return true, ""
	}
	return u.providerAvailable(name)
}

func (u *UI) isProviderSelectable(name, current string) bool {
	if name == current {
		return true
	}
	available, _ := u.providerSetupStatus(name)
	return available
}

func (u *UI) providerSetupHintMessage(name string) string {
	available, hint := u.providerSetupStatus(name)
	if available {
		return ""
	}
	message := "Provider " + providerDisplayName(name) + " needs setup first"
	if strings.TrimSpace(hint) != "" {
		message += ": " + strings.TrimSpace(hint)
	} else {
		message += "."
	}
	return message
}

func (u *UI) handleProviderCommand(ctx context.Context, fields []string) {
	manager, ok := u.manager.(backendController)
	if !ok || u.providerFactory == nil {
		u.printSystemMessage(yellow + "Provider selection is unavailable in this mode." + reset)
		return
	}
	if !u.activeAgentConfigurable() {
		return
	}
	u.screenMu.Lock()
	id, oldProvider, oldModel, runner := u.activeAgent, u.provider, u.model, u.runner
	writer := u.runtimePreferences
	u.screenMu.Unlock()
	revision, err := manager.ConfigurationRevision(id)
	if err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return
	}
	oldThinking := ""
	if thinking, ok := runner.(thinkingRunner); ok {
		oldThinking = thinking.ThinkingLevel()
	}
	names := llm.Names()
	isAvailable := func(name string) bool { return u.isProviderSelectable(name, oldProvider) }
	labels, disabled := providerPickerLabelsWithAvailability(names, isAvailable)
	for {
		name := ""
		if len(fields) >= 2 {
			raw := fields[1]
			if len(fields) > 2 {
				joined := strings.Join(fields[1:], " ")
				if _, ok := resolveProviderID(joined, names); ok {
					raw = joined
				} else {
					u.printSystemMessage(yellow + "Usage: /provider [name]" + reset)
					return
				}
			}
			resolved, ok := resolveProviderID(raw, names)
			if !ok {
				u.printSystemMessage(yellow + "Unknown provider: " + sanitizeDiffLine(raw, "<ESC>") + ". Supported: " + strings.Join(labels, ", ") + reset)
				return
			}
			if !isAvailable(resolved) {
				if message := u.providerSetupHintMessage(resolved); message != "" {
					u.printSystemMessage(yellow + sanitizeDiffLine(message, "<ESC>") + reset)
				} else {
					u.printSystemMessage(yellow + "Provider " + providerDisplayName(resolved) + " needs setup first." + reset)
				}
				return
			}
			name = resolved
		} else {
			u.input.setRaw(true)
			u.beginRawSelector()
			selectedDisplay, accepted, err := selectOptionWithDisabled(u.input, u.themedSelectorWriter(), labels, disabled, providerDisplayName(oldProvider), min(12, max(3, u.height-6)), u.width, ColorEnabled(u.out), "provider", false)
			u.input.setRaw(false)
			u.endRawSelector()
			if err != nil {
				u.printSystemMessage(yellow + "Unable to select provider: " + err.Error() + reset)
				return
			}
			if !accepted {
				return
			}
			resolved, ok := resolveProviderID(stripProviderSetupSuffix(selectedDisplay), names)
			if !ok {
				u.printSystemMessage(yellow + "Unknown provider: " + sanitizeDiffLine(selectedDisplay, "<ESC>") + ". Supported: " + strings.Join(labels, ", ") + reset)
				return
			}
			if !isAvailable(resolved) {
				if message := u.providerSetupHintMessage(resolved); message != "" {
					u.printSystemMessage(yellow + sanitizeDiffLine(message, "<ESC>") + reset)
				} else {
					u.printSystemMessage(yellow + "Provider " + providerDisplayName(resolved) + " needs setup first." + reset)
				}
				continue
			}
			name = resolved
		}
		// Selecting the current row is idempotent but can still save a launch
		// override as the user default, just as /model can.
		if name == oldProvider {
			current, err := manager.ConfigurationRevision(id)
			if err != nil || current != revision {
				u.printSystemMessage(yellow + "Agent configuration changed; try /provider again." + reset)
				return
			}
			u.persistProviderPreference(writer, id, name, oldModel, oldThinking)
			u.printSystemMessage(green + "Provider: " + providerDisplayName(name) + " (unchanged)" + reset)
			return
		}
		fetchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		if u.input != nil {
			u.input.setCancel(cancel)
			defer u.input.setCancel(nil)
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
				err = fmt.Errorf("provider %s does not support model discovery", providerDisplayName(name))
			}
		}
		stopLookup()
		u.clearTransientStatus()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				u.printSystemMessage(dim + "Provider selection cancelled." + reset)
			} else {
				u.printSystemMessage(yellow + "Unable to switch provider: " + err.Error() + reset)
			}
			return
		}
		if len(models) == 0 {
			u.printSystemMessage(yellow + "The provider returned no models; provider unchanged." + reset)
			return
		}
		candidate := &providerModelRunner{provider: provider, models: models}
		// Associate each provider with its last-used model. Switching back
		// restores that model when it is still available instead of keeping
		// the other provider's model.
		u.rememberProviderModel(oldProvider, oldModel, oldThinking)
		model, level := oldModel, oldThinking
		if rememberedModel, rememberedThinking, ok := u.rememberedProviderModel(name); ok && containsOption(models, rememberedModel) {
			model, level = rememberedModel, rememberedThinking
			if !providerThinkingAllowed(provider, model, level) {
				level = ""
			}
		} else if !containsOption(models, oldModel) {
			u.printSystemMessage(dim + "The current model is unavailable on " + providerDisplayName(name) + "; select a model before switching." + reset)
			var accepted bool
			var back bool
			model, level, accepted, back, err = u.selectProviderModel(candidate)
			if back && len(fields) == 1 {
				cancel()
				continue
			}
			if err != nil {
				u.printSystemMessage(yellow + "Unable to select model: " + err.Error() + reset)
				return
			}
			if !accepted {
				return
			}
		} else if !providerThinkingAllowed(provider, model, level) {
			level = ""
		}
		if validator, ok := provider.(llm.ModelValidator); ok {
			if err := validator.ValidateModel(model); err != nil {
				u.printSystemMessage(yellow + err.Error() + reset)
				return
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
			u.printSystemMessage(yellow + "Provider selection cancelled or timed out; provider unchanged." + reset)
			return
		}
		if err := u.commitProvider(manager, id, revision, session.BackendSwitch{Provider: provider, Endpoint: endpoint, Model: model, Thinking: level, ContextCapacity: capacity}); err != nil {
			u.printSystemMessage(yellow + "Unable to switch provider: " + err.Error() + reset)
			return
		}
		u.persistProviderPreference(writer, id, name, model, level)
		u.signalPresentation()
		u.drawStatusBar()
		u.printSystemMessage(green + "Provider: " + providerDisplayName(name) + interfaceGlyph(u.unicode, " · model: ", " | model: ") + sanitizeDiffLine(model, "<ESC>") + reset)
		if oldThinking != "" && level == "" {
			u.printSystemMessage(dim + "Thinking reset to the target provider/model default." + reset)
		}
		u.printSystemMessage(yellow + "History kept. This provider uses a different backend, so the next request may fail — if it does, run /new to start fresh." + reset)
		return
	}
}

// Keep autosave from sampling the new checkpoint with an old view identity.
func (u *UI) commitProvider(manager backendController, id string, revision uint64, next session.BackendSwitch) error {
	if u.persistence != nil {
		u.persistence.mu.Lock()
		defer u.persistence.mu.Unlock()
	}
	if err := manager.SwitchBackend(id, revision, next); err != nil {
		return err
	}
	u.screenMu.Lock()
	if view := u.views[id]; view != nil {
		view.provider, view.model = next.Provider.Name(), next.Model
	}
	if u.activeAgent == id {
		u.provider, u.model = next.Provider.Name(), next.Model
	}
	u.screenMu.Unlock()
	return nil
}

func containsOption(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func providerThinkingAllowed(provider llm.Provider, model, level string) bool {
	if level == "" {
		return true
	}
	thinking, ok := provider.(llm.ThinkingProvider)
	if !ok {
		return false
	}
	capability := thinking.ThinkingCapability(model)
	return capability.Adjustable && containsOption(capability.Levels, level)
}

// A selector adapter for the prepared provider. It never mutates the live Agent.
type providerModelRunner struct {
	provider llm.Provider
	models   []string
}

func (r *providerModelRunner) ListModels(context.Context) ([]string, error) { return r.models, nil }
func (*providerModelRunner) SetModel(string)                                {}
func (r *providerModelRunner) ThinkingCapabilityFor(model string) llm.ThinkingCapability {
	if p, ok := r.provider.(llm.ThinkingProvider); ok {
		return p.ThinkingCapability(model)
	}
	return llm.ThinkingCapability{}
}
func (r *providerModelRunner) ThinkingCapability() llm.ThinkingCapability {
	return llm.ThinkingCapability{}
}
func (*providerModelRunner) SetThinking(string) error { return nil }
func (*providerModelRunner) ThinkingLevel() string    { return "" }

func (u *UI) selectProviderModel(runner *providerModelRunner) (string, string, bool, bool, error) {
	visible, query := min(12, max(3, u.height-6)), ""
	for {
		u.input.setRaw(true)
		u.beginRawSelector()
		model, result, nextQuery, err := selectModelWithQuery(u.input, u.themedSelectorWriter(), runner.models, "", query, visible, u.width, ColorEnabled(u.out))
		u.input.setRaw(false)
		u.endRawSelector()
		query = nextQuery
		if err != nil || result != selectorAccepted {
			return "", "", false, result == selectorBack, err
		}
		level, accepted, back, err := u.selectThinkingLevelWithBack(runner, model, visible)
		if back {
			continue
		}
		return model, level, accepted, false, err
	}
}

func (u *UI) persistProviderPreference(writer RuntimePreferenceWriter, id, provider, model, thinking string) {
	u.rememberProviderModel(provider, model, thinking)
	if writer == nil || id != "main" {
		return
	}
	if err := writer.PersistProvider(provider, model, thinking); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist provider preference: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

// Manual terminal creation inherits the selected tab's backend, but retains its
// existing tool/workspace defaults. Tool-created helpers use CreateInheritedAt.
func (u *UI) createAgentWithProvider(model string) (session.Summary, error) {
	if manager, ok := u.manager.(interface {
		Backend(string) (session.CreationBackend, error)
		CreateWithProvider(string, session.CreationBackend) (session.Summary, error)
	}); ok && u.providerFactory != nil {
		backend, err := manager.Backend(u.activeAgent)
		if err != nil {
			return session.Summary{}, err
		}
		return manager.CreateWithProvider(model, backend)
	}
	return u.manager.Create(model)
}
