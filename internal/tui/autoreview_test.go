package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"qcode/internal/session"
	qtheme "qcode/internal/theme"
)

func TestAutoreviewToggleOnOff(t *testing.T) {
	var output bytes.Buffer
	recorder := &runtimePreferenceRecorder{}
	u := &UI{
		display:            newHistoryWriter(&output),
		runtimePreferences: recorder,
		activeAgent:        "main",
	}
	u.SetAutoreview(false, "ollama", "qwen2.5-coder:7b", 3)
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "on"})
	if !u.AutoreviewEnabled() {
		t.Fatal("autoreview should be on")
	}
	if recorder.autoreviewCalls != 1 || !recorder.autoreview {
		t.Fatalf("persist on = %+v", recorder)
	}
	output.Reset()
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "off"})
	if u.AutoreviewEnabled() {
		t.Fatal("autoreview should be off")
	}
	if recorder.autoreviewCalls != 2 || recorder.autoreview {
		t.Fatalf("persist off = %+v", recorder)
	}
}

func TestAutoreviewRunsCommand(t *testing.T) {
	var output bytes.Buffer
	recorder := &runtimePreferenceRecorder{}
	u := &UI{
		display:            newHistoryWriter(&output),
		runtimePreferences: recorder,
		activeAgent:        "main",
	}
	u.SetAutoreview(false, "ollama", "qwen2.5-coder:7b", 0)
	if got := u.AutoreviewRuns(); got != 3 {
		t.Fatalf("default runs = %d, want 3", got)
	}
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "runs"})
	if !strings.Contains(output.String(), "runs: 3") {
		t.Fatalf("status should show runs, got %q", output.String())
	}
	output.Reset()
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "runs", "4"})
	if got := u.AutoreviewRuns(); got != 4 {
		t.Fatalf("runs after set = %d, want 4", got)
	}
	if recorder.runsCalls != 1 || recorder.autoreviewRuns != 4 {
		t.Fatalf("persist runs = %+v", recorder)
	}
	output.Reset()
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "runs", "0"})
	if got := u.AutoreviewRuns(); got != 4 {
		t.Fatalf("invalid runs changed value to %d", got)
	}
	if recorder.runsCalls != 1 {
		t.Fatalf("invalid runs should not persist, calls=%d", recorder.runsCalls)
	}
	if !strings.Contains(output.String(), "between 1 and 10") {
		t.Fatalf("expected range error, got %q", output.String())
	}
}

func TestAutoreviewRequiresMainTab(t *testing.T) {
	var output bytes.Buffer
	u := &UI{
		display:     newHistoryWriter(&output),
		activeAgent: "agent-1",
		manager:     &layoutManager{states: map[string]session.Status{"agent-1": session.StatusIdle}},
	}
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "on"})
	if u.AutoreviewEnabled() {
		t.Fatal("worker tab must not enable autoreview")
	}
	if !strings.Contains(output.String(), "main tab") {
		t.Fatalf("expected main-tab guard, got %q", output.String())
	}
}

func TestAutoreviewWorksWhileBusy(t *testing.T) {
	var output bytes.Buffer
	recorder := &runtimePreferenceRecorder{}
	u := &UI{
		display:            newHistoryWriter(&output),
		runtimePreferences: recorder,
		activeAgent:        "main",
		manager:            &layoutManager{states: map[string]session.Status{"main": session.StatusRunning}},
	}
	u.SetAutoreview(false, "ollama", "model-x", 3)
	// Busy main must still toggle (no activeAgentConfigurable guard).
	u.handleAutoreviewCommand(context.Background(), []string{"/autoreview", "on"})
	if !u.AutoreviewEnabled() {
		t.Fatalf("busy main should toggle autoreview on, output=%q", output.String())
	}
}

