package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"qcode/internal/agent"
	"qcode/internal/control"
	"qcode/internal/llm"
	"qcode/internal/session"
	"qcode/internal/trace"
)

type providerUIBackend struct {
	name     string
	models   []string
	levels   []string
	err      error
	onModels func()
}

func (p *providerUIBackend) Name() string { return p.name }
func (p *providerUIBackend) Models(context.Context) ([]string, error) {
	if p.onModels != nil {
		p.onModels()
	}
	return append([]string(nil), p.models...), p.err
}
func (p *providerUIBackend) Complete(context.Context, llm.Request, llm.StreamCallback) (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: "assistant", Content: "kept answer"}}, nil
}
func (p *providerUIBackend) ThinkingCapability(string) llm.ThinkingCapability {
	return llm.ThinkingCapability{Adjustable: len(p.levels) > 0, Levels: append([]string(nil), p.levels...)}
}
func (*providerUIBackend) ContextWindow(context.Context, string) (int, error) { return 16000, nil }

func newProviderUI(t *testing.T, keys string) (*UI, *control.Host, *runtimePreferenceRecorder) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.Close() })
	u := New(nil, out, nil, "ollama", "shared", t.TempDir())
	u.input = newInterruptReader(strings.NewReader(""))
	for _, key := range []byte(keys) {
		u.input.data <- key
	}
	close(u.input.data)
	h := control.NewHost(context.Background(), 5)
	u.SetAgentManager(h)
	create := func(id, name, model string, main bool, backend session.CreationBackend) (*agent.Agent, error) {
		p := &providerUIBackend{name: backend.Provider, models: []string{model}, levels: []string{"high"}}
		display, response := u.AddAgentView(id, p.Name(), model)
		a := agent.New(p, model, h.WrapToolset(id, &closeUITools{}, main), trace.New(display, false), response, 3)
		a.SetEndpoint(backend.Endpoint)
		return a, nil
	}
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		return create(id, name, model, main, session.CreationBackend{Provider: "ollama"})
	})
	h.SetProviderFactory(create)
	if _, err := h.CreateMain("shared"); err != nil {
		t.Fatal(err)
	}
	a, _ := h.Agent("main")
	if err := a.SetThinking("high"); err != nil {
		t.Fatal(err)
	}
	u.SetRunner(a)
	recorder := &runtimePreferenceRecorder{}
	u.SetRuntimePreferenceWriter(recorder)
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		return &providerUIBackend{name: name, models: []string{"shared"}}, "https://profile.test/v1", nil
	})
	t.Cleanup(u.shutdownAgentManager)
	return u, h, recorder
}
func providerState(t *testing.T, h *control.Host, id string) agent.SavedState {
	t.Helper()
	agents, _ := h.SaveAgents()
	for _, item := range agents {
		if item.Summary.ID == id {
			var state agent.SavedState
			if err := json.Unmarshal(item.State, &state); err != nil {
				t.Fatal(err)
			}
			return state
		}
	}
	t.Fatalf("missing state %s", id)
	return agent.SavedState{}
}
func providerOutput(u *UI, id string) string {
	return strings.Join(u.views[id].display.history.Lines(), "\n")
}

func TestOpenAIGeminiOpenAIRoundTripRestoresRememberedModelAndThinking(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	u.SetRememberedProviderModels(map[string]string{"gemini": "gemini-model"}, map[string]string{"gemini": "high"})
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		switch name {
		case "openai":
			return &providerUIBackend{name: name, models: []string{"shared"}, levels: []string{"high"}}, "https://openai-profile.test/v1", nil
		case "gemini":
			return &providerUIBackend{name: name, models: []string{"gemini-model"}, levels: []string{"high"}}, "https://gemini-profile.test/v1", nil
		default:
			return nil, "", errors.New("unexpected provider")
		}
	})

	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	openAIState := providerState(t, h, "main")
	if openAIState.Provider != "openai" || openAIState.Model != "shared" || openAIState.Thinking != "high" || openAIState.Endpoint != "https://openai-profile.test/v1" {
		t.Fatalf("initial OpenAI selection = %+v", openAIState)
	}

	u.handleProviderCommand(context.Background(), []string{"/provider", "gemini"})
	geminiState := providerState(t, h, "main")
	if geminiState.Provider != "gemini" || geminiState.Model != "gemini-model" || geminiState.Thinking != "high" || geminiState.Endpoint != "https://gemini-profile.test/v1" {
		t.Fatalf("Gemini selection did not restore its profile state: %+v", geminiState)
	}

	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	restoredOpenAI := providerState(t, h, "main")
	if restoredOpenAI.Provider != "openai" || restoredOpenAI.Model != "shared" || restoredOpenAI.Thinking != "high" || restoredOpenAI.Endpoint != "https://openai-profile.test/v1" {
		t.Fatalf("OpenAI state was not restored: %+v", restoredOpenAI)
	}
	if recorder.providerCalls != 3 || recorder.provider != "openai" || recorder.model != "shared" || recorder.thinking != "high" {
		t.Fatalf("persisted round-trip state = %+v", recorder)
	}
}

