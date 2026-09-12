package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"qcode/internal/learning"
	"qcode/internal/session"
)

type learningRunner interface {
	Learn(context.Context, string, learning.Approver) (string, error)
}

type learningLister interface {
	ListLearning(context.Context) ([]learning.Learning, error)
}

func (u *UI) learn(ctx context.Context, arguments string) {
	runner, ok := u.runner.(learningRunner)
	if !ok {
		u.printSystemMessage(yellow + "Global learning is unavailable." + reset)
		return
	}
	if strings.TrimSpace(arguments) == "list" {
		lister, ok := runner.(learningLister)
		if !ok {
			u.printSystemMessage(yellow + "Global learning is unavailable." + reset)
			return
		}
		items, err := lister.ListLearning(ctx)
		if err != nil {
			u.printSystemMessage(yellow + "Learning error: " + sanitizeDiffLine(err.Error(), "<ESC>") + reset)
			return
		}
		u.printSystemMessage(formatLearningList(items, u.width, ColorEnabled(u.out)))
		return
	}
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	u.input.setCancel(cancel)
	defer u.input.setCancel(nil)
	localApprover := func(ctx context.Context, review []learning.Change) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// Let the line editor receive approval input; Ctrl+C submits an empty line
		// and therefore rejects the proposal. Do not consume it as task input.
		u.input.setCancel(nil)
		defer u.input.setCancel(cancel)
		u.printSystemMessage(formatLearningReview(review, u.width, ColorEnabled(u.out)))
		u.terminal.SetPrompt(yellow + "Apply these global learning changes? [y/N] " + reset)
		answer, err := u.readLine()
		u.terminal.SetPrompt(inputPrompt)
		if err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return approveLearningAnswer(answer), nil
	}
	approver := localApprover
	if broker, ok := u.manager.(interactionController); ok {
		u.screenMu.Lock()
		agentID := u.activeAgent
		u.screenMu.Unlock()
		approver = func(ctx context.Context, review []learning.Change) (bool, error) {
			payload, _ := json.Marshal(review)
			waiter, err := broker.BeginInteraction(session.Interaction{AgentID: agentID, Kind: session.InteractionLearningApproval, Payload: payload})
			if err != nil {
				return false, err
			}
			u.signalPresentation()
			localCtx, cancelLocal := context.WithCancel(ctx)
			defer cancelLocal()
			brokerCtx, cancelBroker := context.WithCancel(ctx)
			defer cancelBroker()
			type approval struct {
				approved bool
				err      error
			}
			localResult := make(chan approval, 1)
			go func() { approved, err := localApprover(localCtx, review); localResult <- approval{approved, err} }()
			type remoteApproval struct {
				resolution session.Resolution
				err        error
			}
			remoteResult := make(chan remoteApproval, 1)
			go func() { resolution, err := waiter.Wait(brokerCtx); remoteResult <- remoteApproval{resolution, err} }()
			select {
			case local := <-localResult:
				if local.err != nil {
					return false, local.err
				}
				value, _ := json.Marshal(local.approved)
				_ = broker.ResolveInteraction(session.Resolution{InteractionID: waiter.InteractionInfo().ID, Value: value, ResolvedBy: "local-tui"})
				u.signalPresentation()
				remote := <-remoteResult
				if remote.err != nil {
					return false, remote.err
				}
				var approved bool
				if err := json.Unmarshal(remote.resolution.Value, &approved); err != nil {
					return false, err
				}
				return approved, nil
			case remote := <-remoteResult:
				cancelLocal()
				if u.input != nil {
					u.input.interruptLine()
				}
				<-localResult
				if remote.err != nil {
					return false, remote.err
				}
				var approved bool
				if err := json.Unmarshal(remote.resolution.Value, &approved); err != nil {
					return false, err
				}
				return approved, nil
			}
		}
	}
	result, err := runner.Learn(taskCtx, arguments, approver)
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

// formatLearningList presents stored records as readable terminal text while
// retaining the ID needed by /learn forget. It intentionally omits storage-only
// metadata such as timestamps, schema version, and source session IDs.
func formatLearningList(items []learning.Learning, width int, color bool) string {
	if len(items) == 0 {
		return "No global learning stored."
	}
	style := func(text, code string) string {
		if color && code != "" {
			return code + text + reset
		}
		return text
	}
	var out strings.Builder
	line := func(text, code, indent string) {
		for _, part := range strings.Split(text, "\n") {
			safe := sanitizeDiffLine(part, "<ESC>")
			fmt.Fprintln(&out, wrapANSI(indent+style(safe, code), width, indent))
		}
	}
	line(fmt.Sprintf("Global learning: %d record(s)", len(items)), bold, "")
	line("Available in every workspace. Use an ID with /learn forget <id>.", dim, "")
	for i, item := range items {
		out.WriteByte('\n')
		line(fmt.Sprintf("%d. %s", i+1, item.Topic), bold+cyan, "")
		line("ID: "+item.ID, dim, "   ")
		line(item.Content, "", "   ")
		if len(item.Tags) > 0 {
			line("Tags: "+strings.Join(item.Tags, ", "), dim, "   ")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// Display the exact content being approved, without storage metadata or JSON.
func formatLearningReview(changes []learning.Change, width int, color bool) string {
	style := func(text, code string) string {
		if color && code != "" {
			return code + text + reset
		}
		return text
	}
	var out strings.Builder
	line := func(text, code, indent string) {
		for _, part := range strings.Split(text, "\n") {
			safe := sanitizeDiffLine(part, "<ESC>")
			fmt.Fprintln(&out, wrapANSI(indent+style(safe, code), width, indent))
		}
	}
	line(fmt.Sprintf("Global learning: %d proposed change(s)", len(changes)), bold, "")
	line("Available in every workspace. Review before saving.", dim, "")
	for i, change := range changes {
		item, action, accent := change.After, "Add", green
		if item == nil {
			item, action, accent = change.Before, "Remove", red
		} else if change.Before != nil {
			action, accent = "Update", yellow
		}
		if item == nil {
			continue
		}
		out.WriteByte('\n')
		line(fmt.Sprintf("%d. %s: %s", i+1, action, item.Topic), bold+accent, "")
		if action == "Update" {
			if change.Before.Topic != item.Topic {
				line("Previous title: "+change.Before.Topic, dim, "   ")
			}
			if change.Before.Content != item.Content {
				line("Was: "+change.Before.Content, dim, "   ")
			}
			if !slices.Equal(change.Before.Tags, item.Tags) {
				line("Previous tags: "+strings.Join(change.Before.Tags, ", "), dim, "   ")
			}
		}
		line(item.Content, "", "   ")
		if len(item.Tags) > 0 {
			line("Tags: "+strings.Join(item.Tags, ", "), dim, "   ")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}
