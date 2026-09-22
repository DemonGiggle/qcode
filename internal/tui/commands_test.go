package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	term "qcode/internal/lineedit"

	"qcode/internal/session"
)

type resettableRunner struct {
	reset bool
}

func (*resettableRunner) Run(context.Context, string) error { return nil }
func (r *resettableRunner) ResetSession()                   { r.reset = true }

type configurableMaxStepsRunner struct {
	maxSteps int
}

func (*configurableMaxStepsRunner) Run(context.Context, string) error { return nil }
func (r *configurableMaxStepsRunner) MaxSteps() int                   { return r.maxSteps }
func (r *configurableMaxStepsRunner) SetMaxSteps(maxSteps int)        { r.maxSteps = maxSteps }

type runtimePreferenceRecorder struct {
	model       string
	thinking    string
	maxSteps    int
	modelCalls  int
	stepsCalls  int
	modelErr    error
	maxStepsErr error
}

func (r *runtimePreferenceRecorder) PersistModel(model, thinking string) error {
	r.modelCalls++
	r.model, r.thinking = model, thinking
	return r.modelErr
}

func (r *runtimePreferenceRecorder) PersistMaxSteps(maxSteps int) error {
	r.stepsCalls++
	r.maxSteps = maxSteps
	return r.maxStepsErr
}

type configurableModelRunner struct {
	configurableMaxStepsRunner
	models []string
	model  string
}

func (*configurableModelRunner) Run(context.Context, string) error { return nil }
func (r *configurableModelRunner) ListModels(context.Context) ([]string, error) {
	return append([]string(nil), r.models...), nil
}
func (r *configurableModelRunner) SetModel(model string) { r.model = model }

type preferenceAgentController struct{ agentController }

func TestMatchingSlashCommands(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{line: "", want: nil},
		{line: "/", want: []string{"/agent", "/bash", "/clear", "/compact", "/diff", "/exit", "/export", "/help", "/history", "/learn", "/maxsteps", "/model", "/new", "/plan", "/resume", "/remote", "/skill", "/quit", "/tool", "/verbose"}},
		{line: "/d", want: []string{"/diff"}},
		{line: "/h", want: []string{"/help", "/history"}},
		{line: "/m", want: []string{"/maxsteps", "/model"}},
		{line: "/max", want: []string{"/maxsteps"}},
		{line: "/n", want: []string{"/new"}},
		{line: "/s", want: []string{"/skill"}},
		{line: "/ski", want: []string{"/skill"}},
		{line: "/qu", want: []string{"/quit"}},
		{line: "/v", want: []string{"/verbose"}},
		{line: "/unknown", want: nil},
		{line: "/help now", want: nil},
	}
	for _, test := range tests {
		matches := matchingSlashCommands(test.line)
		got := make([]string, len(matches))
		for index, match := range matches {
			got[index] = match.name
		}
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Errorf("matchingSlashCommands(%q) = %v, want %v", test.line, got, test.want)
		}
	}
}

func TestPrintCommandHelp(t *testing.T) {
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output), width: 100}

	u.printCommandHelp([]string{"model"})
	if got := output.String(); !strings.Contains(got, "/model [<model> [thinking]]") || !strings.Contains(got, "Choose a model and optional thinking level") {
		t.Fatalf("model help = %q", got)
	}

	output.Reset()
	u.printCommandHelp([]string{"/PLAN"})
	if got := output.String(); !strings.Contains(got, "/plan [off|show|act]") {
		t.Fatalf("plan help = %q", got)
	}

	output.Reset()
	u.printCommandHelp([]string{"missing"})
	if got := output.String(); !strings.Contains(got, "Unknown command: missing") || !strings.Contains(got, "Use /help to list commands") {
		t.Fatalf("unknown command help = %q", got)
	}

	output.Reset()
	u.printCommandHelp([]string{"model", "extra"})
	if got := output.String(); !strings.Contains(got, "Usage: /help [command]") {
		t.Fatalf("help usage = %q", got)
	}
}

