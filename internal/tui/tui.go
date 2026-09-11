package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"qcode/internal/lineedit"

	"qcode/internal/prompt"
	"qcode/internal/session"
)

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	cyan   = "\x1b[36m"
	green  = "\x1b[32m"
	red    = "\x1b[31m"
	yellow = "\x1b[33m"
)

const inputPrompt = cyan + bold + "> " + reset
const planInputPrompt = cyan + bold + "(Plan)> " + reset

var qcodeBanner = []string{
	` #####    #####    #####   ######  #######`,
	`##   ##  ##       ##   ##  ##   ## ##     `,
	`##   ##  ##       ##   ##  ##   ## ##     `,
	`## # ##  ##       ##   ##  ##   ## ###### `,
	`##  ###  ##       ##   ##  ##   ## ##     `,
	`##   ##  ##       ##   ##  ##   ## ##     `,
	` ######   #####    #####   ######  #######`,
}

// bannerColor stores the randomly chosen color palette for the banner gradient
var bannerColor = pickBannerColor()

// bannerGradientStart skips the darkest shades so the banner stays readable on
// terminals with dim palettes or low-quality displays.
const bannerGradientStart = 4

// pickBannerColor randomly selects a color palette for the mono gradient
func pickBannerColor() []int {
	// Color palettes: each is a progression from dark to light shades
	palettes := [][]int{
		{23, 24, 25, 26, 31, 37, 43, 49, 50, 51},         // cyan shades
		{17, 18, 19, 20, 21, 56, 57, 93, 129, 201},       // blue/magenta shades
		{22, 28, 34, 40, 46, 83, 120, 157, 194, 231},     // green shades
		{52, 53, 89, 90, 126, 127, 163, 164, 203, 204},   // red/pink shades
		{58, 94, 130, 166, 172, 178, 184, 186, 220, 229}, // yellow/orange shades
		{53, 54, 55, 91, 92, 98, 99, 134, 135, 141},      // purple shades
	}
	// Use a simple hash of current time to pick a palette
	now := time.Now().UnixNano()
	return palettes[int(now)%len(palettes)]
}

type Runner interface {
	Run(context.Context, string) error
}

type verboseRunner interface {
	SetVerbose(bool)
}

type unicodeRunner interface {
	SetUnicode(bool)
}

type modelRunner interface {
	ListModels(context.Context) ([]string, error)
	SetModel(string)
}

type contextRunner interface {
	ContextRemaining() (int, bool, bool)
	RefreshContext(context.Context)
}

type compactionRunner interface {
	Compact(context.Context) (string, error)
}

type sessionRunner interface {
	ResetSession()
}

type maxStepsRunner interface {
	SetMaxSteps(int)
}

type maxStepsReader interface {
	MaxSteps() int
}

type planController interface {
	PlanMode() bool
	SetPlanMode(bool)
	LatestPlanText() (string, bool)
	ClearLatestPlan()
}

type stepRunner interface {
	StepProgress() (int, int)
}

type skillRunner interface{ SetSkills([]prompt.SkillSummary) }

type selectedSkillsRunner interface {
	SelectedSkills() []prompt.SkillSummary
}

type skillCatalogLoader func() ([]prompt.SkillSummary, error)

type toolRunner interface {
	ToggleTool(name string, enabled bool)
	ToolEnabled(name string) bool
	ToolNames() []string
}

type readWriter struct {
	io.Reader
	io.Writer
}

type historyDisplay interface {
	io.Writer
	AddLine(string)
	Clear()
	Lines() []string
	Snapshot() historySnapshot
	ExportSnapshot() historyExportSnapshot
}

type agentController interface {
	Events() <-chan session.Event
	List() []session.Summary
	Summary(string) (session.Summary, error)
	Runner(string) (any, bool)
	Create(string) (session.Summary, error)
	Start(string, string) error
	Submit(string, string) (session.Submission, error)
	Rename(string, string) error
	Cancel(string) error
	Close(string) error
	Reset(string) error
	UpdateModel(string, string) error
	SetWaitingForApproval(string, bool)
	Shutdown()
}

type UI struct {
	fixedInput         bool
	inputText          string
	inputLabel         string
	inputPosition      int
	inputFrame         string
	inputScreenRows    []string
	inputCursorRow     int
	inputCursorColumn  int
	terminal           *lineedit.Terminal
	display            historyDisplay
	responseWriter     *MarkdownWriter
	commandMenu        slashCommandMenu
	input              *interruptReader
	in                 *os.File
	out                *os.File
	runner             Runner
	provider           string
	model              string
	root               string
	verbose            bool
	width              int
	height             int
	unicode            bool
	viewport           viewport
	statusActive       bool
	statusBarText      string
	planViewActive     bool
	startupNotice      string
	startupChoice      bool
	skills             []prompt.SkillSummary
	skillLocations     []string
	skillCatalogLoader skillCatalogLoader
	onSkills           func([]string)
	consultationCursor uint64 // Guarded by screenMu; persisted with the transcript.
	manager            agentController
	activeAgent        string
	views              map[string]*agentView
	screenMu           sync.Mutex
	drafts             map[string]string
	approvalMu         sync.Mutex
	approvals          map[string][]*approvalRequest
	questionMu         sync.Mutex
	questions          []*questionRequest
	tabMu              sync.Mutex
	pendingTab         int
	agentEventsDone    chan struct{}
	uiEvents           chan struct{}
	taskIndicatorText  string
	demoPrompts        []string
	demoPromptDelay    time.Duration
	demoQueueDelay     time.Duration
	persistence        *sessionPersistence
	sessionHost        *UI
}

