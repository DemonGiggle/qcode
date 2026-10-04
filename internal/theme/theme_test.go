package theme

import (
	"strings"
	"testing"
)

func TestTransformANSIPreservesUnknownRGBChannels(t *testing.T) {
	for _, palette := range All() {
		for _, color := range []string{"\x1b[38;2;31;32;35m", "\x1b[48;2;42;48;36m"} {
			input := color + "text\x1b[0m"
			if got := TransformANSI(input, palette); got != input {
				t.Fatalf("%s reinterpreted RGB channels: %q", palette.ID, got)
			}
		}
	}
}

func TestPresetRoster(t *testing.T) {
	want := []string{"default", "catppuccin-mocha", "dracula", "gruvbox-dark", "solarized-dark", "nord-dark", "catppuccin-latte", "alucard", "gruvbox-light", "solarized-light", "nord-light"}
	got := All()
	if len(got) != len(want) {
		t.Fatalf("got %d palettes, want %d", len(got), len(want))
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("palette %d = %q, want %q", index, got[index].ID, id)
		}
		if _, ok := Lookup(id); !ok {
			t.Errorf("Lookup(%q) failed", id)
		}
	}
}

func TestDefaultLeavesTerminalColorsUntouched(t *testing.T) {
	input := "\x1b[1;31merror\x1b[0m\x1b[2J"
	if got := TransformANSI(input, Default()); got != input {
		t.Fatalf("default transformed %q into %q", input, got)
	}
}

func TestTransformANSIMapsRolesAndPreservesCursorCodes(t *testing.T) {
	dracula, _ := Lookup("dracula")
	input := "\x1b[2J\x1b[1;31merror\x1b[0m\x1b[32mok\x1b[0m"
	got := TransformANSI(input, dracula)
	for _, want := range []string{"\x1b[2J", "\x1b[1;38;2;255;85;85merror", "\x1b[38;2;80;250;123mok"} {
		if !strings.Contains(got, want) {
			t.Errorf("transformed output %q missing %q", got, want)
		}
	}
}

func TestTransformANSIRecolorsEarlierThemeRGB(t *testing.T) {
	mocha, _ := Lookup("catppuccin-mocha")
	dracula, _ := Lookup("dracula")
	oldPrompt := foreground(mocha.Prompt) + "prompt\x1b[0m"
	if got, want := TransformANSI(oldPrompt, dracula), foreground(dracula.Prompt)+"prompt\x1b[0m"; got != want {
		t.Fatalf("recolored prompt = %q, want %q", got, want)
	}
}

func TestPaintRowUsesThemeSurfaceAndFillsWidth(t *testing.T) {
	dracula, _ := Lookup("dracula")
	got := PaintRow("title", 8, dracula)
	if !strings.HasPrefix(got, "\x1b[48;2;40;42;54m\x1b[38;2;248;248;242m") {
		t.Fatalf("row prefix = %q", got)
	}
	if !strings.Contains(got, "title   \x1b[0m") {
		t.Fatalf("row was not padded and reset: %q", got)
	}
}

func TestPaintRowPadsChineseAndEmojiByTerminalCells(t *testing.T) {
	palette, _ := Lookup("gruvbox-dark")
	for _, text := range []string{"甲🛡️", "甲👩‍💻", "甲👍🏽", "甲🇹🇼", "甲1️⃣"} {
		if got := PaintRow(text, 8, palette); !strings.Contains(got, text+"    \x1b[0m") {
			t.Errorf("row padding for %q = %q, want four cells", text, got)
		}
	}
}