func TestProviderSwitchKeepsModelHistoryAndOtherTabs(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	child, err := h.Create("shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.SubmitAndWait(context.Background(), "main", "keep history"); err != nil {
		t.Fatal(err)
	}
	u.drafts[child.ID] = "unfinished draft"
	before := providerState(t, h, "main")
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	after := providerState(t, h, "main")
	if after.Provider != "openai" || after.Model != "shared" || after.Thinking != "" || after.ContextWindow != 16000 || after.Endpoint != "https://profile.test/v1" {
		t.Fatalf("selection: %+v", after)
	}
	if !reflect.DeepEqual(before.Messages, after.Messages) {
		t.Fatal("history changed")
	}
	if u.provider != "openai" || u.views["main"].provider != "openai" || recorder.providerCalls != 1 || recorder.provider != "openai" {
		t.Fatalf("presentation/default mismatch: %+v", recorder)
	}
	if providerState(t, h, child.ID).Provider != "ollama" || u.drafts[child.ID] != "unfinished draft" {
		t.Fatal("other tab changed")
	}
	if !strings.Contains(providerOutput(u, "main"), "History kept.") {
		t.Fatal("missing history warning")
	}
	if err := h.Reset("main"); err != nil {
		t.Fatal(err)
	}
	if providerState(t, h, "main").Provider != "openai" {
		t.Fatal("/new lost provider")
	}
}

func TestProviderSwitchWorkerIsLocalAndCreationUsesCreator(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	child, _ := h.Create("shared")
	if err := u.switchAgent(child.ID); err != nil {
		t.Fatal(err)
	}
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if recorder.providerCalls != 0 || providerState(t, h, "main").Provider != "ollama" {
		t.Fatal("worker changed default/main")
	}
	created, err := u.createAgentWithProvider(context.Background(), "shared", "")
	if err != nil {
		t.Fatal(err)
	}
	if providerState(t, h, created.ID).Provider != "openai" || u.views[created.ID].provider != "openai" {
		t.Fatal("new tab used startup backend")
	}
}

func TestProviderSelectionCancellationAndFailureAreTransactional(t *testing.T) {
	for _, tc := range []struct {
		name, keys string
		target     *providerUIBackend
		factoryErr error
	}{
		{name: "model Escape", keys: "\x1b", target: &providerUIBackend{name: "openai", models: []string{"different"}}},
		{name: "model Ctrl+C", keys: string(ctrlC), target: &providerUIBackend{name: "openai", models: []string{"different"}}},
		{name: "thinking cancel", keys: "\r" + string(ctrlC), target: &providerUIBackend{name: "openai", models: []string{"different"}, levels: []string{"low", "high"}}},
		{name: "empty catalog", target: &providerUIBackend{name: "openai"}},
		{name: "catalog failure", target: &providerUIBackend{name: "openai", err: errors.New("catalog unavailable")}},
		{name: "missing login", factoryErr: errors.New("run qcode setup")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, h, recorder := newProviderUI(t, tc.keys)
			before := providerState(t, h, "main")
			u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) { return tc.target, "", tc.factoryErr })
			u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
			after := providerState(t, h, "main")
			if !reflect.DeepEqual(before, after) || recorder.providerCalls != 0 || u.provider != "ollama" {
				t.Fatal("cancel/failure changed state")
			}
		})
	}
}

func TestProviderModelAndThinkingPicker(t *testing.T) {
	u, h, recorder := newProviderUI(t, "\r\r")
	u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) {
		return &providerUIBackend{name: "openai", models: []string{"different"}, levels: []string{"low", "high"}}, "", nil
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	state := providerState(t, h, "main")
	if state.Model != "different" || state.Thinking != "low" || recorder.model != "different" || recorder.thinking != "low" {
		t.Fatalf("picker did not commit together: %+v", state)
	}
}

