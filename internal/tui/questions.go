package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"qcode/internal/question"
	"qcode/internal/redaction"
	"qcode/internal/session"
)

type questionRequest struct {
	agentID   string
	questions []question.Question
	answers   []string
	next      int
	draft     string
	ctx       context.Context
	result    chan questionResult
}

var errQuestionDeferred = errors.New("question deferred for tab switch")

type questionResult struct {
	answers []string
	err     error
}

// AgentQuestioner returns the UI bridge for agent questions. Questions from
// inactive agents wait until that tab is selected.
func (u *UI) AgentQuestioner(id string) question.Questioner {
	// Resume installs callbacks while the UI is detached, then redirects it
	// to the live UI. Resolve ownership when a question is actually asked.
	return func(ctx context.Context, questions []question.Question) ([]string, error) {
		if u.sessionHost != nil {
			return u.sessionHost.AgentQuestioner(id)(ctx, questions)
		}
		return u.agentQuestioner(id)(ctx, questions)
	}
}

func (u *UI) agentQuestioner(id string) question.Questioner {
	local := func(ctx context.Context, questions []question.Question) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		request := &questionRequest{
			agentID:   id,
			questions: cloneQuestions(questions),
			ctx:       ctx,
			result:    make(chan questionResult, 1),
		}
		u.screenMu.Lock()
		active := u.activeAgent == id
		manager := u.manager
		request.draft = u.drafts[id]
		u.screenMu.Unlock()
		u.questionMu.Lock()
		u.questions = append(u.questions, request)
		u.questionMu.Unlock()
		if manager != nil {
			manager.SetWaitingForApproval(id, true)
		}
		u.signalUIEvent()
		if active && u.input != nil {
			u.input.interruptLine()
		}

		select {
		case result := <-request.result:
			return result.answers, result.err
		case <-ctx.Done():
			if u.removeQuestionRequest(request) {
				u.restoreQuestionDraft(request)
			}
			if manager != nil {
				manager.SetWaitingForApproval(id, false)
			}
			return nil, ctx.Err()
		}
	}
	brokerOwner, ok := u.manager.(interactionController)
	if !ok {
		return local
	}
	return func(ctx context.Context, questions []question.Question) ([]string, error) {
		payload, err := json.Marshal(questions)
		if err != nil {
			return nil, err
		}
		handle, err := brokerOwner.BeginInteraction(session.Interaction{AgentID: id, Kind: session.InteractionQuestions, Payload: payload})
		if err != nil {
			return nil, err
		}
		u.signalPresentation()
		localCtx, cancelLocal := context.WithCancel(ctx)
		defer cancelLocal()
		brokerCtx, cancelBroker := context.WithCancel(ctx)
		defer cancelBroker()
		localResult := make(chan questionResult, 1)
		go func() {
			answers, err := local(localCtx, questions)
			localResult <- questionResult{answers: answers, err: err}
		}()
		remoteResult := make(chan struct {
			resolution session.Resolution
			err        error
		}, 1)
		go func() {
			resolution, err := handle.Wait(brokerCtx)
			remoteResult <- struct {
				resolution session.Resolution
				err        error
			}{resolution, err}
		}()
		select {
		case result := <-localResult:
			if result.err != nil {
				return nil, result.err
			}
			value, _ := json.Marshal(result.answers)
			_ = brokerOwner.ResolveInteraction(session.Resolution{InteractionID: handle.InteractionInfo().ID, Value: value, ResolvedBy: "local-tui"})
			u.signalPresentation()
			resolved := <-remoteResult
			if resolved.err != nil {
				return nil, resolved.err
			}
			var answers []string
			if err := json.Unmarshal(resolved.resolution.Value, &answers); err != nil {
				return nil, err
			}
			return answers, nil
		case resolved := <-remoteResult:
			cancelLocal()
			<-localResult
			if resolved.err != nil {
				return nil, resolved.err
			}
			var answers []string
			if err := json.Unmarshal(resolved.resolution.Value, &answers); err != nil {
				return nil, err
			}
			if len(answers) != len(questions) {
				return nil, fmt.Errorf("remote answer count does not match questions")
			}
			return answers, nil
		}
	}
}

