package tui

import (
	"context"
	"errors"
	"strings"

	"qcode/internal/learning"
)

type learningRunner interface {
	Learn(context.Context, string, learning.Approver) (string, error)
}

func (u *UI) learn(ctx context.Context, arguments string) {
	runner, ok := u.runner.(learningRunner)
	if !ok {
		u.printSystemMessage(yellow + "Global learning is unavailable." + reset)
		return
	}
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	u.input.setCancel(cancel)
	defer u.input.setCancel(nil)
	result, err := runner.Learn(taskCtx, arguments, func(ctx context.Context, review string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// Let the line editor receive approval input; Ctrl+C submits an empty line
		// and therefore rejects the proposal. Do not consume it as task input.
		u.input.setCancel(nil)
		defer u.input.setCancel(cancel)
		u.printSystemMessage(learningDisplay(review))
		u.terminal.SetPrompt(yellow + "Apply these global learning changes? [y/N] " + reset)
		answer, err := u.terminal.ReadLine()
		u.terminal.SetPrompt(inputPrompt)
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return approveLearningAnswer(answer), nil
	})
	u.drawStatusBar()
	if errors.Is(err, context.Canceled) {
		u.printSystemMessage(yellow + "Learning cancelled." + reset)
	} else if err != nil {
		u.printSystemMessage(yellow + "Learning error: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
	} else {
		u.printSystemMessage(learningDisplay(result))
	}
}
func approveLearningAnswer(answer string) bool {
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

func learningDisplay(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = sanitizeDiffLine(line, "<ESC>")
	}
	return strings.Join(lines, "\n")
}
