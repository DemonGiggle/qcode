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
	"sort"
	"strings"
)

type responsesPartialCall struct{ id, name, arguments string }

// completeResponses adapts qcode's normalized turn representation to the
// OpenAI Responses protocol used by the OpenCode Go Muse, Luna, and Grok
// routes. It intentionally lives beside, rather than inside, the older Chat
// Completions adapter: the two APIs have different continuation semantics.
// Thinking levels are sent as the Responses reasoning object when the model
// advertises them.
// Cache policy (#81): this route never emits prompt_cache_key, even when
// Request carries a key. Only the official OpenAI Chat Completions route may
// transmit that field.
func (p *openAIProvider) completeResponses(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	// Reasoning Responses models reject temperature (Luna returns 400).
	// Omit it and keep the provider default, as the Anthropic adapter does.
	inputItems := responsesInput(input.Messages)
	body := buildResponsesBody(input.Model, p.ThinkingCapability(input.Model), input, inputItems, "", false)
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	resp, err := p.postResponses(ctx, payload, onText)
	if err == nil {
		return resp, nil
	}
	// Preserve partial accounting: postResponses returns the accumulated
	// content/usage/id/diagnostics alongside terminal errors so
	// recordResponsesResult still counts billed usage.
	// Opt-in fallback: a comparison or continuation rejection happens before
	// execution, so retrying once without the rejected opt-in field cannot
	// double-bill. Strip only the rejected dimension so the surviving opt-in
	// (comparison vs continuation) is preserved.
	if shouldRetryResponsesWithoutOption(err, input) {
		se, _ := asResponsesStatusError(err)
		lower := strings.ToLower(se.body)
		clean := input
		if clean.ComparisonResponseID != "" && (strings.Contains(lower, "prompt_cache_options") || strings.Contains(lower, "comparison_response")) {
			clean.ComparisonResponseID = ""
		}
		if (clean.PreviousResponseID != "" || clean.ConversationID != "") && (strings.Contains(lower, "previous_response") || strings.Contains(lower, "conversation")) {
			clean.PreviousResponseID = ""
			clean.ConversationID = ""
			// Builder derives store:true from continuation ids, so clearing
			// the ids returns to stateless unless StoreResponses is set.
		}
		if responsesStoreRejection(lower) {
			clean.StoreResponses = false
			// If store itself was rejected, drop continuation as well since
			// chaining implies store:true.
			if strings.Contains(lower, "previous_response") || strings.Contains(lower, "conversation") || clean.PreviousResponseID != "" || clean.ConversationID != "" {
				clean.PreviousResponseID = ""
				clean.ConversationID = ""
			}
		}
		// shouldRetry ensures at least one dimension matched, so clean
		// differs from input here; no generic fallback needed.
		cleanBody := buildResponsesBody(clean.Model, p.ThinkingCapability(clean.Model), clean, inputItems, "", false)
		cleanPayload, merr := json.Marshal(cleanBody)
		if merr != nil {
			return resp, merr
		}
		return p.postResponses(ctx, cleanPayload, onText)
	}
	return resp, err
}

func (p *openAIProvider) postResponses(ctx context.Context, payload []byte, onText StreamCallback) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/responses", bytes.NewReader(payload))
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
		return Response{}, &responsesStatusError{statusCode: resp.StatusCode, status: resp.Status, body: strings.TrimSpace(string(data))}
	}
	return parseResponsesStream(resp.Body, onText)
}

// responsesStatusError preserves the HTTP status for retry decisions while
// keeping the user-facing "provider returned ..." message unchanged.
type responsesStatusError struct {
	statusCode int
	status     string
	body       string
}

func (e *responsesStatusError) Error() string {
	return fmt.Sprintf("provider returned %s: %s", e.status, e.body)
}

