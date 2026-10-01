package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
	"qcode/internal/tui"
)

type remoteSteeringProvider struct{ entered chan string }

func (*remoteSteeringProvider) Name() string { return "remote-steering-test" }
func (p *remoteSteeringProvider) Complete(ctx context.Context, request llm.Request, _ llm.StreamCallback) (llm.Response, error) {
	p.entered <- request.Messages[len(request.Messages)-1].Content
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type remoteSteeringTools struct{}

func (*remoteSteeringTools) Schemas() []llm.Tool        { return nil }
func (*remoteSteeringTools) EnabledSchemas() []llm.Tool { return nil }
func (*remoteSteeringTools) ExecuteDetailed(context.Context, llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{}, nil
}

func TestStructuredPromptEndpointsTargetSelectedAgentAndArbitrateCancellation(t *testing.T) {
	out, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	ui := tui.New(os.Stdin, out, nil, "test", "test", t.TempDir())
	host := control.NewHost(context.Background(), 3)
	defer host.Shutdown()
	ui.SetAgentManager(host)
	provider := &remoteSteeringProvider{entered: make(chan string, 8)}
	host.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		w, _ := ui.AddAgentView(id, "test", model)
		return agent.New(provider, model, &remoteSteeringTools{}, trace.New(io.Discard, false), w, 3), nil
	})
	if _, err := host.CreateMain("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Create("test"); err != nil {
		t.Fatal(err)
	}
	runner, _ := host.Agent("main")
	ui.SetRunner(runner)
	original, err := host.Submit("main", "initial busy task")
	if err != nil {
		t.Fatal(err)
	}
	waitPrompt := func(want string) {
		t.Helper()
		select {
		case got := <-provider.entered:
			if got != want {
				t.Fatalf("prompt=%q want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("model request missing")
		}
	}
	waitPrompt("initial busy task")
	handler := New(ui).routesWithAuth(newAuthStore(tui.RemoteModePureWebOpen))
	post := func(path string, value any, status int) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(value)
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		return response
	}
	submit := func(target, observed, prompt string, intent session.SubmissionIntent) session.Submission {
		t.Helper()
		response := post("/api/v1/prompts", session.PromptSubmission{AgentID: target, ObservedTaskID: observed, Prompt: prompt, Intent: intent, Actor: "spoofed", Source: "spoofed"}, http.StatusAccepted)
		var result session.Submission
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := submit("main", original.RequestID, "first steer", session.IntentAutomatic)
	second := submit("main", original.RequestID, "second steer", session.IntentSteer)
	if first.Intent != session.IntentSteer || second.ParentTaskID != original.RequestID {
		t.Fatal("structured submission contract lost")
	}
	queued := submit("main", original.RequestID, "queued work", session.IntentQueue)
	if queued.QueuePosition != 1 {
		t.Fatal("queue intent steered")
	}
	worker := submit("agent-1", "", "worker task", session.IntentAutomatic)
	waitPrompt("worker task")
	if worker.TargetID != "agent-1" || worker.Intent != session.IntentAutomatic {
		t.Fatal("browser targeted terminal tab")
	}
	records := host.WorkRecords()
	if records[0].Steers[0].State != session.InputSuperseded || records[0].Steers[1].Actor != "pure-web-open" || records[0].Steers[1].Source != "browser" {
		t.Fatal("source or replacement not recorded")
	}
	post("/api/v1/prompts/"+second.RequestID+"/cancel", map[string]string{"agent_id": "main"}, http.StatusOK)
	post("/api/v1/prompts/"+queued.RequestID+"/cancel", map[string]string{"agent_id": "main"}, http.StatusOK)
	queued = submit("main", original.RequestID, "FIFO survives", session.IntentQueue)
	post("/api/v1/agents/main/cancel", map[string]string{"observed_task_id": original.RequestID}, http.StatusOK)
	waitPrompt("FIFO survives")
	post("/api/v1/agents/main/cancel", map[string]string{"observed_task_id": original.RequestID}, http.StatusConflict)
	post("/api/v1/prompts", session.PromptSubmission{AgentID: "main", ObservedTaskID: original.RequestID, Prompt: "stale steer", Intent: session.IntentSteer}, http.StatusConflict)
	post("/api/v1/prompts/"+queued.RequestID+"/cancel", map[string]string{"agent_id": "main"}, http.StatusConflict)
	post("/api/v1/prompts", session.PromptSubmission{AgentID: "main", Prompt: "/exit", Intent: session.IntentQueue}, http.StatusConflict)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/snapshot", nil))
	var snapshot struct {
		Runtime struct {
			SteeringEvents []session.SteeringEvent `json:"steering_events"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil || len(snapshot.Runtime.SteeringEvents) < 3 {
		t.Fatalf("reconnect lost steering events: %v %s", err, response.Body.String())
	}
}
