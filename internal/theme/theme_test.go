package theme

import (
	"strconv"
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
	want := []string{
		"default", "catppuccin-mocha", "dracula", "gruvbox-dark", "solarized-dark", "nord-dark",
		"tokyo-night", "one-dark", "rose-pine", "everforest-dark", "kanagawa-wave", "ayu-dark",
		"catppuccin-latte", "alucard", "gruvbox-light", "solarized-light", "nord-light",
		"tokyo-night-day", "one-light", "rose-pine-dawn", "everforest-light", "kanagawa-lotus", "ayu-light",
	}
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

func TestPresetsHaveUniqueNamesIDsAndColors(t *testing.T) {
	ids, names, colors := map[string]bool{}, map[string]bool{}, map[Palette]string{}
	for _, palette := range All() {
		id, name := strings.ToLower(strings.TrimSpace(palette.ID)), strings.ToLower(strings.TrimSpace(palette.Name))
		if id == "" || ids[id] {
			t.Errorf("empty or duplicate theme ID %q", palette.ID)
		}
		if name == "" || names[name] {
			t.Errorf("empty or duplicate theme name %q", palette.Name)
		}
		ids[id], names[name] = true, true
		if palette.ID == "default" {
			continue
		}
		if palette.Appearance != "Dark" && palette.Appearance != "Light" {
			t.Errorf("%s has invalid appearance %q", palette.ID, palette.Appearance)
		}
		for _, hex := range []string{palette.Background, palette.Text, palette.Prompt, palette.Markdown, palette.Status, palette.Diff, palette.Success, palette.Warning, palette.Error, palette.Accent, palette.Muted, palette.Link} {
			if _, ok := colorParams(38, hex); !ok {
				t.Errorf("%s has invalid color %q", palette.ID, hex)
			}
		}
		if palette.Background == palette.Text || palette.Success == palette.Error {
			t.Errorf("%s has indistinguishable text or diff colors", palette.ID)
		}
		signature := palette
		signature.ID, signature.Name, signature.Appearance = "", "", ""
		if earlier, ok := colors[signature]; ok {
			t.Errorf("%s duplicates the colors of %s", palette.ID, earlier)
		}
		colors[signature] = palette.ID
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

func TestPaintRowColorsTabGapsAndPadding(t *testing.T) {
	const width = 32
	for _, id := range []string{"dracula", "catppuccin-latte"} {
		palette, _ := Lookup(id)
		for _, row := range []string{
			"\t\tthinking",
			"\x1b[90mthinking\ttext\x1b[0m",
			"thinking\x1b[0m\t\t",
		} {
			cells := rowCellBackgrounds(PaintRow(row, width, palette), width)
			for column, color := range cells {
				if color != background(palette.Background) {
					t.Fatalf("%s row %q column %d has background %q, want theme background", id, row, column+1, color)
				}
			}
		}
	}
}

// Replay SGR colors and horizontal tabs. A terminal tab advances the cursor
// without painting the skipped cells, even when a background color is set.
func rowCellBackgrounds(row string, width int) []string {
	cells := make([]string, width)
	column, color := 0, ""
	for len(row) > 0 {
		if strings.HasPrefix(row, "\x1b[") {
			end := strings.IndexByte(row, 'm')
			parts := strings.Split(row[2:end], ";")
			for index := 0; index < len(parts); index++ {
				code, _ := strconv.Atoi(parts[index])
				switch {
				case code == 0 || code == 49:
					color = ""
				case (code == 38 || code == 48) && index+4 < len(parts) && parts[index+1] == "2":
					if code == 48 {
						color = "\x1b[" + strings.Join(parts[index:index+5], ";") + "m"
					}
					index += 4
				}
			}
			row = row[end+1:]
			continue
		}
		if row[0] == '\t' {
			column = min(width-1, (column/8+1)*8)
		} else {
			if column < width {
				cells[column] = color
			}
			column++
		}
		row = row[1:]
	}
	return cells
}
