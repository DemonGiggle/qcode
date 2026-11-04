package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/trace"
)

type planningDialogueProvider struct {
	messages []llm.Message
	requests []llm.Request
}

func (*planningDialogueProvider) Name() string { return "planning-dialogue" }
func (p *planningDialogueProvider) Complete(_ context.Context, request llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	index := min(len(p.requests), len(p.messages)-1)
	p.requests = append(p.requests, request)
	message := p.messages[index]
	if message.Content != "" {
		stream(llm.StreamEvent{Kind: llm.StreamOutput, Text: message.Content})
	}
	return llm.Response{Message: message}, nil
}

func planningQuestionMessage() llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
		ID: "ask", Name: "ask_questions",
		Arguments: json.RawMessage(`{"questions":[{"question":"Which store?","options":["SQLite","Postgres"]}]}`),
	}}}
}

func planningSubmissionMessage(mode ExecutionMode) llm.Message {
	call := llm.ToolCall{ID: "submit", Name: "propose_plan", Arguments: json.RawMessage(`{"title":"Storage","summary":"Use SQLite.","steps":["Configure SQLite."],"validation":["Run storage tests."]}`)}
	if mode == ModeSkillPlan {
		call.Name = "propose_skill"
		call.Arguments = json.RawMessage(`{"name":"storage","location":".qcode/skills","content":"---\ndescription: Configure storage.\n---\n\n# Storage\n\nConfigure SQLite and run storage tests."}`)
	}
	return llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}
}

func setPlanningMode(a *Agent, mode ExecutionMode) {
	if mode == ModePlan {
		a.SetPlanMode(true)
	} else {
		a.SetSkillPlanMode(true)
	}
}

func TestPlanningQuestionClarificationLoop(t *testing.T) {
	for _, mode := range []ExecutionMode{ModePlan, ModeSkillPlan} {
		t.Run(string(mode), func(t *testing.T) {
			provider := &planningDialogueProvider{messages: []llm.Message{
				planningQuestionMessage(),
				{Role: "assistant", Content: "SQLite is local; Postgres runs as a shared service."},
				planningQuestionMessage(),
				{Role: "assistant", Content: "I recommend SQLite for this local application."},
				planningQuestionMessage(),
				planningSubmissionMessage(mode),
			}}
			var out bytes.Buffer
			a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), &out, 8)
			setPlanningMode(a, mode)
			replies := []string{"What is the difference?", "Which would you recommend?", "SQLite"}
			asked := 0
			a.SetQuestioner(func(_ context.Context, questions []Question) ([]string, error) {
				if asked >= len(replies) {
					t.Fatal("questionnaire repeated after the decision")
				}
				if len(questions) != 1 || questions[0].Text != "Which store?" || len(questions[0].Options) != 2 {
					t.Fatalf("question changed: %+v", questions)
				}
				if _, ok := a.LatestPlan(); ok {
					t.Fatal("plan submitted before the decision")
				}
				if _, ok := a.LatestSkillDraft(); ok {
					t.Fatal("skill submitted before the decision")
				}
				reply := replies[asked]
				asked++
				return []string{reply}, nil
			})
			if err := a.Run(context.Background(), "Plan storage for this local application"); err != nil {
				t.Fatal(err)
			}
			if asked != 3 || len(provider.requests) != 6 {
				t.Fatalf("loop ended early: asked=%d requests=%d", asked, len(provider.requests))
			}
			for i, index := range []int{1, 3, 5} {
				messages := provider.requests[index].Messages
				last := messages[len(messages)-1]
				if last.Role != "user" || last.Untrusted || !strings.Contains(last.Content, replies[i]) {
					t.Fatalf("reply was not direct user input: %+v", last)
				}
			}
			if strings.Count(provider.requests[2].Messages[0].Content, prompt.PlanningQuestionContinuation) != 2 {
				t.Fatal("explanation did not trigger a continuation instruction")
			}
			if strings.Count(a.messages[0].Content, prompt.PlanningQuestionContinuation) != 1 {
				t.Fatal("continuation instruction leaked into durable history")
			}
			if !strings.Contains(out.String(), "SQLite is local") || !strings.Contains(out.String(), "I recommend SQLite") {
				t.Fatalf("clarification was not displayed: %s", out.String())
			}
			if mode == ModePlan {
				if _, ready := a.TakePlanDecision(); !ready {
					t.Fatal("completed plan did not reach the implementation decision")
				}
			} else if _, ready := a.TakeSkillPlanDecision(); !ready {
				t.Fatal("completed draft did not reach the creation decision")
			}
		})
	}
}

