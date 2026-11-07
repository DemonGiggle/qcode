package main

import (
	"fmt"
	"strings"

	"qcode/internal/llm"
)

// Synthetic fixture text. Committed, fictional, and stable across runs so
// before/after comparisons measure cache behavior rather than prompt drift.
// Approximate sizes are documented in docs/cachebench.md.
//
// The expanded fixture provides ample cacheable material; per-turn additions
// remain short. Exact token counts depend on the selected model.
const syntheticLedger = `CACHEBENCH SYNTHETIC FIXTURE (fictional inventory ledger).
Aisle 01 holds brass hinges, cedar planks, and glass vials. Aisle 02 holds
iron bolts, jute rope, and kerosene lamps. Aisle 03 holds linen sheets,
maple dowels, and nickel washers. Aisle 04 holds oak beams, paper reams,
and quartz tiles. Aisle 05 holds rubber gaskets, steel rods, and tin cups.
Reorder policy: restock hinges when bins fall below twelve; restock rope
below twenty meters; restock lamps below six. Ledger entries reference
fictional work orders WO-1001 through WO-1048 with deterministic counts.
No real customer data, no credentials, no network addresses appear here.
` + `Ledger appendix: work orders WO-1001..WO-1012 request hinge counts
3,5,2,8,1,4,6,2,7,3,5,2. Orders WO-1013..WO-1024 request rope meters
9,4,11,6,8,2,10,5,7,3,12,4. Orders WO-1025..WO-1036 request lamp counts
2,1,3,2,4,1,2,3,1,2,1,3. Orders WO-1037..WO-1048 request washer counts
14,9,20,7,11,6,18,4,13,8,16,5. Audit rule: every third order is spot
checked; every seventh order is double counted. End of fixture prefix.
` + `Handling rules: count items per aisle, report shortages, never invent
new aisles. Summaries list aisle, item, count, and reorder flag only.
Fixture version CACHEBENCH-v1. All numbers above are synthetic and fixed.
`

// Distinct section headings keep this a deterministic synthetic reference.
var syntheticPrefix = func() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "Reference section %d\n%s\n", i+1, syntheticLedger)
	}
	return b.String()
}()

const syntheticSummary = `CACHEBENCH compacted history (synthetic): aisles 01-05 tallied; 3 shortages flagged (hinges, rope, lamps); ledger WO-1001..WO-1048 applied.`

func syntheticTools(schemaVersion string) []llm.Tool {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"aisle": map[string]any{"type": "string"},
			"item":  map[string]any{"type": "string"},
		},
		"required": []string{"aisle", "item"},
	}
	if schemaVersion == "v2" {
		// Changed-tool-schema control: same tool name, extra optional field
		// so the schema bytes (and any schema-sensitive cache key) differ.
		params["properties"].(map[string]any)["detail"] = map[string]any{"type": "string"}
	}
	return []llm.Tool{
		{
			Name:        "fixture_lookup",
			Description: "Synthetic fixture lookup over fictional aisles.",
			Parameters:  params,
		},
		{
			Name:        "fixture_total",
			Description: "Synthetic total over fictional counts.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"orders": map[string]any{"type": "string"}},
			},
		},
	}
}

// scriptedToolResult returns a deterministic canned tool result. The harness
// never executes real tools; results are fixed strings replayed into the
// scripted conversation.
func scriptedToolResult(turn int) string {
	return fmt.Sprintf("synthetic tool result t%d: aisle-01 hinges=14 rope=22m lamps=5; no reorder beyond flagged", turn)
}

type scenarioTurn struct {
	userSuffix string
	// earlyOverride, when non-empty, replaces the shared prefix (dynamic
	// early-context control). toolSchema selects the tool schema version.
	earlyOverride string
	toolSchema    string
	// compacted replaces full history with the short summary (compacted
	// history control).
	compacted bool
}

type scenario struct {
	name        string
	description string
	turnsList   []scenarioTurn
}

