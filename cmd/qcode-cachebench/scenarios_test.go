package main

import (
	"strings"
	"testing"
)

func TestScenarioPrefixesPersistAcrossTurns(t *testing.T) {
	if len(strings.Fields(syntheticPrefix)) < 1024 {
		t.Fatal("fixture must comfortably exceed the cache minimum")
	}
	for _, scenario := range allScenarios() {
		for i, request := range scenario.turns("gpt-6.1-sol") {
			if len(request.Messages) == 0 || request.Messages[0].Role != "system" {
				t.Fatalf("%s turn %d lost the prefix", scenario.name, i)
			}
			want := syntheticPrefix
			if override := scenario.turnsList[i].earlyOverride; override != "" {
				want = override
			}
			if request.Messages[0].Content != want {
				t.Fatalf("%s turn %d has wrong prefix", scenario.name, i)
			}
		}
	}
}
