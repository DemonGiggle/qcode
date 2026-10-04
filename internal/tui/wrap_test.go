package tui

import (
	"strings"
	"testing"
)

func TestUnicodeDisplayWidths(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"人類陀螺", 8},
		{"🛡️", 2},
		{"👩‍💻", 2},
		{"👍🏽", 2},
		{"🇹🇼", 2},
		{"1️⃣", 2},
		{"a\u0301", 1},
		{cyan + "甲🛡️乙" + reset, 6},
	} {
		if got := visibleWidth(tc.text); got != tc.want {
			t.Errorf("width of %q = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestWrapAndTruncateKeepUnicodeCharactersIntact(t *testing.T) {
	for _, cluster := range []string{"🛡️", "👩‍💻", "👍🏽", "🇹🇼", "1️⃣"} {
		text := "甲" + cluster + "乙"
		if got, want := wrapANSI(text, 4, ""), "甲"+cluster+"\n乙"; got != want {
			t.Errorf("wrap %q = %q, want %q", text, got, want)
		}
		if got, want := truncateDiffLine(text, 5, true), "甲"+cluster+"…"; got != want {
			t.Errorf("truncate %q = %q, want %q", text, got, want)
		}
	}
	text := "改好了，現在人類只管放招，走位全自動"
	if got := strings.ReplaceAll(wrapANSI(text, 9, ""), "\n", ""); got != text {
		t.Fatalf("wrapping lost Chinese text: %q", got)
	}
}

func TestWrapANSIUsesWordBoundaries(t *testing.T) {
	if got := wrapANSI("one two three", 7, ""); got != "one two\nthree" {
		t.Fatalf("wrapped text = %q", got)
	}
}

func TestWrapANSIHardWrapsLongWords(t *testing.T) {
	if got := wrapANSI("abcdefgh", 4, ""); got != "abcd\nefgh" {
		t.Fatalf("wrapped word = %q", got)
	}
}

func TestWrapANSIIgnoresStylesAndIndentsContinuations(t *testing.T) {
	input := cyan + "• " + reset + "one two three"
	want := cyan + "• " + reset + "one two\n  three"
	if got := wrapANSI(input, 9, "  "); got != want {
		t.Fatalf("wrapped styled text = %q, want %q", got, want)
	}
}

func TestWrapANSIDropsIndentThatWouldFillLine(t *testing.T) {
	if got := wrapANSI("one two", 3, "    "); got != "one\ntwo" {
		t.Fatalf("wrapped narrow text = %q", got)
	}
}
