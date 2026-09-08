package tui

import (
	"os"
	"strings"
	"testing"
)

func TestRawSelectorUsesOnlyOutputViewport(t *testing.T) {
	out, err := os.CreateTemp(t.TempDir(), "screen")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	u := &UI{fixedInput: true, out: out, width: 80, height: 24}
	u.beginRawSelector()
	data, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "\x1b[2;21r\x1b[2;1H") {
		t.Fatalf("selector region = %q", got)
	}
	if strings.Contains(got, "\x1b[22;1H") || strings.Contains(got, "\x1b[23;1H") || strings.Contains(got, "\x1b[24;1H") {
		t.Fatalf("selector touched footer rows: %q", got)
	}
}
