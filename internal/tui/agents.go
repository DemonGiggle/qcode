package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"qcode/internal/session"
)

const (
	fullTabSwitchHint      = "Switch tabs: Ctrl+PgUp/PgDn · Alt+,/."
	asciiFullTabSwitchHint = "Switch tabs: Ctrl+PgUp/PgDn | Alt+,/."
	compactTabSwitchHint   = "Switch: Ctrl+Pg/Alt+,/."
)

func (u *UI) handleAgentCommand(ctx context.Context, fields []string) {
	if u.manager == nil {
		u.printSystemMessage(yellow + "Agent tabs are unavailable." + reset)
		return
	}
	if len(fields) == 1 || len(fields) == 2 && fields[1] == "new" {
		u.createAgent(ctx)
		return
	}
	switch fields[1] {
	case "list":
		if len(fields) != 2 {
			u.agentUsage()
			return
		}
		var lines []string
		for _, item := range u.manager.List() {
			marker := " "
			if item.ID == u.activeAgent {
				marker = "*"
			}
			lines = append(lines, fmt.Sprintf("%s %-9s %-18s %-20s %s", marker,
				sanitizeDiffLine(item.ID, "<ESC>"), sanitizeDiffLine(item.Name, "<ESC>"),
				sanitizeDiffLine(item.Model, "<ESC>"), item.Status))
		}
		u.printSystemMessage(strings.Join(lines, "\n"))
	case "switch":
		if len(fields) != 3 {
			u.agentUsage()
			return
		}
		if err := u.switchAgent(fields[2]); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
		}
	case "rename":
		if len(fields) < 4 {
			u.agentUsage()
			return
		}
		if err := u.manager.Rename(fields[2], strings.Join(fields[3:], " ")); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
			return
		}
		u.drawTabBar()
	case "cancel":
		if len(fields) != 3 {
			u.agentUsage()
			return
		}
		if err := u.manager.Cancel(fields[2]); err != nil {
			u.printSystemMessage(yellow + err.Error() + reset)
		}
	case "close":
		if len(fields) != 3 {
			u.agentUsage()
			return
		}
		u.closeAgent(fields[2])
	default:
		u.agentUsage()
	}
}

func (u *UI) createAgent(ctx context.Context) {
	runner, ok := u.runner.(modelRunner)
	if !ok {
		u.printSystemMessage(yellow + "Model selection is unavailable." + reset)
		return
	}
	selected, accepted, err := u.selectAgentModel(ctx, runner)
	if err != nil {
		u.printSystemMessage(yellow + "Unable to select model: " + err.Error() + reset)
		return
	}
	if !accepted {
		u.printSystemMessage(dim + "Agent creation cancelled." + reset)
		return
	}
	summary, err := u.manager.Create(selected)
	if err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return
	}
	if err := u.switchAgent(summary.ID); err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return
	}
	u.printSystemMessage(green + "Created " + summary.ID + "." + reset)
}

func (u *UI) selectAgentModel(ctx context.Context, runner modelRunner) (string, bool, error) {
	fetchCtx, cancel := context.WithCancel(ctx)
	u.input.setCancel(cancel)
	models, err := runner.ListModels(fetchCtx)
	u.input.setCancel(nil)
	cancel()
	if errors.Is(err, context.Canceled) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(models) == 0 {
		return "", false, fmt.Errorf("provider returned no models")
	}
	visible := min(12, max(3, u.height-6))
	u.printSystemMessage(dim + "Type to search; use Up/Down to move, Enter to select, or Ctrl+C to cancel." + reset)
	u.input.setRaw(true)
	selected, accepted, selectErr := selectModel(u.input, u.terminal, models, u.model, visible, u.width, ColorEnabled(u.out))
	u.input.setRaw(false)
	return selected, accepted, selectErr
}

func (u *UI) closeAgent(id string) {
	if id == "main" {
		u.printSystemMessage(yellow + "Main agent cannot be closed." + reset)
		return
	}
	continued, err := u.readChoice("[c] Close " + sanitizeDiffLine(id, "<ESC>") + "  [l] Keep > ")
	if err != nil || !continued {
		return
	}
	if err := u.manager.Close(id); err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return
	}
	if u.activeAgent == id {
		_ = u.switchAgent("main")
	}
	u.RemoveAgentView(id)
	u.drawTabBar()
}

func (u *UI) agentUsage() {
	u.printSystemMessage(dim + "Usage: /agent [new|list|switch <id>|rename <id> <name>|cancel <id>|close <id>]" + reset)
}

