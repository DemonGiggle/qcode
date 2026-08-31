package trace

import (
	"bytes"
	"encoding/json"
	"regexp"
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
	New(&output, false).write("start", "llm", "ollama", at, 0, nil)
	if got := output.String(); got != "[15:40:48] start llm ollama\n" {
		t.Fatalf("text event = %q", got)
	}
}
