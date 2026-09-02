package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMatchingSlashCommands(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{line: "", want: nil},
		{line: "/", want: []string{"/clear", "/exit", "/help", "/quit", "/verbose"}},
		{line: "/h", want: []string{"/help"}},
		{line: "/qu", want: []string{"/quit"}},
		{line: "/v", want: []string{"/verbose"}},
		{line: "/unknown", want: nil},
		{line: "/help now", want: nil},
	}
	for _, test := range tests {
		matches := matchingSlashCommands(test.line)
		got := make([]string, len(matches))
		for index, match := range matches {
			got[index] = match.name
		}
		if strings.Join(got, ",") != strings.Join(test.want, ",") {
			t.Errorf("matchingSlashCommands(%q) = %v, want %v", test.line, got, test.want)
		}
	}
}

func TestFormatRunDuration(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{duration: 400 * time.Microsecond, want: "<1ms"},
		{duration: 1250 * time.Millisecond, want: "1.25s"},
		{duration: time.Minute + 2345*time.Millisecond, want: "1m2.345s"},
	} {
		if got := formatRunDuration(test.duration); got != test.want {
			t.Errorf("formatRunDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestSlashCommandMenuReplacesPreviousRows(t *testing.T) {
	var output bytes.Buffer
	menu := slashCommandMenu{out: &output}
	menu.update(matchingSlashCommands("/"))
	output.Reset()
	menu.update(matchingSlashCommands("/h"))

	got := output.String()
	if strings.Count(got, "\x1b[1A\r\x1b[2K") != len(slashCommands) {
		t.Fatalf("menu did not clear all previous rows: %q", got)
	}
	if !strings.Contains(got, "/help    Show available commands") || strings.Contains(got, "/clear") {
		t.Fatalf("menu did not render filtered command: %q", got)
	}
}

func TestSlashCommandMenuDismissesSubmittedMenu(t *testing.T) {
	var menuOutput, terminalOutput bytes.Buffer
	menu := slashCommandMenu{out: &menuOutput, visible: 2}
	menu.dismiss(&terminalOutput)
	if got := terminalOutput.String(); got != "\x1b[3A\r\x1b[2M\x1b[1B\r" {
		t.Fatalf("dismiss output = %q", got)
	}
	if menu.visible != 0 {
		t.Fatalf("visible = %d", menu.visible)
	}
}

func TestSlashCommandCompletion(t *testing.T) {
	var output bytes.Buffer
	u := UI{commandMenu: slashCommandMenu{out: &output}}

	line, pos, ok := u.completeSlashCommand("/", 1, 'h')
	if !ok || line != "/h" || pos != 2 {
		t.Fatalf("typed completion = %q, %d, %v", line, pos, ok)
	}
	line, pos, ok = u.completeSlashCommand(line, pos, '\t')
	if !ok || line != "/help" || pos != len("/help") {
		t.Fatalf("tab completion = %q, %d, %v", line, pos, ok)
	}
}

func TestSlashCommandMenuCountsWrappedRows(t *testing.T) {
	var output bytes.Buffer
	menu := slashCommandMenu{out: &output, width: 24}
	menu.update([]slashCommand{{name: "/verbose", description: "Toggle detailed action traces"}})
	if menu.visible != 4 {
		t.Fatalf("visible rows = %d; output = %q", menu.visible, output.String())
	}
	for _, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
		if visibleWidth(line) > menu.width {
			t.Fatalf("line width = %d: %q", visibleWidth(line), line)
		}
	}
}