func asResponsesStatusError(err error) (responsesStatusError, bool) {
	if err == nil {
		return responsesStatusError{}, false
	}
	for cur := err; cur != nil; {
		if v, ok := cur.(*responsesStatusError); ok {
			return *v, true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	// Also accept the chatgpt variant so shared helpers work across providers.
	if se, ok := asChatGPTStatusError(err); ok {
		return responsesStatusError{statusCode: se.statusCode, status: se.status, body: se.body}, true
	}
	return responsesStatusError{}, false
}

// responsesStoreRejection requires a param-shaped store match so unrelated
// 400s mentioning "restore"/"storefront" don't trigger a misleading retry.
func responsesStoreRejection(lower string) bool {
	if !strings.Contains(lower, "store") {
		return false
	}
	// Explicit param shape ("store":, param":"store) always counts.
	if strings.Contains(lower, "\"store\"") || strings.Contains(lower, "param\":\"store") || strings.Contains(lower, "'store'") {
		return true
	}
	// Otherwise require error wording so "restore succeeded" doesn't match.
	return strings.Contains(lower, "unsupported") || strings.Contains(lower, "not supported") || strings.Contains(lower, "invalid") || strings.Contains(lower, "unknown")
}

// shouldRetryResponsesWithoutOption reports whether err is a pre-execution
// rejection of an opt-in Responses field, in which case stripping that field
// and retrying once with the same history cannot double-bill.
func shouldRetryResponsesWithoutOption(err error, input Request) bool {
	se, ok := asResponsesStatusError(err)
	if !ok {
		return false
	}
	if se.statusCode != http.StatusBadRequest && se.statusCode != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(se.body)
	if input.ComparisonResponseID != "" && (strings.Contains(lower, "prompt_cache_options") || strings.Contains(lower, "comparison_response")) {
		return true
	}
	if input.PreviousResponseID != "" && (strings.Contains(lower, "previous_response") || strings.Contains(lower, "conversation")) {
		return true
	}
	if input.ConversationID != "" && strings.Contains(lower, "conversation") {
		return true
	}
	if (input.StoreResponses || input.PreviousResponseID != "" || input.ConversationID != "") && responsesStoreRejection(lower) {
		return true
	}
	return false
}

// allowedResponsesBuiltins is the closed set of server-side tool types qcode
// forwards. Unknown types are dropped so a typo cannot silently change the
// cached tool prefix.
var allowedResponsesBuiltins = map[string]bool{
	"web_search":       true,
	"file_search":      true,
	"code_interpreter": true,
	"computer_use":     true,
	"image_generation": true,
	"mcp":              true,
}

// responsesBuiltinTools filters passthrough built-in tools to the known set.
func responsesBuiltinTools(extra []map[string]any) []map[string]any {
	var out []map[string]any
	for _, tool := range extra {
		if tool == nil {
			continue
		}
		typ, _ := tool["type"].(string)
		if !allowedResponsesBuiltins[typ] {
			continue
		}
		out = append(out, tool)
	}
	return out
}

// buildResponsesBody is the shared Responses request builder used by the
// openai/opencode-go and chatgpt providers (issues #115/#116). Zero-value
// opt-ins are omitted so the default wire stays stateless +
// implicit-cache-only: store:false, full history resend, function tools only,
// no comparison, no structured-output overrides.
//
// inputItems is the already-encoded input array (chatgpt prepends durable
// instructions; other routes pass responsesInput directly). cacheKey is the
// session-stable prompt_cache_key; it is only emitted when official==true and
// non-empty, preserving the #81/Codex-parity policy.
func buildResponsesBody(model string, capability ThinkingCapability, input Request, inputItems []any, cacheKey string, official bool) map[string]any {
	body := map[string]any{
		"model":  model,
		"input":  inputItems,
		"stream": true,
	}
	// Stateful continuation is opt-in. A previous_response_id chain requires
	// server-side storage, so requesting a continuation implies store:true
	// even when the caller only set PreviousResponseID. Otherwise store is
	// omitted so the opencode-go Responses route keeps its previous wire
	// (no store key, server default); the chatgpt wrapper re-adds
	// store:false to preserve the SIWC stateless contract.
	if input.StoreResponses || input.PreviousResponseID != "" || input.ConversationID != "" {
		body["store"] = true
	}
	if input.PreviousResponseID != "" {
		body["previous_response_id"] = input.PreviousResponseID
	}
	if input.ConversationID != "" {
		body["conversation"] = input.ConversationID
	}
	fields := thinkingFields(capability, input.Thinking)
	if input.ReasoningSummary != "" {
		// Override the summary detail while keeping the effort from
		// thinkingFields. When no effort is set, synthesize the minimal
		// reasoning object so the summary override still has an effect.
		var reasoning map[string]any
		if raw, ok := fields["reasoning"].(map[string]any); ok {
			reasoning = raw
		} else {
			reasoning = map[string]any{}
			fields = map[string]any{"reasoning": reasoning}
			if len(fields) == 1 && len(reasoning) == 0 {
				// keep fields non-nil; reasoning effort omitted preserves default
			}
		}
		reasoning["summary"] = input.ReasoningSummary
	}
	for field, value := range fields {
		// Skip empty reasoning objects (no effort and no summary override).
		if field == "reasoning" {
			if m, ok := value.(map[string]any); ok && len(m) == 0 {
				continue
			}
		}
		body[field] = value
	}
	if official && cacheKey != "" {
		body["prompt_cache_key"] = cacheKey
	}
	if input.ComparisonResponseID != "" {
		body["prompt_cache_options"] = map[string]any{"comparison_response_id": input.ComparisonResponseID}
	}
	tools := make([]map[string]any, 0, len(input.Tools)+len(input.BuiltinTools))
	for _, tool := range input.Tools {
		item := map[string]any{"type": "function", "name": tool.Name}
		if tool.Description != "" {
			item["description"] = tool.Description
		}
		if tool.Parameters != nil {
			item["parameters"] = tool.Parameters
		}
		tools = append(tools, item)
	}
	tools = append(tools, responsesBuiltinTools(input.BuiltinTools)...)
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if input.ToolChoice != nil {
		body["tool_choice"] = input.ToolChoice
	}
	if input.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *input.ParallelToolCalls
	}
	if input.ResponseFormat != nil {
		format := map[string]any{"type": input.ResponseFormat.Type}
		if input.ResponseFormat.Name != "" {
			format["name"] = input.ResponseFormat.Name
		}
		if input.ResponseFormat.Schema != nil {
			format["schema"] = input.ResponseFormat.Schema
		}
		if input.ResponseFormat.Strict != nil {
			format["strict"] = *input.ResponseFormat.Strict
		}
		text := map[string]any{"format": format}
		if input.Verbosity != "" {
			text["verbosity"] = input.Verbosity
		}
		body["text"] = text
	} else if input.Verbosity != "" {
		body["text"] = map[string]any{"verbosity": input.Verbosity}
	}
	if input.MaxOutputTokens != nil {
		body["max_output_tokens"] = *input.MaxOutputTokens
	}
	if input.ServiceTier != "" {
		body["service_tier"] = input.ServiceTier
	}
	if input.Metadata != nil {
		body["metadata"] = input.Metadata
	}
	if input.Background != nil {
		body["background"] = *input.Background
	}
	if input.Truncation != "" {
		body["truncation"] = input.Truncation
	}
	if len(input.Include) > 0 {
		body["include"] = input.Include
	}
	return body
}

func responsesInput(messages []Message) []any {
	items := make([]any, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case "tool":
			items = append(items, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		case "assistant":
			// A completed Responses turn can contain encrypted reasoning and
			// function-call items needed by a later tool continuation. Replaying
			// the original output items is more reliable than reconstructing them.
			if len(message.ReasoningDetails) > 0 && json.Valid(message.ReasoningDetails) {
				var saved []json.RawMessage
				if json.Unmarshal(message.ReasoningDetails, &saved) == nil {
					for _, item := range saved {
						items = append(items, item)
					}
					continue
				}
			}
			// Skip empty assistant text when tool calls follow: the
			// function_call items already carry the turn. Emitting an empty
			// output_text alongside them is redundant.
			if strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) == 0 {
				items = append(items, responsesMessage("assistant", message.Content, nil))
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(call.Arguments)})
			}
		default:
			items = append(items, responsesMessage(message.Role, message.Content, message.Images))
		}
	}
	return items
}

