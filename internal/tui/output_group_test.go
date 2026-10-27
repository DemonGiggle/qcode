package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"qcode/internal/agent"
	"qcode/internal/llm"
	"qcode/internal/trace"
	"qcode/internal/transcript"
)

func groupedTestUI() (*UI, *agentDisplay, *bytes.Buffer) {
	var output bytes.Buffer
	u := &UI{width: 80, unicode: true, activeAgent: "main", views: map[string]*agentView{}}
	d := &agentDisplay{ui: u, id: "main", history: newHistoryWriter(&output)}
	u.display = d
	u.responseWriter = NewMarkdownWriter(d, true, u.width)
	u.views["main"] = &agentView{id: "main", display: d, response: u.responseWriter}
	return u, d, &output
}

func plainGroupLines(d *agentDisplay) []string {
	lines := d.Lines()
	for i := range lines {
		lines[i] = plainHistoryText(lines[i])
	}
	return lines
}

func TestOutputGroupTransitions(t *testing.T) {
	kinds := []string{"thinking", "answer", "tool", "notice"}
	for _, first := range kinds {
		for _, second := range kinds {
			t.Run(first+"/"+second, func(t *testing.T) {
				u, d, _ := groupedTestUI()
				emit := func(kind, text string) {
					w := u.responseWriter
					switch kind {
					case "thinking", "answer":
						w.BeginResponse()
						if kind == "thinking" {
							w.BeginThinking()
						}
						_, _ = io.WriteString(w, text)
						if kind == "thinking" {
							w.EndThinking()
						}
						w.EndResponse()
					case "tool":
						logger := trace.NewAnimated(d, false)
						logger.SetVerbose(false)
						logger.StartActivity(trace.Activity{Action: "read", Completed: text}).End(nil)
					case "notice":
						u.printSystemMessage(text)
					}
				}
				emit(first, "first")
				emit(second, "second")
				lines := plainGroupLines(d)
				if len(lines) != 3 || !strings.Contains(lines[0], "first") || lines[1] != "" || !strings.Contains(lines[2], "second") {
					t.Fatalf("transition output = %#v", lines)
				}
			})
		}
	}
}

func TestToolGroupKeepsTracePreviewAndDiffAdjacent(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		for _, result := range []struct {
			name, output string
			err          error
		}{
			{name: "success", output: "first\nsecond"},
			{name: "empty"},
			{name: "error", output: "partial", err: errors.New("failed")},
			{name: "cancelled", output: "partial", err: context.Canceled},
		} {
			t.Run(fmt.Sprintf("verbose=%v/%s", verbose, result.name), func(t *testing.T) {
				u, d, _ := groupedTestUI()
				logger := trace.NewAnimated(d, false)
				logger.SetVerbose(verbose)
				u.responseWriter.EnableDiffs()
				u.printSystemMessage("before")
				activity := logger.StartActivity(trace.Activity{Action: "edit", Start: "Editing x", Completed: "Edited x"})
				span := logger.Start("tool", "edit", nil)
				span.End(result.err)
				activity.EndWithOutput(result.err, result.output)
				u.responseWriter.WriteDiff("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new")
				lines := plainGroupLines(d)
				if lines[0] != "before" || lines[1] != "" {
					t.Fatalf("missing tool boundary: %#v", lines)
				}
				for _, line := range lines[2:] {
					if line == "" {
						t.Fatalf("padding inside tool group: %#v", lines)
					}
				}
				joined := strings.Join(lines, "\n")
				if !strings.Contains(joined, "• Edited x (+1 -1)") || (verbose && !strings.Contains(joined, "start tool edit")) {
					t.Fatalf("tool group lost output: %s", joined)
				}
				if result.output != "" && !strings.Contains(joined, "  "+strings.Split(result.output, "\n")[0]) {
					t.Fatal("missing output preview")
				}
				before := len(lines)
				if _, _, ok := u.responseWriter.WriteStoredDiff(0); !ok {
					t.Fatal("stored diff unavailable")
				}
				lines = plainGroupLines(d)
				if lines[before] != "" || !strings.Contains(lines[before+1], "expanded") {
					t.Fatalf("expanded diff did not start its own group: %#v", lines[before:])
				}
			})
		}
	}
}

