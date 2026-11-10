package tui

import (
	"context"
	"testing"

	"qcode/internal/llm"
)

type thinkLabelStub struct {
	level      string
	capability llm.ThinkingCapability
}

func (s thinkLabelStub) Run(context.Context, string) error { return nil }
func (s thinkLabelStub) ThinkingLevel() string              { return s.level }
func (s thinkLabelStub) ThinkingCapability() llm.ThinkingCapability {
	return s.capability
}
func (s thinkLabelStub) ThinkingCapabilityFor(string) llm.ThinkingCapability {
	return s.capability
}
func (s thinkLabelStub) SetThinking(string) error { return nil }

type noThinkStub struct{}

func (noThinkStub) Run(context.Context, string) error { return nil }

func TestThinkingLabelFor(t *testing.T) {
	// Explicit level wins over default.
	got := thinkingLabelFor(thinkLabelStub{
		level:      "low",
		capability: llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"low", "high"}, Default: "high"},
	})
	if got != "low" {
		t.Fatalf("explicit level = %q, want %q", got, "low")
	}
	// Empty level falls back to provider default so THINK stays visible.
	got = thinkingLabelFor(thinkLabelStub{
		level:      "",
		capability: llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"minimal", "low", "medium", "high", "xhigh"}, Default: "high"},
	})
	if got != "high" {
		t.Fatalf("default level = %q, want %q", got, "high")
	}
	// Always-on models keep the generic on label.
	got = thinkingLabelFor(thinkLabelStub{
		capability: llm.ThinkingCapability{Supported: true, AlwaysOn: true, Levels: []string{"max"}, Default: "max"},
	})
	if got != "on" {
		t.Fatalf("always-on = %q, want %q", got, "on")
	}
	// No default and no explicit level hides THINK.
	got = thinkingLabelFor(thinkLabelStub{
		capability: llm.ThinkingCapability{Supported: true, Adjustable: true, Levels: []string{"minimal", "low"}},
	})
	if got != "" {
		t.Fatalf("no default = %q, want empty", got)
	}
	// Runners without thinking support hide THINK.
	if got := thinkingLabelFor(noThinkStub{}); got != "" {
		t.Fatalf("non-thinking runner = %q, want empty", got)
	}
}