func TestPlanningQuestionBatchDefersActionsUntilRepliesAreProcessed(t *testing.T) {
	for _, mode := range []ExecutionMode{ModePlan, ModeSkillPlan} {
		t.Run(string(mode), func(t *testing.T) {
			ask := planningQuestionMessage()
			ask.ToolCalls[0].Arguments = json.RawMessage(`{"questions":[{"question":"Which store?","options":["SQLite","Postgres"]},{"question":"Which directory?"}]}`)
			ask.ToolCalls = append(ask.ToolCalls,
				llm.ToolCall{ID: "read", Name: "read", Arguments: json.RawMessage(`{"path":"unrelated.go"}`)},
				planningSubmissionMessage(mode).ToolCalls[0],
			)
			provider := &planningDialogueProvider{messages: []llm.Message{ask, planningSubmissionMessage(mode)}}
			tools := &modeTestToolset{}
			a := New(provider, "model", tools, trace.New(io.Discard, false), io.Discard, 3)
			setPlanningMode(a, mode)
			a.SetQuestioner(func(context.Context, []Question) ([]string, error) {
				return []string{"What is the difference?", "data/"}, nil
			})
			if err := a.Run(context.Background(), "Plan storage"); err != nil {
				t.Fatal(err)
			}
			if tools.called != "" || len(provider.requests) != 2 {
				t.Fatalf("actions ran before processing replies: tools=%q requests=%d", tools.called, len(provider.requests))
			}
			messages := provider.requests[1].Messages
			for _, message := range messages[len(messages)-4 : len(messages)-1] {
				if message.Role != "tool" {
					t.Fatalf("tool-call pairing broken: %+v", messages)
				}
			}
			last := messages[len(messages)-1]
			if last.Role != "user" || last.Untrusted || !strings.Contains(last.Content, "What is the difference?") || !strings.Contains(last.Content, "data/") {
				t.Fatalf("batched replies lost: %+v", last)
			}
			for _, message := range messages[len(messages)-3 : len(messages)-1] {
				if !strings.Contains(message.Content, "deferred") {
					t.Fatalf("premature action was not deferred: %+v", message)
				}
			}
		})
	}
}

func TestPlanningQuestionLoopRespectsLimitsAndCancellation(t *testing.T) {
	for _, mode := range []ExecutionMode{ModePlan, ModeSkillPlan} {
		for _, cancel := range []bool{false, true} {
			name := string(mode) + "/step-limit"
			if cancel {
				name = string(mode) + "/cancel"
			}
			t.Run(name, func(t *testing.T) {
				provider := &planningDialogueProvider{messages: []llm.Message{
					planningQuestionMessage(), {Role: "assistant", Content: "Here is an explanation."},
				}}
				a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 3)
				setPlanningMode(a, mode)
				a.SetQuestioner(func(context.Context, []Question) ([]string, error) {
					if cancel {
						return nil, context.Canceled
					}
					return []string{"What is the difference?"}, nil
				})
				err := a.Run(context.Background(), "Plan storage")
				if cancel {
					if !errors.Is(err, context.Canceled) || len(provider.requests) != 1 {
						t.Fatalf("cancellation ignored: err=%v requests=%d", err, len(provider.requests))
					}
				} else if err == nil || !strings.Contains(err.Error(), "maximum of 3 model steps") || len(provider.requests) != 3 {
					t.Fatalf("step limit ignored: err=%v requests=%d", err, len(provider.requests))
				}
				if _, ready := a.TakePlanDecision(); ready {
					t.Fatal("incomplete planning triggered implementation")
				}
				if _, ready := a.TakeSkillPlanDecision(); ready {
					t.Fatal("incomplete planning triggered creation")
				}
			})
		}
	}
}

func TestPlanningStandaloneAnswerCanFinishWithoutQuestionnaire(t *testing.T) {
	provider := &planningDialogueProvider{messages: []llm.Message{{Role: "assistant", Content: "SQLite is local."}}}
	a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), io.Discard, 3)
	a.SetPlanMode(true)
	if err := a.Run(context.Background(), "What is SQLite?"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatal("standalone answer unnecessarily continued planning")
	}
}

func TestPlanningQuestionCombinedTextAndAskDefersQuestionnaire(t *testing.T) {
	for _, mode := range []ExecutionMode{ModePlan, ModeSkillPlan} {
		t.Run(string(mode), func(t *testing.T) {
			combined := planningQuestionMessage()
			combined.Content = "Good questions \u2014 let me answer those directly, then I'll re-confirm scope."
			provider := &planningDialogueProvider{messages: []llm.Message{
				planningQuestionMessage(),
				combined,
				{Role: "assistant", Content: "SQLite is local; Postgres runs as a shared service."},
				planningQuestionMessage(),
				planningSubmissionMessage(mode),
			}}
			var out bytes.Buffer
			a := New(provider, "model", &modeTestToolset{}, trace.New(io.Discard, false), &out, 8)
			setPlanningMode(a, mode)
			replies := []string{"What is the difference?", "SQLite"}
			asked := 0
			a.SetQuestioner(func(_ context.Context, questions []Question) ([]string, error) {
				if asked >= len(replies) {
					t.Fatal("questionnaire repeated after the decision")
				}
				reply := replies[asked]
				asked++
				return []string{reply}, nil
			})
			if err := a.Run(context.Background(), "Plan storage"); err != nil {
				t.Fatal(err)
			}
			if asked != 2 {
				t.Fatalf("questionnaire shown before answer was read: asked=%d requests=%d", asked, len(provider.requests))
			}
			if len(provider.requests) != 5 {
				t.Fatalf("combined answer and re-ask were not split: asked=%d requests=%d", asked, len(provider.requests))
			}
			// The deferred turn must not have invoked the questionnaire: the
			// tool result paired with the combined call reports deferral.
			combinedReq := provider.requests[2].Messages
			foundDeferred := false
			for _, message := range combinedReq {
				if message.Role == "tool" && strings.Contains(message.Content, "deferred") {
					foundDeferred = true
				}
			}
			if !foundDeferred {
				t.Fatalf("combined questionnaire was not deferred: %+v", combinedReq)
			}
			if !strings.Contains(out.String(), "Good questions") || !strings.Contains(out.String(), "SQLite is local") {
				t.Fatalf("answer was not shown before re-ask: %s", out.String())
			}
		})
	}
}
