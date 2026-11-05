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
func (p *openAIProvider) completeResponses(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	// Reasoning Responses models reject temperature (Luna returns 400).
	// Omit it and keep the provider default, as the Anthropic adapter does.
	body := map[string]any{
		"model":  input.Model,
		"input":  responsesInput(input.Messages),
		"stream": true,
	}
	for field, value := range thinkingFields(p.ThinkingCapability(input.Model), input.Thinking) {
		body[field] = value
	}
	if len(input.Tools) > 0 {
		tools := make([]map[string]any, 0, len(input.Tools))
		for _, tool := range input.Tools {
			tools = append(tools, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
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
		return Response{}, fmt.Errorf("provider returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return parseResponsesStream(resp.Body, onText)
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

func parseResponsesStream(reader io.Reader, onText StreamCallback) (Response, error) {
	result := Message{Role: "assistant"}
	// Usage snapshots are cumulative: each completed event carries the
	// latest totals for the fields it reports. Merge by presence and
	// validate once at the end so a later snapshot missing a field never
	// erases earlier values.
	accumulated := &Usage{}
	var completedOutput []json.RawMessage
	partials := map[int]*responsesPartialCall{}
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
			OutputIndex int    `json:"output_index"`
			Item        struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"item"`
			Response struct {
				Output []json.RawMessage `json:"output"`
				// Captured raw so malformed usage metadata cannot break
				// text generation; it is decoded leniently per snapshot.
				// responsesUsageWire stays private: wire JSON is never
				// decoded directly into the normalized Usage contract.
				Usage json.RawMessage `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Response{}, fmt.Errorf("decode Responses stream: %w", err)
		}
		if event.Error != nil {
			return Response{}, fmt.Errorf("Responses: %s", event.Error.Message)
		}
		if event.Type == "error" {
			if event.Message == "" {
				return Response{}, fmt.Errorf("Responses: stream error")
			}
			return Response{}, fmt.Errorf("Responses: %s", event.Message)
		}
		if event.Type == "response.failed" {
			if event.Response.Error != nil && event.Response.Error.Message != "" {
				return Response{}, fmt.Errorf("Responses: %s", event.Response.Error.Message)
			}
			return Response{}, fmt.Errorf("Responses: response failed")
		}
		switch event.Type {
		case "response.output_text.delta":
			result.Content += event.Delta
			if onText != nil {
				onText(StreamEvent{Kind: StreamOutput, Text: event.Delta})
			}
		case "response.reasoning_summary_text.delta":
			result.Thinking += event.Delta
			if onText != nil {
				onText(StreamEvent{Kind: StreamThinking, Text: event.Delta})
			}
		case "response.output_item.added":
			if event.Item.Type == "function_call" {
				partials[event.OutputIndex] = &responsesPartialCall{id: event.Item.CallID, name: event.Item.Name, arguments: event.Item.Arguments}
			}
		case "response.function_call_arguments.delta":
			part := partials[event.OutputIndex]
			if part == nil {
				part = &responsesPartialCall{}
				partials[event.OutputIndex] = part
			}
			part.arguments += event.Delta
		case "response.completed":
			completedOutput = event.Response.Output
			if len(event.Response.Usage) != 0 {
				var wire responsesUsageWire
				if err := json.Unmarshal(event.Response.Usage, &wire); err == nil {
					wire.mergeInto(accumulated)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	if len(completedOutput) > 0 {
		result.ReasoningDetails, _ = json.Marshal(completedOutput)
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
	return Response{Message: result, Usage: NormalizeUsage(accumulated)}, nil
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
			}
		}
	}
}