func TestProviderSwitchRestoresRememberedModel(t *testing.T) {
	u, h, _ := newProviderUI(t, "\r")
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		switch name {
		case "openai":
			return &providerUIBackend{name: name, models: []string{"gpt-6-luna"}}, "", nil
		case "ollama":
			return &providerUIBackend{name: name, models: []string{"shared", "other"}}, "", nil
		default:
			return nil, "", errors.New("unknown provider")
		}
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if got := providerState(t, h, "main"); got.Provider != "openai" || got.Model != "gpt-6-luna" {
		t.Fatalf("switch to openai = %+v", got)
	}
	// Switching back must restore ollama's previous model without prompting,
	// even though the openai model is not in the ollama catalog.
	u.handleProviderCommand(context.Background(), []string{"/provider", "ollama"})
	if got := providerState(t, h, "main"); got.Provider != "ollama" || got.Model != "shared" {
		t.Fatalf("switch back did not restore remembered model: %+v", got)
	}
	if provider, thinking, ok := u.rememberedProviderModel("openai"); !ok || provider != "gpt-6-luna" || thinking != "" {
		t.Fatalf("openai remembered = %q %q %v", provider, thinking, ok)
	}
}

func TestProviderSwitchPrefersRememberedOverSharedModelName(t *testing.T) {
	u, h, _ := newProviderUI(t, "\r")
	// Both providers list "shared", but ollama previously used "other".
	// Remember "other" for ollama via a model change, then round-trip.
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		return &providerUIBackend{name: name, models: []string{"shared", "other"}}, "", nil
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if got := providerState(t, h, "main"); got.Model != "shared" {
		t.Fatalf("shared model not kept: %+v", got)
	}
	// Simulate /model other on openai, then switch back and forth.
	u.screenMu.Lock()
	u.model = "other"
	u.screenMu.Unlock()
	u.RememberProviderModel("openai", "other", "")
	u.handleProviderCommand(context.Background(), []string{"/provider", "ollama"})
	if got := providerState(t, h, "main"); got.Model != "shared" {
		t.Fatalf("ollama remembered not restored: %+v", got)
	}
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if got := providerState(t, h, "main"); got.Model != "other" {
		t.Fatalf("openai remembered not restored: %+v", got)
	}
}

func TestProviderRetainsSupportedThinkingAndReportsSaveFailure(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	recorder.modelErr = errors.New("disk full")
	u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) {
		return &providerUIBackend{name: "openai", models: []string{"shared"}, levels: []string{"high"}}, "", nil
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if state := providerState(t, h, "main"); state.Provider != "openai" || state.Thinking != "high" {
		t.Fatal("save failure reverted live selection")
	}
	if !strings.Contains(providerOutput(u, "main"), "unable to persist provider preference: disk full") {
		t.Fatal("save failure not reported")
	}
}

func TestProviderPickerRegistryHighlightCancellationAndSameProvider(t *testing.T) {
	for _, keys := range []string{"\x1b", string(ctrlC), "\r"} {
		t.Run(keys, func(t *testing.T) {
			u, h, recorder := newProviderUI(t, keys)
			calls := 0
			u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) { calls++; return nil, "", nil })
			u.handleProviderCommand(context.Background(), []string{"/provider"})
			if calls != 0 || providerState(t, h, "main").Provider != "ollama" {
				t.Fatal("picker did not highlight current or cancel")
			}
			want := 0
			if keys == "\r" {
				want = 1
			}
			if recorder.providerCalls != want {
				t.Fatal("same provider/cancel persistence mismatch")
			}
		})
	}
}