func TestUpdateMaxSteps(t *testing.T) {
	var output bytes.Buffer
	runner := &configurableMaxStepsRunner{maxSteps: 32}
	u := &UI{runner: runner, display: newHistoryWriter(&output)}

	u.updateMaxSteps([]string{"/maxsteps"})
	if !strings.Contains(output.String(), "Max steps: 32") {
		t.Fatalf("query output = %q", output.String())
	}
	u.updateMaxSteps([]string{"/maxsteps", "64"})
	if runner.maxSteps != 64 || !strings.Contains(output.String(), "Max steps: 64") {
		t.Fatalf("updated max steps = %d, output = %q", runner.maxSteps, output.String())
	}
	output.Reset()
	u.updateMaxSteps([]string{"/maxsteps", "0"})
	if runner.maxSteps != 64 || !strings.Contains(output.String(), "Usage: /maxsteps <positive integer>") {
		t.Fatalf("invalid update = %d, output = %q", runner.maxSteps, output.String())
	}
}

func TestRuntimePreferencePersistenceFollowsSuccessfulRuntimeChanges(t *testing.T) {
	var output bytes.Buffer
	runner := &configurableModelRunner{
		configurableMaxStepsRunner: configurableMaxStepsRunner{maxSteps: 32},
		models:                     []string{"new-model"},
	}
	recorder := &runtimePreferenceRecorder{}
	u := &UI{
		runner:             runner,
		display:            newHistoryWriter(&output),
		input:              newInterruptReader(strings.NewReader("")),
		runtimePreferences: recorder,
	}

	u.setModelCommand(context.Background(), []string{"/model", "new-model"})
	if runner.model != "new-model" || recorder.modelCalls != 1 || recorder.model != "new-model" || recorder.thinking != "" {
		t.Fatalf("model runtime/persistence = (%q, %d, %q, %q)", runner.model, recorder.modelCalls, recorder.model, recorder.thinking)
	}
	u.updateMaxSteps([]string{"/maxsteps", "64"})
	if runner.maxSteps != 64 || recorder.stepsCalls != 1 || recorder.maxSteps != 64 {
		t.Fatalf("max steps runtime/persistence = (%d, %d, %d)", runner.maxSteps, recorder.stepsCalls, recorder.maxSteps)
	}

	recorder.modelErr = errors.New("disk full")
	recorder.maxStepsErr = errors.New("read-only")
	u.setModelCommand(context.Background(), []string{"/model", "new-model"})
	u.updateMaxSteps([]string{"/maxsteps", "96"})
	if runner.model != "new-model" || runner.maxSteps != 96 || !strings.Contains(output.String(), "Warning: unable to persist") {
		t.Fatalf("runtime changes were not retained after persistence failures: runner=%+v output=%q", runner, output.String())
	}
}

func TestInvalidRuntimeCommandsDoNotPersist(t *testing.T) {
	var output bytes.Buffer
	runner := &configurableMaxStepsRunner{maxSteps: 32}
	recorder := &runtimePreferenceRecorder{}
	u := &UI{runner: runner, display: newHistoryWriter(&output), runtimePreferences: recorder}

	u.updateMaxSteps([]string{"/maxsteps", "0"})
	u.updateMaxSteps([]string{"/maxsteps", "not-a-number"})
	if recorder.stepsCalls != 0 || runner.maxSteps != 32 {
		t.Fatalf("invalid max-steps command persisted or changed runtime: calls=%d max=%d", recorder.stepsCalls, runner.maxSteps)
	}
}

func TestRuntimePreferencesAreMainTabOnly(t *testing.T) {
	recorder := &runtimePreferenceRecorder{}
	u := &UI{
		manager:            &preferenceAgentController{},
		activeAgent:        "agent-1",
		runtimePreferences: recorder,
	}

	u.persistModelPreference("child-model", "high")
	u.persistMaxStepsPreference(64)
	if recorder.modelCalls != 0 || recorder.stepsCalls != 0 {
		t.Fatalf("child tab persisted runtime preferences: %+v", recorder)
	}

	u.activeAgent = "main"
	u.persistModelPreference("main-model", "low")
	u.persistMaxStepsPreference(32)
	if recorder.modelCalls != 1 || recorder.stepsCalls != 1 || recorder.model != "main-model" || recorder.thinking != "low" || recorder.maxSteps != 32 {
		t.Fatalf("main tab persistence = %+v", recorder)
	}
}

