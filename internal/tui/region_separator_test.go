package tui

import (
	"io"
	"strings"
	"testing"

	"qcode/internal/session"
)

func stripSeparatorANSI(s string) string {
	for {
		start := strings.Index(s, "\x1b[")
		if start < 0 {
			return s
		}
		end := start + 2
		for end < len(s) && (s[end] < 0x40 || s[end] > 0x7e) {
			end++
		}
		if end < len(s) {
			end++
		}
		s = s[:start] + s[end:]
	}
}

func regionFrameLines(t *testing.T, u *UI, frame func() string) []string {
	t.Helper()
	return strings.Split(frame(), "\n")
}

func requireOneBlank(t *testing.T, lines []string, idxA, idxB int, name string) {
	t.Helper()
	if idxA < 0 || idxB < 0 {
		t.Fatalf("%s: region not found (a=%d b=%d)\n%s", name, idxA, idxB, strings.Join(lines, "\n"))
	}
	if idxB-idxA != 2 {
		t.Fatalf("%s: expected exactly one blank row between %d and %d, got %d\n%s", name, idxA, idxB, idxB-idxA-1, strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(stripSeparatorANSI(lines[idxA+1])) != "" {
		t.Fatalf("%s: middle row not blank: %q\n%s", name, lines[idxA+1], strings.Join(lines, "\n"))
	}
}

func TestRegionsHaveOneLineSeparator(t *testing.T) {
	// Output vs Steer pending.
	t.Run("output vs steer", func(t *testing.T) {
		u, frame := layoutFixture(t)
		u.unicode = true
		u.manager.(*layoutManager).queued = map[string][]session.QueuedPrompt{"main": {
			{RequestID: "steer", Intent: session.IntentSteer, Prompt: "steer text"},
			{RequestID: "q1", Intent: session.IntentQueue, Prompt: "queued one"},
		}}
		_, _ = io.WriteString(u.display, strings.Repeat("output line\n", 40)+"last output\n")
		u.renderInput(inputPrompt, "draft", 5)
		lines := regionFrameLines(t, u, frame)
		idxOut, idxSteer := -1, -1
		for i, l := range lines {
			if strings.Contains(l, "last output") {
				idxOut = i
			}
			if strings.TrimSpace(stripSeparatorANSI(l)) == "Steer" {
				idxSteer = i
			}
		}
		requireOneBlank(t, lines, idxOut, idxSteer, "output vs Steer")
	})

	// Output vs suggestions vs queue.
	t.Run("output vs suggestions vs queue", func(t *testing.T) {
		u, frame := layoutFixture(t)
		u.unicode = true
		u.manager.(*layoutManager).queued = map[string][]session.QueuedPrompt{"main": {
			{RequestID: "steer", Intent: session.IntentSteer, Prompt: "steer text"},
		}}
		_, _ = io.WriteString(u.display, strings.Repeat("output line\n", 40)+"last output\n")
		u.renderInput(inputPrompt, "/", 1)
		lines := regionFrameLines(t, u, frame)
		idxOut, idxFirstSugg, idxLastSugg, idxSteer := -1, -1, -1, -1
		for i, l := range lines {
			if strings.Contains(l, "last output") && idxOut < 0 {
				idxOut = i
			}
			if strings.Contains(l, "  /agent") && idxFirstSugg < 0 {
				idxFirstSugg = i
			}
			if strings.HasPrefix(strings.TrimSpace(stripSeparatorANSI(l)), "/") && strings.Contains(l, "  /") {
				idxLastSugg = i
			}
			if strings.TrimSpace(stripSeparatorANSI(l)) == "Steer" || strings.Contains(l, "Steer |") {
				idxSteer = i
			}
		}
		requireOneBlank(t, lines, idxOut, idxFirstSugg, "output vs suggestions")
		requireOneBlank(t, lines, idxLastSugg, idxSteer, "suggestions vs Steer")
	})

	// Pinned prompt vs output.
	t.Run("pinned vs output", func(t *testing.T) {
		u, frame := layoutFixture(t)
		u.unicode = true
		u.manager.(*layoutManager).latest = map[string]string{"main": "my pinned prompt"}
		_, _ = io.WriteString(u.display, strings.Repeat("output line\n", 40)+"last output\n")
		u.renderInput(inputPrompt, "draft", 5)
		lines := regionFrameLines(t, u, frame)
		idxPin := -1
		for i, l := range lines {
			if strings.Contains(l, "my pinned prompt") {
				idxPin = i
				break
			}
		}
		// Pinned region is one row here; separator is the next blank row and
		// output starts after it.
		if idxPin < 0 {
			t.Fatalf("pinned prompt not found\n%s", strings.Join(lines, "\n"))
		}
		if strings.TrimSpace(stripSeparatorANSI(lines[idxPin+1])) != "" {
			t.Fatalf("missing blank separator below pinned prompt: %q\n%s", lines[idxPin+1], strings.Join(lines, "\n"))
		}
		if strings.TrimSpace(stripSeparatorANSI(lines[idxPin+2])) == "" {
			t.Fatalf("more than one blank below pinned prompt\n%s", strings.Join(lines, "\n"))
		}
	})

	// Steer vs Queued inside the queue already has one blank (covered by
	// TestPendingPanelSeparatesSteeringAndQueuedWork), but verify the full
	// frame also keeps exactly one blank there and one blank above composer.
	t.Run("steer vs queued vs composer", func(t *testing.T) {
		u, frame := layoutFixture(t)
		u.unicode = true
		manager := &pendingLayoutManager{layoutManager: u.manager.(*layoutManager)}
		u.manager = manager
		manager.queued = map[string][]session.QueuedPrompt{"main": {
			{RequestID: "steer", Intent: session.IntentSteer, Prompt: "steer text"},
			{RequestID: "q1", Intent: session.IntentQueue, Prompt: "queued one"},
		}}
		_, _ = io.WriteString(u.display, strings.Repeat("output line\n", 40)+"last output\n")
		u.renderInput(inputPrompt, "draft", 5)
		lines := regionFrameLines(t, u, frame)
		idxSteerItem, idxQueuedHeading, idxQueueItem, idxComposer := -1, -1, -1, -1
		for i, l := range lines {
			if strings.Contains(l, "steer text") {
				idxSteerItem = i
			}
			if strings.Contains(l, "Queued") && strings.Contains(l, "Alt+Q") {
				idxQueuedHeading = i
			}
			if strings.Contains(l, "queued one") {
				idxQueueItem = i
			}
			if strings.HasPrefix(strings.TrimSpace(stripSeparatorANSI(l)), "(Steer)>") {
				idxComposer = i
			}
		}
		requireOneBlank(t, lines, idxSteerItem, idxQueuedHeading, "Steer vs Queued")
		requireOneBlank(t, lines, idxQueueItem, idxComposer, "Queued vs composer")
	})
}