func (u *UI) activeAgentConfigurable() bool {
	if u.manager == nil {
		return true
	}
	summary, err := u.manager.Summary(u.activeAgent)
	if err != nil {
		u.printSystemMessage(yellow + err.Error() + reset)
		return false
	}
	if summary.Status == session.StatusRunning || summary.Status == session.StatusWaitingForApproval {
		u.printSystemMessage(yellow + fmt.Sprintf("Agent %s is %s.", summary.ID, summary.Status) + reset)
		return false
	}
	return true
}

type agentView struct {
	id       string
	provider string
	model    string
	display  *agentDisplay
	response *MarkdownWriter
	page     int
	unseen   bool
	onSkills func([]string)
}

type approvalResult struct {
	selected string
	approved bool
	err      error
}

type approvalRequest struct {
	ctx       context.Context
	requested string
	proposed  string
	result    chan approvalResult
}

type agentDisplay struct {
	ui      *UI
	id      string
	history *historyWriter
}

func (d *agentDisplay) Write(data []byte) (int, error) {
	d.ui.screenMu.Lock()
	defer d.ui.screenMu.Unlock()
	_, _ = d.history.Write(data)
	if d.ui.activeAgent == d.id && d.ui.terminal != nil {
		return d.ui.terminal.Write(data)
	}
	return len(data), nil
}

func (d *agentDisplay) AddLine(line string) { d.history.AddLine(line) }
func (d *agentDisplay) Clear()              { d.history.Clear() }
func (d *agentDisplay) Lines() []string     { return d.history.Lines() }

func (u *UI) watchAgentEvents(events <-chan session.Event) {
	defer close(u.agentEventsDone)
	for event := range events {
		u.handleAgentEvent(event)
	}
}

func (u *UI) handleAgentEvent(event session.Event) {
	u.signalUIEvent()
	u.screenMu.Lock()
	view := u.views[event.Agent.ID]
	active := u.activeAgent == event.Agent.ID
	if view != nil && !active && event.Agent.Status != session.StatusRunning {
		view.unseen = true
	}
	u.screenMu.Unlock()
	if view == nil {
		return
	}
	if active {
		u.updateActiveCancellation()
		// Agent.Run publishes its latest context usage before the manager emits
		// the terminal event. Refresh the footer here just as the synchronous
		// single-agent path did after each run.
		u.drawStatusBar()
	}
	u.drawTabBar()
	if event.Agent.Status != session.StatusRunning && event.Agent.Status != session.StatusWaitingForApproval {
		if view != nil {
			message := string(event.Agent.Status)
			if event.Agent.Status == session.StatusCompleted {
				message = "Completed in " + formatRunDuration(event.Duration)
			} else if event.Agent.Status == session.StatusCancelled {
				message = "Cancelled"
			}
			if event.Agent.Error != "" {
				message = "error: " + sanitizeDiffLine(event.Agent.Error, "<ESC>")
			}
			fmt.Fprintf(view.display, "\n%s%s%s\n\n", dim, message, reset)
		}
	}
	if event.Agent.ID != "main" && event.Agent.Status != session.StatusRunning {
		u.notifyMain(event.Agent)
	}
}

func (u *UI) notifyMain(summary session.Summary) {
	u.screenMu.Lock()
	view := u.views["main"]
	u.screenMu.Unlock()
	if view == nil {
		return
	}
	message := fmt.Sprintf("Agent %s %s", sanitizeDiffLine(summary.Name, "<ESC>"), summary.Status)
	if summary.Error != "" {
		message = fmt.Sprintf("Agent %s error: %s", sanitizeDiffLine(summary.Name, "<ESC>"), sanitizeDiffLine(summary.Error, "<ESC>"))
	}
	fmt.Fprintf(view.display, "\n%s%s%s\n\n", dim, message, reset)
}

func (u *UI) updateActiveCancellation() {
	if u.manager == nil {
		return
	}
	u.screenMu.Lock()
	id := u.activeAgent
	u.screenMu.Unlock()
	summary, err := u.manager.Summary(id)
	if err == nil && (summary.Status == session.StatusRunning || summary.Status == session.StatusWaitingForApproval) {
		u.input.setCancel(func() { _ = u.manager.Cancel(id) })
		return
	}
	u.input.setCancel(nil)
}

