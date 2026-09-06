package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"qcode/internal/llm"
	"qcode/internal/trace"
)

func activityCall(name, arguments string) llm.ToolCall {
	return llm.ToolCall{Name: name, Arguments: json.RawMessage(arguments)}
}

func TestToolActivityUsesAllowlistedSummaries(t *testing.T) {
	tests := []struct {
		name      string
		call      llm.ToolCall
		start     string
		completed string
		category  trace.ActivityCategory
	}{
		{"read", activityCall("read", `{"path":"internal/agent/agent.go"}`), "Reading internal/agent/agent.go", "Read internal/agent/agent.go", trace.ActivityRead},
		{"write", activityCall("write", `{"path":"README.md","content":"secret"}`), "Writing README.md", "Wrote README.md", trace.ActivityWrite},
		{"web search", activityCall("web_search", `{"query":"Go context compaction"}`), `Searching web for "Go context compaction"`, `Searched web for "Go context compaction"`, trace.ActivityRead},
		{"fetch", activityCall("web_fetch", `{"url":"https://example.test/path?token=secret#fragment"}`), "Fetching example.test", "Fetched example.test", trace.ActivityRead},
		{"delegate", activityCall("delegate_task", `{"agent_id":"agent-2","prompt":"secret task"}`), "Consulting agent-2", "Agent agent-2 accepted the task", trace.ActivityAgent},
		{"shell", activityCall("shell", `{"command":"echo secret"}`), "Running shell command", "Ran shell command", trace.ActivityWrite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toolActivity(tc.call)
			if got.Start != tc.start || got.Completed != tc.completed || got.Category != tc.category {
				t.Fatalf("activity = %+v", got)
			}
		})
	}
}

func TestToolActivitySanitizesAndBoundsTargets(t *testing.T) {
	got := toolActivity(activityCall("read", `{"path":"first\nsecond"}`))
	if strings.ContainsAny(got.Start, "\r\n") || !strings.Contains(got.Start, "first?second") {
		t.Fatalf("sanitized activity = %q", got.Start)
	}
	long := strings.Repeat("x", maxActivityTargetRunes+1)
	got = toolActivity(activityCall("read", `{"path":"`+long+`"}`))
	if !strings.HasSuffix(got.Start, "…") || len([]rune(got.Start)) > len([]rune("Reading "))+maxActivityTargetRunes+1 {
		t.Fatalf("bounded activity = %q", got.Start)
	}
}
