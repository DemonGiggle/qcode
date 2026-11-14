package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

const openAIBaseURL = "https://api.openai.com/v1"

type openAIProvider struct {
	baseURL   string
	apiKey    string
	client    HTTPDoer
	name      string
	userAgent string
	sessionID string
	modelMeta *ProviderModelMeta
}

// SessionIdentity preserves the non-secret routing identity used by OpenCode Go.
func (p *openAIProvider) SessionIdentity() string { return p.sessionID }
func (p *openAIProvider) RestoreSessionIdentity(id string) error {
	if id == "" {
		return nil
	}
	data, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if p.name != "opencode-go" || err != nil || len(data) != 16 || len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return fmt.Errorf("invalid provider session identity")
	}
	p.sessionID = id
	return nil
}

func init() {
	Register("openai", newOpenAI)
}

func newOpenAI(config Config) (Provider, error) {
	provider, err := newOpenAICompatible(config, openAIBaseURL, "openai", "", "")
	if err != nil {
		return nil, err
	}
	provider.(*openAIProvider).modelMeta = config.ModelMeta
	return provider, nil
}

// IsOfficialOpenAIEndpoint reports whether baseURL selects OpenAI's default
// endpoint. An empty URL means the built-in endpoint will be used.
func IsOfficialOpenAIEndpoint(baseURL string) bool {
	baseURL = strings.TrimRight(baseURL, "/")
	return baseURL == "" || baseURL == openAIBaseURL
}

func newOpenAICompatible(config Config, defaultBaseURL, name, userAgent, sessionID string) (Provider, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &openAIProvider{baseURL: baseURL, apiKey: config.APIKey, client: httpClient(config), name: name, userAgent: userAgent, sessionID: sessionID}, nil
}

func (p *openAIProvider) Name() string { return p.name }

func (p *openAIProvider) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.sessionID != "" {
		req.Header.Set("x-opencode-session", p.sessionID)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
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
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode provider models: %w", err)
	}
	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		if model.ID == "" {
			continue
		}
		// Google's OpenAI-compatible model list exposes IDs, not chat/tool
		// capability metadata. Exclude only model IDs documented as embedding-only;
		// do not infer that every other (including unknown) ID supports tools.
		if p.name == "gemini" && isKnownGeminiEmbeddingModel(model.ID) {
			continue
		}
		models = append(models, model.ID)
	}
	return models, nil
}

type openAITool struct {
	Type     string `json:"type"`
	Function Tool   `json:"function"`
}

type geminiToolExtraContent struct {
	Google struct {
		ThoughtSignature string `json:"thought_signature,omitempty"`
	} `json:"google,omitempty"`
}

