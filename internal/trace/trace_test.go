package trace

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

var clockPattern = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}$`)

func TestJSONEventsContainTimestampsAndDuration(t *testing.T) {
	var output bytes.Buffer
	span := New(&output, true).Start("tool", "read", map[string]any{"arguments": `{"path":"x"}`})
	span.End(nil)
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	for index, line := range lines {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		timestamp, timestampOK := event["timestamp"].(string)
		if !timestampOK || !clockPattern.MatchString(timestamp) || event["kind"] != "tool" || event["name"] != "read" {
			t.Errorf("event %d = %#v", index, event)
		}
		if index == 1 {
			if _, ok := event["duration_ms"]; !ok {
				t.Errorf("end event has no duration: %#v", event)
			}
		}
	}
}

func TestTimestampUsesLocalTime(t *testing.T) {
	previousLocal := time.Local
	time.Local = time.FixedZone("UTC+8", 8*60*60)
	defer func() { time.Local = previousLocal }()

	at := time.Date(2026, time.August, 31, 7, 40, 48, 272000000, time.UTC)
	if got := formatTimestamp(at); got != "15:40:48" {
		t.Fatalf("timestamp = %q", got)
	}

	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetColor(true)
	logger.writeCompleted(&Span{kind: "llm", name: "ollama", start: at}, time.Second, nil, false)
	if got := output.String(); got != "[15:40:48] start llm ollama (1s)\n" {
		t.Fatalf("text event = %q", got)
	}
}

func TestTextEventIsOneCompletedStartLine(t *testing.T) {
	var output bytes.Buffer
	span := New(&output, false).Start("tool", "read", map[string]any{"arguments": `{"path":"x"}`})
	span.End(nil)
	got := output.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "start tool read (") || strings.Contains(got, "end tool") {
		t.Fatalf("text event = %q", got)
	}
}

func TestAnimatedEventReplacesSpinnerWithDuration(t *testing.T) {
	var output bytes.Buffer
	span := NewAnimated(&output, false).Start("tool", "read", nil)
	span.End(nil)
	got := output.String()
	if !strings.Contains(got, "(⠋)") || !strings.Contains(got, "\x1b[2K") || strings.Contains(got, "end tool") {
		t.Fatalf("animated event = %q", got)
	}
}

func TestNonVerboseAnimatedEventShowsWaitingForWholeTask(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	task := logger.BeginTask()
	span := logger.Start("llm", "fake", nil)
	if got := output.String(); !strings.Contains(got, "Waiting (⠋)") || strings.Contains(got, "start llm") {
		t.Fatalf("active event = %q", got)
	}
	span.End(nil)
	if got := output.String(); strings.HasSuffix(got, "\r\x1b[2K") || strings.Contains(got, "start llm") {
		t.Fatalf("span cleared task indicator = %q", got)
	}
	task.End()
	if got := output.String(); !strings.HasSuffix(got, "\r\x1b[2K") {
		t.Fatalf("completed event = %q", got)
	}
}

func TestTaskIndicatorCanPauseForOutputAndResumeForLocalWork(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	task := logger.BeginTask()
	task.Suspend()
	if got := output.String(); !strings.HasSuffix(got, "\r\x1b[2K") {
		t.Fatalf("suspended task = %q", got)
	}
	output.Reset()
	task.Resume()
	if got := output.String(); !strings.Contains(got, "Waiting (⠋)") {
		t.Fatalf("resumed task = %q", got)
	}
	task.End()
}

func TestActiveTaskIndicatorRepaintsForLocalWork(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	task := logger.BeginTask()
	task.Resume()
	task.End()
	if got := output.String(); strings.Count(got, "Waiting (⠋)") < 2 {
		t.Fatalf("refreshed task = %q", got)
	}
}

func TestASCIIWaitingSpinner(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	logger.SetUnicode(false)
	task := logger.BeginTask()
	task.End()
	if got := output.String(); !strings.Contains(got, "Waiting (|)") || strings.Contains(got, "⠋") {
		t.Fatalf("ASCII task indicator = %q", got)
	}
}

func TestActivityUsesCategoryColorsAndKeepsCompletedLine(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	logger.SetColor(true)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.End(nil)
	got := output.String()
	for _, expected := range []string{traceCyan, traceGreen, "✓", "Read x.go", "\n"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("activity missing %q: %q", expected, got)
		}
	}
}

func TestActivityOutputPreviewIsIndentedMutedAndLimited(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetColor(true)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.EndWithOutput(nil, "first line\nsecond line\nthird line\nfourth line")

	got := output.String()
	if strings.Count(got, "\n") != 4 {
		t.Fatalf("activity preview line count = %d: %q", strings.Count(got, "\n"), got)
	}
	for _, line := range []string{"first line", "second line"} {
		if !strings.Contains(got, traceDim+"  "+line+traceReset+"\n") {
			t.Errorf("missing muted indented line %q: %q", line, got)
		}
	}
	if !strings.Contains(got, traceDim+"  third line…"+traceReset+"\n") {
		t.Errorf("missing muted truncated line: %q", got)
	}
	if strings.Contains(got, traceCyan+"  first line") {
		t.Fatalf("output preview used the activity accent: %q", got)
	}
	if strings.Contains(got, "fourth line") || !strings.Contains(got, "third line…") {
		t.Fatalf("output preview did not mark omitted lines: %q", got)
	}
}

func TestActivityOutputPreviewNormalizesReadLineNumbers(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.EndWithOutput(nil, "      12\tmodule github.com/example/project\n      13\t\n      14\tgo 1.21")

	got := output.String()
	if !strings.Contains(got, "  12 | module github.com/example/project\n") {
		t.Fatalf("line number was not normalized: %q", got)
	}
	if !strings.Contains(got, "  14 | go 1.21\n") {
		t.Fatalf("second line number was not normalized: %q", got)
	}
	if strings.Contains(got, "       12") {
		t.Fatalf("read padding leaked into preview: %q", got)
	}
}

func TestActivityBlocksHaveOneSeparatorBetweenCalls(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetColor(true)
	first := logger.StartActivity(Activity{Action: "read", Start: "Reading one.go", Completed: "Read one.go", Category: ActivityRead})
	first.EndWithOutput(nil, "first")
	second := logger.StartActivity(Activity{Action: "read", Start: "Reading two.go", Completed: "Read two.go", Category: ActivityRead})
	second.EndWithOutput(nil, "second")

	got := output.String()
	if !strings.Contains(got, traceDim+"  first"+traceReset+"\n\n") || !strings.Contains(got, "\n\n"+traceGreen+"✓") {
		t.Fatalf("activity blocks have no single separator: %q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("activity output has a trailing separator: %q", got)
	}
}

func TestActivitySeparatorCanPrecedeResponseWithoutTrailingBlank(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading one.go", Completed: "Read one.go", Category: ActivityRead})
	activity.EndWithOutput(nil, "first")
	logger.SeparateActivity()
	output.WriteString("response\n")

	got := output.String()
	if !strings.Contains(got, "  first\n\nresponse\n") {
		t.Fatalf("response was not separated from activity: %q", got)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("separator was left trailing: %q", got)
	}
}

func TestActivityOutputPreviewSanitizesAndTruncates(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetColor(true)
	logger.SetUnicode(false)
	logger.SetWidth(12)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.EndWithOutput(nil, "\x1b[31m0123456789abcdef\x1b[0m\nsecond\nthird\nfourth")

	got := output.String()
	if strings.Contains(got, "\x1b[31m") {
		t.Fatalf("output preview retained terminal color sequence: %q", got)
	}
	if !strings.Contains(got, traceDim+"  0123456..."+traceReset+"\n") {
		t.Fatalf("long output was not truncated: %q", got)
	}
	if !strings.Contains(got, traceDim+"  third..."+traceReset+"\n") {
		t.Fatalf("ASCII omission marker missing: %q", got)
	}
	if strings.Count(got, "\n") != 4 {
		t.Fatalf("truncated preview line count = %d: %q", strings.Count(got, "\n"), got)
	}
}

func TestActivityJSONIsSafeAndStructured(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, true)
	logger.SetColor(true)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.EndWithOutput(assertionError("denied"), "secret tool output")
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	for index, line := range lines {
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event["kind"] != "activity" || event["action"] != "read" || strings.Contains(string(line), "\x1b[") {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
	var completed map[string]any
	_ = json.Unmarshal(lines[1], &completed)
	if completed["status"] != "error" || completed["summary"] != "Read x.go" {
		t.Fatalf("completed activity = %#v", completed)
	}
	if strings.Contains(output.String(), "denied") {
		t.Fatalf("activity JSON leaked detailed error: %q", output.String())
	}
	if strings.Contains(output.String(), "secret tool output") {
		t.Fatalf("activity JSON leaked tool output: %q", output.String())
	}
}

func TestActivityHonorsDisabledColorAndASCII(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, false)
	logger.SetUnicode(false)
	activity := logger.StartActivity(Activity{Action: "read", Start: "Reading x.go", Completed: "Read x.go", Category: ActivityRead})
	activity.End(nil)
	got := output.String()
	if strings.Contains(got, "\x1b[") || !strings.Contains(got, "OK Read x.go") || strings.Contains(got, "✓") {
		t.Fatalf("plain ASCII activity = %q", got)
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }

func TestTaskIndicatorCanBeDisabledForManagedUI(t *testing.T) {
	var output bytes.Buffer
	logger := NewAnimated(&output, false)
	logger.SetVerbose(false)
	logger.SetTaskIndicator(false)
	task := logger.BeginTask()
	task.End()
	if got := output.String(); got != "" {
		t.Fatalf("disabled task indicator output = %q", got)
	}
}