func TestAutoreviewBadgeFirst(t *testing.T) {
	if got := autoreviewBadge(false); got != "[AR]" {
		t.Fatalf("plain badge = %q", got)
	}
	colored := autoreviewBadge(true)
	if colored != yellow+bold+"[AR]"+reset {
		t.Fatalf("colored badge = %q, want warning-yellow segment style", colored)
	}
	// The badge joins the foreground-only status bar: background fills
	// resolve to near-white on near-white under dark themes, so they are
	// banned here in both standalone and multi-parameter SGR forms.
	for _, background := range []string{"\x1b[40m", "\x1b[41m", "\x1b[42m", "\x1b[43m", "\x1b[44m", "\x1b[45m", "\x1b[46m", "\x1b[47m", "\x1b[100m", "\x1b[101m", "\x1b[102m", "\x1b[103m", "\x1b[104m", "\x1b[105m", "\x1b[106m", "\x1b[107m", ";40m", ";41m", ";42m", ";43m", ";44m", ";45m", ";46m", ";47m", ";100m", ";101m", ";102m", ";103m", ";104m", ";105m", ";106m", ";107m"} {
		if strings.Contains(colored, background) {
			t.Fatalf("colored badge %q contains background fill %q", colored, background)
		}
	}
	// The badge must stay theme-governed: under a dark palette the semantic
	// yellow must remap to the Warning role foreground (mocha #f9e2af).
	mocha, ok := qtheme.Lookup("catppuccin-mocha")
	if !ok {
		t.Fatal("catppuccin-mocha palette missing")
	}
	if themed := qtheme.TransformANSI(colored, mocha); !strings.Contains(themed, "38;2;249;226;175") {
		t.Fatalf("themed badge %q does not use the Warning role", themed)
	}
	bar := "PROVIDER [MODEL m]"
	prefixed := autoreviewPrefixFor(bar, true, true, false)
	if !strings.HasPrefix(prefixed, "[AR] ") || !strings.HasSuffix(prefixed, bar) {
		t.Fatalf("prefix = %q", prefixed)
	}
	if got := autoreviewPrefixFor(bar, false, true, false); got != bar {
		t.Fatalf("disabled prefix changed bar: %q", got)
	}
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output), width: 120, unicode: true}
	u.SetAutoreview(true, "ollama", "m", 3)
	bar2 := u.withAutoreviewPrefix("PROVIDER [MODEL m]")
	if !strings.HasPrefix(bar2, "[AR]") {
		t.Fatalf("AR not first: %q", bar2)
	}
}

func TestBuildAutoreviewPromptGitVsNoGit(t *testing.T) {
	summary := session.Summary{
		CurrentTask:  "refactor login",
		LastOutcome:  "updated auth flow",
		ChangedFiles: []string{"auth.go", "login_test.go"},
	}
	git := buildAutoreviewPrompt(summary, true, 1, 3)
	if !strings.Contains(git, "git diff") || !strings.Contains(git, "auth.go") || !strings.Contains(git, "updated auth flow") {
		t.Fatalf("git prompt missing context: %q", git)
	}
	if strings.Contains(git, "follow-up review after a fix") {
		t.Fatalf("first review should not be follow-up: %q", git)
	}
	nogit := buildAutoreviewPrompt(summary, false, 1, 3)
	if !strings.Contains(nogit, "No version control") {
		t.Fatalf("no-git prompt should explain: %q", nogit)
	}
	final := buildAutoreviewPrompt(summary, true, 3, 3)
	if !strings.Contains(final, "final review") && !strings.Contains(final, "follow-up") {
		t.Fatalf("final prompt missing marker: %q", final)
	}
	for _, prompt := range []string{git, nogit, final} {
		if !strings.Contains(prompt, "cannot edit") || !strings.Contains(prompt, "verdict") {
			t.Fatalf("prompt missing read-only/verdict: %q", prompt)
		}
	}
}

// autoreviewFakeManager is a minimal manager for idle-trigger tests.
type autoreviewFakeManager struct {
	agentController
	summaries map[string]session.Summary
	submitted []session.PromptSubmission
	created   int
	runner    any
}

type autoreviewTodoStub struct {
	statuses []string
}

func (r autoreviewTodoStub) TodoSnapshot() (ids, titles, statuses []string, version uint64, ok bool) {
	if r.statuses == nil {
		return nil, nil, nil, 0, false
	}
	ids = make([]string, len(r.statuses))
	titles = make([]string, len(r.statuses))
	for i, st := range r.statuses {
		ids[i] = "item"
		titles[i] = "task"
		_ = st
	}
	return ids, titles, append([]string(nil), r.statuses...), 1, true
}