func cloneQuestions(questions []question.Question) []question.Question {
	cloned := make([]question.Question, len(questions))
	for i, item := range questions {
		cloned[i] = question.Question{Text: item.Text, Options: append([]string(nil), item.Options...), OptionDescriptions: append([]string(nil), item.OptionDescriptions...), AllowCustom: item.AllowCustom}
	}
	return cloned
}

func (u *UI) removeQuestionRequest(target *questionRequest) bool {
	u.questionMu.Lock()
	defer u.questionMu.Unlock()
	for i, request := range u.questions {
		if request != target {
			continue
		}
		u.questions = append(u.questions[:i], u.questions[i+1:]...)
		return true
	}
	return false
}

func (u *UI) restoreQuestionDraft(request *questionRequest) {
	u.screenMu.Lock()
	if source, ok := u.manager.(interface{ ClosedIDs() []string }); ok {
		for _, id := range source.ClosedIDs() {
			if id == request.agentID {
				u.screenMu.Unlock()
				return
			}
		}
		if summary, err := u.manager.Summary(request.agentID); err == nil && summary.Status == session.StatusClosing {
			u.screenMu.Unlock()
			return
		}
	}
	if request.draft == "" {
		u.screenMu.Unlock()
		return
	}
	draft := request.draft
	u.drafts[request.agentID] = draft
	active := u.activeAgent == request.agentID
	u.screenMu.Unlock()
	if active && u.input != nil {
		u.input.injectDraft(draft)
	}
}

func (u *UI) hasPendingQuestion(id string) bool {
	u.questionMu.Lock()
	defer u.questionMu.Unlock()
	for _, request := range u.questions {
		if request.agentID == id {
			return true
		}
	}
	return false
}

// handlePendingQuestions is called only by the UI goroutine, so all terminal
// reads remain serialized with the line editor.
func (u *UI) handlePendingQuestions(ctx context.Context) bool {
	if u.deferredInteractions[u.activeAgent] {
		return false
	}
	u.screenMu.Lock()
	activeID := u.activeAgent
	manager := u.manager
	u.screenMu.Unlock()
	u.questionMu.Lock()
	var request *questionRequest
	for i, candidate := range u.questions {
		if candidate.agentID == activeID {
			request = candidate
			u.questions = append(u.questions[:i], u.questions[i+1:]...)
			break
		}
	}
	u.questionMu.Unlock()
	if request == nil {
		return false
	}
	u.screenMu.Lock()
	if request.draft == "" {
		request.draft = u.drafts[activeID]
	}
	u.drafts[activeID] = request.draft
	u.screenMu.Unlock()

	var answers []string
	var err error
	if request.ctx.Err() != nil {
		err = request.ctx.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		answers, request.next, err = u.runQuestionnaireProgress(request.ctx, request.questions, request.next, request.answers, "Ctrl+C cancels this request.", true)
	}
	if errors.Is(err, errQuestionDeferred) {
		request.answers = answers
		if u.terminal.SubmissionKey == 27 {
			u.deferredInteractions[activeID] = true
		}
		u.questionMu.Lock()
		u.questions = append([]*questionRequest{request}, u.questions...)
		u.questionMu.Unlock()
		if u.deferredInteractions[activeID] {
			u.restoreQuestionDraft(request)
			u.screenMu.Lock()
			request.draft = ""
			u.screenMu.Unlock()
			return false
		}
		return true
	}
	request.result <- questionResult{answers: answers, err: err}
	u.restoreQuestionDraft(request)
	if manager != nil {
		if errors.Is(err, context.Canceled) && request.ctx.Err() == nil {
			_ = manager.Cancel(activeID)
		}
		manager.SetWaitingForApproval(activeID, false)
	}
	u.updateActiveCancellation()
	return false
}

func (u *UI) runQuestionnaire(ctx context.Context, questions []question.Question) ([]string, error) {
	return u.runQuestionnaireWithFooter(ctx, questions, "Ctrl+C cancels planning.")
}

