package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const geminiNativeBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// isGeminiOfficialBaseURL reports whether baseURL selects Google's managed
// endpoint (either the OpenAI-compat shim or the native v1beta root).
// Empty means the built-in default, which is official. Custom proxies
// keep the OpenAI-compat wire protocol and must not use native.
func isGeminiOfficialBaseURL(baseURL string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return true
	}
	return trimmed == geminiBaseURL || trimmed == geminiNativeBaseURL
}

func geminiUseNative(p *openAIProvider) bool {
	return p.name == "gemini" && isGeminiOfficialBaseURL(p.baseURL)
}

func geminiNativeRoot(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return geminiNativeBaseURL
	}
	if strings.HasSuffix(trimmed, "/openai") {
		trimmed = strings.TrimSuffix(trimmed, "/openai")
		trimmed = strings.TrimRight(trimmed, "/")
		if trimmed == "" {
			return geminiNativeBaseURL
		}
	}
	// Only official roots reach here via geminiUseNative; custom proxies
	// never call this. Still, fall back safely.
	if trimmed == geminiBaseURL {
		return geminiNativeBaseURL
	}
	return trimmed
}

// geminiNativeThinkingConfig maps qcode thinking levels to the native
// GenerateContent thinkingConfig. Gemini 3/4 use thinkingLevel; Gemini 2.5
// uses a token budget. Returning nil omits generationConfig thinking.
// Docs: https://ai.google.dev/gemini-api/docs/thinking (thinking_level for
// 3-series, thinking_budget for 2.5). 2.5 non-none budgets are best-effort
// mappings; none disables thinking (budget 0).
func geminiNativeThinkingConfig(model, level string) map[string]any {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return nil
	}
	normalized := normalizeGeminiModelID(model)
	if strings.HasPrefix(normalized, "gemini-2.5") {
		switch level {
		case "none":
			return map[string]any{"thinkingBudget": 0}
		case "minimal":
			return map[string]any{"thinkingBudget": 512}
		case "low":
			return map[string]any{"thinkingBudget": 1024}
		case "medium":
			return map[string]any{"thinkingBudget": 2048}
		case "high":
			return map[string]any{"thinkingBudget": 8192}
		default:
			return nil
		}
	}
	upper := strings.ToUpper(level)
	// Native enum is uppercase; XHIGH covers Hydrogen's extra tier.
	return map[string]any{"thinkingLevel": upper}
}

func geminiNativeTools(tools []Tool) []any {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]any, 0, len(tools))
	for _, tool := range tools {
		params := geminiToolParameters(tool.Parameters)
		if params == nil {
			params = map[string]any{"type": "object"}
		}
		decls = append(decls, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  params,
		})
	}
	return []any{map[string]any{"functionDeclarations": decls}}
}

