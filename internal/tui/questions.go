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
	if u.sessionHost != nil {
		return u.sessionHost.AgentQuestioner(id)
	}
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
	if request.draft == "" {
		return
	}
	u.screenMu.Lock()
	u.drafts[request.agentID] = request.draft
	active := u.activeAgent == request.agentID
	u.screenMu.Unlock()
	if active && u.input != nil {
		u.input.inject([]byte(request.draft))
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
		u.questionMu.Lock()
		u.questions = append([]*questionRequest{request}, u.questions...)
		u.questionMu.Unlock()
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
	defer u.restorePlanPrompt()

	answers := append([]string(nil), previous...)
	for i := start; i < len(questions); i++ {
		question := questions[i]
		u.printSystemMessage(formatQuestionWithFooter(question, i, len(questions), u.width, footer))
		for {
			if err := questionCtx.Err(); err != nil {
				return nil, i, err
			}
			prompt := fmt.Sprintf("Answer %d/%d> ", i+1, len(questions))
			u.terminal.SetPrompt(prompt)
			u.renderInput(prompt, "", 0)
			answer, err := u.readLine()
			if err != nil {
				return nil, i, err
			}
			if err := questionCtx.Err(); err != nil {
				return nil, i, err
			}
			u.tabMu.Lock()
			switching := u.pendingTab != 0
			u.tabMu.Unlock()
			if switching && allowTabSwitch {
				return answers, i, errQuestionDeferred
			}
			answer = strings.TrimSpace(answer)
			if answer == "" {
				u.printSystemMessage(yellow + "Please enter an answer, or press Ctrl+C to cancel." + reset)
				continue
			}
			answer, valid := normalizeQuestionAnswerWithCustom(answer, question.Options, question.AllowCustom)
			if !valid {
				u.printSystemMessage(yellow + "Choose one of the listed options." + reset)
				continue
			}
			answers = append(answers, answer)
			break
		}
	}
	return answers, len(questions), nil
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
	var output strings.Builder
	fmt.Fprintf(&output, "Question %d/%d:\n", index+1, total)
	text := sanitizeDiffLine(strings.TrimSpace(item.Text), "<ESC>")
	fmt.Fprintf(&output, "%s\n", wrapANSI(text, width, "   "))
	for optionIndex, option := range item.Options {
		option = sanitizeDiffLine(strings.TrimSpace(option), "<ESC>")
		if optionIndex < len(item.OptionDescriptions) {
			if description := sanitizeDiffLine(strings.TrimSpace(item.OptionDescriptions[optionIndex]), "<ESC>"); description != "" {
				option += " — " + description
			}
		}
		fmt.Fprintf(&output, "   %d) %s\n", optionIndex+1, wrapANSI(option, width, "      "))
	}
	if len(item.Options) == 0 || item.AllowCustom {
		output.WriteString("\nChoose an option or type your own answer. ")
	} else {
		output.WriteString("\nChoose one of the listed options. ")
	}
	output.WriteString(footer)
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
