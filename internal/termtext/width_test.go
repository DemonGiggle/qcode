package termtext

import (
	"strings"
	"testing"
)

func TestUnitsPreserveClustersAndANSI(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"\x1b[32m中文👩‍💻\x1b[0m", 6},
		{"\x1b]8;;https://example.test/中文\a甲🛡️\x1b]8;;\a", 4},
		{"\x1b]8;;https://example.test\x1b\\👍🏽\x1b]8;;\x1b\\", 2},
		{"\x1b[2J\x1b[1;1H🇹🇼", 2},
		{"\r\na\u0301", 1},
	} {
		var restored strings.Builder
		for _, unit := range Units(tc.text) {
			restored.WriteString(unit.Text)
		}
		if got := restored.String(); got != tc.text {
			t.Errorf("units changed %q into %q", tc.text, got)
		}
		if got := Width(tc.text); got != tc.want {
			t.Errorf("width of %q = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestUnitsTreatJoinedEmojiAsOneCharacter(t *testing.T) {
	for _, text := range []string{"👩‍💻", "👍🏽", "🇹🇼", "1️⃣", "a\u0301"} {
		units := Units(text)
		if len(units) != 1 || units[0].Text != text {
			t.Errorf("split visible character %q into %+v", text, units)
		}
	}
}
