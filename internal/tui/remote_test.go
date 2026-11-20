package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qcode/internal/agent"
	"qcode/internal/control"
	"qcode/internal/lineedit"
	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/question"
	"qcode/internal/session"
	"qcode/internal/tools"
	"qcode/internal/trace"
)

type fakeRemoteService struct {
	running bool
	url     string
	stopped bool
	issues  int
}

type recordingRemoteService struct {
	fakeRemoteService
	mode    RemoteMode
	address string
}

func (s *recordingRemoteService) Start(_ context.Context, mode RemoteMode, address string) (RemoteStatus, error) {
	s.mode, s.address = mode, address
	return RemoteStatus{}, errors.New("recorded start")
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
	current, err := store.New()
	if err != nil {
		t.Fatal(err)
	}

	u.persistence = &sessionPersistence{store: store, current: current}
	current.Preview = "Current work"
	current.Saved = time.Now().UTC().Add(-time.Hour)
	if err := store.Save(current); err != nil {
		t.Fatal(err)
	}

	resumable, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	resumable.Preview = "  Review \x1b[31mthe release\x1b[0m  "
	resumable.Saved = time.Now().UTC()
	resumable.Agents = []session.SavedAgent{{}}
	if err := store.Save(resumable); err != nil {
		t.Fatal(err)
	}

	concurrent, err := store.New()
	if err != nil {
		t.Fatal(err)
	}

	concurrent.Preview = "open in another process"
	concurrent.Saved = time.Now().UTC()
	if err := store.Save(concurrent); err != nil {
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
	if len(catalog.Sessions) != 3 || catalog.Sessions[0].ID != concurrent.ID || catalog.Sessions[1].ID != resumable.ID || catalog.Sessions[2].ID != current.ID {
		t.Fatalf("sessions = %#v, want every saved session in order", catalog.Sessions)
	}
	if catalog.Sessions[0].Busy || catalog.Sessions[1].Preview != "Review the release" || !catalog.Sessions[2].Current || catalog.Sessions[2].Busy {
		t.Fatalf("session labels = %#v", catalog.Sessions)
	}
	if !catalog.Sessions[2].Recency.Equal(current.Saved) {
		t.Fatalf("current recency = %v, want %v", catalog.Sessions[2].Recency, current.Saved)
	}
	current.Preview = ""
	if err := store.Save(current); err != nil {
		t.Fatal(err)
	}
	catalog = u.RemoteCatalog(context.Background())
	if catalog.Sessions[2].Preview != "<<CURRENT>>" || !catalog.Sessions[2].Current {
		t.Fatalf("current untitled session = %+v", catalog.Sessions[2])
	}
}

func TestRemotePresentationIncludesQueuedPromptsPerAgent(t *testing.T) {
	u, _ := layoutFixture(t)
	manager := u.manager.(*layoutManager)
	manager.queued = map[string][]session.QueuedPrompt{
		"main":    {{RequestID: "request-2", Prompt: "first\nsecond line"}},
		"agent-1": {{RequestID: "request-3", Prompt: "other tab"}},
	}
	u.views["main"] = &agentView{id: "main", display: u.display.(*agentDisplay)}
	u.views["agent-1"] = &agentView{id: "agent-1", display: &agentDisplay{ui: u, id: "agent-1", history: newHistoryWriter(io.Discard)}}
	presentation := u.RemotePresentation()
	if len(presentation.Views) != 2 || presentation.Views[0].ID != "agent-1" || presentation.Views[0].QueuedPrompts[0].Prompt != "other tab" || presentation.Views[1].QueuedPrompts[0].Prompt != "first\nsecond line" {
		t.Fatalf("remote queue snapshot = %+v", presentation.Views)
	}
	encoded, err := json.Marshal(presentation)
	if err != nil || !strings.Contains(string(encoded), `"queued_prompts"`) {
		t.Fatalf("remote queue JSON = %s, %v", encoded, err)
	}
}

func TestRemoteCanWinQuestionInteraction(t *testing.T) {
	host := control.NewHost(context.Background(), 1)
	defer host.Shutdown()
	u := New(nil, nil, nil, "test", "model", ".")
	u.SetAgentManager(host)
	result := make(chan []string, 1)
	go func() {
		answers, _ := u.AgentQuestioner("main")(context.Background(), []question.Question{
			{Text: "Database?", Options: []string{"SQLite", "Postgres"}},
			{Text: "Why?"},
		})
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
	if err := u.ResolveRemoteInteraction("alice@example.com", interaction.ID, []byte(`["Postgres","  "]`)); err == nil {
		t.Fatal("blank remote answer was accepted")
	}
	value, _ := json.Marshal([]string{"Postgres", "  Existing schema  "})
	if err := u.ResolveRemoteInteraction("alice@example.com", interaction.ID, value); err != nil {
		t.Fatal(err)
	}
	select {
	case answers := <-result:
		if len(answers) != 2 || answers[0] != "Postgres" || answers[1] != "Existing schema" {
			t.Fatalf("answers = %v", answers)
		}
	case <-time.After(time.Second):
		t.Fatal("questioner did not receive remote answer")
	}
}

func (s *fakeRemoteService) Networks() ([]RemoteNetwork, error) {
	return []RemoteNetwork{{Name: "wifi", Address: "192.168.1.10", Subnet: "192.168.1.0/24"}}, nil
}
func (s *fakeRemoteService) Start(_ context.Context, mode RemoteMode, _ string) (RemoteStatus, error) {
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
func TestRemoteActiveScreenShowsConnectionsAndAcceptAction(t *testing.T) {
	var output bytes.Buffer
	status := RemoteStatus{Running: true, Mode: RemoteModePureWeb, URL: "http://192.168.1.10:1234", Connections: 2}
	rows := renderRemoteActiveMenu(&output, status, remoteAccept, []string{"Remote login (click or scan; single use)"}, "", "", 120, false)
	if rows == 0 || !strings.Contains(output.String(), "Connections: 2 active browser sessions") {
		t.Fatalf("remote screen = %q", output.String())
	}
	if !strings.Contains(output.String(), "> Accept") || !strings.Contains(output.String(), "  Save QR as PNG") || !strings.Contains(output.String(), "  Close Connection") {
		t.Fatalf("remote close action is not secondary: %q", output.String())
	}
	if !strings.Contains(output.String(), "unencrypted") {
		t.Fatalf("Pure Web warning missing: %q", output.String())
	}
}

func TestRemoteActiveScreenSavesQRFromMenu(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	input := newInterruptReader(strings.NewReader(arrowDownSequence + "\r\x1b"))
	input.setRaw(true)
	input.start()
	var screen bytes.Buffer
	terminal := lineedit.NewTerminal(readWriter{Reader: input, Writer: &screen}, inputPrompt)
	u := &UI{input: input, terminal: terminal, width: 80, height: 24}
	u.showRemoteLogin(RemoteLogin{URL: "https://host/#login=secret", ExpiresAt: time.Now().Add(time.Minute)})
	defer u.clearRemoteLogin()
	if err := u.showRemoteActive(RemoteStatus{Running: true, Mode: RemoteModeTailscale, URL: "https://host/"}); err != nil {
		t.Fatal(err)
	}
	shown := strings.ReplaceAll(screen.String(), "\r\n", "")
	if u.remoteQRFile == "" || !strings.Contains(shown, "QR PNG:") || !strings.Contains(shown, filepath.Base(u.remoteQRFile)) {
		t.Fatalf("save action did not show QR PNG path: %q", screen.String())
	}
	if _, err := os.Stat(u.remoteQRFile); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCloseConfirmationDistinguishesBackAndCancel(t *testing.T) {
	testConfirmation := func(t *testing.T, inputData string) remoteCloseResult {
		t.Helper()
		input := newInterruptReader(strings.NewReader(inputData))
		input.setRaw(true)
		input.start()
		t.Cleanup(func() { input.setRaw(false) })
		var screen bytes.Buffer
		terminal := lineedit.NewTerminal(readWriter{Reader: input, Writer: &screen}, inputPrompt)
		out, err := os.CreateTemp(t.TempDir(), "remote-close")
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		u := &UI{input: input, terminal: terminal, out: out, width: 80}
		result, err := u.confirmRemoteClose(RemoteStatus{Connections: 2})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	if got := testConfirmation(t, "\x1b"); got != remoteCloseBack {
		t.Fatalf("Escape result = %v, want back", got)
	}
	if got := testConfirmation(t, string([]byte{ctrlC})); got != remoteCloseCancelled {
		t.Fatalf("Ctrl+C result = %v, want cancelled", got)
	}
}

func TestRemoteActiveScreenKeepsWrappedLoginURLClickable(t *testing.T) {
	var output bytes.Buffer
	status := RemoteStatus{Running: true, Mode: RemoteModeTailscale, URL: "https://host.tailnet.ts.net/qcode/test"}
	loginURL := status.URL + "#login=" + strings.Repeat("a", 43)
	loginRows := []string{
		"Remote login (click or scan; single use)",
		"Valid for 3 minutes; expires at 12:34:56",
		loginURL[:60],
		loginURL[60:],
	}
	renderRemoteActiveMenu(&output, status, remoteAccept, loginRows, loginURL, "", 80, false)
	linkStart := "\x1b]8;;" + loginURL + "\x1b\\"
	if got := strings.Count(output.String(), linkStart); got != 3 {
		t.Fatalf("complete login target appears %d times, want heading and 2 URL rows in %q", got, output.String())
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

func TestRemoteCommandStartsSelectedMode(t *testing.T) {
	for _, test := range []struct {
		name, keys, address string
		mode                RemoteMode
	}{
		{name: "pure web", keys: "\r", mode: RemoteModePureWeb, address: "192.168.1.10"},
		{name: "pure web open", keys: arrowDownSequence + "\r", mode: RemoteModePureWebOpen, address: "192.168.1.10"},
		{name: "tailscale", keys: arrowDownSequence + arrowDownSequence + "\r", mode: RemoteModeTailscale},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := newInterruptReader(strings.NewReader(test.keys))
			input.setRaw(true)
			input.start()
			var screen, messages bytes.Buffer
			terminal := lineedit.NewTerminal(readWriter{Reader: input, Writer: &screen}, inputPrompt)
			out, err := os.CreateTemp(t.TempDir(), "remote-command")
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			service := &recordingRemoteService{}
			u := &UI{input: input, terminal: terminal, out: out, display: newHistoryWriter(&messages), remoteService: service, width: 80}

			u.handleRemoteCommand(context.Background(), []string{"/remote"})

			if service.mode != test.mode || service.address != test.address {
				t.Fatalf("Start(mode, address) = (%q, %q), want (%q, %q)", service.mode, service.address, test.mode, test.address)
			}
		})
	}
}

func TestRemoteNetworkMenuShowsInterfacesAndSubnets(t *testing.T) {
	var output bytes.Buffer
	renderRemoteNetworkMenu(&output, []RemoteNetwork{
		{Name: "wifi", Address: "192.168.1.10", Subnet: "192.168.1.0/24"},
		{Name: "ethernet", Address: "10.0.0.5", Subnet: "10.0.0.0/24"},
	}, 0, 120, false)
	got := output.String()
	if !strings.Contains(got, "> wifi — 192.168.1.10 (subnet 192.168.1.0/24)") || !strings.Contains(got, "ethernet — 10.0.0.5 (subnet 10.0.0.0/24)") {
		t.Fatalf("network menu = %q", got)
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

func TestRemoteCloudThinkingDiscoversSelectedModelAndValidatesOnColdCache(t *testing.T) {
	ctx := context.Background()
	u, host, _ := newProviderUI(t, "")
	var lookups []string
	client := cloudPickerHTTP(func(r *http.Request) (*http.Response, error) {
		body := `{"models":[{"name":"Cloud:One"},{"name":"Cloud:Two"}]}`
		if r.URL.Path == "/api/show" {
			var request struct{ Model string }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				return nil, err
			}
			lookups = append(lookups, request.Model)
			body = `{"context_length":32768,"thinking":{"values":[false,true],"default":true}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	p, err := llm.New(llm.OllamaCloudKind, llm.Config{APIKey: "fixture-key", HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := host.ConfigurationRevision("main")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.SwitchBackend("main", revision, session.BackendSwitch{Provider: p, Model: "Cloud:One"}); err != nil {
		t.Fatal(err)
	}
	catalog, err := u.RemoteAgentCatalog(ctx, "main")
	if err != nil || !catalog.DeferredThinking || len(catalog.Thinking) != 0 || len(lookups) != 0 {
		t.Fatalf("initial catalog = %+v, lookups = %v, error = %v", catalog, lookups, err)
	}
	catalog, err = u.RemoteAgentCatalogForModel(ctx, "main", "Cloud:Two")
	if err != nil || strings.Join(catalog.Thinking["Cloud:Two"].Levels, ",") != "off,on" || strings.Join(lookups, ",") != "Cloud:Two" {
		t.Fatalf("selected catalog = %+v, lookups = %v, error = %v", catalog, lookups, err)
	}
	if err := u.ConfigureRemoteAgent(ctx, "main", RemoteAgentSettings{Model: "Cloud:One", Thinking: "on"}); err != nil {
		t.Fatal(err)
	}
	owner, _ := host.Agent("main")
	if owner.ThinkingLevel() != "on" || strings.Join(lookups, ",") != "Cloud:Two,Cloud:One" {
		t.Fatalf("thinking = %q, lookups = %v", owner.ThinkingLevel(), lookups)
	}
}

func TestRemoteAgentSettingsTargetBrowserTab(t *testing.T) {
	ctx := context.Background()
	u, host, _ := newProviderUI(t, "")
	provider := &providerUIBackend{name: "worker", models: []string{"helper", "faster"}, levels: []string{"high"}}
	host.SetFactory(func(id, _ string, model string, main bool) (*agent.Agent, error) {
		registry, err := tools.NewWithOptions(t.TempDir(), tools.Options{})
		if err != nil {
			return nil, err
		}
		display, response := u.AddAgentView(id, provider.Name(), model)
		return agent.New(provider, model, host.WrapToolset(id, registry, main), trace.New(display, false), response, 3), nil
	})
	worker, err := host.Create("helper")
	if err != nil {
		t.Fatal(err)
	}
	u.rememberDraft("keep typing this in the terminal")
	u.SetSkillCatalog([]prompt.SkillSummary{{Name: "review"}}, nil)
	u.SetSkillBodyLoader(func([]string) (map[string]string, error) {
		return map[string]string{"review": "review contents"}, nil
	})
	var selected, always []string
	u.SetAgentSkillHandler(worker.ID, func(names, alwaysNames []string) {
		selected, always = names, alwaysNames
	})

	catalog, err := u.RemoteAgentCatalog(ctx, worker.ID)
	if err != nil || !strings.Contains(strings.Join(catalog.Models, ","), "faster") {
		t.Fatalf("worker catalog = %+v, %v", catalog, err)
	}
	if _, err := u.RemoteAgentCatalog(ctx, "unknown"); err == nil {
		t.Fatal("unknown agent catalog succeeded")
	}
	for _, change := range []RemoteAgentSettings{
		{Model: "faster", Thinking: "high"},
		{Tools: map[string]bool{"read": false}},
		{Skills: []string{"review!"}},
	} {
		if err := u.ConfigureRemoteAgent(ctx, worker.ID, change); err != nil {
			t.Fatalf("apply %+v: %v", change, err)
		}
	}
	workerRunner, _ := host.Agent(worker.ID)
	workerSummary, err := host.Summary(worker.ID)
	if err != nil || workerSummary.Model != "faster" || workerRunner.ThinkingLevel() != "high" || workerRunner.ToolEnabled("read") {
		t.Fatalf("worker = %+v, thinking=%q, read=%v, err=%v", workerSummary, workerRunner.ThinkingLevel(), workerRunner.ToolEnabled("read"), err)
	}
	if strings.Join(selected, ",") != "review" || strings.Join(always, ",") != "review" || !workerRunner.SelectedSkills()[0].AlwaysLoad {
		t.Fatalf("worker skills = %v (always: %v)", selected, always)
	}
	main, _ := host.Agent("main")
	if state := providerState(t, host, "main"); state.Model != "shared" || state.Thinking != "high" || len(main.SelectedSkills()) != 0 {
		t.Fatalf("main settings changed: %+v", state)
	}
	if u.activeAgent != "main" || u.drafts["main"] != "keep typing this in the terminal" || len(u.input.injected) != 0 {
		t.Fatal("remote settings affected the terminal selection or draft")
	}

	if err := u.ConfigureRemoteAgent(ctx, worker.ID, RemoteAgentSettings{Tools: map[string]bool{"read": true, "unknown": true}}); err == nil || workerRunner.ToolEnabled("read") {
		t.Fatal("invalid tool batch was partially applied")
	}
	if err := u.ConfigureRemoteAgent(ctx, worker.ID, RemoteAgentSettings{Tools: map[string]bool{}}); !errors.Is(err, ErrInvalidRemoteAgentSettings) {
		t.Fatalf("empty tool patch error = %v", err)
	}
	if err := u.ConfigureRemoteAgent(ctx, worker.ID, RemoteAgentSettings{Skills: []string{}}); err != nil || len(workerRunner.SelectedSkills()) != 0 {
		t.Fatalf("clearing skills = %v, %v", workerRunner.SelectedSkills(), err)
	}

	provider.onModels = func() { _ = host.Close(worker.ID) }
	if err := u.ConfigureRemoteAgent(ctx, worker.ID, RemoteAgentSettings{Model: "helper"}); err == nil {
		t.Fatal("model change succeeded after target closed during discovery")
	}
}

func TestRemoteCatalogReportsMissingRunner(t *testing.T) {
	u, _ := layoutFixture(t)
	if _, err := u.RemoteAgentCatalog(context.Background(), "main"); !errors.Is(err, session.ErrUnknownAgent) {
		t.Fatalf("missing runner error = %v", err)
	}
	if got := u.remoteAgentStatusBar("main", "provider", "model", "/workspace"); got != "Agent unavailable" {
		t.Fatalf("status for a missing runner = %q", got)
	}
}

type remoteContextLookupProvider struct {
	providerUIBackend
	started chan struct{}
	release chan struct{}
}

func (p *remoteContextLookupProvider) ContextWindow(ctx context.Context, _ string) (int, error) {
	close(p.started)
	select {
	case <-p.release:
		return 32000, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func TestRemoteModelDiscoversContextOutsideCommitLocks(t *testing.T) {
	ctx := context.Background()
	u, host, _ := newProviderUI(t, "")
	u.persistence = &sessionPersistence{requests: make(chan struct{}, 1)}
	provider := &remoteContextLookupProvider{
		providerUIBackend: providerUIBackend{name: "worker", models: []string{"worker", "replacement"}},
		started:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	host.SetFactory(func(id, _ string, model string, main bool) (*agent.Agent, error) {
		display, response := u.AddAgentView(id, provider.Name(), model)
		return agent.New(provider, model, host.WrapToolset(id, &closeUITools{}, main), trace.New(display, false), response, 3), nil
	})
	worker, err := host.Create("worker")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- u.ConfigureRemoteAgent(ctx, worker.ID, RemoteAgentSettings{Model: "replacement"}) }()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider context discovery did not start")
	}
	persistenceUnlocked := u.persistence.mu.TryLock()
	if persistenceUnlocked {
		u.persistence.mu.Unlock()
	}
	summary := make(chan error, 1)
	go func() {
		_, err := host.Summary(worker.ID)
		summary <- err
	}()
	var lookupErr error
	select {
	case lookupErr = <-summary:
	case <-time.After(time.Second):
		lookupErr = errors.New("provider discovery blocked access to the agent manager")
	}
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !persistenceUnlocked || lookupErr != nil {
		t.Fatalf("provider discovery held commit locks: persistence unlocked=%t, summary error=%v", persistenceUnlocked, lookupErr)
	}
	if state := providerState(t, host, worker.ID); state.Model != "replacement" || state.ContextWindow != 32000 {
		t.Fatalf("model=%s, context window=%d", state.Model, state.ContextWindow)
	}
}