type openAIToolCall struct {
	Index        int                     `json:"index,omitempty"`
	ID           string                  `json:"id,omitempty"`
	Type         string                  `json:"type,omitempty"`
	ExtraContent *geminiToolExtraContent `json:"extra_content,omitempty"`
	Function     struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// openAIStreamToolCall mirrors openAIToolCall on the wire, but Index is a
// pointer so a missing "index" field (nil) can be distinguished from an
// explicit index 0. Some OpenAI-compatible providers (notably Gemini) reuse
// index 0 or omit it for parallel calls; keying accumulation on int index
// alone merges distinct calls into e.g. "todo_addtodo_addtodo_add".
type openAIStreamToolCall struct {
	Index        *int                    `json:"index"`
	ID           string                  `json:"id,omitempty"`
	Type         string                  `json:"type,omitempty"`
	ExtraContent *geminiToolExtraContent `json:"extra_content,omitempty"`
	Function     struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openAIMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ReasoningDetails json.RawMessage  `json:"reasoning_details,omitempty"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

func (p *openAIProvider) Complete(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	if p.name == "gemini" {
		if err := p.geminiValidateThinking(input.Model, input.Thinking); err != nil {
			return Response{}, err
		}
	}
	if p.name == "opencode-go" && openCodeGoModelRoutes[input.Model] == openCodeGoResponsesRoute {
		return p.completeResponses(ctx, input, onText)
	}
	if p.name == "opencode-go" && openCodeGoModelRoutes[input.Model] == openCodeGoMessagesRoute {
		return p.completeAnthropicMessages(ctx, input, onText)
	}
	capability := p.ThinkingCapability(input.Model)
	messages := make([]openAIMessage, 0, len(input.Messages))
	for _, message := range input.Messages {
		converted := openAIMessage{Role: message.Role, Content: openAIContent(message), Name: message.Name, ToolCallID: message.ToolCallID}
		if message.Role == "assistant" {
			switch capability.ReplayFormat {
			case ThinkingReplayReasoningContent:
				// DeepSeek-compatible models require this field on every
				// replayed assistant turn while thinking is active, including an
				// empty value for messages that did not emit visible reasoning.
				reasoning := message.Thinking
				converted.ReasoningContent = &reasoning
			case ThinkingReplayReasoningDetails:
				converted.ReasoningDetails = message.ReasoningDetails
			}
		}
		for i, call := range message.ToolCalls {
			item := openAIToolCall{Index: i, ID: call.ID, Type: "function"}
			if p.name == "gemini" && call.ThoughtSignature != "" {
				item.ExtraContent = &geminiToolExtraContent{}
				item.ExtraContent.Google.ThoughtSignature = call.ThoughtSignature
			}
			item.Function.Name = call.Name
			item.Function.Arguments = string(call.Arguments)
			converted.ToolCalls = append(converted.ToolCalls, item)
		}
		messages = append(messages, converted)
	}
	tools := make([]openAITool, 0, len(input.Tools))
	for _, tool := range input.Tools {
		parameters := tool.Parameters
		if p.name == "gemini" {
			// Gemini's OpenAI compatibility layer rejects additionalProperties
			// in function schemas with 400 INVALID_ARGUMENT. Shared schemas
			// keep it for other providers, so sanitize a copy here.
			parameters = geminiToolParameters(parameters)
		}
		tools = append(tools, openAITool{Type: "function", Function: Tool{Name: tool.Name, Description: tool.Description, Parameters: parameters}})
	}
	body := make(map[string]any, 8)
	if p.name != "gemini" {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	body["model"], body["messages"], body["stream"] = input.Model, messages, true
	// Gemini uses fixed sampling defaults (temperature 1.0, topP 0.95, topK 64);
	// custom values are ignored and strict validation can reject them, so omit
	// temperature on the Gemini route even when Request carries one.
	if input.Temperature != nil && p.name != "gemini" && (p.name != "openai" || !capability.Supported) {
		body["temperature"] = *input.Temperature
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	for field, value := range thinkingFields(capability, input.Thinking) {
		body[field] = value
	}
	// Session-stable cache control (#81): emit prompt_cache_key only on the
	// official OpenAI Chat Completions route. Custom base URLs, opencode-go
	// (all three routes share this adapter when name != "openai"), chatgpt,
	// and ollama must omit the field even when Request carries a key. The
	// existing x-opencode-session header behavior is unchanged. Never retry
	// after a cache-field rejection; surface the provider error normally.
	if p.name == "openai" && IsOfficialOpenAIEndpoint(p.baseURL) && input.PromptCacheKey != "" {
		body["prompt_cache_key"] = input.PromptCacheKey
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.sessionID != "" {
		req.Header.Set("x-opencode-session", p.sessionID)
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
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
	return parseOpenAIStream(resp.Body, onText)
}

func openAIContent(message Message) any {
	if len(message.Images) == 0 {
		return message.Content
	}
	parts := make([]map[string]any, 0, len(message.Images)+1)
	if message.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": message.Content})
	}
	for _, image := range message.Images {
		dataURL := "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": dataURL},
		})
	}
	return parts
}

// openAIUsageWire is the private Chat Completions usage shape. Pointer
// fields preserve presence: nil means unreported, a pointer to zero is an
// explicitly reported zero. Only the documented detail paths are mapped:
// prompt_tokens_details.cached_tokens and
// completion_tokens_details.reasoning_tokens. No write field is mapped on
// this route: the matrix documents no exact Chat Completions write wire
// path, and writes are never inferred from cache misses.
type openAIUsageWire struct {
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// mergeInto folds one usage snapshot into the accumulator by presence: only
// explicitly present fields replace earlier values. Missing or null fields
// leave earlier values intact, while an explicit zero replaces a previous
// value. UncachedInputTokens is never derived here; no exact subtraction is
// permitted without documented component semantics.
func (w *openAIUsageWire) mergeInto(acc *Usage) {
	if w == nil || acc == nil {
		return
	}
	if w.PromptTokens != nil {
		acc.InputTokens = w.PromptTokens
	}
	if w.CompletionTokens != nil {
		acc.OutputTokens = w.CompletionTokens
	}
	if w.PromptTokensDetails != nil && w.PromptTokensDetails.CachedTokens != nil {
		acc.CacheReadTokens = w.PromptTokensDetails.CachedTokens
	}
	if w.CompletionTokensDetails != nil && w.CompletionTokensDetails.ReasoningTokens != nil {
		acc.ReasoningTokens = w.CompletionTokensDetails.ReasoningTokens
	}
}

func parseOpenAIStream(reader io.Reader, onText StreamCallback) (Response, error) {
	result := Message{Role: "assistant"}
	// Usage snapshots are cumulative: each event carries the latest totals
	// for the fields it reports. Merge by presence and validate once at the
	// end so a later snapshot missing a field never erases earlier values.
	accumulated := &Usage{}
	type partialCall struct {
		id, name, arguments, thoughtSignature string
		sourceIndex                            int // original wire index, -1 when missing
	}
	partials := map[int]*partialCall{}
	order := []int{}
	byID := map[string]int{}
	sourceGroups := map[int][]int{}
	maxUsed := -1
	alloc := func(source int) int {
		maxUsed++
		slot := maxUsed
		partials[slot] = &partialCall{sourceIndex: source}
		order = append(order, slot)
		sourceGroups[source] = append(sourceGroups[source], slot)
		return slot
	}
	// ensureSlot returns the slot for an explicit wire index, creating it on
	// first use while keeping creation order for final emission.
	ensureExplicit := func(idx int) int {
		if part, ok := partials[idx]; ok {
			_ = part
			return idx
		}
		if idx > maxUsed {
			maxUsed = idx
		}
		partials[idx] = &partialCall{sourceIndex: idx}
		order = append(order, idx)
		sourceGroups[idx] = append(sourceGroups[idx], idx)
		return idx
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		// openAIUsageWire stays private to the adapter: wire JSON is never
		// decoded directly into the normalized Usage contract. Usage is
		// captured as raw JSON first so malformed usage metadata cannot
		// break text generation; it is decoded leniently per snapshot.
		var event struct {
			Usage json.RawMessage `json:"usage"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error,omitempty"`
			Choices []struct {
				Delta struct {
					Content          string                 `json:"content"`
					ReasoningContent string                 `json:"reasoning_content"`
					Reasoning        string                 `json:"reasoning"`
					ReasoningDetails json.RawMessage        `json:"reasoning_details"`
					ToolCalls        []openAIStreamToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Response{}, fmt.Errorf("decode provider stream: %w", err)
		}
		if len(event.Usage) != 0 {
			var wire openAIUsageWire
			if err := json.Unmarshal(event.Usage, &wire); err == nil {
				wire.mergeInto(accumulated)
			}
		}
		if event.Error != nil {
			return Response{}, errors.New(event.Error.Message)
		}
		for _, choice := range event.Choices {
			reasoning := choice.Delta.ReasoningContent
			if reasoning == "" {
				reasoning = choice.Delta.Reasoning
			}
			if reasoning != "" && onText != nil {
				onText(StreamEvent{Kind: StreamThinking, Text: reasoning})
			}
			result.Thinking += reasoning
			if len(choice.Delta.ReasoningDetails) > 0 {
				result.ReasoningDetails = append(json.RawMessage(nil), choice.Delta.ReasoningDetails...)
			}
			if choice.Delta.Content != "" {
				result.Content += choice.Delta.Content
				if onText != nil {
					onText(StreamEvent{Kind: StreamOutput, Text: choice.Delta.Content})
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				sig := ""
				if delta.ExtraContent != nil {
					sig = delta.ExtraContent.Google.ThoughtSignature
				}
				hasPayload := delta.ID != "" || delta.Function.Name != "" || delta.Function.Arguments != ""
				if delta.Index != nil {
					idx := *delta.Index
					// Route by ID first: a known call ID may arrive with a
					// different index after a provider re-orders chunks.
					if delta.ID != "" {
						if slot, ok := byID[delta.ID]; ok {
							part := partials[slot]
							if sig != "" {
								part.thoughtSignature = sig
							}
							part.name += delta.Function.Name
							part.arguments += delta.Function.Arguments
							continue
						}
					}
					if existing, ok := partials[idx]; ok {
						if sig != "" && !hasPayload {
							// Thought-signature-only delta: attach to the
							// first sibling from the same wire index that
							// still lacks a signature.
							target := idx
							for _, s := range sourceGroups[idx] {
								if partials[s].thoughtSignature == "" {
									target = s
									break
								}
							}
							partials[target].thoughtSignature = sig
							continue
						}
						if delta.ID != "" && existing.id != "" && delta.ID != existing.id {
							// Same wire index reused for a distinct call
							// (Gemini parallel calls). Split instead of
							// concatenating names/arguments.
							slot := alloc(idx)
							part := partials[slot]
							part.id = delta.ID
							byID[delta.ID] = slot
							if sig != "" {
								part.thoughtSignature = sig
							}
							part.name = delta.Function.Name
							part.arguments = delta.Function.Arguments
							continue
						}
						if delta.ID != "" && existing.id == "" && (existing.name != "" || existing.arguments != "") {
							// Existing slot has content but no ID and a new
							// ID arrives on the same index: distinct call.
							slot := alloc(idx)
							part := partials[slot]
							part.id = delta.ID
							byID[delta.ID] = slot
							if sig != "" {
								part.thoughtSignature = sig
							}
							part.name = delta.Function.Name
							part.arguments = delta.Function.Arguments
							continue
						}
						slot := ensureExplicit(idx)
						part := partials[slot]
						if delta.ID != "" {
							part.id = delta.ID
							byID[delta.ID] = slot
						}
						if sig != "" {
							part.thoughtSignature = sig
						}
						part.name += delta.Function.Name
						part.arguments += delta.Function.Arguments
						continue
					}
					// First use of this explicit index.
					if delta.ID != "" {
						if slot, ok := byID[delta.ID]; ok {
							part := partials[slot]
							if sig != "" {
								part.thoughtSignature = sig
							}
							part.name += delta.Function.Name
							part.arguments += delta.Function.Arguments
							continue
						}
					}
					slot := ensureExplicit(idx)
					part := partials[slot]
					if delta.ID != "" {
						part.id = delta.ID
						byID[delta.ID] = slot
					}
					if sig != "" {
						part.thoughtSignature = sig
					}
					part.name += delta.Function.Name
					part.arguments += delta.Function.Arguments
					continue
				}
				// Missing index: route by ID when available, otherwise by
				// shape. Standard continuations have empty name; a new
				// non-empty name after a completed call is a new call.
				if delta.ID != "" {
					if slot, ok := byID[delta.ID]; ok {
						part := partials[slot]
						if sig != "" {
							part.thoughtSignature = sig
						}
						part.name += delta.Function.Name
						part.arguments += delta.Function.Arguments
						continue
					}
					slot := alloc(-1)
					part := partials[slot]
					part.id = delta.ID
					byID[delta.ID] = slot
					if sig != "" {
						part.thoughtSignature = sig
					}
					part.name = delta.Function.Name
					part.arguments = delta.Function.Arguments
					continue
				}
				if sig != "" && !hasPayload {
					if len(order) == 0 {
						slot := alloc(-1)
						partials[slot].thoughtSignature = sig
						continue
					}
					target := order[len(order)-1]
					for _, s := range sourceGroups[-1] {
						if partials[s].thoughtSignature == "" {
							target = s
							break
						}
					}
					// Prefer a sibling from the missing-index group, but
					// fall back to the most recent slot overall so the
					// signature is not dropped.
					if len(sourceGroups[-1]) == 0 {
						for i := len(order) - 1; i >= 0; i-- {
							if partials[order[i]].thoughtSignature == "" {
								target = order[i]
								break
							}
						}
					}
					partials[target].thoughtSignature = sig
					continue
				}
				if len(order) == 0 {
					slot := alloc(-1)
					part := partials[slot]
					if sig != "" {
						part.thoughtSignature = sig
					}
					part.name = delta.Function.Name
					part.arguments = delta.Function.Arguments
					continue
				}
				last := order[len(order)-1]
				lastPart := partials[last]
				if delta.Function.Name != "" && lastPart.name != "" && lastPart.arguments != "" && json.Valid(json.RawMessage(lastPart.arguments)) {
					// Complete JSON followed by a new name with no ID/index:
					// likely a new call whose framing was dropped. Split
					// rather than concatenating into an unknown tool.
					slot := alloc(-1)
					part := partials[slot]
					if sig != "" {
						part.thoughtSignature = sig
					}
					part.name = delta.Function.Name
					part.arguments = delta.Function.Arguments
					continue
				}
				if sig != "" {
					lastPart.thoughtSignature = sig
				}
				lastPart.name += delta.Function.Name
				lastPart.arguments += delta.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	// Emit in first-seen order, not 0..len(map)-1, so sparse or split
	// indexes are never dropped.
	seen := make(map[int]bool, len(order))
	for _, slot := range order {
		if seen[slot] {
			continue
		}
		seen[slot] = true
		part := partials[slot]
		if part == nil {
			continue
		}
		// Skip empty placeholders that never received a name.
		if part.id == "" && part.name == "" && part.arguments == "" && part.thoughtSignature == "" {
			continue
		}
		arguments := json.RawMessage(part.arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: part.id, Name: part.name, Arguments: arguments, ThoughtSignature: part.thoughtSignature})
	}
	// Any explicit-index slot missed by order (defensive: should be none).
	rest := make([]int, 0, len(partials))
	for idx := range partials {
		if !seen[idx] {
			rest = append(rest, idx)
		}
	}
	sort.Ints(rest)
	for _, idx := range rest {
		part := partials[idx]
		if part == nil {
			continue
		}
		if part.id == "" && part.name == "" && part.arguments == "" && part.thoughtSignature == "" {
			continue
		}
		arguments := json.RawMessage(part.arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: part.id, Name: part.name, Arguments: arguments, ThoughtSignature: part.thoughtSignature})
	}
	return Response{Message: result, Usage: NormalizeUsage(accumulated)}, nil
}
