package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qcode/internal/control"
	"qcode/internal/prompt"
	"qcode/internal/question"
	"qcode/internal/session"
)

type fakeRemoteService struct {
	running bool
	url     string
	command string
	ip      string
	stopped bool
}

type remoteCatalogRunner struct {
	selected []prompt.SkillSummary
	tools    map[string]bool
}

func (r *remoteCatalogRunner) Run(context.Context, string) error { return nil }
func (r *remoteCatalogRunner) ListModels(context.Context) ([]string, error) {
	return []string{"model-a", "model-b"}, nil
}
func (r *remoteCatalogRunner) SetModel(string)                      {}
func (r *remoteCatalogRunner) ToggleTool(name string, enabled bool) { r.tools[name] = enabled }
func (r *remoteCatalogRunner) ToolNames() []string                  { return []string{"read", "write"} }
func (r *remoteCatalogRunner) ToolEnabled(name string) bool         { return r.tools[name] }
func (r *remoteCatalogRunner) SelectedSkills() []prompt.SkillSummary {
	return append([]prompt.SkillSummary(nil), r.selected...)
}

func TestRemoteCatalogIncludesSelectorStateAndResumableSessions(t *testing.T) {
	root := t.TempDir()
	runner := &remoteCatalogRunner{
		selected: []prompt.SkillSummary{{Name: "review"}},
		tools:    map[string]bool{"read": true, "write": false},
	}
	u := New(nil, nil, runner, "test", "model-a", root)
	u.SetSkillCatalogLoader(func() ([]prompt.SkillSummary, error) {
		return []prompt.SkillSummary{
			{Name: "review", Description: "Review changes"},
			{Name: "deploy", Description: "Deploy safely"},
		}, nil
	})
	store, err := session.Open(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	current, currentLock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release(currentLock)
	u.persistence = &sessionPersistence{store: store, current: current}

	resumable, resumableLock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	resumable.Preview = "  Review \x1b[31mthe release\x1b[0m  "
	resumable.Saved = time.Now().UTC()
	resumable.Agents = []session.SavedAgent{{}}
	if err := store.Save(resumable); err != nil {
		t.Fatal(err)
	}
	session.Release(resumableLock)

	busy, busyLock, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release(busyLock)
	busy.Preview = "busy"
	busy.Saved = time.Now().UTC()
	if err := store.Save(busy); err != nil {
		t.Fatal(err)
	}

	catalog := u.RemoteCatalog(context.Background())
	if got, want := len(catalog.Models), 2; got != want || catalog.Models[0] != "model-a" {
		t.Fatalf("models = %#v, want two models", catalog.Models)
	}
	if len(catalog.Tools) != 2 || !catalog.Tools[0].Enabled || catalog.Tools[1].Enabled {
		t.Fatalf("tools = %#v", catalog.Tools)
	}
	if len(catalog.Skills) != 2 || !catalog.Skills[0].Selected || catalog.Skills[1].Selected {
		t.Fatalf("skills = %#v", catalog.Skills)
	}
	if len(catalog.Sessions) != 1 || catalog.Sessions[0].ID != resumable.ID || catalog.Sessions[0].Preview != "Review the release" {
		t.Fatalf("sessions = %#v, want only resumable saved session", catalog.Sessions)
	}
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
func (s *fakeRemoteService) ServeCommand() string        { return s.command }
func (s *fakeRemoteService) TailscaleIP() string         { return s.ip }

func TestRemoteCommandLifecycle(t *testing.T) {
	var output bytes.Buffer
	history := newHistoryWriter(&output)
	service := &fakeRemoteService{command: "tailscale serve --https=443 --set-path=/qcode/test http://127.0.0.1:1234", ip: "100.64.0.1"}
	u := &UI{display: history, remoteService: service}
	u.handleRemoteCommand(context.Background(), []string{"/remote"})
	if !service.running {
		t.Fatal("remote service did not start")
	}
	if !strings.Contains(output.String(), "Tailscale command: "+service.command) {
		t.Fatalf("remote command output = %q", output.String())
	}
	if !strings.Contains(output.String(), "Remote access requires a browser signed in to this Tailscale tailnet.") {
		t.Fatalf("remote access hint output = %q", output.String())
	}
	if !strings.Contains(output.String(), "DNS check: this hostname should resolve to Tailscale IP "+service.ip) {
		t.Fatalf("remote IP output = %q", output.String())
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