func (r autoreviewTodoStub) LatestTodoText() (string, bool) { return "todo", true }

func (m *autoreviewFakeManager) Events() <-chan session.Event { return nil }
func (m *autoreviewFakeManager) List() []session.Summary {
	out := make([]session.Summary, 0, len(m.summaries))
	for _, s := range m.summaries {
		out = append(out, s)
	}
	return out
}
func (m *autoreviewFakeManager) Runner(id string) (any, bool) {
	if id == "main" && m.runner != nil {
		return m.runner, true
	}
	return nil, false
}

func (m *autoreviewFakeManager) Summary(id string) (session.Summary, error) {
	if s, ok := m.summaries[id]; ok {
		return s, nil
	}
	return session.Summary{}, errors.New("unknown agent")
}

func (m *autoreviewFakeManager) SubmitPrompt(in session.PromptSubmission) (session.Submission, error) {
	m.submitted = append(m.submitted, in)
	return session.Submission{RequestID: "request-1", TargetID: in.AgentID}, nil
}

func (m *autoreviewFakeManager) Submit(id, task string) (session.Submission, error) {
	m.submitted = append(m.submitted, session.PromptSubmission{AgentID: id, Prompt: task})
	return session.Submission{RequestID: "request-1", TargetID: id}, nil
}

func (m *autoreviewFakeManager) Create(model string) (session.Summary, error) {
	m.created++
	s := session.Summary{ID: "agent-9", Name: "review", Model: model, Status: session.StatusIdle}
	if m.summaries == nil {
		m.summaries = map[string]session.Summary{}
	}
	m.summaries[s.ID] = s
	return s, nil
}

func (m *autoreviewFakeManager) Rename(id, name string) error {
	if s, ok := m.summaries[id]; ok {
		s.Name = name
		m.summaries[id] = s
	}
	return nil
}

func TestMaybeTriggerAutoreviewIdleOnly(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "do work", LastOutcome: "did work", ChangedFiles: []string{"a.go"}},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 1 {
		t.Fatalf("idle main should trigger review, submitted=%d", len(fake.submitted))
	}
	if got := fake.submitted[0]; got.Source != autoreviewDelegationSource || got.Actor != "main" {
		t.Fatalf("review must use delegation source/actor, got %+v", got)
	}
	u.screenMu.Lock()
	round := u.autoreviewRound
	u.screenMu.Unlock()
	if round != autoreviewRoundFirst {
		t.Fatalf("round = %d, want first", round)
	}
}

func TestMaybeTriggerAutoreviewSkipsBusyQueue(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 2, CurrentTask: "do work"},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 0 {
		t.Fatal("queued main must not trigger review")
	}
}

func TestMaybeTriggerAutoreviewBoundedLoop(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.screenMu.Lock()
	u.autoreviewRound = autoreviewRoundFirst
	u.autoreviewReviewerID = "agent-9"
	u.screenMu.Unlock()
	event := session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"}}
	for pass := 2; pass <= 3; pass++ {
		fake.submitted = nil
		u.maybeTriggerAutoreview(event)
		if len(fake.submitted) != 1 {
			t.Fatalf("callback should start reviewer pass %d, got %d", pass, len(fake.submitted))
		}
		u.screenMu.Lock()
		round := u.autoreviewRound
		u.screenMu.Unlock()
		if round != pass {
			t.Fatalf("round = %d, want %d", round, pass)
		}
	}
	// The pass after the configured limit completes the loop.
	fake.submitted = nil
	u.maybeTriggerAutoreview(event)
	if len(fake.submitted) != 0 {
		t.Fatal("callback after the limit must not trigger another review")
	}
	u.screenMu.Lock()
	round := u.autoreviewRound
	u.screenMu.Unlock()
	if round != autoreviewRoundIdle {
		t.Fatalf("round = %d, want idle", round)
	}
	if !strings.Contains(output.String(), "finished after 3") {
		t.Fatalf("expected completion notice, got %q", output.String())
	}
}