// geminiNativeContents converts provider-neutral messages to native
// system_instruction + contents. Consecutive tool messages are merged into
// a single user Content with multiple functionResponse parts, matching the
// official loop (append model reply verbatim, then one user Content with
// all results). See docs/apiyi function-calling guide + official
// ai.google.dev/gemini-api/docs/function-calling.
func geminiNativeContents(messages []Message) (map[string]any, []any) {
	var systemInstruction map[string]any
	start := 0
	if len(messages) > 0 && messages[0].Role == "system" {
		if text := messages[0].Content; text != "" {
			systemInstruction = map[string]any{"parts": []any{map[string]any{"text": text}}}
		}
		start = 1
	}
	contents := []any{}
	var pendingToolParts []any
	flushTools := func() {
		if len(pendingToolParts) > 0 {
			contents = append(contents, map[string]any{"role": "user", "parts": pendingToolParts})
			pendingToolParts = nil
		}
	}
	for _, msg := range messages[start:] {
		switch msg.Role {
		case "system":
			// Should not happen after prefix stabilization (trailing
			// context is role user), but keep it as user data, never as
			// a second instruction.
			flushTools()
			parts := []any{}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, img := range msg.Images {
				parts = append(parts, map[string]any{"inlineData": map[string]any{
					"mimeType": img.MediaType,
					"data":     base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
		case "user":
			flushTools()
			parts := []any{}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, img := range msg.Images {
				parts = append(parts, map[string]any{"inlineData": map[string]any{
					"mimeType": img.MediaType,
					"data":     base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		case "assistant":
			flushTools()
			parts := []any{}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for i, call := range msg.ToolCalls {
				if call.Name == "" {
					continue
				}
				var args any
				if len(call.Arguments) == 0 {
					args = map[string]any{}
				} else {
					var decoded any
					if err := json.Unmarshal(call.Arguments, &decoded); err != nil {
						args = map[string]any{}
					} else {
						if decoded == nil {
							args = map[string]any{}
						} else {
							args = decoded
						}
					}
				}
				part := map[string]any{"functionCall": map[string]any{
					"name": call.Name,
					"args": args,
				}}
				if call.ThoughtSignature != "" {
					part["thoughtSignature"] = call.ThoughtSignature
				} else if msg.Thinking != "" && i == 0 {
					// No per-call signature; nothing to attach. Text
					// thoughtSignatures are not tracked separately.
				}
				parts = append(parts, part)
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		case "tool":
			name := msg.Name
			if name == "" {
				name = msg.ToolCallID
			}
			if name == "" {
				name = "tool"
			}
			resp := map[string]any{"output": msg.Content}
			pendingToolParts = append(pendingToolParts, map[string]any{
				"functionResponse": map[string]any{
					"name":     name,
					"response": resp,
				},
			})
		default:
			flushTools()
			if msg.Content == "" && len(msg.Images) == 0 {
				continue
			}
			parts := []any{}
			if msg.Content != "" {
				parts = append(parts, map[string]any{"text": msg.Content})
			}
			for _, img := range msg.Images {
				parts = append(parts, map[string]any{"inlineData": map[string]any{
					"mimeType": img.MediaType,
					"data":     base64.StdEncoding.EncodeToString(img.Data),
				}})
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	flushTools()
	if contents == nil {
		contents = []any{}
	}
	return systemInstruction, contents
}

// geminiNativeUsageWire mirrors GenerateContent usageMetadata (camelCase).
// All fields are pointers: nil means unreported, pointer-to-zero is an
// explicit reported zero.
type geminiNativeUsageWire struct {
	PromptTokenCount         *int `json:"promptTokenCount"`
	CandidatesTokenCount     *int `json:"candidatesTokenCount"`
	ThoughtsTokenCount       *int `json:"thoughtsTokenCount"`
	CachedContentTokenCount  *int `json:"cachedContentTokenCount"`
	ToolUsePromptTokenCount  *int `json:"toolUsePromptTokenCount"`
	TotalTokenCount          *int `json:"totalTokenCount"`
}

func (w *geminiNativeUsageWire) mergeInto(acc *Usage) {
	if w == nil || acc == nil {
		return
	}
	if w.PromptTokenCount != nil {
		acc.InputTokens = w.PromptTokenCount
	}
	// Output already includes reasoning per llm.Usage contract: candidates
	// plus thoughts when both are reported. thoughtsTokenCount is also
	// surfaced separately as ReasoningTokens (subset, never added).
	if w.CandidatesTokenCount != nil || w.ThoughtsTokenCount != nil {
		sum := 0
		has := false
		if w.CandidatesTokenCount != nil && *w.CandidatesTokenCount >= 0 {
			sum += *w.CandidatesTokenCount
			has = true
		}
		if w.ThoughtsTokenCount != nil && *w.ThoughtsTokenCount >= 0 {
			sum += *w.ThoughtsTokenCount
			has = true
		}
		if has {
			v := sum
			acc.OutputTokens = &v
		}
	}
	if w.ThoughtsTokenCount != nil {
		acc.ReasoningTokens = w.ThoughtsTokenCount
	}
	if w.CachedContentTokenCount != nil {
		acc.CacheReadTokens = w.CachedContentTokenCount
	}
	// No write field on this route: never infer writes from misses.
}

func (p *openAIProvider) completeGeminiNative(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	systemInstruction, contents := geminiNativeContents(input.Messages)
	body := map[string]any{
		"contents": contents,
	}
	if systemInstruction != nil {
		body["systemInstruction"] = systemInstruction
	}
	if tools := geminiNativeTools(input.Tools); tools != nil {
		body["tools"] = tools
	}
	if cfg := geminiNativeThinkingConfig(input.Model, input.Thinking); cfg != nil {
		body["generationConfig"] = map[string]any{"thinkingConfig": cfg}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	model := normalizeGeminiModelID(input.Model)
	if model == "" {
		model = strings.TrimSpace(input.Model)
	}
	root := geminiNativeRoot(p.baseURL)
	endpoint := root + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.apiKey != "" {
		req.Header.Set("x-goog-api-key", p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return Response{}, fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return parseGeminiNativeStream(resp.Body, onText)
}

type geminiNativePart struct {
	Text             string         `json:"text"`
	Thought          bool           `json:"thought"`
	ThoughtSignature string         `json:"thoughtSignature"`
	FunctionCall     *struct {
		Name string         `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		Name string `json:"name"`
	} `json:"functionResponse"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
}

func parseGeminiNativeStream(reader io.Reader, onText StreamCallback) (Response, error) {
	result := Message{Role: "assistant"}
	accumulated := &Usage{}
	type pendingCall struct {
		name, args, thoughtSignature string
	}
	var calls []pendingCall
	var callOrder []int
	byNameArgs := map[string]int{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var event struct {
			Candidates []struct {
				Content *struct {
					Role  string             `json:"role"`
					Parts []geminiNativePart `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata *geminiNativeUsageWire `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			// Malformed chunks must not break text generation; usage
			// handling below is lenient per-chunk.
			continue
		}
		if event.UsageMetadata != nil {
			event.UsageMetadata.mergeInto(accumulated)
		}
		for _, cand := range event.Candidates {
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				// Thought signatures without text still matter for
				// function calls; text-only signature parts carry no
				// displayable content.
				if part.FunctionCall != nil && part.FunctionCall.Name != "" {
					argsStr := "{}"
					if len(part.FunctionCall.Args) > 0 {
						var v any
						if err := json.Unmarshal(part.FunctionCall.Args, &v); err == nil {
							// Re-encode canonically so ToolCall.Arguments
							// is always valid JSON.
							if encoded, err := json.Marshal(v); err == nil {
								argsStr = string(encoded)
							}
						}
					}
					key := part.FunctionCall.Name + "\x00" + argsStr + "\x00" + part.ThoughtSignature
					if idx, ok := byNameArgs[key]; ok {
						// Same call repeated across chunks (retry of the
						// same part): keep first, update signature if the
						// later chunk carries it.
						if calls[idx].thoughtSignature == "" && part.ThoughtSignature != "" {
							calls[idx].thoughtSignature = part.ThoughtSignature
						}
						continue
					}
					// Distinct calls share names/args legitimately in
					// parallel calls; only merge exact duplicates above.
					// Streaming may split one call's args across chunks?
					// Native sends whole args per part, so each part is a
					// complete call.
					calls = append(calls, pendingCall{name: part.FunctionCall.Name, args: argsStr, thoughtSignature: part.ThoughtSignature})
					idx := len(calls) - 1
					callOrder = append(callOrder, idx)
					byNameArgs[key] = idx
					continue
				}
				if part.FunctionResponse != nil {
					continue
				}
				if part.InlineData != nil {
					continue
				}
				if part.Text == "" {
					continue
				}
				if part.Thought {
					result.Thinking += part.Text
					if onText != nil {
						onText(StreamEvent{Kind: StreamThinking, Text: part.Text})
					}
					continue
				}
				result.Content += part.Text
				if onText != nil {
					onText(StreamEvent{Kind: StreamOutput, Text: part.Text})
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	for _, idx := range callOrder {
		c := calls[idx]
		if c.name == "" {
			continue
		}
		id := fmt.Sprintf("call-%d", idx+1)
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID:               id,
			Name:             c.name,
			Arguments:        json.RawMessage(c.args),
			ThoughtSignature: c.thoughtSignature,
		})
	}
	return Response{Message: result, Usage: NormalizeUsage(accumulated)}, nil
}

func (p *openAIProvider) geminiNativeModels(ctx context.Context) ([]string, error) {
	root := geminiNativeRoot(p.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/models?pageSize=100", nil)
	if err != nil {
		return nil, err
	}
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.apiKey != "" {
		req.Header.Set("x-goog-api-key", p.apiKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
		// Compat fallback: some proxies speak OpenAI shape even on native
		// root; accept it so listing stays robust.
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode provider models: %w", err)
	}
	models := make([]string, 0, len(payload.Models)+len(payload.Data))
	for _, m := range payload.Models {
		id := normalizeGeminiModelID(m.Name)
		// Native names look like "models/gemini-..."; bare IDs are
		// accepted as-is for forward compatibility.
		if m.Name != "" && id == "" {
			continue
		}
		if id == "" {
			continue
		}
		if isKnownGeminiEmbeddingModel(id) {
			continue
		}
		models = appendUnique(models, id)
	}
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		id := normalizeGeminiModelID(m.ID)
		if id == "" || isKnownGeminiEmbeddingModel(id) {
			continue
		}
		models = appendUnique(models, id)
	}
	return models, nil
}
