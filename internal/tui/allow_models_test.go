package tui

import (
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