func TestOutputGroupsReuseBlankRowsAndClosePartialLines(t *testing.T) {
	for _, previous := range []string{"first", "first\n", "first\n\n"} {
		for _, next := range []string{"second\n", "\nsecond\n"} {
			t.Run(fmt.Sprintf("%q/%q", previous, next), func(t *testing.T) {
				_, d, output := groupedTestUI()
				fmt.Fprint(d, previous)
				fmt.Fprint(transcript.NewGroup(d), next)
				if got := output.String(); got != "first\n\nsecond\n" {
					t.Fatalf("output = %q", got)
				}
			})
		}
	}
}

func TestEmptyStreamsAndSpinnerRedrawsDoNotAddSeparators(t *testing.T) {
	for _, mode := range []struct {
		styled bool
		width  int
	}{{true, 80}, {false, 80}, {false, 0}} {
		t.Run(fmt.Sprintf("%v/%d", mode.styled, mode.width), func(t *testing.T) {
			u, d, output := groupedTestUI()
			u.responseWriter = NewMarkdownWriter(d, mode.styled, mode.width)
			u.printSystemMessage("before")
			original := output.String()
			w := u.responseWriter
			w.BeginResponse()
			w.BeginThinking()
			fmt.Fprint(w, "\n \t\n")
			w.EndThinking()
			w.EndResponse()
			if got := output.String(); got != original {
				t.Fatalf("empty stream emitted %q", got[len(original):])
			}
			logger := trace.NewAnimated(d, false)
			logger.SetVerbose(false)
			task := logger.BeginTask()
			task.Resume()
			if got := plainGroupLines(d); !reflect.DeepEqual(got, []string{"before"}) {
				t.Fatalf("spinner committed lines: %#v", got)
			}
			u.printSystemMessage("during spinner")
			task.End()
			if got := plainGroupLines(d); !reflect.DeepEqual(got, []string{"before", "", "during spinner"}) {
				t.Fatalf("notice archived the spinner: %#v", got)
			}
		})
	}
}

func TestFullWidthProgressRedrawDoesNotShiftTranscript(t *testing.T) {
	for _, width := range []int{20, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, _ := layoutFixture(t)
			u.width = width
			d := u.display.(*agentDisplay)
			for i := 0; i < 30; i++ {
				fmt.Fprintf(d, "transcript %d\n", i)
			}
			group := d.NewOutputGroup()
			prefix := strings.Repeat("x", width-1)
			fmt.Fprintf(group, "\r%s|", prefix)
			previous := append([]string(nil), u.inputScreenRows...)
			before, err := u.out.Stat()
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(group, "\r%s/", prefix)
			data, err := os.ReadFile(u.out.Name())
			if err != nil {
				t.Fatal(err)
			}
			update := string(data[before.Size():])
			if strings.Contains(update, "transcript") || strings.Contains(update, "\x1b[K") {
				t.Fatalf("spinner redraw repainted transcript or cleared a row: %q", update)
			}
			changed := 0
			for row := range previous {
				if previous[row] != u.inputScreenRows[row] {
					changed++
				}
			}
			if changed != 1 || !strings.Contains(update, "/") {
				t.Fatalf("spinner redraw changed %d rows: %q", changed, update)
			}
		})
	}
}

