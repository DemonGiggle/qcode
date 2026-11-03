package control

import (
	"context"
	"io"
	"testing"

	"qcode/internal/agent"
	"qcode/internal/trace"
)

func TestHostBackendAPIs(t *testing.T) {
	h := NewHost(context.Background(), 3)
	defer h.Shutdown()
	h.SetFactory(func(id, name, model string, main bool) (*agent.Agent, error) {
		return agent.New(&hostProvider{}, model, &hostTools{}, trace.New(io.Discard, false), io.Discard, 4), nil
	})
	if _, err := h.CreateMain("old"); err != nil {
		t.Fatal(err)
	}
	rev, err := h.ConfigurationRevision("main")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.SwitchBackend("main", rev, agent.BackendSwitch{Provider: &hostProvider{}, Model: "new"}); err != nil {
		t.Fatal(err)
	}
	next, _ := h.ConfigurationRevision("main")
	if next == rev {
		t.Fatal("revision unchanged")
	}
	var selected agent.CreationBackend
	h.SetProviderFactory(func(id, name, model string, main bool, backend agent.CreationBackend) (*agent.Agent, error) {
		selected = backend
		return agent.New(&hostProvider{}, model, &hostTools{}, trace.New(io.Discard, false), io.Discard, 4), nil
	})
	backend := agent.CreationBackend{Provider: "selected", Endpoint: "https://selected.example"}
	if _, err := h.CreateWithProvider("model", backend); err != nil {
		t.Fatal(err)
	}
	if selected != backend {
		t.Fatal(selected)
	}
}