func TestFormatRunDuration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{duration: 400 * time.Microsecond, want: "<1ms"},
		{duration: 1250 * time.Millisecond, want: "1s"},
		{duration: 42*time.Second + 800*time.Millisecond, want: "43s"},
		{duration: time.Minute + 2345*time.Millisecond, want: "1m2s"},
		{duration: time.Minute + 42*time.Second + 800*time.Millisecond, want: "1m43s"},
	} {
		if got := formatRunDuration(test.duration); got != test.want {
			t.Errorf("formatRunDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestPrintSystemMessageHasBlankLinesAroundIt(t *testing.T) {
	var output bytes.Buffer
	u := UI{display: newHistoryWriter(&output)}
	u.printSystemMessage("Completed in 1.25s")

	if got, want := output.String(), "\nCompleted in 1.25s\n\n"; got != want {
		t.Fatalf("system message output = %q, want %q", got, want)
	}
}

func TestFormatAgentErrorKeepsMaxStepsNoticeCalm(t *testing.T) {
	notice := maxStepsNoticePrefix + "5 model steps."
	if got := formatAgentError(notice); got != notice {
		t.Fatalf("max-steps message = %q, want %q", got, notice)
	}
	if got := formatAgentError("provider unavailable"); got != "error: provider unavailable" {
		t.Fatalf("provider error = %q", got)
	}
}

func TestExitMessageHighlightsResumeCommand(t *testing.T) {
	plain := exitMessage(false)
	if strings.Contains(plain, "\x1b[") || !strings.Contains(plain, "+-----------------------------------------------------------+") || !strings.Contains(plain, "type /resume to resume it") {
		t.Fatalf("plain exit message = %q", plain)
	}
	colored := exitMessage(true)
	if !strings.Contains(colored, cyan+"+") || !strings.Contains(colored, green+"Session saved!"+reset) || !strings.Contains(colored, bold+yellow+"/resume"+reset) {
		t.Fatalf("colored exit message = %q", colored)
	}
}

func TestPrintExitMessageDoesNotRequirePriorSave(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "exit-message")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := UI{out: out, persistence: &sessionPersistence{}}
	u.printExitMessage()
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(message), "/resume") {
		t.Fatalf("exit message = %q", message)
	}
}

func TestStartNewSessionResetsRunner(t *testing.T) {
	var output bytes.Buffer
	runner := &resettableRunner{}
	u := UI{
		runner:         runner,
		display:        newHistoryWriter(&output),
		responseWriter: NewMarkdownWriter(&output, false, 80),
	}

	u.startNewSession()

	if !runner.reset {
		t.Fatal("runner session was not reset")
	}
	if !strings.Contains(output.String(), "New session started; previous context cleared.") {
		t.Fatalf("output = %q, want reset confirmation", output.String())
	}
}

func TestHeaderLogoUsesASCIIBannerWithNarrowFallback(t *testing.T) {
	wide := strings.Join(headerLogo(42), "\n")
	if len(headerLogo(42)) != 7 || !strings.Contains(wide, "#") {
		t.Fatalf("wide logo = %q", wide)
	}
	for _, character := range wide {
		if character > 0x7f {
			t.Fatalf("banner contains non-ASCII character %q", character)
		}
	}
	if got := headerLogo(41); len(got) != 1 || got[0] != "qcode" {
		t.Fatalf("narrow logo = %v", got)
	}
}

func TestGradientLineSkipsDarkestPaletteShades(t *testing.T) {
	previous := bannerColor
	bannerColor = []int{10, 20, 30, 40, 50, 60}
	t.Cleanup(func() { bannerColor = previous })

	got := gradientLine("##")
	if !strings.Contains(got, "\x1b[38;5;50m#") || !strings.Contains(got, "\x1b[38;5;60m#") {
		t.Fatalf("gradient = %q, want the two lightest shades", got)
	}
}

func TestStatusBarPlainFallback(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 80, true, false)
	want := "ollama [MODEL qwen] [WS ~/code]"
	if got != want {
		t.Fatalf("status bar = %q, want %q", got, want)
	}
}

func TestStatusBarSegmentOrder(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 120, true, false, "73% left", "I:1.2K O:340")
	want := "ollama [MODEL qwen] [CTX 73% left] [WS ~/code] [TOK I:1.2K O:340]"
	if got != want {
		t.Fatalf("status bar = %q, want %q", got, want)
	}
}

func TestStatusBarShowsStepProgress(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 120, true, false, "73% left", "I:1.2K O:340", "2/32")
	want := "ollama [MODEL qwen] [CTX 73% left] [WS ~/code] [TOK I:1.2K O:340] [STEP 2/32]"
	if got != want {
		t.Fatalf("status bar = %q, want %q", got, want)
	}
}