func allScenarios() []scenario {
	return []scenario{
		{
			name:        "stable-long-prefix",
			description: "Long stable prefix with short per-turn additions; cache-friendly baseline.",
			turnsList: []scenarioTurn{
				{userSuffix: "tally aisle 01"},
				{userSuffix: "tally aisle 02"},
				{userSuffix: "tally aisle 03"},
			},
		},
		{
			name:        "dynamic-early-context",
			description: "Early context changes each turn; prefix-invalidation control.",
			turnsList: []scenarioTurn{
				{userSuffix: "tally aisle 01"},
				{userSuffix: "tally aisle 02 (shift B)", earlyOverride: dynamicPrefix(1)},
				{userSuffix: "tally aisle 03 (shift C)", earlyOverride: dynamicPrefix(2)},
			},
		},
		{
			name:        "changed-tool-schema",
			description: "Tool schema changes each turn; schema-sensitivity control.",
			turnsList: []scenarioTurn{
				{userSuffix: "tally aisle 01", toolSchema: "v1"},
				{userSuffix: "tally aisle 02", toolSchema: "v2"},
				{userSuffix: "tally aisle 03", toolSchema: "v2"},
			},
		},
		{
			name:        "compacted-history",
			description: "History replaced by short summary; compaction control.",
			turnsList: []scenarioTurn{
				{userSuffix: "tally aisle 01"},
				{userSuffix: "tally aisle 02", compacted: true},
				{userSuffix: "tally aisle 03", compacted: true},
			},
		},
	}
}

func dynamicPrefix(turn int) string {
	return fmt.Sprintf("CACHEBENCH DYNAMIC SHIFT t%d (synthetic): reorder thresholds rotated; hinges=%d rope=%d lamps=%d. ",
		turn, 10+turn, 18+turn, 5+turn) + syntheticPrefix
}

// turns builds the cumulative scripted request for each turn index. History
// is replayed from fixed strings; assistant text and tool results are canned.
func (s scenario) turns(model string) []llm.Request {
	_ = model
	schema := "v1"
	var history []llm.Message
	out := make([]llm.Request, 0, len(s.turnsList))
	for i, t := range s.turnsList {
		if t.toolSchema != "" {
			schema = t.toolSchema
		} else if s.name == "changed-tool-schema" && i > 0 {
			schema = "v2"
		}
		prefix := syntheticPrefix
		if t.earlyOverride != "" {
			prefix = t.earlyOverride
		}
		var msgs []llm.Message
		if t.compacted && len(history) > 0 {
			msgs = []llm.Message{
				{Role: "system", Content: prefix},
				{Role: "user", Content: syntheticSummary},
				{Role: "user", Content: "synthetic follow-up t" + itoa(i) + ": " + t.userSuffix},
			}
		} else {
			msgs = append([]llm.Message{{Role: "system", Content: prefix}}, history...)
			msgs = append(msgs, llm.Message{Role: "user", Content: "synthetic request t" + itoa(i) + ": " + t.userSuffix})
		}
		history = append(history,
			llm.Message{Role: "user", Content: "synthetic request t" + itoa(i) + ": " + t.userSuffix},
			llm.Message{Role: "assistant", Content: "synthetic acknowledgement t" + itoa(i),
				ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("call-t%d", i), Name: "fixture_lookup",
					Arguments: []byte(`{"aisle":"aisle-01","item":"hinges"}`)}}},
			llm.Message{Role: "tool", Content: scriptedToolResult(i), ToolCallID: fmt.Sprintf("call-t%d", i), Name: "fixture_lookup"},
		)
		out = append(out, llm.Request{
			Model:    "",
			Messages: msgs,
			Tools:    syntheticTools(schema),
		})
	}
	return out
}

func selectScenarios(filter string) ([]scenario, error) {
	all := allScenarios()
	if strings.TrimSpace(filter) == "" {
		return all, nil
	}
	want := map[string]bool{}
	for _, part := range strings.Split(filter, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			want[part] = true
		}
	}
	var out []scenario
	for _, s := range all {
		if want[s.name] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("unknown scenarios %q (available: stable-long-prefix, dynamic-early-context, changed-tool-schema, compacted-history)", filter)
	}
	return out, nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
