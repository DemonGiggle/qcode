package tui

import (
	"context"
	"qcode/internal/config"
	"qcode/internal/llm"
	"strings"
	"testing"
)

func TestSelectAllowModelsSpaceCycle(t *testing.T) {
	models := []string{"gpt-5", "gpt-5-mini"}
	// Space on first row (smart), Down, Space x2 (balanced), Enter.
	input := strings.NewReader(" " + arrowDownSequence + "  " + "\r")
	var output testWriter
	selected, accepted, err := selectAllowModels(input, &output, models, nil, 5, 80, false, "openai")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if !accepted {
		t.Fatal("not accepted")
	}
	if selected[0] != allowRoleSmart {
		t.Fatalf("first row = %d, want smart", selected[0])
	}
	if selected[1] != allowRoleBalanced {
		t.Fatalf("second row = %d, want balanced", selected[1])
	}
}

func TestSelectAllowModelsFilter(t *testing.T) {
	models := []string{"gpt-5", "gpt-4o", "mini"}
	input := strings.NewReader("gpt-5\r")
	var output testWriter
	selected, accepted, err := selectAllowModels(input, &output, models, map[int]int{0: allowRoleSmart, 1: allowRoleCheap}, 5, 80, false, "openai")
	if err != nil || !accepted {
		t.Fatalf("select = %v, %v, %v", selected, accepted, err)
	}
	// Filter "gpt-5" matches only index 0; Enter should keep its smart role.
	if len(selected) != 2 {
		t.Fatalf("filter must preserve off-screen selection, got %+v", selected)
	}
	if selected[0] != allowRoleSmart {
		t.Fatalf("first row lost: %+v", selected)
	}
}

type testWriter struct{ buf strings.Builder }

func (w *testWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func TestCrossProviderAllowModelsPreservesOthersAndMissingModels(t *testing.T) {
	u, h, _ := newProviderUI(t, "\r")
	h.SetAllowedAgentModels([]config.AllowedAgentModel{{Provider: "ollama", Model: "keep", Role: "cheap"}, {Provider: "openai", Model: "missing", Role: "smart"}})
	u.chooseAllowModels(context.Background(), "openai")
	entries := h.AllowedAgentModels()
	if len(entries) != 2 {
		t.Fatalf("save dropped selections: %+v", entries)
	}
	if u.currentAllowProvider() != "ollama" {
		t.Fatal("curation switched active provider")
	}
	u.clearAllowModels("openai")
	entries = h.AllowedAgentModels()
	if len(entries) != 1 || entries[0].Provider != "ollama" {
		t.Fatalf("clear affected another provider: %+v", entries)
	}
	u.clearAllowModels("*")
	if len(h.AllowedAgentModels()) != 0 {
		t.Fatal("clear all failed")
	}
}

func TestCrossProviderAllowModelsCancelPreservesRoster(t *testing.T) {
	u, h, _ := newProviderUI(t, " \x1b")
	h.SetAllowedAgentModels([]config.AllowedAgentModel{{Provider: "openai", Model: "shared", Role: "cheap"}})
	u.chooseAllowModels(context.Background(), "openai")
	entries := h.AllowedAgentModels()
	if len(entries) != 1 || entries[0].Role != "cheap" {
		t.Fatalf("cancel changed roster: %+v", entries)
	}
}

func TestAllowProviderPickerOnlyFetchesChosenProvider(t *testing.T) {
	u, _, _ := newProviderUI(t, "\r\r\x1b")
	calls := 0
	u.SetProviderFactory(func(_ context.Context, name string) (llm.Provider, string, error) {
		calls++
		return &providerUIBackend{name: name, models: []string{"shared"}}, "", nil
	})
	u.chooseAllowProvider(context.Background())
	if calls != 1 {
		t.Fatalf("provider picker fetched %d catalogs", calls)
	}
	if u.currentAllowProvider() != "ollama" {
		t.Fatal("picker changed active backend")
	}
}