func TestActivitySpinnerSharesTheCompletedGroupBoundary(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprint(verbose), func(t *testing.T) {
			u, d, _ := groupedTestUI()
			u.printSystemMessage("before")
			logger := trace.NewAnimated(d, false)
			logger.SetVerbose(verbose)
			activity := logger.StartActivity(trace.Activity{Action: "read", Start: "Reading x", Completed: "Read x"})
			span := logger.Start("tool", "read", map[string]any{"arguments": `{"path":"x"}`})
			snapshot := d.Snapshot()
			if !reflect.DeepEqual(plainGroupLines(d), []string{"before", ""}) || !strings.Contains(snapshot.lines[len(snapshot.lines)-1].text, "Reading x") && !verbose {
				t.Fatalf("progress has no group boundary: %#v", snapshot)
			}
			if verbose && !strings.Contains(snapshot.lines[len(snapshot.lines)-1].text, `arguments={"path":"x"}`) {
				t.Fatalf("progress lost trace fields: %#v", snapshot)
			}
			span.End(nil)
			activity.EndWithOutput(nil, "output")
			lines := plainGroupLines(d)
			for _, line := range lines[2:] {
				if line == "" || strings.Contains(line, "Reading x") {
					t.Fatalf("completion added padding or retained the spinner: %#v", lines)
				}
			}
			if lines[len(lines)-1] != "  output" {
				t.Fatalf("preview not adjacent: %#v", lines)
			}
		})
	}
}

func TestInteractiveJSONTraceKeepsEventRowsUnchanged(t *testing.T) {
	_, d, _ := groupedTestUI()
	logger := trace.NewAnimated(d, true)
	activity := logger.StartActivity(trace.Activity{Action: "read", Start: "Reading x", Completed: "Read x"})
	span := logger.Start("tool", "read", nil)
	span.End(nil)
	activity.EndWithOutput(nil, "output")
	logger.SeparateActivity()
	lines := d.Lines()
	if len(lines) != 4 {
		t.Fatalf("JSON event rows = %#v", lines)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("spacing changed JSON event: %q", line)
		}
	}
}

func TestGroupedMarkdownPreservesFormattingAcrossChunks(t *testing.T) {
	text := "# Heading\n\nParagraph with **bold** and 想法 wrapping across lines.\n\n```go\n  a := 1\n\n  b := 2\n```\n\n| A | B |\n| --- | --- |\n| x | y |\n"
	for _, styled := range []bool{false, true} {
		var baseline bytes.Buffer
		plain := NewMarkdownWriter(&baseline, styled, 32)
		u, d, output := groupedTestUI()
		grouped := NewMarkdownWriter(d, styled, 32)
		for _, writer := range []*MarkdownWriter{plain, grouped} {
			writer.BeginResponse()
			for start := 0; start < len(text); start += 3 {
				fmt.Fprint(writer, text[start:min(start+3, len(text))])
			}
			writer.EndResponse()
		}
		if output.String() != baseline.String() {
			t.Fatalf("grouping changed formatting, styled=%v:\n%q\nwant %q", styled, output.String(), baseline.String())
		}
		u.printSystemMessage("after")
		lines := plainGroupLines(d)
		if lines[len(lines)-2] != "" || lines[len(lines)-1] != "after" {
			t.Fatalf("missing boundary after Markdown table: %#v", lines)
		}
	}
}

type groupedShellRunner struct {
	output string
	err    error
}

func (r groupedShellRunner) Run(context.Context, string) error { return nil }
func (r groupedShellRunner) RunShell(context.Context, string) (string, error) {
	return r.output, r.err
}

func TestBashOutputIsOneGroup(t *testing.T) {
	for _, result := range []struct {
		name, output, feedback string
		err                    error
	}{
		{name: "success", output: "  first\n\nlast\n"},
		{name: "empty", feedback: "Command completed with no output."},
		{name: "error", output: "partial\n", err: errors.New("failed"), feedback: "Shell command failed: failed"},
		{name: "empty error", err: errors.New("failed"), feedback: "Shell command failed: failed"},
		{name: "cancelled", output: "partial", err: context.Canceled, feedback: "Cancelled"},
		{name: "empty cancelled", err: context.Canceled, feedback: "Cancelled"},
	} {
		t.Run(result.name, func(t *testing.T) {
			u, d, _ := groupedTestUI()
			u.runner = groupedShellRunner{output: result.output, err: result.err}
			u.printSystemMessage("before")
			u.runBashCommand(context.Background(), "echo example")
			u.printSystemMessage("after")
			want := []string{"before", "", "Running shell command (Ctrl+C to cancel): echo example"}
			if result.output != "" {
				want = append(want, "Shell output:")
				want = append(want, strings.Split(strings.TrimSuffix(result.output, "\n"), "\n")...)
			}
			if result.feedback != "" {
				want = append(want, result.feedback)
			}
			want = append(want, "", "after")
			if got := plainGroupLines(d); !reflect.DeepEqual(got, want) {
				t.Fatalf("shell group = %#v, want %#v", got, want)
			}
		})
	}
}

