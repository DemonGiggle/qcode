package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"qcode/internal/learning"
	"qcode/internal/llm"
	"qcode/internal/prompt"
)

// LearningApprover receives the entire proposed change set, including before
// and after values. It is a user interface callback, never a model tool.
type LearningApprover = learning.Approver

func (a *Agent) SetLearning(store learning.Store, budget int) {
	a.learningStore = store
	a.learningBudget = budget
	a.learningContext = ""
	a.invalidateContextUsage()
}

func (a *Agent) requestMessages(ctx context.Context) []llm.Message {
	extra := ""
	if a.learningStore != nil && a.learningBudget > 0 {
		query := ""
		for i := len(a.messages) - 1; i >= 0; i-- {
			if a.messages[i].Role == "user" {
				query = a.messages[i].Content
				break
			}
		}
		budget := a.learningBudget - learning.EstimatedTokens(prompt.LearningReference)
		if budget > 0 && query != "" {
			items, err := a.learningStore.Search(ctx, query, budget)
			if err != nil {
				fmt.Fprintln(a.out, "Learning warning:", err)
			} else if len(items) > 0 {
				extra = prompt.LearningReference + learning.Context(items)
			}
		}
	}
	if a.learningContext != extra {
		a.contextUsage = nil
	}
	a.learningContext = extra
	messages := append([]llm.Message(nil), a.messages...)
	if extra != "" && len(messages) > 0 {
		messages[0].Content += extra
	}
	return messages
}

// Learn is reachable only through an explicit interactive /learn command.
// Neither proposals nor transient extraction requests enter chat history.
func (a *Agent) Learn(ctx context.Context, arguments string, approve LearningApprover) (string, error) {
	if a.learningStore == nil {
		return "", fmt.Errorf("global learning is unavailable in this session")
	}
	fields := strings.Fields(arguments)
	action := "extract"
	if len(fields) > 0 {
		action = fields[0]
	}
	valid := false
	switch action {
	case "extract":
		valid = len(fields) == 0
	case "list", "compact":
		valid = len(fields) == 1
	case "forget":
		valid = len(fields) == 2
	}
	if !valid {
		return "", fmt.Errorf("usage: /learn | /learn list | /learn forget <id> | /learn compact")
	}
	snapshot, err := a.learningStore.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	if action == "list" {
		if len(snapshot.Items) == 0 {
			return "No global learning stored.", nil
		}
		data, _ := json.MarshalIndent(snapshot.Items, "", "  ")
		return "Global learning (shared across workspaces):\n" + string(data), nil
	}
	if approve == nil {
		return "", fmt.Errorf("learning changes require interactive user review")
	}
	var plan learning.Plan
	if action == "forget" {
		for i := range snapshot.Items {
			if snapshot.Items[i].ID == fields[1] {
				plan = learning.Plan{Revision: snapshot.Revision, Changes: []learning.Change{{Before: &snapshot.Items[i]}}}
				break
			}
		}
		if len(plan.Changes) == 0 {
			return "", fmt.Errorf("unknown global learning ID %q", fields[1])
		}
	} else {
		if action == "compact" && len(snapshot.Items) == 0 {
			return "No global learning to compact.", nil
		}
		session := a.learningSession()
		if action == "extract" && len(session) == 0 {
			return "No conversation to learn from yet.", nil
		}
		input, _ := json.Marshal(struct {
			Existing []learning.Learning `json:"existing"`
			Session  []llm.Message       `json:"session,omitempty"`
		}{snapshot.Items, sessionForAction(action, session)})
		if len(input) > learning.MaxProposalBytes {
			return "", fmt.Errorf("learning review input exceeds 64 KiB; use /learn forget to reduce stored items before retrying")
		}
		instruction := prompt.LearningExtract
		if action == "compact" {
			instruction = prompt.LearningCompact
		}
		task := a.trace.BeginTask()
		span := a.trace.Start("llm", a.provider.Name(), map[string]any{"model": a.model, "operation": "learn " + action})
		proposalCtx, cancel := context.WithCancel(ctx)
		streamedBytes := 0
		response, requestErr := a.provider.Complete(proposalCtx, llm.Request{Model: a.model, Messages: []llm.Message{{Role: "system", Content: instruction}, {Role: "user", Content: string(input)}}}, func(event llm.StreamEvent) {
			streamedBytes += len(event.Text)
			if streamedBytes > learning.MaxProposalBytes {
				cancel()
			}
		})
		cancel()
		if streamedBytes > learning.MaxProposalBytes {
			requestErr = fmt.Errorf("learning proposal stream exceeds 64 KiB")
		}
		span.End(requestErr)
		task.End()
		if requestErr != nil {
			return "", requestErr
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(response.Message.ToolCalls) > 0 {
			return "", fmt.Errorf("learning proposals must not contain tool calls")
		}
		if len(response.Message.Content) > learning.MaxProposalBytes {
			return "", fmt.Errorf("learning proposal exceeds 64 KiB")
		}
		var proposal struct {
			Changes []learning.Draft `json:"changes"`
		}
		if err := learning.Decode([]byte(response.Message.Content), &proposal); err != nil {
			return "", fmt.Errorf("invalid learning proposal: %w", err)
		}
		if a.learningSessionID == "" {
			a.learningSessionID, err = learning.NewID()
			if err != nil {
				return "", err
			}
		}
		plan, err = learning.NewPlan(snapshot, proposal.Changes, a.learningSessionID, action == "compact")
		if err != nil {
			return "", fmt.Errorf("invalid learning proposal: %w", err)
		}
	}
	if len(plan.Changes) == 0 {
		return "No durable global learning changes proposed.", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	approved, err := approve(ctx, plan.Changes)
	if err != nil {
		return "", err
	}
	if !approved {
		return "Learning cancelled; nothing saved.", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backup, err := a.learningStore.Apply(ctx, plan)
	if err != nil {
		return "", err
	}
	a.learningContext = ""
	a.invalidateContextUsage()
	result := fmt.Sprintf("Saved %d approved global learning change(s).", len(plan.Changes))
	if backup != "" {
		result += "\nCompaction backup: " + backup
	}
	return result, nil
}

func sessionForAction(action string, session []llm.Message) []llm.Message {
	if action == "compact" {
		return nil
	}
	return session
}
func (a *Agent) learningSession() []llm.Message {
	// Only human/assistant text, never tool output, images, reasoning, or tool
	// arguments. Retain at most the latest 24 KiB of sanitized conversation.
	var reversed []llm.Message
	remaining := 24 * 1024
	for i := len(a.messages) - 1; i >= 0 && remaining > 0; i-- {
		m := a.messages[i]
		if m.Role != "user" && m.Role != "assistant" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		content := learning.Redact(m.Content)
		if len(content) > remaining {
			end := remaining
			for end > 0 && !utf8.RuneStart(content[end]) {
				end--
			}
			content = content[:end]
		}
		remaining -= len(content)
		reversed = append(reversed, llm.Message{Role: m.Role, Content: content})
	}
	result := make([]llm.Message, len(reversed))
	for i, m := range reversed {
		result[len(reversed)-1-i] = m
	}
	return result
}
