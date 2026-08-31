package trace

import (
	"bytes"
	"encoding/json"
	"testing"
)

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
		if event["timestamp"] == nil || event["kind"] != "tool" || event["name"] != "read" {
			t.Errorf("event %d = %#v", index, event)
		}
		if index == 1 {
			if _, ok := event["duration_ms"]; !ok {
				t.Errorf("end event has no duration: %#v", event)
			}
		}
	}
}