func TestNarrowStatusBarKeepsStepProgress(t *testing.T) {
	got := statusBar("openai", "a-very-long-model-name", "/workspace", 32, true, false, "73% left", "I:1.2K O:340", "2/32")
	if !strings.Contains(got, "[STEP 2/32]") || visibleWidth(got) > 32 {
		t.Fatalf("narrow status bar = %q, width = %d", got, visibleWidth(got))
	}
}

func TestStatusBarUsesColoredSegments(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 80, true, true)
	for _, sequence := range []string{cyan, magenta, blue} {
		if !strings.Contains(got, sequence) {
			t.Fatalf("status bar %q does not contain color %q", got, sequence)
		}
	}
	if !strings.Contains(got, cyan+bold+"ollama"+reset) {
		t.Fatalf("provider is not bold in status bar: %q", got)
	}
	for _, background := range []string{"\x1b[40m", "\x1b[41m", "\x1b[42m", "\x1b[43m", "\x1b[44m", "\x1b[45m", "\x1b[46m", "\x1b[47m"} {
		if strings.Contains(got, background) {
			t.Fatalf("status bar contains background color %q: %q", background, got)
		}
	}
	if !strings.HasSuffix(got, reset) {
		t.Fatalf("status bar does not restore terminal styling: %q", got)
	}
}

func TestWebStatusBarUsesReadableModelAndWorkspaceColors(t *testing.T) {
	got := statusBarWithRemoteColors("ollama", "qwen", "~/code", 80, true, true, false, webStatusModelColor, webStatusWorkspaceColor)
	for _, color := range []string{webStatusModelColor, webStatusWorkspaceColor} {
		if !strings.Contains(got, color) {
			t.Fatalf("web status bar %q does not contain color %q", got, color)
		}
	}
	if strings.Contains(got, magenta) || strings.Contains(got, blue) {
		t.Fatalf("web status bar still contains dark ANSI model/workspace colors: %q", got)
	}
}

func TestStatusBarShowsRemoteBadgeAtBeginning(t *testing.T) {
	plain := statusBarWithRemote("ollama", "qwen", "~/code", 80, true, false, true)
	if !strings.HasPrefix(plain, "[REMOTE] ") {
		t.Fatalf("plain remote status bar = %q", plain)
	}

	colored := statusBarWithRemote("ollama", "qwen", "~/code", 80, true, true, true)
	if !strings.HasPrefix(colored, "\x1b[1;30;42m REMOTE "+reset) {
		t.Fatalf("colored remote status bar = %q", colored)
	}
	if visibleWidth(colored) > 80 {
		t.Fatalf("colored remote status bar width = %d", visibleWidth(colored))
	}
}

func TestStatusBarFitsTerminalWidth(t *testing.T) {
	got := statusBar("openai", "a-very-long-model-name", "~/a/very/long/workspace/path", 32, true, true)
	if visibleWidth(got) > 32 {
		t.Fatalf("status bar width = %d, want at most 32: %q", visibleWidth(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("truncated status bar has no ellipsis: %q", got)
	}
}

func TestStatusBarShortensWorkspacePathBeforeTruncatingBar(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/src/company/projects/qcode", 72, true, false, "73% left", "I:1.2K O:340")
	if visibleWidth(got) > 72 {
		t.Fatalf("status bar width = %d, want at most 72: %q", visibleWidth(got), got)
	}
	if !strings.Contains(got, "[WS ~/…/qcode]") {
		t.Fatalf("status bar = %q, want an elided workspace path", got)
	}
	if !strings.Contains(got, "[TOK I:1.2K O:340]") {
		t.Fatalf("status bar = %q, workspace shortening displaced token totals", got)
	}
}

func TestStatusBarLimitsWorkspacePathOnWideTerminals(t *testing.T) {
	got := statusBar("ollama", "qwen", "/home/user/src/company/projects/qcode", 120, true, false, "73% left", "I:1.2K O:340")
	if visibleWidth(got) > 120 {
		t.Fatalf("status bar width = %d, want at most 120: %q", visibleWidth(got), got)
	}
	if !strings.Contains(got, "[WS …/company/projects/qcode]") {
		t.Fatalf("status bar = %q, want the workspace path capped to its useful tail", got)
	}
	if visibleWidth("…/company/projects/qcode") > maxWorkspaceStatusWidth {
		t.Fatalf("workspace path exceeds cap: %q", got)
	}
}

func TestShortenWorkspacePathKeepsTrailingComponents(t *testing.T) {
	tests := []struct {
		path    string
		width   int
		unicode bool
		want    string
	}{
		{path: "~/src/company/projects/qcode", width: 18, unicode: true, want: "~/…/projects/qcode"},
		{path: "/home/user/src/company/projects/qcode", width: 13, unicode: true, want: "…/qcode"},
		{path: "/home/user/src/company/projects/qcode", width: 16, unicode: false, want: ".../qcode"},
	}
	for _, test := range tests {
		if got := shortenWorkspacePath(test.path, test.width, test.unicode); got != test.want {
			t.Errorf("shortenWorkspacePath(%q, %d, %t) = %q, want %q", test.path, test.width, test.unicode, got, test.want)
		}
	}
}

func TestTabBarShowsActiveAgentAndStatuses(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "review", Status: session.StatusRunning, QueueDepth: 2},
		{ID: "agent-2", Name: "tests", Status: session.StatusCompleted},
	}
	got := tabBar(summaries, "agent-1", nil, 80, false, false)
	if !strings.Contains(got, "[main -]") || !strings.Contains(got, "*[review > +2]") || !strings.Contains(got, "[tests +]") {
		t.Fatalf("tab bar = %q", got)
	}
	if visibleWidth(got) > 80 {
		t.Fatalf("tab bar width = %d", visibleWidth(got))
	}
	if !strings.HasSuffix(got, asciiFullTabSwitchHint) {
		t.Fatalf("tab bar hint = %q", got)
	}
}