func responsesMessage(role, text string, images []Image) map[string]any {
	contentType := "input_text"
	if role == "assistant" {
		// The Responses API validates assistant message content strictly:
		// only output_text (and refusal) are accepted. Sending input_text
		// here fails the second turn (after tool calls) with
		// "Invalid value: 'input_text' ... param: input[1].content[0]".
		contentType = "output_text"
	}
	content := make([]map[string]any, 0, len(images)+1)
	if text != "" || len(images) == 0 {
		content = append(content, map[string]any{"type": contentType, "text": text})
	}
	for _, image := range images {
		content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + image.MediaType + ";base64," + base64.StdEncoding.EncodeToString(image.Data)})
	}
	return map[string]any{"role": role, "content": content}
}

// responsesUsageWire is the private Responses usage shape shared by the
// ChatGPT and OpenCode Go Responses routes. Pointer fields preserve
// presence: nil means unreported, a pointer to zero is an explicitly
// reported zero. Only documented detail paths are mapped:
// input_tokens_details.cached_tokens,
// input_tokens_details.cache_write_tokens (when explicitly present), and
// output_tokens_details.reasoning_tokens. No other write path is mapped;
// writes are never inferred from cache misses.
type responsesUsageWire struct {
	InputTokens        *int `json:"input_tokens"`
	OutputTokens       *int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     *int `json:"cached_tokens"`
		CacheWriteTokens *int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

// mergeInto folds one usage snapshot into the accumulator by presence: only
// explicitly present fields replace earlier values. Missing or null fields
// leave earlier values intact, while an explicit zero replaces a previous
// value. UncachedInputTokens is never derived here; no exact subtraction is
// permitted without documented component semantics.
func (w *responsesUsageWire) mergeInto(acc *Usage) {
	if w == nil || acc == nil {
		return
	}
	if w.InputTokens != nil {
		acc.InputTokens = w.InputTokens
	}
	if w.OutputTokens != nil {
		acc.OutputTokens = w.OutputTokens
	}
	if w.InputTokensDetails != nil {
		if w.InputTokensDetails.CachedTokens != nil {
			acc.CacheReadTokens = w.InputTokensDetails.CachedTokens
		}
		if w.InputTokensDetails.CacheWriteTokens != nil {
			acc.CacheWriteTokens = w.InputTokensDetails.CacheWriteTokens
		}
	}
	if w.OutputTokensDetails != nil && w.OutputTokensDetails.ReasoningTokens != nil {
		acc.ReasoningTokens = w.OutputTokensDetails.ReasoningTokens
	}
}

// responsesDiagnosticsWire is the private prompt_cache_diagnostics shape.
// Pointer counts preserve presence; type/reason are plain strings.
type responsesDiagnosticsWire struct {
	Type                     string `json:"type"`
	Reason                   string `json:"reason"`
	ComparisonReusableTokens *int   `json:"comparison_reusable_tokens"`
	CacheMissedTokens        *int   `json:"cache_missed_tokens"`
}

func (w *responsesDiagnosticsWire) toPublic() *ResponsesCacheDiagnostics {
	if w == nil || w.Type == "" {
		return nil
	}
	return &ResponsesCacheDiagnostics{
		Type:                     w.Type,
		Reason:                   w.Reason,
		ComparisonReusableTokens: w.ComparisonReusableTokens,
		CacheMissedTokens:        w.CacheMissedTokens,
	}
}

// ResponsesIncompleteError reports a terminal incomplete response while
// preserving the partial completion for accounting. Reason is the server's
// incomplete_details.reason (for example max_output_tokens, content_filter).
type ResponsesIncompleteError struct {
	Reason   string
	Response Response
}

func (e *ResponsesIncompleteError) Error() string {
	if e == nil || e.Reason == "" {
		return "Responses: response incomplete"
	}
	return "Responses: response incomplete (" + e.Reason + ")"
}

// ResponsesFailedError reports a terminal failed response while preserving
// the partial completion for accounting.
type ResponsesFailedError struct {
	Message  string
	Response Response
}

func (e *ResponsesFailedError) Error() string {
	if e == nil || e.Message == "" {
		return "Responses: response failed"
	}
	return "Responses: " + e.Message
}

func parseResponsesStream(reader io.Reader, onText StreamCallback) (Response, error) {
	result := Message{Role: "assistant"}
	// Usage snapshots are cumulative: each completed event carries the
	// latest totals for the fields it reports. Merge by presence and
	// validate once at the end so a later snapshot missing a field never
	// erases earlier values.
	accumulated := &Usage{}
	var completedOutput []json.RawMessage
	var responseID string
	var diagnostics *ResponsesCacheDiagnostics
	partials := map[int]*responsesPartialCall{}
	finalize := func() Response {
		if len(completedOutput) > 0 {
			// Preserve output items for replay; ignore marshal errors.
			if details, err := json.Marshal(completedOutput); err == nil {
				result.ReasoningDetails = details
			}
			responsesCompletedOutput(&result, completedOutput, partials)
		}
		indexes := make([]int, 0, len(partials))
		for index := range partials {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			part := partials[index]
			if part.name == "" {
				continue
			}
			arguments := json.RawMessage(part.arguments)
			if !json.Valid(arguments) {
				arguments = json.RawMessage(`{}`)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{ID: part.id, Name: part.name, Arguments: arguments})
		}
		return Response{Message: result, Usage: NormalizeUsage(accumulated), ResponseID: responseID, CacheDiagnostics: diagnostics}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type        string `json:"type"`
			Delta       string `json:"delta"`
			Text        string `json:"text"`
			Arguments   string `json:"arguments"`
			OutputIndex int    `json:"output_index"`
			Item        struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response struct {
				ID     string            `json:"id"`
				Output []json.RawMessage `json:"output"`
				// Captured raw so malformed usage metadata cannot break
				// text generation; it is decoded leniently per snapshot.
				// responsesUsageWire stays private: wire JSON is never
				// decoded directly into the normalized Usage contract.
				Usage             json.RawMessage `json:"usage"`
				Diagnostics       json.RawMessage `json:"prompt_cache_diagnostics"`
				Status            string          `json:"status"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Response{}, fmt.Errorf("decode Responses stream: %w", err)
		}
		if event.Error != nil {
			partial := finalize()
			return partial, fmt.Errorf("Responses: %s", event.Error.Message)
		}
		if event.Type == "error" {
			partial := finalize()
			if event.Message == "" {
				return partial, fmt.Errorf("Responses: stream error")
			}
			return partial, fmt.Errorf("Responses: %s", event.Message)
		}
		if event.Type == "response.failed" {
			msg := ""
			if event.Response.Error != nil {
				msg = event.Response.Error.Message
			}
			partial := finalize()
			return partial, &ResponsesFailedError{Message: msg, Response: partial}
		}
		if event.Type == "response.incomplete" {
			reason := ""
			if event.Response.IncompleteDetails != nil {
				reason = event.Response.IncompleteDetails.Reason
			}
			if reason == "" && event.IncompleteDetails != nil {
				reason = event.IncompleteDetails.Reason
			}
			partial := finalize()
			return partial, &ResponsesIncompleteError{Reason: reason, Response: partial}
		}
		switch event.Type {
		case "response.output_text.delta":
			result.Content += event.Delta
			if onText != nil {
				onText(StreamEvent{Kind: StreamOutput, Text: event.Delta})
			}
		case "response.output_text.done":
			if event.Text != "" && !strings.Contains(result.Content, event.Text) {
				// Done carries the full text; delta already streamed it, so
				// only fill when delta path produced nothing (defensive).
				if result.Content == "" {
					result.Content = event.Text
				}
			}
		case "response.refusal.delta":
			// Refusals are terminal model text: surface them like output so
			// callers never see an empty completion with no explanation.
			result.Content += event.Delta
			if onText != nil {
				onText(StreamEvent{Kind: StreamOutput, Text: event.Delta})
			}
		case "response.refusal.done":
			if event.Text != "" && result.Content == "" {
				result.Content = event.Text
				if onText != nil {
					onText(StreamEvent{Kind: StreamOutput, Text: event.Text})
				}
			}
		case "response.reasoning_summary_text.delta":
			result.Thinking += event.Delta
			if onText != nil {
				onText(StreamEvent{Kind: StreamThinking, Text: event.Delta})
			}
		case "response.reasoning_summary_text.done":
			if event.Text != "" && result.Thinking == "" {
				result.Thinking = event.Text
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				partials[event.OutputIndex] = &responsesPartialCall{id: event.Item.CallID, name: event.Item.Name, arguments: event.Item.Arguments}
			}
		case "response.output_item.done":
			// Done repeats the completed item; fill any gaps the delta path missed.
			if event.Item.Type == "function_call" {
				part := partials[event.OutputIndex]
				if part == nil {
					part = &responsesPartialCall{}
					partials[event.OutputIndex] = part
				}
				if part.id == "" {
					part.id = event.Item.CallID
				}
				if part.name == "" {
					part.name = event.Item.Name
				}
				if part.arguments == "" {
					part.arguments = event.Item.Arguments
				}
			}
		case "response.function_call_arguments.delta":
			part := partials[event.OutputIndex]
			if part == nil {
				part = &responsesPartialCall{}
				partials[event.OutputIndex] = part
			}
			part.arguments += event.Delta
		case "response.function_call_arguments.done":
			doneArgs := event.Arguments
			if doneArgs == "" {
				doneArgs = event.Text
			}
			if doneArgs != "" {
				part := partials[event.OutputIndex]
				if part == nil {
					part = &responsesPartialCall{}
					partials[event.OutputIndex] = part
				}
				if part.arguments == "" {
					part.arguments = doneArgs
				}
			}
		case "response.completed":
			completedOutput = event.Response.Output
			if event.Response.ID != "" {
				responseID = event.Response.ID
			}
			if len(event.Response.Usage) != 0 {
				var wire responsesUsageWire
				if err := json.Unmarshal(event.Response.Usage, &wire); err == nil {
					wire.mergeInto(accumulated)
				}
			}
			if len(event.Response.Diagnostics) != 0 && string(event.Response.Diagnostics) != "null" {
				var wire responsesDiagnosticsWire
				if err := json.Unmarshal(event.Response.Diagnostics, &wire); err == nil {
					if diag := wire.toPublic(); diag != nil {
						diagnostics = diag
					}
				}
			}
			if event.Response.Status == "incomplete" {
				reason := ""
				if event.Response.IncompleteDetails != nil {
					reason = event.Response.IncompleteDetails.Reason
				}
				partial := finalize()
				return partial, &ResponsesIncompleteError{Reason: reason, Response: partial}
			}
			if event.Response.Status == "failed" {
				msg := ""
				if event.Response.Error != nil {
					msg = event.Response.Error.Message
				}
				partial := finalize()
				return partial, &ResponsesFailedError{Message: msg, Response: partial}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		partial := finalize()
		return partial, err
	}
	return finalize(), nil
}

func responsesCompletedOutput(result *Message, output []json.RawMessage, partials map[int]*responsesPartialCall) {
	for index, raw := range output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "function_call" {
			part := partials[index]
			if part == nil {
				part = &responsesPartialCall{}
				partials[index] = part
			}
			if part.id == "" {
				part.id = item.CallID
			}
			if part.name == "" {
				part.name = item.Name
			}
			if part.arguments == "" {
				part.arguments = item.Arguments
			}
			continue
		}
		if item.Type == "message" && result.Content == "" {
			for _, block := range item.Content {
				if block.Type == "output_text" {
					result.Content += block.Text
				}
				if block.Type == "refusal" {
					result.Content += block.Text
				}
			}
		}
	}
}
