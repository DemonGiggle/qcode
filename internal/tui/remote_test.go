package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/question"
	"qcode/internal/session"
)

type fakeRemoteService struct {
	running bool
	url     string
	stopped bool
	issues  int
}

type remoteCatalogRunner struct {
	selected []prompt.SkillSummary
	tools    map[string]bool
	thinking map[string]llm.ThinkingCapability
	level    string
}

func (r *remoteCatalogRunner) Run(context.Context, string) error { return nil }
func (r *remoteCatalogRunner) ListModels(context.Context) ([]string, error) {
	return []string{"model-a", "model-b"}, nil
}
func (r *remoteCatalogRunner) SetModel(string)                      {}
func (r *remoteCatalogRunner) ToggleTool(name string, enabled bool) { r.tools[name] = enabled }
func (r *remoteCatalogRunner) ToolNames() []string                  { return []string{"read", "write"} }
func (r *remoteCatalogRunner) ToolEnabled(name string) bool         { return r.tools[name] }
func (r *remoteCatalogRunner) ThinkingCapability() llm.ThinkingCapability {
	return r.thinking["model-a"]
}
func (r *remoteCatalogRunner) ThinkingCapabilityFor(model string) llm.ThinkingCapability {
	return r.thinking[model]
}
func (r *remoteCatalogRunner) SetThinking(level string) error { r.level = level; return nil }
func (r *remoteCatalogRunner) ThinkingLevel() string          { return r.level }
func (r *remoteCatalogRunner) SelectedSkills() []prompt.SkillSummary {
	return append([]prompt.SkillSummary(nil), r.selected...)
}

func TestRemoteCatalogIncludesSelectorStateAndResumableSessions(t *testing.T) {
	root := t.TempDir()
	runner := &remoteCatalogRunner{
		selected: []prompt.SkillSummary{{Name: "review"}},
		tools:    map[string]bool{"read": true, "write": false},
		thinking: map[string]llm.ThinkingCapability{
			"model-a": {Adjustable: true, Levels: []string{"low", "high"}},
		},
		level: "low",
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
	if got := catalog.Thinking["model-a"]; len(got.Levels) != 2 || got.Levels[0] != "low" || got.Current != "low" {
		t.Fatalf("thinking = %#v, want model-a levels and current level", catalog.Thinking)
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

func (s *fakeRemoteService) Start(_ context.Context, mode RemoteMode) (RemoteStatus, error) {
	s.running = true
	if mode == RemoteModePureWeb {
		s.url = "http://192.168.1.10:1234"
	} else {
		s.url = "https://host.tailnet.ts.net/qcode/test"
	}
	return s.Status(), nil
}
func (s *fakeRemoteService) Stop() error { s.running, s.stopped = false, true; return nil }
func (s *fakeRemoteService) IssueLogin(context.Context) (RemoteLogin, error) {
	s.issues++
	return RemoteLogin{URL: s.url + "#login=test-secret", ExpiresAt: time.Now().Add(3 * time.Minute)}, nil
}
func (s *fakeRemoteService) LoginState() string { return "available" }
func (s *fakeRemoteService) Status() RemoteStatus {
	return RemoteStatus{Running: s.running, Mode: RemoteModeTailscale, URL: s.url, Connections: 2}
}
func TestRemoteActiveScreenShowsConnectionsAndSecondaryClose(t *testing.T) {
	var output bytes.Buffer
	status := RemoteStatus{Running: true, Mode: RemoteModePureWeb, URL: "http://192.168.1.10:1234", Connections: 2}
	rows := renderRemoteActiveMenu(&output, status, remoteKeepOpen, []string{"Remote login (click or scan; single use)"}, 120, false)
	if rows == 0 || !strings.Contains(output.String(), "Connections: 2 active browser sessions") {
		t.Fatalf("remote screen = %q", output.String())
	}
	if !strings.Contains(output.String(), "> Keep connection open") || !strings.Contains(output.String(), "  Close Connection") {
		t.Fatalf("remote close action is not secondary: %q", output.String())
	}
	if !strings.Contains(output.String(), "unencrypted") {
		t.Fatalf("Pure Web warning missing: %q", output.String())
	}
}

func TestRemoteModeMenuDefaultsToPureWeb(t *testing.T) {
	var output bytes.Buffer
	renderRemoteModeMenu(&output, []RemoteMode{RemoteModePureWeb, RemoteModePureWebOpen, RemoteModeTailscale}, 0, 120, false)
	got := output.String()
	if !strings.Contains(got, "> Pure Web") || !strings.Contains(got, "Pure Web (No auth, danger!)") || !strings.Contains(got, "  Tailscale") {
		t.Fatalf("remote mode menu = %q", got)
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

func TestRemoteConnectionLogsOnlyInVerboseMode(t *testing.T) {
	var output bytes.Buffer
	u := &UI{display: newHistoryWriter(&output), verbose: true}
	u.RemoteConnection("alice@example.com", true)
	u.RemoteConnection("alice@example.com", false)
	u.RemoteRequestRejected("GET", "/api/v1/events", "missing Tailscale-User-Login")
	if got := output.String(); !strings.Contains(got, "Remote connect: alice@example.com") || !strings.Contains(got, "Remote disconnect: alice@example.com") {
		t.Fatalf("verbose remote connection log = %q", got)
	}
	if !strings.Contains(output.String(), "Remote reject: missing Tailscale-User-Login (GET /api/v1/events)") {
		t.Fatalf("verbose remote rejection log = %q", output.String())
	}

	output.Reset()
	u.verbose = false
	u.RemoteConnection("alice@example.com", true)
	u.RemoteConnection("alice@example.com", false)
	u.RemoteRequestRejected("GET", "/api/v1/events", "missing Tailscale-User-Login")
	if output.Len() != 0 {
		t.Fatalf("non-verbose remote connection log = %q", output.String())
	}
}