func (u *UI) runQuestionnaireWithFooter(ctx context.Context, questions []question.Question, footer string) ([]string, error) {
	answers, _, err := u.runQuestionnaireProgress(ctx, questions, 0, nil, footer, false)
	return answers, err
}

const questionNavHint = "Use /back, /next (Alt+Left/Right), or /1-N to revisit; /done submits."

type questionCommand int

const (
	questionCmdNone questionCommand = iota
	questionCmdBack
	questionCmdNext
	questionCmdDone
	questionCmdJump
)

// parseQuestionCommand recognizes navigation typed at an answer prompt. These
// words are reserved only while a multi-question questionnaire is open; typed
// answers still accept any other text, including paths starting with a slash.
func parseQuestionCommand(answer string, total int) (questionCommand, int) {
	switch strings.ToLower(answer) {
	case "/back", "/prev":
		return questionCmdBack, 0
	case "/next":
		return questionCmdNext, 0
	case "/done", "/submit":
		return questionCmdDone, 0
	}
	if strings.HasPrefix(answer, "/") {
		if number, err := strconv.Atoi(answer[1:]); err == nil && number >= 1 && number <= total {
			return questionCmdJump, number - 1
		}
	}
	return questionCmdNone, 0
}

func firstUnanswered(answers []string) int {
	for i, answer := range answers {
		if answer == "" {
			return i
		}
	}
	return -1
}

// nextQuestionIndex moves to the closest unanswered question, wrapping around.
// When every answer is present it returns total, the review step.
func nextQuestionIndex(answers []string, from int) int {
	total := len(answers)
	for step := 1; step <= total; step++ {
		index := (from + step) % total
		if answers[index] == "" {
			return index
		}
	}
	return total
}

