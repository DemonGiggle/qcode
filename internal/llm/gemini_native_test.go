package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestGeminiNativeRootAndOfficialDetection(t *testing.T) {
	if !isGeminiOfficialBaseURL("") {
		t.Fatal("empty baseURL must be official")
	}
	if !isGeminiOfficialBaseURL(geminiBaseURL) {
		t.Fatal("compat default must be official")
	}
	if !isGeminiOfficialBaseURL(geminiNativeBaseURL) {
		t.Fatal("native root must be official")
	}
	if !isGeminiOfficialBaseURL(geminiNativeBaseURL + "/") {
		t.Fatal("native root with slash must be official")
	}
	if isGeminiOfficialBaseURL("https://proxy.example/v1") {
		t.Fatal("proxy must not be official")
	}
	if got := geminiNativeRoot(geminiBaseURL); got != geminiNativeBaseURL {
		t.Fatalf("compat root = %q, want %q", got, geminiNativeBaseURL)
	}
	if got := geminiNativeRoot(""); got != geminiNativeBaseURL {
		t.Fatalf("empty root = %q", got)
	}
	p, _ := newGemini(Config{})
	if !geminiUseNative(p.(*openAIProvider)) {
		t.Fatal("default Gemini provider must use native")
	}
	proxy, _ := newGemini(Config{BaseURL: "https://proxy.example/v1"})
	if geminiUseNative(proxy.(*openAIProvider)) {
		t.Fatal("proxy Gemini must keep compat wire")
	}
}

func TestGeminiNativeThinkingConfigMapping(t *testing.T) {
	if got := geminiNativeThinkingConfig("gemini-3-pro-preview", ""); got != nil {
		t.Fatalf("empty level must omit config, got %v", got)
	}
	if got := geminiNativeThinkingConfig("gemini-3-pro-preview", "high"); got["thinkingLevel"] != "HIGH" {
		t.Fatalf("3-series high = %v", got)
	}
	if got := geminiNativeThinkingConfig("gemini-4-hydrogen", "xhigh"); got["thinkingLevel"] != "XHIGH" {
		t.Fatalf("hydrogen xhigh = %v", got)
	}
	if got := geminiNativeThinkingConfig("gemini-2.5-pro", "none"); got["thinkingBudget"] != 0 {
		t.Fatalf("2.5 none = %v, want budget 0", got)
	}
	if got := geminiNativeThinkingConfig("gemini-2.5-flash", "high"); got["thinkingBudget"] != 8192 {
		t.Fatalf("2.5 high budget = %v", got)
	}
	if got := geminiNativeThinkingConfig("gemini-2.5-flash", "minimal"); got["thinkingBudget"] != 512 {
		t.Fatalf("2.5 minimal budget = %v", got)
	}
}

func TestGeminiNativeContentsMapping(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "you are a coder"},
		{Role: "user", Content: "hi", Images: []Image{{MediaType: "image/png", Data: []byte{1, 2, 3}}}},
		{Role: "assistant", Content: "checking", ToolCalls: []ToolCall{
			{ID: "call-1", Name: "lookup", Arguments: json.RawMessage(`{"q":"one"}`), ThoughtSignature: "sig-one"},
			{ID: "call-2", Name: "inspect", Arguments: json.RawMessage(`{"q":"two"}`)},
		}},
		{Role: "tool", ToolCallID: "call-1", Name: "lookup", Content: "r1"},
		{Role: "tool", ToolCallID: "call-2", Name: "inspect", Content: "r2"},
	}
	sys, contents := geminiNativeContents(msgs)
	if sys["parts"].([]any)[0].(map[string]any)["text"] != "you are a coder" {
		t.Fatalf("system_instruction = %v", sys)
	}
	if len(contents) != 3 {
		t.Fatalf("contents len = %d, want 3 (user, model, merged tools)", len(contents))
	}
	user := contents[0].(map[string]any)
	if user["role"] != "user" {
		t.Fatalf("first role = %v", user["role"])
	}
	userParts := user["parts"].([]any)
	if len(userParts) != 2 {
		t.Fatalf("user parts = %d, want text+image", len(userParts))
	}
	imgPart := userParts[1].(map[string]any)["inlineData"].(map[string]any)
	if imgPart["mimeType"] != "image/png" {
		t.Fatalf("image mime = %v", imgPart)
	}
	model := contents[1].(map[string]any)
	if model["role"] != "model" {
		t.Fatalf("second role = %v", model["role"])
	}
	modelParts := model["parts"].([]any)
	if len(modelParts) != 3 {
		t.Fatalf("model parts = %d, want text+2 calls", len(modelParts))
	}
	fc := modelParts[1].(map[string]any)["functionCall"].(map[string]any)
	if fc["name"] != "lookup" {
		t.Fatalf("first call = %v", fc)
	}
	if modelParts[1].(map[string]any)["thoughtSignature"] != "sig-one" {
		t.Fatalf("signature not preserved: %v", modelParts[1])
	}
	tools := contents[2].(map[string]any)
	if tools["role"] != "user" {
		t.Fatalf("tool role = %v", tools["role"])
	}
	toolParts := tools["parts"].([]any)
	if len(toolParts) != 2 {
		t.Fatalf("tool parts = %d, want merged 2", len(toolParts))
	}
	fr := toolParts[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "lookup" {
		t.Fatalf("tool name = %v", fr)
	}
	if fr["response"].(map[string]any)["output"] != "r1" {
		t.Fatalf("tool output = %v", fr["response"])
	}
}

