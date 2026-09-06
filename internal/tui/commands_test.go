package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/term"

	"qcode/internal/session"
)

type resettableRunner struct {
	reset bool
}

func (*resettableRunner) Run(context.Context, string) error { return nil }
func (r *resettableRunner) ResetSession()                   { r.reset = true }

func TestMatchingSlashCommands(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{line: "", want: nil},
		{line: "/", want: []string{"/agent", "/clear", "/diff", "/exit", "/help", "/learn", "/model", "/new", "/skill", "/quit", "/tool", "/verbose"}},
		{line: "/d", want: []string{"/diff"}},
		{line: "/h", want: []string{"/help"}},
		{line: "/m", want: []string{"/model"}},
		{line: "/n", want: []string{"/new"}},
		{line: "/s", want: []string{"/skill"}},
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
		{duration: 1250 * time.Millisecond, want: "1s"},
		{duration: 42*time.Second + 800*time.Millisecond, want: "43s"},
		{duration: time.Minute + 2345*time.Millisecond, want: "1m2s"},
		{duration: time.Minute + 42*time.Second + 800*time.Millisecond, want: "1m43s"},
	} {
		if got := formatRunDuration(test.duration); got != test.want {
			t.Errorf("formatRunDuration(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestPrintSystemMessageHasBlankLinesAroundIt(t *testing.T) {
	var output bytes.Buffer
	u := UI{display: newHistoryWriter(&output)}
	u.printSystemMessage("Completed in 1.25s")

	if got, want := output.String(), "\nCompleted in 1.25s\n\n"; got != want {
		t.Fatalf("system message output = %q, want %q", got, want)
	}
}

func TestStartNewSessionResetsRunner(t *testing.T) {
	var output bytes.Buffer
	runner := &resettableRunner{}
	u := UI{
		runner:         runner,
		display:        newHistoryWriter(&output),
		responseWriter: NewMarkdownWriter(&output, false, 80),
	}

	u.startNewSession()

	if !runner.reset {
		t.Fatal("runner session was not reset")
	}
	if !strings.Contains(output.String(), "New session started; previous context cleared.") {
		t.Fatalf("output = %q, want reset confirmation", output.String())
	}
}

func TestHeaderLogoUsesASCIIBannerWithNarrowFallback(t *testing.T) {
	wide := strings.Join(headerLogo(42), "\n")
	if len(headerLogo(42)) != 7 || !strings.Contains(wide, "#") {
		t.Fatalf("wide logo = %q", wide)
	}
	for _, character := range wide {
		if character > 0x7f {
			t.Fatalf("banner contains non-ASCII character %q", character)
		}
	}
	if got := headerLogo(41); len(got) != 1 || got[0] != "qcode" {
		t.Fatalf("narrow logo = %v", got)
	}
}

func TestGradientLineSkipsDarkestPaletteShades(t *testing.T) {
	previous := bannerColor
	bannerColor = []int{10, 20, 30, 40, 50, 60}
	t.Cleanup(func() { bannerColor = previous })

	got := gradientLine("##")
	if !strings.Contains(got, "\x1b[38;5;50m#") || !strings.Contains(got, "\x1b[38;5;60m#") {
		t.Fatalf("gradient = %q, want the two lightest shades", got)
	}
}

func TestStatusBarPlainFallback(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 80, true, false)
	want := "[PROVIDER ollama] [MODEL qwen] [WORKSPACE ~/code]"
	if got != want {
		t.Fatalf("status bar = %q, want %q", got, want)
	}
}

func TestStatusBarUsesColoredSegments(t *testing.T) {
	got := statusBar("ollama", "qwen", "~/code", 80, true, true)
	for _, sequence := range []string{cyan, magenta, blue} {
		if !strings.Contains(got, sequence) {
			t.Fatalf("status bar %q does not contain color %q", got, sequence)
		}
	}
	for _, background := range []string{"\x1b[40m", "\x1b[41m", "\x1b[42m", "\x1b[43m", "\x1b[44m", "\x1b[45m", "\x1b[46m", "\x1b[47m"} {
		if strings.Contains(got, background) {
			t.Fatalf("status bar contains background color %q: %q", background, got)
		}
	}
	if !strings.HasSuffix(got, reset) {
		t.Fatalf("status bar does not restore terminal styling: %q", got)
	}
}

func TestStatusBarFitsTerminalWidth(t *testing.T) {
	got := statusBar("openai", "a-very-long-model-name", "~/a/very/long/workspace/path", 32, true, true)
	if visibleWidth(got) > 32 {
		t.Fatalf("status bar width = %d, want at most 32: %q", visibleWidth(got), got)
	}
	if !strings.Contains(got, "…") {
		t.Fatalf("truncated status bar has no ellipsis: %q", got)
	}
}

func TestTabBarShowsActiveAgentAndStatuses(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "review", Status: session.StatusRunning},
		{ID: "agent-2", Name: "tests", Status: session.StatusCompleted},
	}
	got := tabBar(summaries, "agent-1", nil, 80, false, false)
	if !strings.Contains(got, "[main -]") || !strings.Contains(got, "*[review >]") || !strings.Contains(got, "[tests +]") {
		t.Fatalf("tab bar = %q", got)
	}
	if visibleWidth(got) > 80 {
		t.Fatalf("tab bar width = %d", visibleWidth(got))
	}
	if !strings.HasSuffix(got, asciiFullTabSwitchHint) {
		t.Fatalf("tab bar hint = %q", got)
	}
}