func TestTabBarUsesCompactSwitchHintWhenNeeded(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "review", Status: session.StatusRunning},
		{ID: "agent-2", Name: "tests", Status: session.StatusCompleted},
	}
	got := tabBar(summaries, "agent-1", nil, 60, true, false)
	if !strings.HasSuffix(got, compactTabSwitchHint) {
		t.Fatalf("compact tab hint = %q", got)
	}
	if visibleWidth(got) != 60 {
		t.Fatalf("compact tab bar width = %d", visibleWidth(got))
	}
}

func TestTabBarKeepsActiveAgentOnNarrowScreen(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "first-long-agent", Status: session.StatusCompleted},
		{ID: "agent-2", Name: "active", Status: session.StatusRunning},
		{ID: "agent-3", Name: "third-long-agent", Status: session.StatusFailed},
	}
	got := tabBar(summaries, "agent-2", nil, 24, false, false)
	if !strings.Contains(got, "m") || !strings.Contains(got, "active") || !strings.Contains(got, ">") || visibleWidth(got) > 24 {
		t.Fatalf("narrow tab bar = %q (width %d)", got, visibleWidth(got))
	}
	if strings.Contains(got, "Switch") {
		t.Fatalf("narrow tab bar unexpectedly contains hint = %q", got)
	}
}