// runQuestionnaireProgress answers every question before returning. With more
// than one question the user can revisit and re-answer earlier questions, then
// review the whole set before submitting it to the agent. `previous` holds the
// answers collected so far ("" means unanswered) and `start` is the question
// being shown, or len(questions) for the review step. Both are returned on
// Esc/tab deferral so the same state can be restored later.
func (u *UI) runQuestionnaireProgress(ctx context.Context, questions []question.Question, start int, previous []string, footer string, allowTabSwitch bool) ([]string, int, error) {
	if len(questions) == 0 {
		return nil, start, fmt.Errorf("questionnaire has no questions")
	}
	questionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if u.input != nil {
		var wakeOnce sync.Once
		wakeInput := func() {
			wakeOnce.Do(func() { u.input.interruptLine() })
		}
		watchDone := make(chan struct{})
		go func() {
			select {
			case <-questionCtx.Done():
				select {
				case <-watchDone:
					return
				default:
				}
				wakeInput()
			case <-watchDone:
			}
		}()
		defer close(watchDone)
		u.input.setCancel(func() {
			cancel()
			wakeInput()
		})
		defer u.input.setCancel(nil)
	}
	u.screenMu.Lock()
	savedBrowsing := u.activeViewportLocked().browsing
	savedAnchor := u.activeViewportLocked().anchor
	savedPanel := u.questionPanel
	u.activeViewportLocked().browsing = false
	u.questionnaireActive = true
	u.screenMu.Unlock()
	defer func() {
		u.screenMu.Lock()
		u.questionnaireActive = false
		u.questionPanel = savedPanel
		u.questionNav = 0
		u.activeViewportLocked().browsing = savedBrowsing
		u.activeViewportLocked().anchor = savedAnchor
		u.screenMu.Unlock()
		u.restorePlanPrompt()
	}()

	total := len(questions)
	navigable := total > 1
	answers := make([]string, total)
	copy(answers, previous)
	index := max(0, min(start, total))
	displayed := make([]question.Question, total)
	for i, item := range questions {
		displayed[i] = redaction.Copy(u.redaction, redaction.Terminal, item)
	}
	questionFooter := footer
	if navigable {
		questionFooter += " " + questionNavHint
	}
	width := u.width
	// Retain logical lines in history so resizing does not wrap old breaks again.
	if u.fixedInput {
		width = 0
	}
	printedIndex := -1
	for {
		if err := questionCtx.Err(); err != nil {
			return nil, index, err
		}
		if !navigable && index >= total {
			return answers, total, nil
		}
		if navigable {
			u.setQuestionPanel(index, displayed, u.displayQuestionAnswers(answers))
		}
		if index != printedIndex {
			if index == total {
				u.printSystemMessage(formatQuestionReview(displayed, u.displayQuestionAnswers(answers), width, footer))
			} else {
				u.printSystemMessage(formatQuestionWithAnswer(displayed[index], index, total, width, questionFooter, u.displayQuestionAnswer(answers[index])))
			}
			printedIndex = index
		}
		prompt := fmt.Sprintf("Answer %d/%d> ", index+1, total)
		if index == total {
			prompt = fmt.Sprintf("Submit %d/%d> ", total, total)
		}
		u.terminal.SetPrompt(prompt)
		u.renderInput(prompt, "", 0)
		answer, err := u.readLine()
		if u.terminal.SubmissionKey == 27 && allowTabSwitch {
			return answers, index, errQuestionDeferred
		}
		if err != nil {
			return nil, index, err
		}
		if err := questionCtx.Err(); err != nil {
			return nil, index, err
		}
		u.tabMu.Lock()
		switching := u.pendingTab != 0
		u.tabMu.Unlock()
		if switching && allowTabSwitch {
			return answers, index, errQuestionDeferred
		}
		// Alt+Left/Right submit the uncommitted line (asynchronously injected
		// Enter), so consume that request before the line is treated as an
		// answer.
		if direction := u.takeQuestionNav(); direction != 0 && navigable {
			switch {
			case direction < 0 && index > 0:
				index--
			case direction > 0 && index < total:
				index++
			case direction < 0:
				u.printSystemMessage(yellow + "Already at the first question." + reset)
			default:
				u.printSystemMessage(yellow + "Already at the last question; press Enter to submit." + reset)
			}
			continue
		}
		answer = strings.TrimSpace(answer)
		if navigable {
			command, target := parseQuestionCommand(answer, total)
			switch command {
			case questionCmdBack:
				if index == 0 {
					u.printSystemMessage(yellow + "Already at the first question." + reset)
				} else {
					index--
				}
				continue
			case questionCmdNext:
				if index < total {
					index++
				} else {
					u.printSystemMessage(yellow + "Already at the last question; press Enter to submit." + reset)
				}
				continue
			case questionCmdJump:
				index = target
				continue
			case questionCmdDone:
				if missing := firstUnanswered(answers); missing >= 0 {
					u.printSystemMessage(yellow + fmt.Sprintf("Question %d still needs an answer.", missing+1) + reset)
					index = missing
				} else {
					return answers, total, nil
				}
				continue
			}
		}
		if index == total {
			if answer != "" {
				u.printSystemMessage(yellow + "Press Enter to submit, or /back (or /1-" + strconv.Itoa(total) + ") to revise an answer." + reset)
				continue
			}
			if missing := firstUnanswered(answers); missing >= 0 {
				u.printSystemMessage(yellow + fmt.Sprintf("Question %d still needs an answer.", missing+1) + reset)
				index = missing
				continue
			}
			return answers, total, nil
		}
		if answer == "" {
			if answers[index] != "" {
				index = nextQuestionIndex(answers, index)
				continue
			}
			feedback := "Please enter an answer, or press Ctrl+C to cancel."
			if navigable {
				feedback = "Please enter an answer, use /next to skip, or press Ctrl+C to cancel."
			}
			u.printSystemMessage(yellow + feedback + reset)
			continue
		}
		answer, valid := normalizeQuestionAnswerWithCustom(answer, questions[index].Options, questions[index].AllowCustom)
		if !valid {
			u.printSystemMessage(yellow + "Choose one of the listed options." + reset)
			continue
		}
		answers[index] = answer
		if !navigable {
			return answers, total, nil
		}
		index = nextQuestionIndex(answers, index)
	}
}