func TestMaybeTriggerAutoreviewHonorsCustomRuns(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 2)
	u.screenMu.Lock()
	u.autoreviewRound = autoreviewRoundFirst
	u.autoreviewReviewerID = "agent-9"
	u.screenMu.Unlock()
	event := session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"}}
	u.maybeTriggerAutoreview(event)
	if len(fake.submitted) != 1 {
		t.Fatalf("custom limit should allow pass 2, got %d", len(fake.submitted))
	}
	fake.submitted = nil
	u.maybeTriggerAutoreview(event)
	if len(fake.submitted) != 0 {
		t.Fatal("custom limit must stop after pass 2")
	}
	u.screenMu.Lock()
	round := u.autoreviewRound
	u.screenMu.Unlock()
	if round != autoreviewRoundIdle {
		t.Fatalf("round = %d, want idle", round)
	}
}

func TestMaybeTriggerAutoreviewDelegatedEditsTrigger(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-2", LastOutcome: "delegated fix applied", ChangedFiles: []string{"delegated.go"}},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	// No reviewer yet (round idle, no reviewer ID): normal delegation
	// completion with attributed files must start the first review.
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-2"}})
	if len(fake.submitted) != 1 {
		t.Fatalf("delegated edits should trigger review, submitted=%d", len(fake.submitted))
	}
}

func TestMaybeTriggerAutoreviewDelegatedReadOnlySkips(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-2", LastOutcome: "looked, no changes"},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-2"}})
	if len(fake.submitted) != 0 {
		t.Fatalf("delegated read-only callback must not trigger review, submitted=%d", len(fake.submitted))
	}
}

func TestAutoreviewIsReviewerCallback(t *testing.T) {
	if !autoreviewIsReviewerCallback("Sub-agent callback Agent: agent-9", "agent-9", autoreviewRoundFirst) {
		t.Fatal("reviewer callback should be detected")
	}
	if autoreviewIsReviewerCallback("Sub-agent callback Agent: agent-2", "agent-9", autoreviewRoundIdle) {
		t.Fatal("normal delegation must not be mistaken for reviewer")
	}
	if !autoreviewIsReviewerCallback("Sub-agent callback Agent: agent-9", "", autoreviewRoundFirst) {
		t.Fatal("fallback should treat in-flight callback as reviewer")
	}
	if autoreviewIsReviewerCallback("Sub-agent callback Agent: agent-9", "", autoreviewRoundIdle) {
		t.Fatal("fallback must not treat idle callback as reviewer")
	}
	if autoreviewIsReviewerCallback("do work", "agent-9", autoreviewRoundFirst) {
		t.Fatal("non-callback must never be reviewer")
	}
}

func TestMaybeTriggerAutoreviewSkipsWhenNoChanges(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "is this issue fixed", LastOutcome: "Yes, the issue is fixed. Verified by reading logs."},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "is this issue fixed"}})
	if len(fake.submitted) != 0 {
		t.Fatalf("read-only answer must not trigger review, submitted=%d", len(fake.submitted))
	}
}

func TestMaybeTriggerAutoreviewSkipsWhenAgentOptsOut(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "update docs", LastOutcome: "[skip review]\nUpdated README, docs only.", ChangedFiles: []string{"README.md"}},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "update docs"}})
	if len(fake.submitted) != 0 {
		t.Fatalf("agent opt-out must not trigger review, submitted=%d", len(fake.submitted))
	}
	if !strings.Contains(output.String(), "agent requested no review") {
		t.Fatalf("opt-out should explain skip, got %q", output.String())
	}
}