func TestTabBarWindowsManyAgentsAroundActive(t *testing.T) {
	summaries := []session.Summary{{ID: "main", Name: "main", Status: session.StatusIdle}}
	for i := 1; i < 20; i++ {
		summaries = append(summaries, session.Summary{
			ID: fmt.Sprintf("agent-%d", i), Name: fmt.Sprintf("worker-%02d", i), Status: session.StatusIdle,
		})
	}

	got := tabBar(summaries, "agent-10", nil, 72, false, false)
	for _, want := range []string{"[main -]", "*[worker-10 -]", "<", ">"} {
		if !strings.Contains(got, want) {
			t.Fatalf("windowed tab bar lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "[worker-01") || strings.Contains(got, "[worker-19") {
		t.Fatalf("windowed tab bar included distant agents: %q", got)
	}
	if visibleWidth(got) > 72 {
		t.Fatalf("windowed tab bar width = %d: %q", visibleWidth(got), got)
	}

	late := tabBar(summaries, "agent-19", nil, 72, true, false)
	if !strings.Contains(late, "*[worker-19 ○]") || !strings.Contains(late, "‹") || strings.Contains(late, "›") {
		t.Fatalf("late tab window did not follow active agent: %q", late)
	}
}

func TestAgentDisplayIsolatesBackgroundOutput(t *testing.T) {
	var terminalOutput bytes.Buffer
	terminal := term.NewTerminal(readWriter{Reader: strings.NewReader(""), Writer: &terminalOutput}, "> ")
	u := &UI{terminal: terminal, activeAgent: "main"}
	mainHistory := newHistoryWriter(io.Discard)
	workerHistory := newHistoryWriter(io.Discard)
	mainDisplay := &agentDisplay{ui: u, id: "main", history: mainHistory}
	workerDisplay := &agentDisplay{ui: u, id: "agent-1", history: workerHistory}
	_, _ = workerDisplay.Write([]byte("background\n"))
	_, _ = mainDisplay.Write([]byte("foreground\n"))
	if strings.Contains(terminalOutput.String(), "background") || !strings.Contains(terminalOutput.String(), "foreground") {
		t.Fatalf("terminal output = %q", terminalOutput.String())
	}
	if got := strings.Join(workerHistory.Lines(), "\n"); !strings.Contains(got, "background") {
		t.Fatalf("worker history = %q", got)
	}
}

type queuedSubmissionManager struct {
	agentController
	prompt string
}

func (m *queuedSubmissionManager) Submit(_ string, prompt string) (session.Submission, error) {
	m.prompt = prompt
	return session.Submission{RequestID: "request-2", TargetID: "main", QueuePosition: 2}, nil
}

func (*queuedSubmissionManager) Summary(string) (session.Summary, error) {
	return session.Summary{ID: "main", Status: session.StatusRunning, QueueDepth: 2}, nil
}

func TestRunActiveTaskShowsQueuedPosition(t *testing.T) {
	manager := &queuedSubmissionManager{}
	history := newHistoryWriter(io.Discard)
	u := &UI{
		manager: manager, activeAgent: "main", display: history,
		input: newInterruptReader(nil),
	}
	if err := u.runActiveTask(context.Background(), "second prompt"); err != nil {
		t.Fatal(err)
	}
	if manager.prompt != "second prompt" || !strings.Contains(strings.Join(history.Lines(), "\n"), "Queued #2") {
		t.Fatalf("prompt = %q, history = %q", manager.prompt, history.Lines())
	}
}

func TestSlashCommandMenuReplacesPreviousRows(t *testing.T) {
	var output bytes.Buffer
	menu := slashCommandMenu{out: &output}
	menu.update(matchingSlashCommands("/"))
	output.Reset()
	menu.update(matchingSlashCommands("/h"))

	got := output.String()
	if strings.Count(got, "\x1b[1A\r\x1b[2K") != len(slashCommands) {
		t.Fatalf("menu did not clear all previous rows: %q", got)
	}
	if !strings.Contains(got, "/help    List commands or explain one command") || strings.Contains(got, "/clear") {
		t.Fatalf("menu did not render filtered command: %q", got)
	}
}

func TestSlashCommandMenuDismissesSubmittedMenu(t *testing.T) {
	var menuOutput, terminalOutput bytes.Buffer
	menu := slashCommandMenu{out: &menuOutput, visible: 2}
	menu.dismiss(&terminalOutput)
	if got := terminalOutput.String(); got != "\x1b[3A\r\x1b[2M\x1b[1B\r" {
		t.Fatalf("dismiss output = %q", got)
	}
	if menu.visible != 0 {
		t.Fatalf("visible = %d", menu.visible)
	}
}

func TestSlashCommandCompletion(t *testing.T) {
	var output bytes.Buffer
	u := UI{commandMenu: slashCommandMenu{out: &output}}

	line, pos, ok := u.completeSlashCommand("/", 1, 'h')
	if !ok || line != "/h" || pos != 2 {
		t.Fatalf("typed completion = %q, %d, %v", line, pos, ok)
	}
	line, pos, ok = u.completeSlashCommand(line, pos, '\t')
	if !ok || line != "/help" || pos != len("/help") {
		t.Fatalf("tab completion = %q, %d, %v", line, pos, ok)
	}
}

func TestSlashCommandMenuCountsWrappedRows(t *testing.T) {
	var output bytes.Buffer
	menu := slashCommandMenu{out: &output, width: 24}
	menu.update([]slashCommand{{name: "/verbose", description: "Toggle detailed action traces"}})
	if menu.visible != 4 {
		t.Fatalf("visible rows = %d; output = %q", menu.visible, output.String())
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if visibleWidth(line) > menu.width {
			t.Fatalf("line width = %d: %q", visibleWidth(line), line)
		}
	}
}