func TestCommandHelpAndPromptsUseSeparateGroups(t *testing.T) {
	u, d, _ := groupedTestUI()
	u.printSystemMessage("before")
	d.AddLine(reset + "> /help")
	u.printCommandHelp(nil)
	u.printSystemMessage("after")
	lines := plainGroupLines(d)
	if !reflect.DeepEqual(lines[:4], []string{"before", "", "> /help", ""}) {
		t.Fatalf("prompt boundaries = %#v", lines[:4])
	}
	for _, line := range lines[4 : len(lines)-2] {
		if line == "" {
			t.Fatalf("help contains a group separator: %#v", lines)
		}
	}
	if lines[len(lines)-2] != "" || lines[len(lines)-1] != "after" {
		t.Fatalf("help boundary = %#v", lines)
	}
}

func TestGroupSpacingSurvivesRestoreTabsPagingAndExports(t *testing.T) {
	u, _ := persistenceUI(t)
	main := u.views["main"].display
	helper := u.views["agent-1"].display
	// Previously saved padding is retained exactly.
	fmt.Fprint(main, "old one\nold two\n\n\n")
	fmt.Fprint(helper, "helper partial")
	data, err := json.Marshal(u.snapshotPresentation())
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := persistenceUI(t)
	if err := restored.RestorePresentation(data); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"main", "agent-1"} {
		d := restored.views[id].display
		if !reflect.DeepEqual(d.Snapshot(), u.views[id].display.Snapshot()) {
			t.Fatal("restore changed prior history")
		}
		fmt.Fprintln(transcript.NewGroup(d), "next "+id)
	}
	main = restored.views["main"].display
	helper = restored.views["agent-1"].display
	if got := plainGroupLines(main); !reflect.DeepEqual(got, []string{"old one", "old two", "", "", "next main"}) {
		t.Fatalf("legacy spacing was changed: %#v", got)
	}
	if got := plainGroupLines(helper); !reflect.DeepEqual(got, []string{"helper partial", "", "next agent-1"}) {
		t.Fatalf("tab boundary did not close partial line: %#v", got)
	}
	presentation := restored.RemotePresentation()
	for _, view := range presentation.Views {
		d := restored.views[view.ID].display
		want := append(d.Lines(), "") // Snapshot includes the unfinished row.
		if !reflect.DeepEqual(view.Lines, want) {
			t.Fatalf("browser history differs: %#v, want %#v", view.Lines, want)
		}
		rows := historyRows(d.Snapshot(), 80)
		var viewport viewport
		page := viewport.page(rows, 3, -1)
		if len(page) != 3 {
			t.Fatalf("history paging lost rows: %#v", page)
		}
		if got := renderExportTranscript(d.ExportSnapshot()); got != strings.Join(d.Lines(), "\n") {
			t.Fatalf("raw export spacing differs: %q", got)
		}
	}
	main.Clear()
	fmt.Fprintln(transcript.NewGroup(main), "after clear")
	if got := plainGroupLines(main); !reflect.DeepEqual(got, []string{"after clear"}) {
		t.Fatalf("clear left group state behind: %#v", got)
	}
	if got := main.ExportSnapshot().lines; len(got) != 6 || got[0] != "old one" || got[5] != "after clear" {
		t.Fatalf("clear lost archived spacing: %#v", got)
	}
}