func TestProviderDisplayNamesAccepted(t *testing.T) {
	names := llm.Names()
	labels := providerPickerLabels(names)
	want := []string{"ChatGPT", "Gemini", "Ollama", "Ollama Cloud", "OpenAI", "OpenCode Go"}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("picker labels = %q, want %q", labels, want)
	}
	for input, wantID := range map[string]string{
		"OpenAI":       "openai",
		"OPENAI":       "openai",
		"openai":       "openai",
		"OpenCode Go":  "opencode-go",
		"opencode-go":  "opencode-go",
		"ChatGPT":      "chatgpt",
		"Ollama":       "ollama",
		"Ollama Cloud": "ollama-cloud",
	} {
		if got, ok := resolveProviderID(input, names); !ok || got != wantID {
			t.Fatalf("resolveProviderID(%q) = %q, %v; want %q", input, got, ok, wantID)
		}
	}

	u, h, _ := newProviderUI(t, "")
	u.handleProviderCommand(context.Background(), []string{"/provider", "OpenAI"})
	if got := providerState(t, h, "main"); got.Provider != "openai" {
		t.Fatalf("display name switch stored %q, want openai", got.Provider)
	}
	if got := providerOutput(u, "main"); !strings.Contains(got, "Provider: OpenAI") {
		t.Fatalf("switch message missing display name: %q", got)
	}

	u.handleProviderCommand(context.Background(), []string{"/provider", "OpenCode", "Go"})
	if got := providerState(t, h, "main"); got.Provider != "opencode-go" {
		t.Fatalf("spaced display name switch stored %q, want opencode-go", got.Provider)
	}
}

func TestProviderUnknownUsageAndUnavailableModes(t *testing.T) {
	u, _, recorder := newProviderUI(t, "")
	u.handleProviderCommand(context.Background(), []string{"/provider", "typo"})
	if got := providerOutput(u, "main"); !strings.Contains(got, "Unknown provider: typo") || !strings.Contains(got, strings.Join(providerPickerLabels(llm.Names()), ", ")) {
		t.Fatal(got)
	}
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai", "extra"})
	u.SetProviderFactory(nil)
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if recorder.providerCalls != 0 {
		t.Fatal("bad/unavailable command persisted")
	}
	u.printCommandHelp([]string{"provider"})
	if !strings.Contains(providerOutput(u, "main"), "/provider [name]") {
		t.Fatal("missing help")
	}
	if err := u.SubmitRemote("browser", "/provider\topenai"); err == nil {
		t.Fatal("remote provider command accepted")
	}
}

func TestProviderDiscoveryCannotOverwriteConcurrentWork(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) {
		return &providerUIBackend{name: "openai", models: []string{"shared"}, onModels: func() {
			if _, err := h.SubmitAndWait(context.Background(), "main", "remote work"); err != nil {
				t.Error(err)
			}
		}}, "", nil
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if providerState(t, h, "main").Provider != "ollama" || recorder.providerCalls != 0 {
		t.Fatal("stale selection overwrote completed remote work")
	}
}

func TestProviderSwitchConcurrentBrowserCatalog(t *testing.T) {
	u, _, _ := newProviderUI(t, "")
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				u.RemoteCatalog(context.Background())
			}
		}
	}()
	for _, name := range []string{"openai", "ollama", "openai"} {
		u.handleProviderCommand(context.Background(), []string{"/provider", name})
	}
	close(done)
	wg.Wait()
}

func TestMixedProviderPresentationResumesEachTab(t *testing.T) {
	u, h, _ := newProviderUI(t, "")
	child, err := h.Create("shared")
	if err != nil {
		t.Fatal(err)
	}
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if err := u.switchAgent(child.ID); err != nil {
		t.Fatal(err)
	}
	u.handleProviderCommand(context.Background(), []string{"/provider", "chatgpt"})
	u.drafts["main"] = "main draft"
	u.drafts[child.ID] = "worker draft"
	agents, next := h.SaveAgents()
	presentation, err := json.Marshal(u.snapshotPresentation())
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]agent.SavedState)
	for _, item := range agents {
		var state agent.SavedState
		if err := json.Unmarshal(item.State, &state); err != nil {
			t.Fatal(err)
		}
		states[item.Summary.ID] = state
	}
	restored := control.NewHost(context.Background(), 5)
	detached := New(nil, u.out, nil, "ollama", "launch-model", u.root)
	detached.SetDetachedAgentManager(restored)
	detached.SetProviderFactory(u.providerFactory)
	restored.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		state := states[id]
		p := &providerUIBackend{name: state.Provider, models: []string{model}}
		display, response := detached.AddAgentView(id, p.Name(), model)
		a := agent.New(p, model, restored.WrapToolset(id, &closeUITools{}, main), trace.New(display, false), response, 3)
		a.SetEndpoint(state.Endpoint)
		return a, nil
	})
	t.Cleanup(restored.Shutdown)
	if err := restored.RestoreAgents(agents, next); err != nil {
		t.Fatal(err)
	}
	if err := detached.RestorePresentation(presentation); err != nil {
		t.Fatal(err)
	}
	detached.activateRestoredTab()
	if detached.views["main"].provider != "openai" || detached.views[child.ID].provider != "chatgpt" || detached.provider != "chatgpt" {
		t.Fatal("resume reused launch provider")
	}
	if detached.drafts["main"] != "main draft" || detached.drafts[child.ID] != "worker draft" {
		t.Fatal("resume lost drafts")
	}
	if err := detached.SubmitRemote("browser", "/provider openai"); err == nil {
		t.Fatal("resume enabled browser provider switching")
	}
}

