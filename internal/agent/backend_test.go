package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/prompt"
	"qcode/internal/trace"
)

type switchProvider struct {
	managerProvider
	identity int
}

func (*switchProvider) Name() string { return "switched" }
func (*switchProvider) ThinkingCapability(string) llm.ThinkingCapability {
	return llm.ThinkingCapability{Adjustable: true, Levels: []string{"high"}}
}

func TestSwitchBackendPreservesStateAndInvalidatesContext(t *testing.T) {
	m := newTestManager(t, 4)
	a, _ := m.Agent("main")
	if _, err := m.SubmitAndWait(context.Background(), "main", "hello"); err != nil {
		t.Fatal(err)
	}
	a.SetSkillsWithContents([]prompt.SkillSummary{{Name: "skill"}}, map[string]string{"skill": "body"})
	a.SetPlanMode(true)
	a.contextWindow, a.contextOverride = 4000, 9000
	a.contextUsage, a.contextMessages = &llm.Usage{InputTokens: 100}, len(a.messages)
	a.sessionUsage = llm.SessionUsage{InputTokens: 123, TotalTokens: 123}
	before := a.ContextSnapshot()
	tools := a.tools
	work := append([]byte(nil), mustJSON(t, m.work)...)
	rev, _ := m.ConfigurationRevision("main")
	next := BackendSwitch{Provider: &switchProvider{}, Model: a.model, Endpoint: "https://new.example", Thinking: "high", ContextCapacity: 32000}
	if err := m.SwitchBackend("main", rev, next); err != nil {
		t.Fatal(err)
	}
	current, _ := m.Agent("main")
	if current != a || a.tools != tools || !reflect.DeepEqual(before, a.ContextSnapshot()) || !a.PlanMode() || len(a.SelectedSkills()) != 1 || a.AlwaysContents()["skill"] != "body" {
		t.Fatal("switch replaced agent, history, tools, modes or skills")
	}
	if !reflect.DeepEqual(work, mustJSON(t, m.work)) || a.SessionUsage().TotalTokens != 123 {
		t.Fatal("work or cumulative usage changed")
	}
	if a.contextWindow != 32000 || a.contextOverride != 0 || a.contextUsage != nil || a.contextMessages != 0 {
		t.Fatal("old context accounting retained")
	}
	var saved SavedState
	if err := json.Unmarshal(*a.checkpoint.Load(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "switched" || saved.Endpoint != next.Endpoint || saved.Thinking != "high" {
		t.Fatalf("bad checkpoint: %+v", saved)
	}
	for len(m.events) > 0 {
		<-m.events
	}
	rev, _ = m.ConfigurationRevision("main")
	next.ContextCapacity = 0
	if err := m.SwitchBackend("main", rev, next); err != nil {
		t.Fatal(err)
	}
	if _, known := a.ContextCapacity(); known {
		t.Fatal("same-model switch retained capacity")
	}
	select {
	case event := <-m.Events():
		if event.Agent.Model != next.Model {
			t.Fatal(event)
		}
		if !event.DirectoryChanged {
			t.Fatal("backend switch must refresh without reporting completion")
		}
	default:
		t.Fatal("no change event")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSwitchBackendRejectsStaleAndInvalidThinking(t *testing.T) {
	for _, setting := range []string{"model", "thinking", "endpoint", "summary"} {
		t.Run(setting, func(t *testing.T) {
			m := newTestManager(t, 3)
			a, _ := m.Agent("main")
			rev, _ := m.ConfigurationRevision("main")
			if err := m.Configure("main", func() error {
				switch setting {
				case "model":
					a.SetModel("remote")
				case "thinking":
					return a.SetThinking("")
				case "endpoint":
					a.SetEndpoint("remote")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if setting == "summary" {
				if err := m.UpdateModel("main", "remote"); err != nil {
					t.Fatal(err)
				}
			}
			before := append([]byte(nil), (*a.checkpoint.Load())...)
			if err := m.SwitchBackend("main", rev, BackendSwitch{Provider: &switchProvider{}, Model: "new"}); !errors.Is(err, ErrStaleConfiguration) {
				t.Fatalf("got %v", err)
			}
			rev, _ = m.ConfigurationRevision("main")
			if err := m.SwitchBackend("main", rev, BackendSwitch{Provider: &switchProvider{}, Model: "new", Thinking: "invalid"}); err == nil {
				t.Fatal("accepted invalid thinking")
			}
			if !reflect.DeepEqual(before, []byte(*a.checkpoint.Load())) {
				t.Fatal("rejected switch mutated state")
			}
		})
	}
}

func TestSwitchBackendDiscoveryRaces(t *testing.T) {
	for _, action := range []string{"start", "close", "approval", "queue"} {
		t.Run(action, func(t *testing.T) {
			m := newTestManager(t, 3)
			child, err := m.Create("worker")
			if err != nil {
				t.Fatal(err)
			}
			rev, _ := m.ConfigurationRevision(child.ID)
			discovering, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				close(discovering)
				<-release // External network preparation is deliberately blocked.
				result <- m.SwitchBackend(child.ID, rev, BackendSwitch{Provider: &switchProvider{}, Model: "new"})
			}()
			<-discovering
			if action == "close" {
				err = m.Close(child.ID)
			} else {
				err = m.Start(child.ID, "block")
				if action == "approval" {
					m.SetWaitingForApproval(child.ID, true)
				}
				if action == "queue" {
					_, err = m.Submit(child.ID, "queued")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-result; err == nil {
				t.Fatal("switch accepted after concurrent start/close")
			}
		})
	}
}

func TestProviderAwareCreationAndInheritance(t *testing.T) {
	m := newTestManager(t, 5)
	parent, _ := m.Agent("main")
	parent.SetSkills([]prompt.SkillSummary{{Name: "inherited"}})
	parent.SetMaxSteps(11)
	rev, _ := m.ConfigurationRevision("main")
	if err := m.SwitchBackend("main", rev, BackendSwitch{Provider: &switchProvider{}, Model: "parent-model", Endpoint: "https://parent.example"}); err != nil {
		t.Fatal(err)
	}
	var requests []CreationBackend
	m.SetProviderFactory(func(id, name, model string, main bool, backend CreationBackend) (*Agent, error) {
		requests = append(requests, backend)
		a := New(&switchProvider{identity: len(requests)}, model, m.WrapToolset(id, &managerToolset{}, main), trace.New(io.Discard, false), io.Discard, 4)
		a.SetEndpoint(backend.Endpoint)
		return a, nil
	})
	inherited, err := m.CreateInheritedAt(context.Background(), "main", "", "")
	if err != nil {
		t.Fatal(err)
	}
	child, _ := m.Agent(inherited.ID)
	if requests[0] != (CreationBackend{Provider: "switched", Endpoint: "https://parent.example"}) || inherited.Model != "parent-model" {
		t.Fatal(requests, inherited)
	}
	if child.provider == parent.provider || child.MaxSteps() != 11 || len(child.SelectedSkills()) != 1 {
		t.Fatal("provider identity or capabilities not inherited correctly")
	}
	explicit, err := m.CreateWithProvider("chosen", CreationBackend{Provider: "other", Endpoint: "https://other.example"})
	if err != nil {
		t.Fatal(err)
	}
	another, _ := m.Agent(explicit.ID)
	if requests[1].Provider != "other" || another.provider == child.provider {
		t.Fatal("explicit backend or new provider identity lost")
	}
}
