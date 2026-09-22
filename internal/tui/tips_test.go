package tui

import (
	"bytes"
	"strings"
	"testing"
)

func TestStartupTipStableWithinProcess(t *testing.T) {
	first := startupTip()
	second := startupTip()
	if first == "" {
		t.Fatal("startup tip is empty")
	}
	if first != second {
		t.Fatalf("startup tip changed within process: %q vs %q", first, second)
	}
}

func TestAllTipsFitOneOrTwoLines(t *testing.T) {
	for _, tip := range tipTexts {
		line := "Tip: " + tip
		wrapped := wrapANSI(line, 80, "  ")
		lines := strings.Split(wrapped, "\n")
		if len(lines) < 1 || len(lines) > 2 {
			t.Fatalf("tip renders as %d lines at width 80: %q", len(lines), tip)
		}
		for _, l := range lines {
			if visibleWidth(l) > 80 {
				t.Fatalf("tip line exceeds width 80: %q", l)
			}
		}
	}
}

func TestFormatTipColorRespectsFlag(t *testing.T) {
	colored := formatTip(80, true)
	if !strings.Contains(colored, tipVivid) || !strings.Contains(colored, reset) || !strings.Contains(colored, "Tip: ") {
		t.Fatalf("colored tip missing vivid styling: %q", colored)
	}
	plain := formatTip(80, false)
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("plain tip should not contain ANSI codes: %q", plain)
	}
	if !strings.Contains(plain, "Tip: ") {
		t.Fatalf("plain tip missing prefix: %q", plain)
	}
}

func TestFormatTipStaysWithinTwoLinesOnNarrowTerminals(t *testing.T) {
	for _, width := range []int{40, 30, 20} {
		got := formatTip(width, false)
		lines := strings.Split(got, "\n")
		if len(lines) > 2 {
			t.Fatalf("width %d produced %d lines: %q", width, len(lines), got)
		}
		for _, l := range lines {
			if visibleWidth(l) > width {
				t.Fatalf("width %d line exceeds width: %q", width, l)
			}
		}
	}
}

func TestPrintHeaderShowsTipBelowToolSummary(t *testing.T) {
	var out bytes.Buffer
	u := &UI{runner: statusRunner{}, display: newHistoryWriter(&out), width: 80, statusActive: true}
	u.printHeader()
	got := out.String()
	enabled := strings.Index(got, "Tools enabled:")
	disabled := strings.Index(got, "Tools disabled:")
	tip := strings.Index(got, "Tip: ")
	if enabled < 0 || disabled < 0 || tip < 0 {
		t.Fatalf("header missing tools/tip block: %q", got)
	}
	if !(enabled < disabled && disabled < tip) {
		t.Fatalf("tip is not below tools block: %q", got)
	}
}

func TestPrintTipWritesTipLine(t *testing.T) {
	var out bytes.Buffer
	u := &UI{runner: statusRunner{}, display: newHistoryWriter(&out), width: 80}
	u.printTip()
	got := out.String()
	if !strings.Contains(got, "Tip: ") {
		t.Fatalf("printTip output missing tip: %q", got)
	}
	// printTip with nil out must fall back to plain text without panicking.
	if strings.Contains(got, "\x1b") {
		t.Fatalf("nil-out tip should be plain text: %q", got)
	}
}
