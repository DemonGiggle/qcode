package tui

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"qcode/internal/control"
	"qcode/internal/question"
)

type fakeRemoteService struct {
	running bool
	url     string
	stopped bool
}

func TestRemoteCanWinQuestionInteraction(t *testing.T) {
	host := control.NewHost(context.Background(), 1)
	defer host.Shutdown()
	u := New(nil, nil, nil, "test", "model", ".")
	u.SetAgentManager(host)
	result := make(chan []string, 1)
	go func() {
		answers, _ := u.AgentQuestioner("main")(context.Background(), []question.Question{{Text: "Database?", Options: []string{"SQLite", "Postgres"}}})
		result <- answers
	}()
	deadline := time.After(time.Second)
	var interaction control.Interaction
	for interaction.ID == "" {
		select {
		case <-deadline:
			t.Fatal("interaction was not published")
		default:
			pending := host.PendingInteractions()
			if len(pending) > 0 {
				interaction = pending[0]
			} else {
				time.Sleep(time.Millisecond)
			}
		}
	}
	value, _ := json.Marshal([]string{"Postgres"})
	if err := u.ResolveRemoteInteraction("alice@example.com", interaction.ID, value); err != nil {
		t.Fatal(err)
	}
	select {
	case answers := <-result:
		if len(answers) != 1 || answers[0] != "Postgres" {
			t.Fatalf("answers = %v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("questioner did not receive remote answer")
	}
}

func (s *fakeRemoteService) Start(context.Context) (string, error) {
	s.running = true
	s.url = "https://host.tailnet.ts.net/qcode/test/"
	return s.url, nil
}
func (s *fakeRemoteService) Stop() error                 { s.running, s.stopped = false, true; return nil }
func (s *fakeRemoteService) Status() (bool, string, int) { return s.running, s.url, 2 }

func TestRemoteCommandLifecycle(t *testing.T) {
	history := newHistoryWriter(io.Discard)
	service := &fakeRemoteService{}
	u := &UI{display: history, remoteService: service}
	u.handleRemoteCommand(context.Background(), []string{"/remote"})
	if !service.running {
		t.Fatal("remote service did not start")
	}
	u.handleRemoteCommand(context.Background(), []string{"/remote", "off"})
	if !service.stopped {
		t.Fatal("remote service did not stop")
	}
}

func TestRemoteOwnerCommandsAreRejected(t *testing.T) {
	u := New(nil, nil, nil, "test", "model", ".")
	for _, line := range []string{"/exit", "/quit", "/remote off"} {
		if err := u.SubmitRemote("alice@example.com", line); err == nil {
			t.Fatalf("SubmitRemote(%q) succeeded", line)
		}
	}
}