func TestGeminiNativeToolsStripAdditionalProperties(t *testing.T) {
	tools := geminiNativeTools([]Tool{{
		Name: "consult", Description: "d",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"x": map[string]any{"type": "string", "additionalProperties": false},
			},
			"additionalProperties": false,
		},
	}})
	encoded, _ := json.Marshal(tools)
	if strings.Contains(string(encoded), "additionalProperties") {
		t.Fatalf("native tools retain additionalProperties: %s", encoded)
	}
	if !strings.Contains(string(encoded), "functionDeclarations") {
		t.Fatalf("native tools must use functionDeclarations: %s", encoded)
	}
}

func TestGeminiNativeUsageWireMapping(t *testing.T) {
	wire := &geminiNativeUsageWire{
		PromptTokenCount:        IntPtr(100),
		CandidatesTokenCount:    IntPtr(20),
		ThoughtsTokenCount:      IntPtr(5),
		CachedContentTokenCount: IntPtr(10),
	}
	acc := &Usage{}
	wire.mergeInto(acc)
	if v, ok := acc.KnownInput(); !ok || v != 100 {
		t.Fatalf("input = %v", acc.InputTokens)
	}
	if v, ok := acc.KnownOutput(); !ok || v != 25 {
		t.Fatalf("output must be 20+5=25, got %v", acc.OutputTokens)
	}
	if v, ok := acc.KnownReasoning(); !ok || v != 5 {
		t.Fatalf("reasoning = %v", acc.ReasoningTokens)
	}
	if v, ok := acc.KnownCacheRead(); !ok || v != 10 {
		t.Fatalf("cache = %v", acc.CacheReadTokens)
	}
	// Negative values become unknown via NormalizeUsage.
	bad := &Usage{InputTokens: IntPtr(10), OutputTokens: IntPtr(5), CacheReadTokens: IntPtr(-1)}
	norm := NormalizeUsage(bad)
	if _, ok := norm.KnownCacheRead(); ok {
		t.Fatal("negative cache read must become unknown")
	}
	// Cache exceeding input is discarded, totals retained.
	over := &Usage{InputTokens: IntPtr(10), CacheReadTokens: IntPtr(99)}
	norm = NormalizeUsage(over)
	if _, ok := norm.KnownCacheRead(); ok {
		t.Fatal("oversized cache must be discarded")
	}
	if v, ok := norm.KnownInput(); !ok || v != 10 {
		t.Fatalf("input retained = %v", norm.InputTokens)
	}
}

func TestGeminiNativeParseThoughtAndMalformed(t *testing.T) {
	stream := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking now","thought":true}]}}],"usageMetadata":{"promptTokenCount":50}}` + "\n" +
		"not a data line\n" +
		"data: {malformed json\n" +
		"data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":7,"cachedContentTokenCount":12}}` + "\n"
	var thinking, output strings.Builder
	got, err := parseGeminiNativeStream(strings.NewReader(stream), func(e StreamEvent) {
		if e.Kind == StreamThinking {
			thinking.WriteString(e.Text)
		} else {
			output.WriteString(e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if thinking.String() != "thinking now" {
		t.Fatalf("thinking = %q", thinking.String())
	}
	if output.String() != "answer" || got.Message.Content != "answer" {
		t.Fatalf("output = %q / %q", output.String(), got.Message.Content)
	}
	if got.Message.Thinking != "thinking now" {
		t.Fatalf("message thinking = %q", got.Message.Thinking)
	}
	if v, ok := got.Usage.KnownCacheRead(); !ok || v != 12 {
		t.Fatalf("cache = %v", got.Usage)
	}
}

func TestGeminiNativeCompleteSendsNativeWire(t *testing.T) {
	var body map[string]any
	var seenURL, googKey, bearer string
	p, err := newGemini(Config{HTTP: doerFunc(func(r *http.Request) (*http.Response, error) {
		seenURL, googKey, bearer = r.URL.String(), r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		stream := "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}` + "\n"
		return response(http.StatusOK, stream), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	// Use a test key via provider internals: set apiKey through Config is
	// redacted in display but preserved on the wire; here Config has no key
	// so headers stay empty, URL/body still assert native shape.
	_ = googKey
	_ = bearer
	got, err := p.Complete(context.Background(), Request{
		Model:    "models/gemini-3-flash-preview",
		Messages: []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}},
		Tools:    []Tool{{Name: "lookup", Description: "d", Parameters: map[string]any{"type": "object"}}},
		Thinking: "low",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seenURL, "/v1beta/models/gemini-3-flash-preview:streamGenerateContent") {
		t.Fatalf("URL = %s, want normalized native endpoint without models/ prefix duplication", seenURL)
	}
	if _, ok := body["systemInstruction"]; !ok {
		t.Fatalf("native body must carry systemInstruction: %v", body)
	}
	contents, _ := body["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("system lifted, contents = %v", body["contents"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	genCfg, _ := body["generationConfig"].(map[string]any)
	thinkCfg, _ := genCfg["thinkingConfig"].(map[string]any)
	if thinkCfg["thinkingLevel"] != "LOW" {
		t.Fatalf("thinking = %v", body["generationConfig"])
	}
	if got.Message.Content != "ok" {
		t.Fatalf("content = %q", got.Message.Content)
	}
	if !reflect.DeepEqual(p.(ThinkingProvider).ThinkingCapability("models/gemini-3-flash-preview").Levels, []string{"minimal", "low", "medium", "high"}) {
		t.Fatal("prefixed model must resolve thinking")
	}
}