func TestTabBarUsesCompactSwitchHintWhenNeeded(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "review", Status: session.StatusRunning},
		{ID: "agent-2", Name: "tests", Status: session.StatusCompleted},
	}
	got := tabBar(summaries, "agent-1", nil, 60, true, false)
	if !strings.HasSuffix(got, compactTabSwitchHint) {
		t.Fatalf("compact tab hint = %q", got)
	}
	if visibleWidth(got) != 60 {
		t.Fatalf("compact tab bar width = %d", visibleWidth(got))
	}
}

func TestTabBarKeepsActiveAgentOnNarrowScreen(t *testing.T) {
	summaries := []session.Summary{
		{ID: "main", Name: "main", Status: session.StatusIdle},
		{ID: "agent-1", Name: "first-long-agent", Status: session.StatusCompleted},
		{ID: "agent-2", Name: "active", Status: session.StatusRunning},
		{ID: "agent-3", Name: "third-long-agent", Status: session.StatusFailed},
	}
	got := tabBar(summaries, "agent-2", nil, 24, false, false)
	if !strings.Contains(got, "m") || !strings.Contains(got, "active") || !strings.Contains(got, ">") || visibleWidth(got) > 24 {
		t.Fatalf("narrow tab bar = %q (width %d)", got, visibleWidth(got))
	}
	if strings.Contains(got, "Switch") {
		t.Fatalf("narrow tab bar unexpectedly contains hint = %q", got)
	}
}

func TestAgentDisplayIsolatesBackgroundOutput(t *testing.T) {
	var terminalOutput bytes.Buffer
	terminal := term.NewTerminal(readWriter{Reader: strings.NewReader(""), Writer: &terminalOutput}, "> ")
	u := &UI{terminal: terminal, activeAgent: "main"}
	mainHistory := newHistoryWriter(io.Discard)
	workerHistory := newHistoryWriter(io.Discard)
	mainDisplay := &agentDisplay{ui: u, id: "main", history: mainHistory}
	workerDisplay := &agentDisplay{ui: u, id: "agent-1", history: workerHistory}
	_, _ = workerDisplay.Write([]byte("background\n"))
	_, _ = mainDisplay.Write([]byte("foreground\n"))
	if strings.Contains(terminalOutput.String(), "background") || !strings.Contains(terminalOutput.String(), "foreground") {
		t.Fatalf("terminal output = %q", terminalOutput.String())
	}
	if got := strings.Join(workerHistory.Lines(), "\n"); !strings.Contains(got, "background") {
		t.Fatalf("worker history = %q", got)
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