func (u *UI) restorePlanPrompt() {
	label := u.currentInputPrompt()
	if u.terminal != nil {
		u.terminal.SetPrompt(label)
	}
	u.screenMu.Lock()
	u.inputLabel = label
	u.paintFixedLocked(0)
	u.screenMu.Unlock()
}

func formatQuestion(item question.Question, index, total, width int) string {
	return formatQuestionWithFooter(item, index, total, width, "Ctrl+C cancels these questions.")
}

func formatQuestionWithFooter(item question.Question, index, total, width int, footer string) string {
	return formatQuestionWithAnswer(item, index, total, width, footer, "")
}

// formatQuestionWithAnswer keeps the original inline question layout. When a
// previously answered question is revisited, `current` shows the answer that
// Enter will keep, so it can be replaced without retyping the whole choice.
func formatQuestionWithAnswer(item question.Question, index, total, width int, footer, current string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Question %d/%d:\n", index+1, total)
	lines := strings.Split(strings.TrimSpace(item.Text), "\n")
	for i, line := range lines {
		lines[i] = sanitizeDiffLine(line, "<ESC>")
	}
	text := strings.Join(lines, "\n")
	fmt.Fprintf(&output, "%s\n", wrapANSI(text, width, "   "))
	for optionIndex, option := range item.Options {
		option = sanitizeDiffLine(strings.TrimSpace(option), "<ESC>")
		if optionIndex < len(item.OptionDescriptions) {
			if description := sanitizeDiffLine(strings.TrimSpace(item.OptionDescriptions[optionIndex]), "<ESC>"); description != "" {
				option += interfaceGlyph(UnicodeEnabled(), " — ", " - ") + description
			}
		}
		prefix := fmt.Sprintf("   %d) ", optionIndex+1)
		fmt.Fprintln(&output, wrapANSIWithPrefix(prefix, option, width))
	}
	if current != "" {
		fmt.Fprintln(&output, wrapANSI("Current answer: "+sanitizeDiffLine(current, "<ESC>"), width, "   "))
	}
	var hint string
	if len(item.Options) == 0 || item.AllowCustom {
		hint = "Choose an option or type your own answer. "
	} else {
		hint = "Choose one of the listed options. "
	}
	output.WriteByte('\n')
	output.WriteString(wrapANSI(hint+footer, width, ""))
	return output.String()
}

// formatQuestionReview lists every question with its collected answer before
// the questionnaire is submitted, making a revisiting round easy to verify.
func formatQuestionReview(items []question.Question, answers []string, width int, footer string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Review your answers (%d/%d):\n", len(items), len(items))
	for i, item := range items {
		questionText := strings.Join(strings.Fields(strings.TrimSpace(item.Text)), " ")
		answer := "(unanswered)"
		if i < len(answers) && answers[i] != "" {
			answer = answers[i]
		}
		fmt.Fprintln(&output, wrapANSIWithPrefix(fmt.Sprintf("  %d) ", i+1), sanitizeDiffLine(questionText, "<ESC>"), width))
		fmt.Fprintln(&output, wrapANSIWithPrefix("     ", "Answer: "+sanitizeDiffLine(answer, "<ESC>"), width))
	}
	output.WriteByte('\n')
	output.WriteString(wrapANSI("Enter submits, /back or /1-"+strconv.Itoa(len(items))+" revises a question. "+footer, width, ""))
	return output.String()
}

func normalizeQuestionAnswer(answer string, options []string) (string, bool) {
	return normalizeQuestionAnswerWithCustom(answer, options, true)
}

func normalizeQuestionAnswerWithCustom(answer string, options []string, allowCustom bool) (string, bool) {
	if len(options) == 0 {
		return answer, true
	}
	if index, err := strconv.Atoi(answer); err == nil && index >= 1 && index <= len(options) {
		return options[index-1], true
	}
	for _, option := range options {
		if strings.EqualFold(answer, option) {
			return option, true
		}
	}
	if allowCustom {
		return answer, true
	}
	return "", false
}