func TestProviderPickerLabelsMarkSetupFirst(t *testing.T) {
	names := []string{"chatgpt", "ollama", "openai"}
	labels, disabled := providerPickerLabelsWithAvailability(names, func(name string) bool {
		return name == "ollama"
	})
	want := []string{"ChatGPT" + providerNeedsSetupSuffix, "Ollama", "OpenAI" + providerNeedsSetupSuffix}
	if !reflect.DeepEqual(labels, want) {
		t.Fatalf("labels = %q, want %q", labels, want)
	}
	if len(disabled) != 2 || !disabled[labels[0]] || !disabled[labels[2]] || disabled[labels[1]] {
		t.Fatalf("disabled = %v", disabled)
	}
	if got, ok := resolveProviderID(labels[0], names); !ok || got != "chatgpt" {
		t.Fatalf("suffixed resolve = %q, %v", got, ok)
	}
}

func TestProviderTypedBlocksUnconfiguredWithoutFactory(t *testing.T) {
	u, h, recorder := newProviderUI(t, "")
	calls := 0
	u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) {
		calls++
		return &providerUIBackend{name: "openai", models: []string{"shared"}}, "", nil
	})
	u.SetProviderAvailabilityChecker(func(name string) (bool, string) {
		if name == "openai" {
			return false, "configure [providers.openai].api_key"
		}
		return true, ""
	})
	u.handleProviderCommand(context.Background(), []string{"/provider", "openai"})
	if calls != 0 {
		t.Fatal("unconfigured provider reached factory")
	}
	if providerState(t, h, "main").Provider != "ollama" || recorder.providerCalls != 0 {
		t.Fatal("unconfigured switch changed state")
	}
	if got := providerOutput(u, "main"); !strings.Contains(got, "needs setup first") || !strings.Contains(got, "configure [providers.openai].api_key") {
		t.Fatalf("missing setup guidance: %q", got)
	}
}

func TestProviderPickerSkipsUnconfigured(t *testing.T) {
	// Registry order is chatgpt, ollama, openai, opencode-go. Only ollama
	// (current) and opencode-go are configured; Down from ollama must skip
	// disabled openai and switch to opencode-go.
	u, h, _ := newProviderUI(t, arrowDownSequence+"\r")
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		return &providerUIBackend{name: name, models: []string{"shared"}}, "", nil
	})
	u.SetProviderAvailabilityChecker(func(name string) (bool, string) {
		if name == "ollama" || name == "opencode-go" {
			return true, ""
		}
		return false, "setup"
	})
	u.handleProviderCommand(context.Background(), []string{"/provider"})
	if got := providerState(t, h, "main").Provider; got != "opencode-go" {
		t.Fatalf("picker did not skip disabled: %+v", got)
	}
}

func TestAgentNewRefreshesContext(t *testing.T) {
	u, h, _ := newProviderUI(t, "")
	u.handleAgentCommand(context.Background(), []string{"/agent", "new", "shared"})
	if u.activeAgent == "main" {
		t.Fatal("agent new did not switch to child")
	}
	// The runner's context capacity should be refreshed for the new agent.
	childRunner, ok := h.Runner(u.activeAgent)
	if !ok {
		t.Fatalf("missing child runner %q", u.activeAgent)
	}
	type capRunner interface{ ContextCapacity() (int, bool) }
	capProvider, ok := childRunner.(capRunner)
	if !ok {
		t.Fatalf("child runner does not implement ContextCapacity")
	}
	if capacity, known := capProvider.ContextCapacity(); !known || capacity != 16000 {
		t.Fatalf("expected refreshed context capacity 16000, got capacity=%d, known=%v", capacity, known)
	}
}

