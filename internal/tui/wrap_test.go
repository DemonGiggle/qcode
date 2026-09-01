package tui

import "testing"

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
