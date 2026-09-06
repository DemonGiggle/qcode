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

type sessionRunner interface {
	ResetSession()
}

type skillRunner interface{ SetSkills([]prompt.SkillSummary) }

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
}

type agentController interface {
	Events() <-chan session.Event
	List() []session.Summary
	Summary(string) (session.Summary, error)
	Runner(string) (any, bool)
	Create(string) (session.Summary, error)
	Start(string, string) error
	Rename(string, string) error
	Cancel(string) error
	Close(string) error
	Reset(string) error
	UpdateModel(string, string) error
	SetWaitingForApproval(string, bool)
	Shutdown()
}

type UI struct {
	terminal          *lineedit.Terminal
	display           historyDisplay
	responseWriter    *MarkdownWriter
	commandMenu       slashCommandMenu
	input             *interruptReader
	in                *os.File
	out               *os.File
	runner            Runner
	provider          string
	model             string
	root              string
	verbose           bool
	width             int
	height            int
	unicode           bool
	pageMu            sync.Mutex
	pageOffset        int
	pageActive        bool
	statusActive      bool
	startupNotice     string
	startupChoice     bool
	skills            []prompt.SkillSummary
	onSkills          func([]string)
	manager           agentController
	activeAgent       string
	views             map[string]*agentView
	screenMu          sync.Mutex
	drafts            map[string]string
	approvalMu        sync.Mutex
	approvals         map[string][]*approvalRequest
	tabMu             sync.Mutex
	pendingTab        int
	agentEventsDone   chan struct{}
	uiEvents          chan struct{}
	taskIndicatorText string
}

// SetSkillCatalog configures the optional /skill selector.
func (u *UI) SetSkillCatalog(skills []prompt.SkillSummary, onChange func([]string)) {
	u.skills = append([]prompt.SkillSummary(nil), skills...)
	u.onSkills = onChange
}

