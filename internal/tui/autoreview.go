package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qcode/internal/session"
)

// Autoreview state machine rounds.
// 0 idle, 1 first review in flight (reviewer running or callback processing),
// 2 final review in flight after fix.
const (
	autoreviewRoundIdle = iota
	autoreviewRoundFirst
	autoreviewRoundFinal
)

// autoreviewDelegationSource mirrors agent delegationSource ("delegation").
// Reviewer tasks submitted with this source cause the manager to queue an
// automatic delegation callback to main when the reviewer finishes.
const autoreviewDelegationSource = "delegation"

// autoreviewBadge returns the AR status badge. It is always rendered first
// when autoreview is enabled and is never hidden by /statusline. It uses the
// same foreground-only segment style as the rest of the status bar (no
// background fill): the semantic yellow maps to the theme's Warning role via
// TransformANSI, so contrast follows the active palette like MODE and STEP.
// A filled badge cannot work here: under dark themes the Text foreground and
// the Warning background both resolve to near-white, yielding ~1.1:1 contrast.
func autoreviewBadge(color bool) string {
	if !color {
		return "[AR]"
	}
	return yellow + bold + "[AR]" + reset
}

// AutoreviewEnabled reports whether automatic review is on.
func (u *UI) AutoreviewEnabled() bool {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return u.autoreviewEnabled
}

// AutoreviewReviewer returns the configured reviewer provider/model.
func (u *UI) AutoreviewReviewer() (provider, model string) {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	return u.autoreviewProvider, u.autoreviewModel
}

// SetAutoreview installs persisted autoreview state at startup. Empty
// provider/model clears the reviewer.
func (u *UI) SetAutoreview(enabled bool, provider, model string) {
	u.screenMu.Lock()
	u.autoreviewEnabled = enabled
	u.autoreviewProvider = strings.TrimSpace(provider)
	u.autoreviewModel = strings.TrimSpace(model)
	if !enabled {
		u.autoreviewRound = autoreviewRoundIdle
	}
	u.screenMu.Unlock()
	u.syncAutoreviewToMain()
}

// syncAutoreviewToMain advertises the toggle to main's system prompt so the
// agent knows when it decides review eligibility. Best-effort: main may not
// exist yet at startup or during restore.
func (u *UI) syncAutoreviewToMain() {
	if u.manager == nil {
		return
	}
	u.screenMu.Lock()
	enabled := u.autoreviewEnabled
	u.screenMu.Unlock()
	value, ok := u.manager.Runner("main")
	if !ok {
		return
	}
	if controller, ok := value.(autoreviewController); ok {
		controller.SetAutoreviewEnabled(enabled)
	}
}

// autoreviewStatusLocked reports enabled/provider/model assuming screenMu held.
func (u *UI) autoreviewStatusLocked() (bool, string, string) {
	return u.autoreviewEnabled, u.autoreviewProvider, u.autoreviewModel
}

// withAutoreviewPrefix prepends the AR badge first when enabled. Plain
// rendering joins with a space; colored rendering uses the dimmed middle-dot
// separator for consistency with the rest of the bar. Callers hold screenMu
// when rendering the status bar (like statusBar itself); direct reads match
// the existing statusBar field access pattern and avoid reentrant locking.
func (u *UI) withAutoreviewPrefix(bar string) string {
	enabled := u.autoreviewEnabled
	unicode := u.unicode
	out := u.out
	return autoreviewPrefixFor(bar, enabled, unicode, out != nil && ColorEnabled(out))
}

func autoreviewPrefixFor(bar string, enabled, unicode, color bool) string {
	if !enabled {
		return bar
	}
	badge := autoreviewBadge(color)
	if bar == "" {
		return badge
	}
	if !color {
		return badge + " " + bar
	}
	sep := statusSeparatorColored(unicode)
	return badge + sep + bar
}