// SetSkillCatalog configures the optional /skill selector.
func (u *UI) SetSkillCatalog(skills []prompt.SkillSummary, onChange func([]string)) {
	u.skills = append([]prompt.SkillSummary(nil), skills...)
	u.onSkills = onChange
}

// SetSkillLocations configures the directories shown by /skill before its
// selector. Missing directories are intentionally retained in this list.
func (u *UI) SetSkillLocations(locations []string) {
	u.skillLocations = append([]string(nil), locations...)
}

// SetSkillCatalogLoader defers skill discovery until /skill needs the catalog.
// The loader may refresh the catalog on each command invocation.
func (u *UI) SetSkillCatalogLoader(loader skillCatalogLoader) {
	u.skillCatalogLoader = loader
}

func (u *UI) ensureSkillCatalog() bool {
	if u.skillCatalogLoader == nil {
		return true
	}
	summaries, err := u.skillCatalogLoader()
	if err != nil {
		u.printSystemMessage(yellow + "Cannot discover skills: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		return false
	}
	u.SetSkillCatalog(summaries, u.onSkills)
	return true
}

func New(in, out *os.File, runner Runner, provider, model, root string) *UI {
	input := newInterruptReader(in)
	rw := readWriter{Reader: input, Writer: out}
	t := lineedit.NewTerminal(rw, inputPrompt)
	width, height := terminalSize(out)
	unicodeEnabled := UnicodeEnabled()
	t.SetSize(width, height)
	display := &agentDisplay{history: newHistoryWriter(io.Discard)}
	responseWriter := NewMarkdownWriter(display, ColorEnabled(out), width)
	responseWriter.SetUnicode(unicodeEnabled)
	responseWriter.EnableDiffs()
	u := &UI{
		terminal:       t,
		display:        display,
		responseWriter: responseWriter,
		commandMenu:    slashCommandMenu{out: t, color: ColorEnabled(out), width: width},
		input:          input,
		in:             in,
		out:            out,
		runner:         runner,
		provider:       provider,
		model:          model,
		root:           root,
		width:          width,
		height:         height,
		unicode:        unicodeEnabled,
		views:          make(map[string]*agentView),
		drafts:         make(map[string]string),
		approvals:      make(map[string][]*approvalRequest),
		uiEvents:       make(chan struct{}, 1),
	}
	display.ui = u
	t.AutoCompleteCallback = u.completeSlashCommand
	input.setPageHandler(u.showPage)
	input.setTabHandler(u.requestTabSwitch)
	u.SetRunner(runner)
	return u
}

func (u *UI) Writer() io.Writer { return u.display }

func (u *UI) ResponseWriter() io.Writer { return u.responseWriter }

func (u *UI) readLine() (string, error) {
	return u.terminal.ReadLine()
}

func (u *UI) printExitMessage() {
	if u.persistence == nil {
		return
	}
	fmt.Fprint(u.out, exitMessage(ColorEnabled(u.out)))
}

func exitMessage(color bool) string {
	const width = 57
	const title = "Session saved!"
	const hint = "Next time in this folder, type /resume to resume it."
	border := "+" + strings.Repeat("-", width+2) + "+\r\n"
	if !color {
		return "\r\n" + border + fmt.Sprintf("| %-*s |\r\n| %-*s |\r\n", width, title, width, hint) + border
	}
	coloredBorder := cyan + border + reset
	coloredTitle := bold + green + title + reset
	coloredHint := "Next time in this folder, type " + bold + yellow + "/resume" + reset + " to resume it."
	return "\r\n" + coloredBorder + cyan + "|" + reset + " " + coloredTitle + strings.Repeat(" ", width-len(title)) + " " + cyan + "|" + reset + "\r\n" + cyan + "|" + reset + " " + coloredHint + strings.Repeat(" ", width-len(hint)) + " " + cyan + "|" + reset + "\r\n" + coloredBorder
}

// AddAgentView creates an isolated output/history buffer for an agent.
func (u *UI) AddAgentView(id, provider, model string) (io.Writer, io.Writer) {
	if u.sessionHost != nil {
		return u.sessionHost.AddAgentView(id, provider, model)
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	history := newHistoryWriter(io.Discard)
	display := &agentDisplay{ui: u, id: id, history: history}
	response := NewMarkdownWriter(display, ColorEnabled(u.out), u.width)
	response.SetUnicode(u.unicode)
	response.EnableDiffs()
	u.views[id] = &agentView{id: id, provider: provider, model: model, display: display, response: response}
	if u.activeAgent == "" || id == "main" {
		u.activeAgent = id
		u.display = display
		u.responseWriter = response
		u.provider = provider
		u.model = model
	}
	return display, response
}

func (u *UI) SetAgentSkillHandler(id string, onChange func([]string)) {
	if u.sessionHost != nil {
		u.sessionHost.SetAgentSkillHandler(id, onChange)
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	if view := u.views[id]; view != nil {
		view.onSkills = onChange
		if id == u.activeAgent {
			u.onSkills = onChange
		}
	}
}

func (u *UI) RemoveAgentView(id string) {
	u.screenMu.Lock()
	delete(u.views, id)
	delete(u.drafts, id)
	u.screenMu.Unlock()
}

func (u *UI) SetAgentManager(manager agentController) {
	u.screenMu.Lock()
	u.manager = manager
	u.screenMu.Unlock()
	if manager == nil {
		return
	}
	u.agentEventsDone = make(chan struct{})
	go u.watchAgentEvents(manager.Events())
}

func (u *UI) shutdownAgentManager() {
	if u.manager == nil {
		return
	}
	u.manager.Shutdown()
	if u.agentEventsDone != nil {
		<-u.agentEventsDone
	}
}

func (u *UI) SetRunner(runner Runner) {
	u.screenMu.Lock()
	u.runner = runner
	if controller, ok := runner.(planController); ok && controller.PlanMode() {
		u.inputLabel = planInputPrompt
	} else {
		u.inputLabel = inputPrompt
	}
	u.screenMu.Unlock()
	if controller, ok := runner.(planController); ok {
		if controller.PlanMode() {
			u.terminal.SetPrompt(planInputPrompt)
		} else {
			u.terminal.SetPrompt(inputPrompt)
		}
	}
	if configurable, ok := runner.(verboseRunner); ok {
		configurable.SetVerbose(u.verbose)
	}
	if configurable, ok := runner.(unicodeRunner); ok {
		configurable.SetUnicode(u.unicode)
	}
}

// SetStartupNotice displays sandbox status between the banner and first user
// prompt. requireChoice offers Continue or Leave before the session starts.
func (u *UI) SetStartupNotice(message string, requireChoice bool) {
	u.startupNotice = message
	u.startupChoice = requireChoice
}

// SetDemoPromptScript configures prompts that are injected into the line
// editor after the interactive UI starts. It is used only by --demo to make
// the asynchronous prompt queue visible in a deterministic recording.
func (u *UI) SetDemoPromptScript(prompts []string, firstDelay, queueDelay time.Duration) {
	u.demoPrompts = append([]string(nil), prompts...)
	u.demoPromptDelay = firstDelay
	u.demoQueueDelay = queueDelay
}

func (u *UI) Run(ctx context.Context) error {
	if u.runner == nil {
		return fmt.Errorf("terminal UI has no agent runner")
	}
	if !term.IsTerminal(int(u.in.Fd())) || !term.IsTerminal(int(u.out.Fd())) {
		u.shutdownAgentManager()
		return fmt.Errorf("interactive mode requires a terminal; pass a prompt argument for one-shot mode")
	}
	if runner, ok := u.runner.(contextRunner); ok {
		runner.RefreshContext(ctx)
	}
	state, err := term.MakeRaw(int(u.in.Fd()))
	if err != nil {
		u.shutdownAgentManager()
		return fmt.Errorf("enable terminal mode: %w", err)
	}
	defer func() {
		u.teardownStatusBar()
		_ = term.Restore(int(u.in.Fd()), state)
		u.printExitMessage()
	}()
	if u.manager != nil {
		defer func() { u.shutdownAgentManager(); u.closeSession() }()
	}
	u.input.start()
	u.fixedInput = true
	u.inputLabel = inputPrompt
	if controller, ok := u.runner.(planController); ok && controller.PlanMode() {
		u.inputLabel = planInputPrompt
		u.terminal.SetPrompt(planInputPrompt)
	} else {
		u.terminal.SetPrompt(inputPrompt)
	}
	u.terminal.RenderInput = u.renderInput
	u.setupStatusBar()
	stopResize := u.watchResize()
	defer stopResize()
	stopTaskIndicator := u.watchTaskIndicator()
	defer stopTaskIndicator()
	stopSessions := u.watchSessions()
	defer stopSessions()

	u.printHeader()
	if u.startupNotice != "" {
		u.printSystemMessage(yellow + u.startupNotice + reset)
	}
	if u.startupChoice {
		continued, choiceErr := u.readChoice("[c] Continue without sandbox  [l] Leave > ")
		if choiceErr != nil {
			if choiceErr == io.EOF {
				return nil
			}
			return choiceErr
		}
		if !continued {
			return nil
		}
	}
	stopDemoScript := u.startDemoPromptScript(ctx)
	defer stopDemoScript()
	for {
		u.reportSave(u.saveSession(false))
		u.handlePendingTabSwitch()
		u.handlePendingApproval(ctx)
		u.handlePendingQuestions(ctx)
		line, err := u.readLine()
		u.commandMenu.dismiss(u.out)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		u.tabMu.Lock()
		tabPending := u.pendingTab != 0
		u.tabMu.Unlock()
		if !tabPending {
			u.screenMu.Lock()
			u.drafts[u.activeAgent] = ""
			u.screenMu.Unlock()
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/resume" {
			u.resumeSession()
			continue
		}
		if u.fixedInput {
			u.display.AddLine(reset + "\n> " + line)
		} else {
			u.display.AddLine("> " + line)
		}
		u.resetPage()
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "/agent" {
			u.handleAgentCommand(ctx, fields)
			continue
		}
		if len(fields) > 0 && fields[0] == "/learn" {
			u.learn(ctx, strings.TrimSpace(strings.TrimPrefix(line, "/learn")))
			continue
		}
		if len(fields) > 0 && fields[0] == "/diff" {
			u.expandDiff(fields)
			continue
		}
		if len(fields) > 0 && fields[0] == "/export" {
			argument := strings.TrimSpace(strings.TrimPrefix(line, "/export"))
			path, exportErr := u.exportSession(argument)
			if exportErr != nil {
				u.printSystemMessage(yellow + "Export failed: " + sanitizeDiffLine(exportErr.Error(), "<ESC>") + reset)
			} else {
				u.printSystemMessage(green + "Exported session to " + sanitizeDiffLine(path, "<ESC>") + reset)
			}
			continue
		}
		if len(fields) > 0 && fields[0] == "/maxsteps" {
			u.updateMaxSteps(fields)
			u.drawStatusBar()
			continue
		}
		if len(fields) > 0 && fields[0] == "/plan" {
			u.handlePlanCommand(ctx, fields)
			continue
		}
		switch line {
		case "/quit", "/exit":
			return nil
		case "/clear":
			fmt.Fprint(u.terminal, "\x1b[2J\x1b[H")
			u.display.Clear()
			u.responseWriter.ResetDiffs()
			u.resetStatusLayout()
			u.printHeader()
			continue
		case "/compact":
			u.compactConversation(ctx)
			continue
		case "/help":
			u.printCommandHelp()
			continue
		case "/model":
			u.chooseModel(ctx)
			continue
		case "/skill":
			u.chooseSkills()
			u.drawStatusBar()
			continue
		case "/tool":
			u.chooseTools()
			u.drawStatusBar()
			continue
		case "/new":
			u.startNewSession()
			continue
		case "/verbose":
			u.screenMu.Lock()
			u.verbose = !u.verbose
			u.screenMu.Unlock()
			if configurable, ok := u.runner.(verboseRunner); ok {
				configurable.SetVerbose(u.verbose)
			}
			state := "off"
			if u.verbose {
				state = "on"
			}
			u.printSystemMessage(fmt.Sprintf("%sVerbose tracing: %s%s", dim, state, reset))
			continue
		}
		u.responseWriter.ResetDiffs()
		started := time.Now()
		if u.manager != nil {
			err = u.runActiveTask(ctx, line)
			if err != nil {
				u.printSystemMessage(yellow + "error: " + err.Error() + reset)
			}
			continue
		}
		taskCtx, cancel := context.WithCancel(ctx)
		u.input.setCancel(cancel)
		err = u.runActiveTask(taskCtx, line)
		u.input.setCancel(nil)
		cancel()
		u.drawStatusBar()
		if errors.Is(err, context.Canceled) {
			u.printSystemMessage(yellow + "Cancelled" + reset)
		} else if err != nil {
			u.printSystemMessage(yellow + "error: " + err.Error() + reset)
		} else {
			u.printSystemMessage(fmt.Sprintf("%s%sCompleted in %s%s", magenta, bold, formatRunDuration(time.Since(started)), reset))
		}
	}
}

func (u *UI) startDemoPromptScript(ctx context.Context) func() {
	if len(u.demoPrompts) == 0 || u.input == nil {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for index, prompt := range u.demoPrompts {
			delay := u.demoQueueDelay
			if index == 0 {
				delay = u.demoPromptDelay
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			case <-done:
				timer.Stop()
				return
			}
			u.input.inject([]byte(strings.TrimSpace(prompt) + "\r"))
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (u *UI) readChoice(prompt string) (bool, error) {
	u.terminal.SetPrompt(yellow + prompt + reset)
	defer u.terminal.SetPrompt(inputPrompt)
	for {
		line, err := u.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "c", "continue", "y", "yes":
			return true, nil
		case "l", "leave", "n", "no", "":
			return false, nil
		default:
			u.printSystemMessage(yellow + "Enter c to continue or l to leave." + reset)
		}
	}
}

// ApproveDirectory implements the interactive callback used by sandboxed file
// tools and request_directory_access.
func (u *UI) ApproveDirectory(ctx context.Context, requested, proposed string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	u.resetPage()
	u.printSystemMessage(yellow + "Additional directory access requested: " + sanitizeDiffLine(requested, "<ESC>") + reset)
	u.terminal.SetPrompt(yellow + "Directory to grant (Enter for " + sanitizeDiffLine(proposed, "<ESC>") + "): " + reset)
	line, err := u.readLine()
	u.terminal.SetPrompt(inputPrompt)
	if err != nil {
		return "", false, err
	}
	selected := strings.TrimSpace(line)
	if selected == "" {
		selected = proposed
	}
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(u.root, selected)
	}
	sensitive := false
	if home, homeErr := os.UserHomeDir(); homeErr == nil && pathContainsForUI(selected, home) {
		sensitive = true
		u.printSystemMessage(yellow + bold + "Warning: this grant exposes your home directory or an ancestor containing it." + reset)
	}
	u.terminal.SetPrompt(yellow + "Grant read/write access to " + sanitizeDiffLine(selected, "<ESC>") + " for this session? [y/N] " + reset)
	answer, err := u.readLine()
	u.terminal.SetPrompt(inputPrompt)
	if err != nil {
		return "", false, err
	}
	approved := strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
	if approved && sensitive {
		u.terminal.SetPrompt(yellow + bold + "Confirm broad home access by typing YES: " + reset)
		confirmation, confirmErr := u.readLine()
		u.terminal.SetPrompt(inputPrompt)
		if confirmErr != nil {
			return "", false, confirmErr
		}
		approved = strings.TrimSpace(confirmation) == "YES"
	}
	return selected, approved, nil
}

func pathContainsForUI(parent, child string) bool {
	parent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	child, err = filepath.Abs(child)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (u *UI) startNewSession() {
	if u.manager != nil {
		if err := u.manager.Reset(u.activeAgent); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
			return
		}
		u.setInputModePrompt(false)
		u.drawTabBar()
		u.drawStatusBar()
		u.responseWriter.ResetDiffs()
		u.printSystemMessage(green + "New session started; previous context cleared." + reset)
		return
	}
	resetter, ok := u.runner.(sessionRunner)
	if !ok {
		u.printSystemMessage(yellow + "Starting a new session is unavailable." + reset)
		return
	}
	resetter.ResetSession()
	u.setInputModePrompt(false)
	u.drawStatusBar()
	u.responseWriter.ResetDiffs()
	u.printSystemMessage(green + "New session started; previous context cleared." + reset)
}

func (u *UI) compactConversation(ctx context.Context) {
	runner, ok := u.runner.(compactionRunner)
	if !ok {
		u.printSystemMessage(yellow + "Conversation compaction is unavailable." + reset)
		return
	}
	u.printSystemMessage(dim + "Compacting conversation..." + reset)
	result, err := runner.Compact(ctx)
	u.drawStatusBar()
	if err != nil {
		u.printSystemMessage(yellow + "Conversation compaction failed: " + err.Error() + reset)
		return
	}
	u.printSystemMessage(green + result + reset)
}

func (u *UI) chooseModel(ctx context.Context) {
	if !u.activeAgentConfigurable() {
		return
	}
	runner, ok := u.runner.(modelRunner)
	if !ok {
		u.printSystemMessage(yellow + "Model selection is unavailable." + reset)
		return
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	u.input.setCancel(cancel)
	models, err := runner.ListModels(fetchCtx)
	u.input.setCancel(nil)
	cancel()
	if errors.Is(err, context.Canceled) {
		u.printSystemMessage(yellow + "Model selection cancelled." + reset)
		return
	}
	if err != nil {
		u.printSystemMessage(yellow + "Unable to list models: " + err.Error() + reset)
		return
	}
	if len(models) == 0 {
		u.printSystemMessage(dim + "The provider returned no models." + reset)
		return
	}
	visible := u.height - 6
	if visible > 12 {
		visible = 12
	}
	if visible < 3 {
		visible = 3
	}
	u.input.setRaw(true)
	u.beginRawSelector()
	selected, accepted, selectErr := selectModel(u.input, u.terminal, models, u.model, visible, u.width, ColorEnabled(u.out))
	u.input.setRaw(false)
	u.endRawSelector()
	if selectErr != nil {
		u.printSystemMessage(yellow + "Unable to select model: " + selectErr.Error() + reset)
		return
	}
	if !accepted {
		return
	}
	if u.manager != nil {
		if err := u.manager.UpdateModel(u.activeAgent, selected); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
			return
		}
		u.screenMu.Lock()
		if view := u.views[u.activeAgent]; view != nil {
			view.model = selected
		}
		u.screenMu.Unlock()
	}
	runner.SetModel(selected)
	if tracker, ok := u.runner.(contextRunner); ok {
		tracker.RefreshContext(ctx)
	}
	u.screenMu.Lock()
	u.model = selected
	u.screenMu.Unlock()
	u.drawStatusBar()
	u.printSystemMessage(fmt.Sprintf("%sModel: %s%s", green, selected, reset))
}

func (u *UI) expandDiff(fields []string) {
	if len(fields) > 2 {
		u.printSystemMessage(yellow + "Usage: /diff [number]" + reset)
		return
	}
	number := 0
	if len(fields) == 2 {
		parsed, err := strconv.Atoi(fields[1])
		if err != nil || parsed < 1 {
			u.printSystemMessage(yellow + "Usage: /diff [number]" + reset)
			return
		}
		number = parsed
	}
	requested, total, ok := u.responseWriter.WriteStoredDiff(number)
	if ok {
		return
	}
	if total == 0 {
		u.printSystemMessage(dim + "No diffs are available from the latest run." + reset)
		return
	}
	u.printSystemMessage(fmt.Sprintf("%sDiff %d not found; available diffs: 1-%d.%s", yellow, requested, total, reset))
}

func (u *UI) updateMaxSteps(fields []string) {
	if len(fields) == 1 {
		if reader, ok := u.runner.(maxStepsReader); ok {
			u.printSystemMessage(fmt.Sprintf("%sMax steps: %d%s", green, reader.MaxSteps(), reset))
			return
		}
		u.printSystemMessage(yellow + "Maximum model steps are unavailable." + reset)
		return
	}
	if len(fields) != 2 {
		u.printSystemMessage(yellow + "Usage: /maxsteps <positive integer>" + reset)
		return
	}
	maxSteps, err := strconv.Atoi(fields[1])
	if err != nil || maxSteps <= 0 {
		u.printSystemMessage(yellow + "Usage: /maxsteps <positive integer>" + reset)
		return
	}
	runner, ok := u.runner.(maxStepsRunner)
	if !ok {
		u.printSystemMessage(yellow + "Maximum model steps are unavailable." + reset)
		return
	}
	runner.SetMaxSteps(maxSteps)
	u.printSystemMessage(fmt.Sprintf("%sMax steps: %d%s", green, maxSteps, reset))
}

func (u *UI) handlePlanCommand(ctx context.Context, fields []string) {
	if len(fields) > 2 {
		u.printSystemMessage(yellow + "Usage: /plan [off|show|act]" + reset)
		return
	}
	controller, ok := u.runner.(planController)
	if !ok {
		u.printSystemMessage(yellow + "Plan mode is unavailable." + reset)
		return
	}
	if len(fields) == 2 && fields[1] == "show" {
		plan, exists := controller.LatestPlanText()
		if !exists {
			u.printSystemMessage(yellow + "No complete plan is available. Ask qcode to submit a plan first." + reset)
			return
		}
		if err := u.showPlanView(ctx, plan); err != nil && !errors.Is(err, context.Canceled) {
			u.printSystemMessage(yellow + "Unable to show plan: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
		}
		return
	}
	if !u.activeAgentConfigurable() {
		return
	}
	setPrompt := func(plan bool) {
		u.setInputModePrompt(plan)
		u.drawStatusBar()
	}
	switch {
	case len(fields) == 1:
		if !controller.PlanMode() {
			controller.ClearLatestPlan()
		}
		controller.SetPlanMode(true)
		setPrompt(true)
		u.printSystemMessage(green + "Plan mode enabled; workspace mutation tools are unavailable." + reset)
	case fields[1] == "off":
		controller.SetPlanMode(false)
		setPrompt(false)
		u.printSystemMessage(green + "Plan mode disabled." + reset)
	case fields[1] == "act":
		plan, exists := controller.LatestPlanText()
		if !exists {
			u.printSystemMessage(yellow + "No complete plan is available. Ask qcode to submit a plan first." + reset)
			return
		}
		if u.manager == nil {
			u.printSystemMessage(yellow + "Plan execution requires the interactive agent manager." + reset)
			return
		}
		controller.SetPlanMode(false)
		setPrompt(false)
		implementation := "Implement the approved plan below. Do not re-plan. If a required assumption is invalid, stop and explain before changing files.\n\n" + plan
		if _, err := u.manager.Submit(u.activeAgent, implementation); err != nil {
			controller.SetPlanMode(true)
			setPrompt(true)
			u.printSystemMessage(yellow + "Unable to start plan implementation: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
		u.printSystemMessage(green + "Plan approved; implementation started." + reset)
	default:
		u.printSystemMessage(yellow + "Usage: /plan [off|show|act]" + reset)
	}
}

func (u *UI) setInputModePrompt(plan bool) {
	label := inputPrompt
	if plan {
		label = planInputPrompt
	}
	if u.terminal != nil {
		u.terminal.SetPrompt(label)
	}
	u.screenMu.Lock()
	u.inputLabel = label
	u.paintFixedLocked(0)
	u.screenMu.Unlock()
}

// printSystemMessage separates status and command feedback from surrounding
// conversation so it remains easy to scan in both the terminal and history.
func (u *UI) printSystemMessage(message string) {
	fmt.Fprintf(u.display, "\n%s\n\n", message)
}

func formatRunDuration(duration time.Duration) string {
	if duration < time.Millisecond {
		return "<1ms"
	}
	if duration >= time.Second {
		return duration.Round(time.Second).String()
	}
	return duration.Round(time.Millisecond).String()
}

func (u *UI) completeSlashCommand(line string, pos int, key rune) (string, int, bool) {
	if u.fixedInput {
		if key == '\t' {
			matches := matchingSlashCommands(line)
			if len(matches) > 0 {
				return matches[0].name, len(matches[0].name), true
			}
		}
		return line, pos, false
	}
	if key == '\t' {
		matches := matchingSlashCommands(line)
		if len(matches) == 0 {
			u.commandMenu.update(nil)
			return line, pos, false
		}
		completed := matches[0].name
		u.commandMenu.update(matchingSlashCommands(completed))
		u.rememberDraft(completed)
		return completed, len(completed), true
	}
	if key < 32 || pos < 0 || pos > len(line) {
		return line, pos, false
	}
	inserted := string(key)
	newLine := line[:pos] + inserted + line[pos:]
	u.commandMenu.update(matchingSlashCommands(newLine))
	u.rememberDraft(newLine)
	return newLine, pos + len(inserted), true
}

func (u *UI) rememberDraft(line string) {
	if u.manager == nil {
		return
	}
	u.screenMu.Lock()
	u.drafts[u.activeAgent] = line
	u.screenMu.Unlock()
}

func (u *UI) printCommandHelp() {
	for _, command := range slashCommands {
		line := fmt.Sprintf("%s%-8s%s %s%s%s", cyan, command.name, reset, dim, command.description, reset)
		fmt.Fprintln(u.display, wrapANSI(line, u.width, "         "))
	}
}

func (u *UI) printHeader() {
	fmt.Fprint(u.display, "\r\n")
	colorEnabled := ColorEnabled(u.out)
	for _, line := range headerLogo(u.width) {
		if colorEnabled {
			fmt.Fprintf(u.display, "%s%s%s\r\n", bold, gradientLine(line), reset)
		} else {
			fmt.Fprintf(u.display, "%s\r\n", line)
		}
	}
	fmt.Fprintf(u.display, "\r\n")
	u.printToolSummary()
	if !u.statusActive {
		fmt.Fprintf(u.display, "%s\r\n", statusBar(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, ColorEnabled(u.out), u.contextLabel(), u.usageLabel(), u.stepsLabel(), u.modeLabel()))
	}
}

func displayRoot(root string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, relErr := filepath.Rel(home, root); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join("~", rel)
		}
	}
	return root
}

func (u *UI) setupStatusBar() {
	if u.height < 4 {
		return
	}
	u.statusActive = true
	// Start the full-screen layout from a clean viewport. CSI 2J clears the
	// visible screen without erasing the terminal's scrollback history.
	fmt.Fprint(u.out, "\x1b[2J\x1b[H")
	u.resetStatusLayout()
}

func (u *UI) resetStatusLayout() {
	if !u.statusActive {
		return
	}
	// Reserve the first row for tabs, the last row for status, and leave the
	// row above status blank. Conversation output scrolls between them.
	fmt.Fprintf(u.out, "\x1b[2;%dr\x1b[2;1H", u.height-2)
	u.drawTabBar()
	u.drawStatusBar()
}

func (u *UI) drawStatusBar() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.drawStatusBarLocked()
}

func (u *UI) drawStatusBarLocked() {
	u.renderStatusBarLocked(true)
}

func (u *UI) refreshStatusBar() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.renderStatusBarLocked(false)
}

func (u *UI) renderStatusBarLocked(force bool) {
	if !u.statusActive {
		return
	}
	bar := statusBar(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, ColorEnabled(u.out), u.contextLabel(), u.usageLabel(), u.stepsLabel(), u.modeLabel())
	if !force && bar == u.statusBarText {
		return
	}
	u.statusBarText = bar
	fmt.Fprintf(u.out, "\x1b[s\x1b[%d;1H\x1b[2K%s\x1b[u", u.height, bar)
}

func (u *UI) teardownStatusBar() {
	if !u.statusActive {
		return
	}
	u.statusActive = false
	// Restore full-screen scrolling and clear the reserved footer rows before
	// returning control to the invoking shell.
	fmt.Fprintf(u.out, "\x1b[r\x1b[%d;1H\x1b[J", u.height-1)
}

func statusBar(provider, model, root string, width int, unicodeEnabled, color bool, contextLabel ...string) string {
	provider = sanitizeDiffLine(provider, "<ESC>")
	model = sanitizeDiffLine(model, "<ESC>")
	root = sanitizeDiffLine(root, "<ESC>")

	if !color {
		parts := []string{
			provider,
			"[MODEL " + model + "]",
		}
		if len(contextLabel) > 0 {
			parts = append(parts, "[CTX "+contextLabel[0]+"]")
		}
		parts = append(parts, "[WS "+root+"]")
		if len(contextLabel) > 1 {
			parts = append(parts, "[TOK "+contextLabel[1]+"]")
		}
		if len(contextLabel) > 2 && contextLabel[2] != "" {
			parts = append(parts, "[STEP "+contextLabel[2]+"]")
		}
		if len(contextLabel) > 3 && contextLabel[3] != "" {
			parts = append(parts, "[MODE "+contextLabel[3]+"]")
		}
		bar := strings.Join(parts, " ")
		if width > 0 && visibleWidth(bar) > width {
			if dynamic := compactStatusBar(contextLabel, false, unicodeEnabled); dynamic != "" && visibleWidth(dynamic) <= width {
				return dynamic
			}
		}
		return truncateDiffLine(bar, width, unicodeEnabled)
	}

	segments := []string{
		statusValue(provider, cyan),
		statusSegment("MODEL", model, magenta),
	}
	if len(contextLabel) > 0 {
		segments = append(segments, statusSegment("CTX", contextLabel[0], green))
	}
	segments = append(segments, statusSegment("WS", root, blue))
	if len(contextLabel) > 1 {
		segments = append(segments, statusSegment("TOK", contextLabel[1], cyan))
	}
	if len(contextLabel) > 2 && contextLabel[2] != "" {
		segments = append(segments, statusSegment("STEP", contextLabel[2], yellow))
	}
	if len(contextLabel) > 3 && contextLabel[3] != "" {
		segments = append(segments, statusSegment("MODE", contextLabel[3], yellow))
	}
	separator := dim + "  │  " + reset
	if !unicodeEnabled {
		separator = dim + "  |  " + reset
	}
	bar := strings.Join(segments, separator)
	if width > 0 && visibleWidth(bar) > width {
		if dynamic := compactStatusBar(contextLabel, true, unicodeEnabled); dynamic != "" && visibleWidth(dynamic) <= width {
			return dynamic + reset
		}
		bar = truncateDiffLine(bar, width, unicodeEnabled)
	}
	return bar + reset
}

func compactStatusBar(labels []string, color, unicodeEnabled bool) string {
	if len(labels) == 0 {
		return ""
	}
	separator := "  │  "
	if !unicodeEnabled {
		separator = "  |  "
	}
	segments := []string{statusSegment("CTX", labels[0], green)}
	if len(labels) > 2 && labels[2] != "" {
		// Keep step progress visible on narrow terminals; token totals are less
		// actionable while a request is running.
		segments = append(segments, statusSegment("STEP", labels[2], yellow))
	} else if len(labels) > 1 {
		segments = append(segments, statusSegment("TOK", labels[1], cyan))
	}
	if !color {
		parts := []string{"[CTX " + labels[0] + "]"}
		if len(labels) > 2 && labels[2] != "" {
			parts = append(parts, "[STEP "+labels[2]+"]")
		} else if len(labels) > 1 {
			parts = append(parts, "[TOK "+labels[1]+"]")
		}
		return strings.Join(parts, " ")
	}
	return strings.Join(segments, separator)
}

func statusSegment(label, value, color string) string {
	return color + bold + label + reset + " " + color + value + reset
}

func statusValue(value, color string) string {
	return color + bold + value + reset
}

func headerLogo(width int) []string {
	for _, line := range qcodeBanner {
		if width > 0 && visibleWidth(line) > width {
			return []string{"qcode"}
		}
	}
	return qcodeBanner
}

// gradientLine applies a mono gradient using shades from the banner color palette
func gradientLine(line string) string {
	var result strings.Builder
	visibleChars := 0
	// Count visible characters first
	for _, ch := range line {
		if ch != ' ' {
			visibleChars++
		}
	}

	if visibleChars == 0 {
		return line
	}

	// Apply mono gradient using palette shades
	coloredIndex := 0
	for _, ch := range line {
		if ch == ' ' {
			result.WriteRune(ch)
		} else {
			// Map progress across the lighter portion of the palette.
			progress := float64(coloredIndex) / float64(visibleChars-1)
			start := min(bannerGradientStart, len(bannerColor)-1)
			paletteIndex := start + int(progress*float64(len(bannerColor)-start-1))
			colorCode := bannerColor[paletteIndex]

			result.WriteString(fmt.Sprintf("\x1b[38;5;%dm%c", colorCode, ch))
			coloredIndex++
		}
	}
	return result.String()
}

func (u *UI) resetPage() {
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	v := u.activeViewportLocked()
	if v.browsing {
		v.browsing = false
		u.repaintActiveLocked(0)
	}
}

func (u *UI) showPage(direction int) {
	u.commandMenu.reset()
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.repaintActiveLocked(direction)
}

func terminalSize(out *os.File) (int, int) {
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return 80, 24
	}
	return width, height
}

func ColorEnabled(out *os.File) bool {
	return os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(out.Fd()))
}

func OutputWidth(out *os.File) int {
	if !term.IsTerminal(int(out.Fd())) {
		return 0
	}
	width, _, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 {
		return 80
	}
	return width
}

func (u *UI) contextLabel() string {
	if runner, ok := u.runner.(contextRunner); ok {
		remaining, known, estimated := runner.ContextRemaining()
		if known {
			prefix := ""
			if estimated {
				prefix = "~"
			}
			return fmt.Sprintf("%s%d%% left", prefix, remaining)
		}
	}
	return "unknown"
}

func (u *UI) modeLabel() string {
	if controller, ok := u.runner.(planController); ok && controller.PlanMode() {
		return "PLAN"
	}
	return ""
}

func (u *UI) printToolSummary() {
	runner, ok := u.runner.(toolRunner)
	if !ok {
		return
	}
	var enabled, disabled []string
	for _, name := range runner.ToolNames() {
		safe := sanitizeDiffLine(name, "<ESC>")
		if runner.ToolEnabled(name) {
			enabled = append(enabled, safe)
		} else {
			disabled = append(disabled, safe)
		}
	}
	sort.Strings(enabled)
	sort.Strings(disabled)
	join := func(names []string) string {
		if len(names) == 0 {
			return "none"
		}
		return strings.Join(names, ", ")
	}
	for _, line := range []string{"Tools enabled: " + join(enabled), "Tools disabled: " + join(disabled)} {
		fmt.Fprintln(u.display, wrapANSI(line, u.width, "  "))
	}
	fmt.Fprintln(u.display)
}
