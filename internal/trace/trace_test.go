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