func New(in, out *os.File, runner Runner, provider, model, root string) *UI {
	input := newInterruptReader(in)
	rw := readWriter{Reader: input, Writer: out}
	t := lineedit.NewTerminal(rw, inputPrompt)
	width, height := terminalSize(out)
	unicodeEnabled := UnicodeEnabled()
	t.SetSize(width, height)
	display := newHistoryWriter(t)
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

// AddAgentView creates an isolated output/history buffer for an agent.
func (u *UI) AddAgentView(id, provider, model string) (io.Writer, io.Writer) {
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
	u.manager = manager
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
	u.runner = runner
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
	}()
	if u.manager != nil {
		defer u.shutdownAgentManager()
	}
	u.input.start()
	u.setupStatusBar()
	stopResize := u.watchResize()
	defer stopResize()
	stopTaskIndicator := u.watchTaskIndicator()
	defer stopTaskIndicator()

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
	for {
		u.handlePendingTabSwitch()
		u.handlePendingApproval(ctx)
		if u.activeAgentRunning() {
			if err := u.waitForAgentEvent(ctx); err != nil {
				return err
			}
			continue
		}
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
		u.display.AddLine("> " + line)
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
			u.verbose = !u.verbose
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
	u.drawStatusBar()
	u.responseWriter.ResetDiffs()
	u.printSystemMessage(green + "New session started; previous context cleared." + reset)
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
	u.printSystemMessage(dim + "Type to search; use Up/Down to move, Enter to select, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	selected, accepted, selectErr := selectModel(u.input, u.terminal, models, u.model, visible, u.width, ColorEnabled(u.out))
	u.input.setRaw(false)
	if selectErr != nil {
		u.printSystemMessage(yellow + "Unable to select model: " + selectErr.Error() + reset)
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Model selection cancelled." + reset)
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
	u.model = selected
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
		fmt.Fprintf(u.display, "%s\r\n", statusBar(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, ColorEnabled(u.out), u.contextLabel()))
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
	if !u.statusActive {
		return
	}
	bar := statusBar(u.provider, u.model, displayRoot(u.root), u.width, u.unicode, ColorEnabled(u.out), u.contextLabel())
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
		bar := fmt.Sprintf("[PROVIDER %s] [MODEL %s] [WORKSPACE %s]", provider, model, root)
		if len(contextLabel) > 0 {
			bar = "[CONTEXT " + contextLabel[0] + "] " + bar
		}
		return truncateDiffLine(bar, width, unicodeEnabled)
	}

	segments := []string{
		statusSegment("PROVIDER", provider, cyan),
		statusSegment("MODEL", model, magenta),
		statusSegment("WORKSPACE", root, blue),
	}
	if len(contextLabel) > 0 {
		segments = append([]string{statusSegment("CONTEXT", contextLabel[0], green)}, segments...)
	}
	separator := dim + "  │  " + reset
	if !unicodeEnabled {
		separator = dim + "  |  " + reset
	}
	bar := strings.Join(segments, separator)
	if width > 0 && visibleWidth(bar) > width {
		bar = truncateDiffLine(bar, width, unicodeEnabled)
	}
	return bar + reset
}

func statusSegment(label, value, color string) string {
	return color + bold + label + reset + " " + color + value + reset
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
	u.pageMu.Lock()
	active := u.pageActive
	u.pageOffset = 0
	u.pageActive = false
	u.pageMu.Unlock()
	if u.manager != nil {
		u.screenMu.Lock()
		if view := u.views[u.activeAgent]; view != nil {
			view.page = 0
		}
		u.screenMu.Unlock()
	}
	if !active {
		return
	}

	u.screenMu.Lock()
	display := u.display
	width, height := u.width, u.height
	u.screenMu.Unlock()
	lines := visualHistoryLines(display, width)
	pageHeight := height - 4
	page, _ := historyPage(lines, pageHeight, 0, 0)
	var output strings.Builder
	output.WriteString("\x1b[2;1H\x1b[J")
	output.WriteString(strings.Join(page, "\n"))
	if len(page) > 0 {
		output.WriteByte('\n')
	}
	_, _ = u.terminal.Write([]byte(output.String()))
	u.drawTabBar()
	u.drawStatusBar()
}

func (u *UI) showPage(direction int) {
	u.pageMu.Lock()
	u.screenMu.Lock()
	display := u.display
	width, height := u.width, u.height
	u.screenMu.Unlock()
	lines := visualHistoryLines(display, width)
	pageSize := height - 4
	page, offset := historyPage(lines, pageSize, u.pageOffset, direction)
	u.pageOffset = offset
	u.pageActive = true
	u.pageMu.Unlock()
	if u.manager != nil {
		u.screenMu.Lock()
		if view := u.views[u.activeAgent]; view != nil {
			view.page = offset
		}
		u.screenMu.Unlock()
	}

	u.commandMenu.reset()
	start := len(lines) - offset - len(page) + 1
	end := len(lines) - offset
	if len(page) == 0 {
		start, end = 0, 0
	}
	var output strings.Builder
	output.WriteString("\x1b[2;1H\x1b[J")
	output.WriteString(strings.Join(page, "\n"))
	if len(page) > 0 {
		output.WriteByte('\n')
	}
	separator := "·"
	if !u.unicode {
		separator = "-"
	}
	fmt.Fprintf(&output, "%s[%d-%d of %d %s PgUp/PgDn]%s\n", dim, start, end, len(lines), separator, reset)
	_, _ = u.terminal.Write([]byte(output.String()))
	u.drawTabBar()
	u.drawStatusBar()
}

func visualHistoryLines(display historyDisplay, width int) []string {
	logical := display.Lines()
	visual := make([]string, 0, len(logical))
	for _, line := range logical {
		visual = append(visual, strings.Split(wrapANSI(line, width, ""), "\n")...)
	}
	return visual
}

func (u *UI) visualHistoryLines() []string {
	u.screenMu.Lock()
	display, width := u.display, u.width
	u.screenMu.Unlock()
	return visualHistoryLines(display, width)
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