func (u *UI) persistAutoreviewPreference(enabled bool) {
	writer, main := u.runtimePreferenceTarget()
	if writer == nil || !main {
		return
	}
	if err := writer.PersistAutoreview(enabled); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist autoreview preference: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

func (u *UI) persistAutoreviewModel(provider, model string) {
	writer, main := u.runtimePreferenceTarget()
	if writer == nil || !main {
		return
	}
	if err := writer.PersistAutoreviewModel(provider, model); err != nil {
		u.printSystemMessage(yellow + "Warning: unable to persist autoreview reviewer: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	}
}

// handleAutoreviewCommand implements /autoreview [on|off]. Bare /autoreview
// opens the reviewer provider/model picker. It is main-tab only but
// intentionally works while main is busy: the toggle applies immediately and
// the next idle transition picks it up.
func (u *UI) handleAutoreviewCommand(ctx context.Context, fields []string) {
	if u.manager != nil {
		u.screenMu.Lock()
		active := u.activeAgent
		u.screenMu.Unlock()
		if active != "" && active != "main" {
			u.printSystemMessage(yellow + "Autoreview is configured from the main tab." + reset)
			return
		}
	}
	if len(fields) == 2 && (fields[1] == "on" || fields[1] == "off") {
		enabled := fields[1] == "on"
		u.screenMu.Lock()
		hasReviewer := strings.TrimSpace(u.autoreviewProvider) != "" && strings.TrimSpace(u.autoreviewModel) != ""
		u.screenMu.Unlock()
		if enabled && !hasReviewer {
			if !u.pickAutoreviewReviewer(ctx) {
				u.printSystemMessage(yellow + "Autoreview needs a reviewer model; pick one with /autoreview." + reset)
				return
			}
			u.screenMu.Lock()
			hasReviewer = strings.TrimSpace(u.autoreviewProvider) != "" && strings.TrimSpace(u.autoreviewModel) != ""
			u.screenMu.Unlock()
			if !hasReviewer {
				return
			}
			enabled = true
		}
		u.screenMu.Lock()
		u.autoreviewEnabled = enabled
		if !enabled {
			u.autoreviewRound = autoreviewRoundIdle
		} else if u.autoreviewRound != autoreviewRoundFirst && u.autoreviewRound != autoreviewRoundFinal {
			u.autoreviewRound = autoreviewRoundIdle
		}
		provider, model := u.autoreviewProvider, u.autoreviewModel
		u.screenMu.Unlock()
		u.syncAutoreviewToMain()
		u.persistAutoreviewPreference(enabled)
		u.drawStatusBar()
		if enabled {
			msg := "Autoreview on."
			if provider != "" && model != "" {
				msg += " Reviewer: " + provider + "/" + model + "."
			}
			u.printSystemMessage(green + msg + reset)
		} else {
			u.printSystemMessage(green + "Autoreview off." + reset)
		}
		return
	}
	if len(fields) == 1 {
		if !u.pickAutoreviewReviewer(ctx) {
			u.screenMu.Lock()
			enabled, provider, model := u.autoreviewStatusLocked()
			u.screenMu.Unlock()
			state := "off"
			if enabled {
				state = "on"
			}
			reviewer := "none"
			if strings.TrimSpace(provider) != "" && strings.TrimSpace(model) != "" {
				reviewer = provider + "/" + model
			}
			u.printSystemMessage("Autoreview: " + state + "; reviewer: " + reviewer)
			return
		}
		// Picker sets the reviewer and enables autoreview.
		u.screenMu.Lock()
		provider, model := u.autoreviewProvider, u.autoreviewModel
		enabled := u.autoreviewEnabled
		u.screenMu.Unlock()
		u.drawStatusBar()
		if enabled && provider != "" && model != "" {
			u.printSystemMessage(green + "Autoreview on. Reviewer: " + provider + "/" + model + "." + reset)
		}
		return
	}
	u.printSystemMessage(yellow + "Usage: /autoreview [on|off]" + reset)
}

// pickAutoreviewReviewer opens provider then model selectors for any
// configured provider, bypassing the /agent allow-models roster. It stores
// the choice, enables autoreview, and persists both. False means cancelled.
func (u *UI) pickAutoreviewReviewer(ctx context.Context) bool {
	if u.providerFactory == nil {
		u.printSystemMessage(yellow + "Reviewer selection is unavailable in this mode." + reset)
		return false
	}
	names := u.availableProviderNames()
	if len(names) == 0 {
		u.printSystemMessage(yellow + "No providers are available." + reset)
		return false
	}
	isAvailable := func(name string) bool { return u.isProviderSelectable(name, name) }
	labels, disabled := u.providerLabelsWithAvailability(names, isAvailable)
	u.input.setRaw(true)
	u.beginRawSelector()
	selectedDisplay, accepted, err := selectOptionWithDisabled(u.input, u.themedSelectorWriter(), labels, disabled, "", min(12, max(3, u.height-6)), u.width, ColorEnabled(u.out), "reviewer provider", false)
	u.input.setRaw(false)
	u.endRawSelector()
	if err != nil {
		u.printSystemMessage(yellow + "Unable to select reviewer provider: " + err.Error() + reset)
		return false
	}
	if !accepted {
		return false
	}
	provider, ok := u.resolveProviderID(stripProviderSetupSuffix(selectedDisplay), names)
	if !ok {
		u.printSystemMessage(yellow + "Unknown provider: " + sanitizeDiffLine(selectedDisplay, "<ESC>") + reset)
		return false
	}
	if !isAvailable(provider) {
		if message := u.providerSetupHintMessage(provider); message != "" {
			u.printSystemMessage(yellow + sanitizeDiffLine(message, "<ESC>") + reset)
		} else {
			u.printSystemMessage(yellow + "Provider " + u.providerLabel(provider) + " needs setup first." + reset)
		}
		return false
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if u.input != nil {
		u.input.setCancel(cancel)
		defer u.input.setCancel(nil)
	}
	u.showTransientStatus(dim + "Preparing reviewer... Ctrl+C to cancel" + reset)
	backend, endpoint, err := u.providerFactory(fetchCtx, provider)
	var models []string
	if err == nil {
		if lister, ok := interface{}(backend).(interface {
			Models(context.Context) ([]string, error)
		}); ok && backend != nil {
			models, err = lister.Models(fetchCtx)
		} else if backend == nil {
			err = fmt.Errorf("provider factory returned no backend")
		} else {
			err = fmt.Errorf("provider %s does not support model discovery", u.providerLabel(provider))
		}
	}
	_ = endpoint
	u.clearTransientStatus()
	if err != nil {
		u.printSystemMessage(yellow + "Unable to list reviewer models: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return false
	}
	if len(models) == 0 {
		u.printSystemMessage(yellow + "The provider returned no models; reviewer unchanged." + reset)
		return false
	}
	visible := min(12, max(3, u.height-6))
	u.screenMu.Lock()
	currentModel := u.autoreviewModel
	u.screenMu.Unlock()
	u.input.setRaw(true)
	u.beginRawSelector()
	selectedModel, result, _, selectErr := selectModelWithQuery(u.input, u.themedSelectorWriter(), models, currentModel, "", visible, u.width, ColorEnabled(u.out))
	u.input.setRaw(false)
	u.endRawSelector()
	if selectErr != nil {
		u.printSystemMessage(yellow + "Unable to select reviewer model: " + selectErr.Error() + reset)
		return false
	}
	if result != selectorAccepted || strings.TrimSpace(selectedModel) == "" {
		return false
	}
	u.screenMu.Lock()
	u.autoreviewProvider = provider
	u.autoreviewModel = strings.TrimSpace(selectedModel)
	u.autoreviewEnabled = true
	if u.autoreviewRound != autoreviewRoundFirst && u.autoreviewRound != autoreviewRoundFinal {
		u.autoreviewRound = autoreviewRoundIdle
	}
	u.screenMu.Unlock()
	u.syncAutoreviewToMain()
	u.persistAutoreviewModel(provider, strings.TrimSpace(selectedModel))
	u.persistAutoreviewPreference(true)
	u.drawStatusBar()
	return true
}

// buildAutoreviewPrompt composes the reviewer task. When the workspace has
// version control the reviewer self-serves the diff; otherwise main sends its
// summary plus changed files and the reviewer reads those files.
func buildAutoreviewPrompt(summary session.Summary, hasGit, isFinal bool) string {
	var b strings.Builder
	if isFinal {
		b.WriteString("You are a code reviewer doing a follow-up review after a fix. You cannot edit files (write/edit are disabled). Use shell only for searching and diffs (git diff, git log, grep); use read/list/search to inspect code.\n")
	} else {
		b.WriteString("You are a code reviewer. You cannot edit files (write/edit are disabled). Use shell only for searching and diffs (git diff, git log, grep); use read/list/search to inspect code.\n")
	}
	task := strings.TrimSpace(summary.CurrentTask)
	outcome := strings.TrimSpace(summary.LastOutcome)
	if task != "" {
		b.WriteString("Main task: " + truncateAutoreview(task, 1500) + "\n")
	}
	if outcome != "" && outcome != task {
		b.WriteString("Main summary of work done: " + truncateAutoreview(outcome, 2000) + "\n")
	}
	if len(summary.ChangedFiles) > 0 {
		joined := truncateAutoreview(strings.Join(summary.ChangedFiles, ","), 800)
		b.WriteString("Changed files: " + joined + "\n")
	}
	if hasGit {
		b.WriteString("The repository has version control. Run git status, git diff, and git log yourself to review the actual changes.\n")
	} else {
		b.WriteString("No version control was detected. Review the changed files listed above (read them directly) along with main's summary; main cannot provide a git diff.\n")
	}
	if isFinal {
		b.WriteString("This is the final review after a fix. Focus on whether previously reported issues were addressed and whether the fix introduced new problems.\n")
	}
	b.WriteString("Return a free-form verdict with findings (what is good, what must be fixed, file/line references). Main will judge your intent and apply a fix if you report issues, then may request one final review. Be concise and specific.")
	return b.String()
}

func truncateAutoreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// autoreviewWorkdir resolves the workspace directory for git checks.
func (u *UI) autoreviewWorkdir() string {
	u.screenMu.Lock()
	root := u.root
	runner := u.runner
	u.screenMu.Unlock()
	cwd := root
	if runner != nil {
		if w, ok := runner.(interface{ WorkingDirectory() string }); ok {
			if dir := strings.TrimSpace(w.WorkingDirectory()); dir != "" {
				cwd = dir
			}
		}
	}
	return strings.TrimSpace(cwd)
}

// autoreviewHasGit reports whether the workspace root has version control.
func (u *UI) autoreviewHasGit() bool {
	cwd := u.autoreviewWorkdir()
	if cwd == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(cwd, ".git"))
	return err == nil && st != nil
}

// maybeTriggerAutoreview starts a review when main becomes fully idle with an
// empty queue. It is called from handleAgentEvent. Normal user tasks and
// delegated-work callbacks start the first review (round 0->1) only when the
// agent actually changed files, including delegated edits now attributed to
// main's callback summary; read-only answers (e.g. "is this issue fixed" with
// no edits) and tasks where the agent explicitly opts out never trigger a
// review. The agent decides via its actions and via an explicit skip marker
// in its final response (see AutoreviewSuffix). Reviewer callbacks advance
// the bounded loop (round 1->final, round 2->idle).
func (u *UI) maybeTriggerAutoreview(event session.Event) {
	if u.manager == nil {
		return
	}
	if event.Agent.ID != "main" {
		return
	}
	if event.Agent.Status != session.StatusCompleted {
		return
	}
	if strings.TrimSpace(event.Agent.CurrentTask) == "/compact" {
		return
	}
	u.screenMu.Lock()
	enabled := u.autoreviewEnabled
	provider := strings.TrimSpace(u.autoreviewProvider)
	model := strings.TrimSpace(u.autoreviewModel)
	round := u.autoreviewRound
	reviewerID := strings.TrimSpace(u.autoreviewReviewerID)
	u.screenMu.Unlock()
	if !enabled || provider == "" || model == "" {
		return
	}
	summary, err := u.manager.Summary("main")
	if err != nil {
		return
	}
	if summary.QueueDepth != 0 || summary.ActiveTaskID != "" {
		return
	}
	if summary.Status == session.StatusRunning || summary.Status == session.StatusWaitingForApproval || summary.Status == session.StatusClosing {
		return
	}
	isReviewer := autoreviewIsReviewerCallback(event.Agent.CurrentTask, reviewerID, round)
	if isReviewer {
		// Reviewer callbacks advance the bounded loop without eligibility
		// checks: the review was already deemed necessary when it started,
		// and the reviewer itself is read-only.
		switch {
		case round == autoreviewRoundFirst:
			u.startAutoreview(summary, true)
		case round == autoreviewRoundFinal:
			u.screenMu.Lock()
			u.autoreviewRound = autoreviewRoundIdle
			u.screenMu.Unlock()
			u.printSystemMessage(dim + "Autoreview final verdict processed." + reset)
			u.drawStatusBar()
		default:
			// Unexpected reviewer callback while idle; ignore.
		}
		return
	}
	if reason, skip := autoreviewSkipReason(summary); skip {
		// Read-only or explicitly-skipped work cancels any in-flight chain
		// so a stale reviewer verdict cannot restart the loop. Clean skips
		// stay silent to keep read-only answers quiet; explicit agent
		// skips print so the decision is visible.
		// Delegated edits are already attributed to main's callback
		// summary via the manager, so no workspace-dirty fallback is
		// needed here and global dirt would over-trigger on pristine
		// questions.
		if round != autoreviewRoundIdle {
			u.screenMu.Lock()
			u.autoreviewRound = autoreviewRoundIdle
			u.screenMu.Unlock()
		}
		if reason == autoreviewSkipAgent {
			u.printSystemMessage(dim + "Autoreview skipped (agent requested no review)." + reset)
			u.drawStatusBar()
		}
		return
	}
	switch {
	case round == autoreviewRoundIdle:
		// Covers both fresh user work and delegated-work callbacks
		// (isCallback) whose files were attributed to main. Starts the
		// first review once.
		u.startAutoreview(summary, false)
	case round != autoreviewRoundIdle:
		// New work (user task or delegated completion) landed while a
		// review chain was in flight; restart the chain for the latest
		// task so every change gets reviewed once.
		u.screenMu.Lock()
		u.autoreviewRound = autoreviewRoundIdle
		u.screenMu.Unlock()
		u.startAutoreview(summary, false)
	default:
		// Unreachable; kept to avoid review storms on future rounds.
	}
}

// autoreviewIsReviewerCallback reports whether a main completion is the
// automatic callback from the reusable reviewer tab. Reviewer callbacks
// advance the bounded loop without eligibility checks. Normal delegation
// callbacks fall through to the regular ChangedFiles gate so delegated
// edits are reviewed once via attribution.
func autoreviewIsReviewerCallback(currentTask, reviewerID string, round int) bool {
	if !strings.Contains(currentTask, "Sub-agent callback") {
		return false
	}
	if strings.TrimSpace(reviewerID) != "" {
		return strings.Contains(currentTask, strings.TrimSpace(reviewerID))
	}
	// Fallback for tests and pre-ID state: treat in-flight callbacks as
	// reviewer callbacks to preserve the bounded loop.
	return round == autoreviewRoundFirst || round == autoreviewRoundFinal
}

// autoreview skip reasons for maybeTriggerAutoreview.
const (
	autoreviewSkipNone  = ""
	autoreviewSkipClean = "no file changes"
	autoreviewSkipAgent = "agent requested no review"
)

// autoreviewSkipReason reports whether the just-finished main task should not
// trigger a review. The agent decides: read-only work with no file changes
// never triggers, and an explicit skip marker in the final response opts out
// even when files changed.
func autoreviewSkipReason(summary session.Summary) (string, bool) {
	if !autoreviewHasChanges(summary) {
		return autoreviewSkipClean, true
	}
	if autoreviewAgentSkipped(summary.LastOutcome) {
		return autoreviewSkipAgent, true
	}
	return autoreviewSkipNone, false
}

// autoreviewHasChanges reports whether the last main request touched code.
func autoreviewHasChanges(summary session.Summary) bool {
	for _, path := range summary.ChangedFiles {
		if strings.TrimSpace(path) != "" {
			return true
		}
	}
	return false
}

// autoreviewAgentSkipped reports whether main's final response explicitly opts
// out of review. Matching is case-insensitive and requires either a bracketed
// tag or an explicit no-review phrase so ordinary prose (for example "I did
// not skip review") never skips by accident. The prompt suffix tells the
// model to lead with the tag so it survives LastOutcome head-truncation.
func autoreviewAgentSkipped(outcome string) bool {
	lowered := strings.ToLower(outcome)
	for _, marker := range []string{
		"[skip review]",
		"[no review]",
		"no review needed",
		"no review required",
		"no code review needed",
	} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// startAutoreview ensures the reusable reviewer tab and submits the review
// task with delegation source so completion enqueues a callback to main.
func (u *UI) startAutoreview(mainSummary session.Summary, isFinal bool) {
	reviewerID, ok := u.ensureAutoreviewReviewer()
	if !ok {
		return
	}
	prompt := buildAutoreviewPrompt(mainSummary, u.autoreviewHasGit(), isFinal)
	submitted := false
	if prompter, ok := u.manager.(interface {
		SubmitPrompt(session.PromptSubmission) (session.Submission, error)
	}); ok {
		intent := session.IntentQueue
		_, err := prompter.SubmitPrompt(session.PromptSubmission{
			AgentID: reviewerID,
			Prompt:  prompt,
			Intent:  intent,
			Source:  autoreviewDelegationSource,
			Actor:   "main",
		})
		if err == nil {
			submitted = true
		} else {
			u.printSystemMessage(yellow + "Autoreview could not start review: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
	}
	if !submitted {
		if _, err := u.manager.Submit(reviewerID, prompt); err != nil {
			u.printSystemMessage(yellow + "Autoreview could not start review: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
	}
	next := autoreviewRoundFirst
	if isFinal {
		next = autoreviewRoundFinal
	}
	u.screenMu.Lock()
	u.autoreviewRound = next
	u.autoreviewReviewerID = reviewerID
	u.screenMu.Unlock()
	label := "Autoreview started"
	if isFinal {
		label = "Autoreview final review started"
	}
	u.printSystemMessage(dim + label + " with " + reviewerID + "." + reset)
	u.drawStatusBar()
}

// ensureAutoreviewReviewer finds or creates the reusable reviewer tab with
// the configured provider/model, bypassing the allow-models roster. Write
// and edit stay disabled; shell remains for search/diff.
func (u *UI) ensureAutoreviewReviewer() (string, bool) {
	u.screenMu.Lock()
	provider := strings.TrimSpace(u.autoreviewProvider)
	model := strings.TrimSpace(u.autoreviewModel)
	existing := strings.TrimSpace(u.autoreviewReviewerID)
	u.screenMu.Unlock()
	if provider == "" || model == "" {
		u.printSystemMessage(yellow + "Autoreview needs a reviewer model; pick one with /autoreview." + reset)
		return "", false
	}
	if existing != "" {
		if summary, err := u.manager.Summary(existing); err == nil {
			if summary.Status != session.StatusClosing && summary.Status != session.StatusClosed {
				if summary.Model == model {
					u.restrictReviewerTools(existing)
					return existing, true
				}
				// Reviewer model changed; close the stale tab when idle.
				if summary.Status != session.StatusRunning && summary.Status != session.StatusWaitingForApproval {
					_ = u.manager.Close(existing)
				} else {
					u.restrictReviewerTools(existing)
					return existing, true
				}
			}
		}
	}
	// Reuse any live tab already named review to avoid tab spam.
	for _, summary := range u.manager.List() {
		if strings.EqualFold(strings.TrimSpace(summary.Name), "review") && summary.Status != session.StatusClosing && summary.Status != session.StatusClosed {
			if summary.Model == model {
				u.screenMu.Lock()
				u.autoreviewReviewerID = summary.ID
				u.screenMu.Unlock()
				u.restrictReviewerTools(summary.ID)
				return summary.ID, true
			}
		}
	}
	created, err := u.createAutoreviewReviewer(provider, model)
	if err != nil {
		u.printSystemMessage(yellow + "Autoreview could not create reviewer: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return "", false
	}
	u.screenMu.Lock()
	u.autoreviewReviewerID = created.ID
	u.screenMu.Unlock()
	u.restrictReviewerTools(created.ID)
	return created.ID, true
}

func (u *UI) createAutoreviewReviewer(provider, model string) (session.Summary, error) {
	type backendProvider interface {
		Backend(string) (session.CreationBackend, error)
		CreateWithProvider(string, session.CreationBackend) (session.Summary, error)
	}
	if manager, ok := u.manager.(backendProvider); ok {
		backend := session.CreationBackend{Provider: provider, ResolveProfile: true}
		summary, err := manager.CreateWithProvider(model, backend)
		if err == nil {
			_ = u.manager.Rename(summary.ID, "review")
			return summary, nil
		}
		// Fall through to plain creation when the backend path fails.
		_ = err
	}
	summary, err := u.manager.Create(model)
	if err != nil {
		return session.Summary{}, err
	}
	_ = u.manager.Rename(summary.ID, "review")
	return summary, nil
}

func (u *UI) restrictReviewerTools(id string) {
	value, ok := u.manager.Runner(id)
	if !ok {
		return
	}
	runner, ok := value.(toolRunner)
	if !ok {
		return
	}
	for _, name := range []string{"write", "edit"} {
		func() {
			defer func() { _ = recover() }()
			runner.ToggleTool(name, false)
		}()
	}
}