func (u *UI) switchAgent(id string) error {
	if u.manager == nil {
		return fmt.Errorf("agent tabs are unavailable")
	}
	summary, err := u.manager.Summary(id)
	if err != nil {
		return err
	}
	value, ok := u.manager.Runner(id)
	if !ok {
		return fmt.Errorf("unknown agent %q", id)
	}
	runner, ok := value.(Runner)
	if !ok {
		return fmt.Errorf("agent %q cannot run in the terminal", id)
	}
	u.screenMu.Lock()
	view := u.views[id]
	if view == nil {
		u.screenMu.Unlock()
		return fmt.Errorf("agent %q has no terminal view", id)
	}
	u.activeAgent = id
	view.unseen = false
	u.display = view.display
	u.responseWriter = view.response
	u.provider = view.provider
	u.model = summary.Model
	view.model = summary.Model
	u.runner = runner
	u.onSkills = view.onSkills
	page := view.page
	u.screenMu.Unlock()
	u.pageMu.Lock()
	u.pageOffset = page
	u.pageActive = page > 0
	u.pageMu.Unlock()
	u.SetRunner(runner)
	u.updateActiveCancellation()
	u.repaintActive()
	return nil
}

// AgentDirectoryApprover suspends background approval requests until their tab
// is active, keeping all terminal reads on the UI goroutine.
func (u *UI) AgentDirectoryApprover(id string) func(context.Context, string, string) (string, bool, error) {
	return func(ctx context.Context, requested, proposed string) (string, bool, error) {
		request := &approvalRequest{ctx: ctx, requested: requested, proposed: proposed, result: make(chan approvalResult, 1)}
		u.screenMu.Lock()
		active := u.activeAgent == id
		u.screenMu.Unlock()
		u.approvalMu.Lock()
		u.approvals[id] = append(u.approvals[id], request)
		u.approvalMu.Unlock()
		if u.manager != nil {
			u.manager.SetWaitingForApproval(id, true)
		}
		if active {
			u.input.interruptLine()
		}
		select {
		case result := <-request.result:
			return result.selected, result.approved, result.err
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}
}

func (u *UI) handlePendingApproval(ctx context.Context) {
	if u.manager == nil {
		return
	}
	u.approvalMu.Lock()
	queue := u.approvals[u.activeAgent]
	if len(queue) == 0 {
		u.approvalMu.Unlock()
		return
	}
	request := queue[0]
	u.approvals[u.activeAgent] = queue[1:]
	id := u.activeAgent
	u.approvalMu.Unlock()
	u.input.setCancel(nil)
	selected, approved, err := u.ApproveDirectory(request.ctx, request.requested, request.proposed)
	select {
	case request.result <- approvalResult{selected: selected, approved: approved, err: err}:
	case <-ctx.Done():
	}
	u.manager.SetWaitingForApproval(id, false)
	u.updateActiveCancellation()
}

func (u *UI) switchRelative(direction int) {
	if u.manager == nil {
		return
	}
	list := u.manager.List()
	if len(list) < 2 {
		return
	}
	index := 0
	for i, item := range list {
		if item.ID == u.activeAgent {
			index = i
			break
		}
	}
	index = (index + direction + len(list)) % len(list)
	_ = u.switchAgent(list[index].ID)
}

func (u *UI) requestTabSwitch(direction int) {
	u.tabMu.Lock()
	u.pendingTab = direction
	u.tabMu.Unlock()
	u.signalUIEvent()
	if !u.activeAgentRunning() {
		u.input.interruptLine()
	}
}

func (u *UI) handlePendingTabSwitch() {
	u.tabMu.Lock()
	direction := u.pendingTab
	u.pendingTab = 0
	u.tabMu.Unlock()
	if direction == 0 {
		return
	}
	u.switchRelative(direction)
	u.screenMu.Lock()
	draft := u.drafts[u.activeAgent]
	u.screenMu.Unlock()
	if draft != "" {
		u.input.inject([]byte(draft))
	}
}

func (u *UI) repaintActive() {
	u.screenMu.Lock()
	statusActive := u.statusActive
	display := u.display
	width, height := u.width, u.height
	u.screenMu.Unlock()
	if !statusActive {
		return
	}
	lines := visualHistoryLines(display, width)
	pageHeight := max(1, height-4)
	u.pageMu.Lock()
	offset := u.pageOffset
	u.pageMu.Unlock()
	page, _ := historyPage(lines, pageHeight, offset, 0)
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.taskIndicatorText = ""
	var output strings.Builder
	output.WriteString("\x1b[2;1H\x1b[J")
	if len(page) > 0 {
		output.WriteString(strings.Join(page, "\n"))
		output.WriteByte('\n')
	}
	_, _ = u.terminal.Write([]byte(output.String()))
	u.drawTabBarLocked()
	u.drawStatusBarLocked()
}

func (u *UI) drawTabBar() {
	if u.manager == nil {
		return
	}
	u.screenMu.Lock()
	defer u.screenMu.Unlock()
	u.drawTabBarLocked()
}

func (u *UI) drawTabBarLocked() {
	if !u.statusActive || u.manager == nil {
		return
	}
	bar := tabBar(u.manager.List(), u.activeAgent, u.views, u.width, u.unicode, ColorEnabled(u.out))
	fmt.Fprintf(u.out, "\x1b[s\x1b[1;1H\x1b[2K%s\x1b[u", bar)
}

func tabBar(summaries []session.Summary, active string, views map[string]*agentView, width int, unicodeEnabled, color bool) string {
	bar := renderTabs(summaries, active, views, unicodeEnabled, color, false)
	if width <= 0 || visibleWidth(bar) <= width {
		return withTabSwitchHint(bar, width, unicodeEnabled, color)
	}
	ordered := make([]session.Summary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.ID == "main" {
			ordered = append(ordered, summary)
		}
	}
	if active != "main" {
		for _, summary := range summaries {
			if summary.ID == active {
				ordered = append(ordered, summary)
			}
		}
	}
	for _, summary := range summaries {
		if summary.ID != "main" && summary.ID != active {
			ordered = append(ordered, summary)
		}
	}
	bar = renderTabs(ordered, active, views, unicodeEnabled, color, true)
	if visibleWidth(bar) <= width {
		return withTabSwitchHint(bar, width, unicodeEnabled, color)
	}
	if active != "main" {
		var essential []session.Summary
		for _, summary := range ordered {
			if summary.ID == "main" || summary.ID == active {
				copy := summary
				if copy.ID == "main" {
					copy.Name = "m"
				} else {
					copy.Name = truncateDiffLine(copy.Name, max(1, width-10), unicodeEnabled)
				}
				essential = append(essential, copy)
			}
		}
		bar = renderTabs(essential, active, views, unicodeEnabled, color, false)
	}
	return withTabSwitchHint(truncateDiffLine(bar, width, unicodeEnabled), width, unicodeEnabled, color)
}

func withTabSwitchHint(bar string, width int, unicodeEnabled, color bool) string {
	if width <= 0 {
		return bar
	}
	hints := []string{compactTabSwitchHint}
	if unicodeEnabled {
		hints = append([]string{fullTabSwitchHint}, hints...)
	} else {
		hints = append([]string{asciiFullTabSwitchHint}, hints...)
	}
	for _, plainHint := range hints {
		gap := width - visibleWidth(bar) - visibleWidth(plainHint)
		if gap < 2 {
			continue
		}
		hint := plainHint
		if color {
			hint = dim + hint + reset
		}
		return bar + strings.Repeat(" ", gap) + hint
	}
	return bar
}

func renderTabs(summaries []session.Summary, active string, views map[string]*agentView, unicodeEnabled, color, compact bool) string {
	parts := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		indicator := statusIndicator(summary.Status, unicodeEnabled)
		unseen := views[summary.ID] != nil && views[summary.ID].unseen
		name := sanitizeDiffLine(summary.Name, "<ESC>")
		if compact && summary.ID != "main" && summary.ID != active {
			name = ""
		} else if visibleWidth(name) > 18 {
			name = truncateDiffLine(name, 18, unicodeEnabled)
		}
		label := "[" + indicator + "]"
		if name != "" {
			label = fmt.Sprintf("[%s %s]", name, indicator)
		}
		if color {
			if summary.ID == active {
				label = cyan + bold + label + reset
			} else if unseen {
				label = yellow + bold + label + reset
			} else {
				label = dim + label + reset
			}
		} else if summary.ID == active || unseen {
			marker := "*"
			if unseen {
				marker = "!"
			}
			label = marker + label
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " ")
}

func statusIndicator(status session.Status, unicodeEnabled bool) string {
	if !unicodeEnabled {
		switch status {
		case session.StatusRunning:
			return ">"
		case session.StatusWaitingForApproval:
			return "?"
		case session.StatusCompleted:
			return "+"
		case session.StatusFailed:
			return "!"
		case session.StatusCancelled:
			return "x"
		default:
			return "-"
		}
	}
	switch status {
	case session.StatusRunning:
		return "●"
	case session.StatusWaitingForApproval:
		return "?"
	case session.StatusCompleted:
		return "✓"
	case session.StatusFailed:
		return "!"
	case session.StatusCancelled:
		return "×"
	default:
		return "○"
	}
}

func (u *UI) runActiveTask(ctx context.Context, line string) error {
	if u.manager == nil {
		return u.runner.Run(ctx, line)
	}
	if err := u.manager.Start(u.activeAgent, line); err != nil {
		return err
	}
	u.updateActiveCancellation()
	u.drawTaskIndicator()
	return nil
}

var _ io.Writer = (*agentDisplay)(nil)