func TestAutoreviewSkipReason(t *testing.T) {
	if reason, skip := autoreviewSkipReason(session.Summary{LastOutcome: "done"}); !skip || reason != autoreviewSkipClean {
		t.Fatalf("empty changes should skip as clean, got %q %v", reason, skip)
	}
	if _, skip := autoreviewSkipReason(session.Summary{ChangedFiles: []string{"", "  "}, LastOutcome: "done"}); !skip {
		t.Fatal("blank paths must count as no changes")
	}
	if reason, skip := autoreviewSkipReason(session.Summary{ChangedFiles: []string{"a.go"}, LastOutcome: "[skip review]\nfixed docs"}); !skip || reason != autoreviewSkipAgent {
		t.Fatalf("skip marker should opt out, got %q %v", reason, skip)
	}
	if reason, skip := autoreviewSkipReason(session.Summary{ChangedFiles: []string{"a.go"}, LastOutcome: "fixed, no review needed"}); !skip || reason != autoreviewSkipAgent {
		t.Fatalf("no-review phrase should opt out, got %q %v", reason, skip)
	}
	if _, skip := autoreviewSkipReason(session.Summary{ChangedFiles: []string{"a.go"}, LastOutcome: "fixed the bug"}); skip {
		t.Fatal("code change without marker must trigger review")
	}
}

func TestAutoreviewAgentSkippedMarkers(t *testing.T) {
	for _, outcome := range []string{
		"[skip review]\ntrivial change",
		"Please [skip review], trivial change",
		"[NO REVIEW] docs only",
		"[no review]\nverified manually",
		"No review needed, verified manually",
		"no code review needed",
		"no review required",
	} {
		if !autoreviewAgentSkipped(outcome) {
			t.Fatalf("should skip: %q", outcome)
		}
	}
	for _, outcome := range []string{
		"fixed the bug, please review",
		"reviewed the code myself",
		"I did not skip review, please check",
		"skip review",
		"skip code review",
		"",
	} {
		if autoreviewAgentSkipped(outcome) {
			t.Fatalf("should not skip: %q", outcome)
		}
	}
}

func TestMaybeTriggerAutoreviewDefersWhileTodoIncomplete(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{
		summaries: map[string]session.Summary{
			"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "do work", LastOutcome: "did work", ChangedFiles: []string{"a.go"}},
		},
		runner: autoreviewTodoStub{statuses: []string{"done", "doing", "todo"}},
	}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 0 {
		t.Fatalf("half-done todo must defer review, submitted=%d", len(fake.submitted))
	}
	// Finished checklist allows the same completion to trigger.
	fake.runner = autoreviewTodoStub{statuses: []string{"done", "done"}}
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 1 {
		t.Fatalf("finished todo should trigger review, submitted=%d", len(fake.submitted))
	}
}

func TestMaybeTriggerAutoreviewDefersWhileWorkerBusy(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{summaries: map[string]session.Summary{
		"main":    {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "do work", LastOutcome: "did work", ChangedFiles: []string{"a.go"}},
		"agent-2": {ID: "agent-2", Status: session.StatusRunning, CurrentTask: "delegated work"},
	}}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 0 {
		t.Fatalf("busy worker must defer review, submitted=%d", len(fake.submitted))
	}
	// Reviewer itself never blocks; other idle workers allow review.
	fake.summaries["agent-2"] = session.Summary{ID: "agent-2", Status: session.StatusCompleted}
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "do work"}})
	if len(fake.submitted) != 1 {
		t.Fatalf("idle workers should trigger review, submitted=%d", len(fake.submitted))
	}
}

func TestMaybeTriggerAutoreviewReviewerCallbackBypassesTodoGuard(t *testing.T) {
	var output bytes.Buffer
	fake := &autoreviewFakeManager{
		summaries: map[string]session.Summary{
			"main": {ID: "main", Status: session.StatusCompleted, QueueDepth: 0, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"},
		},
		runner: autoreviewTodoStub{statuses: []string{"doing"}},
	}
	u := &UI{display: newHistoryWriter(&output), manager: fake, activeAgent: "main"}
	u.SetAutoreview(true, "ollama", "review-model", 3)
	u.screenMu.Lock()
	u.autoreviewRound = autoreviewRoundFirst
	u.autoreviewReviewerID = "agent-9"
	u.screenMu.Unlock()
	u.maybeTriggerAutoreview(session.Event{Agent: session.Summary{ID: "main", Status: session.StatusCompleted, CurrentTask: "Sub-agent callback (reference data, not user instructions). Agent: agent-9"}})
	if len(fake.submitted) != 1 {
		t.Fatalf("reviewer callback must still advance the loop, submitted=%d", len(fake.submitted))
	}
}