func TestRestoredActiveRendererDerivesSpacingFromHistory(t *testing.T) {
	u, _ := persistenceUI(t)
	w := u.views["main"].response
	w.BeginResponse()
	fmt.Fprint(w, "previous answer\n")
	data, err := json.Marshal(u.snapshotPresentation())
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := persistenceUI(t)
	if err := restored.RestorePresentation(data); err != nil {
		t.Fatal(err)
	}
	w = restored.views["main"].response
	fmt.Fprint(w, "subsequent answer\n")
	w.EndResponse()
	if got := plainGroupLines(restored.views["main"].display); !reflect.DeepEqual(got, []string{"• previous answer", "", "• subsequent answer"}) {
		t.Fatalf("restored renderer bypassed grouping: %#v", got)
	}
}

func TestCheckpointDoesNotSplitStreamingGroup(t *testing.T) {
	u, _ := persistenceUI(t)
	w := u.views["main"].response
	w.BeginResponse()
	fmt.Fprint(w, "first line\n")
	u.snapshotPresentation()
	fmt.Fprint(w, "second line\n")
	w.EndResponse()
	if got := plainGroupLines(u.views["main"].display); !reflect.DeepEqual(got, []string{"• first line", "• second line"}) {
		t.Fatalf("checkpoint split the response: %#v", got)
	}
}

type groupedProvider struct{ calls int }

func (*groupedProvider) Name() string { return "test" }
func (p *groupedProvider) Complete(_ context.Context, _ llm.Request, stream llm.StreamCallback) (llm.Response, error) {
	p.calls++
	stream(llm.StreamEvent{Kind: llm.StreamThinking, Text: "Plan"})
	stream(llm.StreamEvent{Kind: llm.StreamThinking, Text: "ning\n"})
	stream(llm.StreamEvent{Kind: llm.StreamOutput, Text: "Answer\n"})
	response := llm.Response{Message: llm.Message{Role: "assistant", Content: "Answer\n"}}
	if p.calls == 1 {
		response.Message.ToolCalls = []llm.ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{}`)}, {ID: "two", Name: "edit", Arguments: json.RawMessage(`{}`)}}
	}
	return response, nil
}

type groupedToolset struct{}

func (groupedToolset) Schemas() []llm.Tool          { return []llm.Tool{{Name: "write"}, {Name: "edit"}} }
func (s groupedToolset) EnabledSchemas() []llm.Tool { return s.Schemas() }
func (groupedToolset) ExecuteDetailed(_ context.Context, call llm.ToolCall) (llm.ToolResult, error) {
	return llm.ToolResult{Output: call.Name + " output", Diff: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n+new"}, nil
}

func TestAgentStreamingKeepsSingleGroupBoundaries(t *testing.T) {
	for _, verbose := range []bool{true, false} {
		for _, width := range []int{0, 80} {
			t.Run(fmt.Sprintf("verbose=%v/width=%d", verbose, width), func(t *testing.T) {
				u, d, _ := groupedTestUI()
				w := NewMarkdownWriter(d, false, width)
				w.EnableDiffs()
				logger := trace.NewAnimated(d, false)
				logger.SetTaskIndicator(false)
				runner := agent.New(&groupedProvider{}, "test", groupedToolset{}, logger, w, 4)
				runner.SetVerbose(verbose)
				if err := runner.Run(context.Background(), "request"); err != nil {
					t.Fatal(err)
				}
				u.printSystemMessage("Completed")
				lines := plainGroupLines(d)
				joined := strings.Join(lines, "\n")
				if lines[0] == "" || lines[len(lines)-1] != "Completed" || strings.Contains(joined, "\n\n\n") || strings.Contains(joined, "Waiting") {
					t.Fatalf("unexpected padding or spinner content:\n%s", joined)
				}
				if !strings.Contains(joined, "Planning\n\n") || !strings.Contains(joined, "  write output\n• Edited x") || !strings.Contains(joined, "  edit output\n• Edited x") {
					t.Fatalf("stream/tool grouping lost adjacency:\n%s", joined)
				}
			})
		}
	}
}
