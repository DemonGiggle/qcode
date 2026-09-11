package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"qcode/internal/question"
)

type questionRequest struct {
	agentID   string
	questions []question.Question
	ctx       context.Context
	result    chan questionResult
}

type questionResult struct {
	answers []string
	err     error
}

// AgentQuestioner returns the UI bridge used by an agent's Plan-mode
// ask_questions tool. Questions from inactive agents wait until that tab is
// selected, just like directory approvals.
func (u *UI) AgentQuestioner(id string) question.Questioner {
	if u.sessionHost != nil {
		return u.sessionHost.AgentQuestioner(id)
	}
	return func(ctx context.Context, questions []question.Question) ([]string, error) {
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
			u.removeQuestionRequest(request)
			return nil, ctx.Err()
		}
	}
}

func cloneQuestions(questions []question.Question) []question.Question {
	cloned := make([]question.Question, len(questions))
	for i, item := range questions {
		cloned[i] = question.Question{Text: item.Text, Options: append([]string(nil), item.Options...), AllowCustom: item.AllowCustom}
	}
	return cloned
}

func (u *UI) removeQuestionRequest(target *questionRequest) {
	u.questionMu.Lock()
	defer u.questionMu.Unlock()
	for i, request := range u.questions {
		if request != target {
			continue
		}
		u.questions = append(u.questions[:i], u.questions[i+1:]...)
		return
	}
}

// handlePendingQuestions is called only by the UI goroutine, so all terminal
// reads remain serialized with the line editor.
func (u *UI) handlePendingQuestions(ctx context.Context) {
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
		return
	}

	var answers []string
	var err error
	if request.ctx.Err() != nil {
		err = request.ctx.Err()
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		answers, err = u.runQuestionnaire(request.ctx, request.questions)
	}
	request.result <- questionResult{answers: answers, err: err}
	if manager != nil {
		if errors.Is(err, context.Canceled) && request.ctx.Err() == nil {
			_ = manager.Cancel(activeID)
		}
		manager.SetWaitingForApproval(activeID, false)
	}
	u.updateActiveCancellation()
}

func (u *UI) runQuestionnaire(ctx context.Context, questions []question.Question) ([]string, error) {
	return u.runQuestionnaireWithFooter(ctx, questions, "Ctrl+C cancels planning.")
}

func (u *UI) runQuestionnaireWithFooter(ctx context.Context, questions []question.Question, footer string) ([]string, error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("questionnaire has no questions")
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

	answers := make([]string, 0, len(questions))
	for i, question := range questions {
		u.printSystemMessage(formatQuestionWithFooter(question, i, len(questions), u.width, footer))
		for {
			if err := questionCtx.Err(); err != nil {
				return nil, err
			}
			prompt := fmt.Sprintf("Answer %d/%d> ", i+1, len(questions))
			u.terminal.SetPrompt(prompt)
			u.renderInput(prompt, "", 0)
			answer, err := u.readLine()
			if err != nil {
				return nil, err
			}
			if err := questionCtx.Err(); err != nil {
				return nil, err
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
	return answers, nil
}

func (u *UI) restorePlanPrompt() {
	plan := false
	if controller, ok := u.runner.(planController); ok {
		plan = controller.PlanMode()
	}
	u.setInputModePrompt(plan)
}

func formatQuestion(item question.Question, index, total, width int) string {
	return formatQuestionWithFooter(item, index, total, width, "Ctrl+C cancels planning.")
}

func formatQuestionWithFooter(item question.Question, index, total, width int, footer string) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Question %d/%d:\n", index+1, total)
	text := sanitizeDiffLine(strings.TrimSpace(item.Text), "<ESC>")
	fmt.Fprintf(&output, "%s\n", wrapANSI(text, width, "   "))
	for optionIndex, option := range item.Options {
		option = sanitizeDiffLine(strings.TrimSpace(option), "<ESC>")
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