func TestConfiguredInstancePickerLabels(t *testing.T) {
	u := &UI{}
	u.SetProviderInstances(map[string]string{"lab": "ollama"})
	names := u.availableProviderNames()
	if name, ok := u.resolveProviderID("lab (ollama)", names); !ok || name != "lab" {
		t.Fatal("instance label did not resolve")
	}
	labels, disabled := u.providerLabelsWithAvailability([]string{"lab"}, func(string) bool { return false })
	want := "lab (ollama)" + providerNeedsSetupSuffix
	if len(labels) != 1 || labels[0] != want || !disabled[want] {
		t.Fatalf("labels = %v, disabled = %v", labels, disabled)
	}
	if _, ok := u.resolveProviderID("unknown", names); ok {
		t.Fatal("unknown instance accepted")
	}
}

// The real adapter loads only the selected model's metadata before offering
// thinking settings. Accepting Model default must not turn it into an override.
func TestCloudProviderPickerLoadsSelectedMetadataAndKeepsDefault(t *testing.T) {
	for _, tc := range []struct {
		name, keys, wantProvider string
		wantSaves                int
	}{
		{"keep model default", "\r\r", "ollama-cloud", 1},
		{"cancel thinking picker", "\r\x03", "ollama", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, h, recorder := newProviderUI(t, tc.keys)
			var paths []string
			p, err := llm.New(llm.OllamaCloudKind, llm.Config{APIKey: "fixture-key", HTTP: cloudPickerHTTP(func(req *http.Request) (*http.Response, error) {
				paths = append(paths, req.URL.Path)
				var body string
				switch req.URL.Path {
				case "/api/tags":
					body = `{"models":[{"name":"Cloud:Model"},{"name":"other"}]}`
				case "/api/show":
					var selection map[string]string
					if err := json.NewDecoder(req.Body).Decode(&selection); err != nil || selection["model"] != "Cloud:Model" {
						t.Errorf("unexpected model metadata request: %v, %v", selection, err)
					}
					body = `{"context_length":32768,"thinking":{"values":[false,true],"default":true}}`
				default:
					t.Errorf("unexpected request %s", req.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			u.SetProviderFactory(func(context.Context, string) (llm.Provider, string, error) { return p, "https://ollama.com", nil })
			u.handleProviderCommand(context.Background(), []string{"/provider", "ollama-cloud"})
			state := providerState(t, h, "main")
			if !reflect.DeepEqual(paths, []string{"/api/tags", "/api/show"}) || state.Provider != tc.wantProvider || recorder.providerCalls != tc.wantSaves {
				t.Fatalf("wrong provider switch/cancel: requests=%v provider=%q saves=%d", paths, state.Provider, recorder.providerCalls)
			}
			if tc.wantSaves > 0 && (state.Model != "Cloud:Model" || state.Thinking != "" || state.ContextWindow != 32768) {
				t.Fatalf("wrong Cloud selection: %+v", state)
			}
		})
	}
}

type cloudPickerHTTP func(*http.Request) (*http.Response, error)

func (f cloudPickerHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudTerminalWorkerCancellationRollsBackCreation(t *testing.T) {
	u, host, _ := newProviderUI(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host.SetProviderFactory(func(id, _ string, model string, main bool, _ session.CreationBackend) (*agent.Agent, error) {
		client := cloudPickerHTTP(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path == "/api/show" {
				cancel()
				return nil, r.Context().Err()
			}
			body := `{"models":[{"name":"Cloud:Model"}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		p, err := llm.New(llm.OllamaCloudKind, llm.Config{APIKey: "fixture-key", HTTP: client})
		if err != nil {
			return nil, err
		}
		display, response := u.AddAgentView(id, p.Name(), model)
		child := agent.New(p, model, host.WrapToolset(id, &closeUITools{}, main), trace.New(display, false), response, 3)
		child.SetCreationRollback(func() { u.RemoveAgentView(id) })
		return child, nil
	})
	u.handleAgentCommand(ctx, []string{"/agent", "new", "Cloud:Model", "on"})
	if ctx.Err() == nil || len(host.List()) != 1 || len(u.views) != 1 || u.activeAgent != "main" {
		t.Fatalf("cancelled worker creation left state behind: agents=%v, views=%d, active=%q", host.List(), len(u.views), u.activeAgent)
	}
}
