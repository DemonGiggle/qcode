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
	"strings"
)

const anthropicVersion = "2023-06-01"

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// completeAnthropicMessages adapts qcode's normalized conversation to the
// Anthropic Messages protocol used by the Qwen and MiniMax OpenCode Go routes.
func (p *openAIProvider) completeAnthropicMessages(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	system, messages := anthropicMessages(input.Messages)
	body := map[string]any{
		"model":      input.Model,
		"max_tokens": 8192,
		"stream":     true,
		"messages":   messages,
	}
	if system != "" {
		body["system"] = system
	}
	if len(input.Tools) > 0 {
		tools := make([]anthropicTool, 0, len(input.Tools))
		for _, tool := range input.Tools {
			tools = append(tools, anthropicTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.Parameters})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if p.userAgent != "" {
		req.Header.Set("User-Agent", p.userAgent)
	}
	if p.sessionID != "" {
		req.Header.Set("x-opencode-session", p.sessionID)
	}
	if p.apiKey != "" {
		req.Header.Set("x-api-key", p.apiKey)
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
	return parseAnthropicStream(resp.Body, onText)
}

func anthropicMessages(input []Message) (string, []anthropicMessage) {
	var systemParts []string
	output := make([]anthropicMessage, 0, len(input))
	appendContent := func(role string, block any) {
		if len(output) > 0 && output[len(output)-1].Role == role {
			if blocks, ok := output[len(output)-1].Content.([]map[string]any); ok {
				output[len(output)-1].Content = append(blocks, block.(map[string]any))
				return
			}
		}
		output = append(output, anthropicMessage{Role: role, Content: []map[string]any{block.(map[string]any)}})
	}
	for _, message := range input {
		switch message.Role {
		case "system":
			if message.Content != "" {
				systemParts = append(systemParts, message.Content)
			}
		case "tool":
			appendContent("user", map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content})
		case "assistant":
			blocks := make([]map[string]any, 0, len(message.ToolCalls)+1)
			if message.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": message.Content})
			}
			for _, call := range message.ToolCalls {
				input := json.RawMessage(call.Arguments)
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
			}
			if len(blocks) > 0 {
				output = append(output, anthropicMessage{Role: "assistant", Content: blocks})
			}
		default: // user and forward-compatible normalized roles
			output = append(output, anthropicMessage{Role: "user", Content: anthropicContent(message)})
		}
	}
	return strings.Join(systemParts, "\n\n"), output
}

func anthropicContent(message Message) any {
	if len(message.Images) == 0 {
		return message.Content
	}
	parts := make([]map[string]any, 0, len(message.Images)+1)
	if message.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": message.Content})
	}
	for _, image := range message.Images {
		parts = append(parts, map[string]any{
			"type":   "image",
			"source": map[string]any{"type": "base64", "media_type": image.MediaType, "data": base64.StdEncoding.EncodeToString(image.Data)},
		})
	}
	return parts
}

// anthropicUsageWire is the private Anthropic Messages usage shape shared by
// the OpenCode Go Messages route. Pointer fields preserve presence: nil means
// unreported, a pointer to zero is an explicitly reported zero. The Anthropic
// wire separates logical input into three components:
//
//	input_tokens (uncached/non-cache input),
//	cache_read_input_tokens,
//	cache_creation_input_tokens (aggregate cache write).
//
// The normalized complete InputTokens is derived as the sum of the three
// components ONLY when all three are known. A gateway that reports only
// input_tokens therefore yields known uncached input with unknown complete
// input. TTL-specific subcounts inside cache_creation are intentionally
// ignored: the aggregate creation count is used once and never added to TTL
// breakdowns, and no pricing is synthesized from TTLs. No cache-control
// request fields are emitted in this issue.
type anthropicUsageWire struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// mergeInto folds one usage snapshot into the accumulator by presence: only
// explicitly present fields replace earlier values. Missing or null fields
// leave earlier values intact, while an explicit zero replaces a previous
// value. Wire input_tokens maps to UncachedInputTokens (never directly to
// complete InputTokens); reads map to CacheReadTokens and aggregate creation
// maps to CacheWriteTokens. TTL subcounts are ignored. Complete InputTokens
// is computed once at the end, not here.
func (w *anthropicUsageWire) mergeInto(acc *Usage) {
	if w == nil || acc == nil {
		return
	}
	if w.InputTokens != nil {
		acc.UncachedInputTokens = w.InputTokens
	}
	if w.OutputTokens != nil {
		acc.OutputTokens = w.OutputTokens
	}
	if w.CacheReadInputTokens != nil {
		acc.CacheReadTokens = w.CacheReadInputTokens
	}
	if w.CacheCreationInputTokens != nil {
		acc.CacheWriteTokens = w.CacheCreationInputTokens
	}
}

func parseAnthropicStream(reader io.Reader, onText StreamCallback) (Response, error) {
	result := Message{Role: "assistant"}
	// Usage snapshots are cumulative: message_start carries the input side
	// and message_delta carries later output updates. Merge by presence and
	// validate once at the end so a later snapshot missing a field never
	// erases earlier values. Output updates replace earlier output counts;
	// successive deltas are never added.
	accumulated := &Usage{}
	type partialTool struct {
		id, name, input, initial string
		receivedDelta            bool
	}
	tools := map[int]*partialTool{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		// anthropicUsageWire stays private to the adapter: wire JSON is never
		// decoded directly into the normalized Usage contract. Usage is
		// captured as raw JSON first so malformed usage metadata cannot
		// break text generation; it is decoded leniently per snapshot.
		var event struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Message *struct {
				Usage json.RawMessage `json:"usage"`
			} `json:"message"`
			Usage        json.RawMessage `json:"usage"`
			ContentBlock *struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return Response{}, fmt.Errorf("decode Anthropic stream: %w", err)
		}
		if event.Error != nil {
			return Response{}, fmt.Errorf("Anthropic: %s", event.Error.Message)
		}
		if event.Message != nil && len(event.Message.Usage) != 0 {
			var wire anthropicUsageWire
			if err := json.Unmarshal(event.Message.Usage, &wire); err == nil {
				wire.mergeInto(accumulated)
			}
		}
		if len(event.Usage) != 0 {
			var wire anthropicUsageWire
			if err := json.Unmarshal(event.Usage, &wire); err == nil {
				wire.mergeInto(accumulated)
			}
		}
		if event.Type == "content_block_start" && event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			part := &partialTool{id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			if len(event.ContentBlock.Input) > 0 {
				part.initial = string(event.ContentBlock.Input)
			}
			tools[event.Index] = part
		}
		if event.Type == "content_block_delta" && event.Delta != nil {
			switch event.Delta.Type {
			case "text_delta":
				result.Content += event.Delta.Text
				if onText != nil {
					onText(StreamEvent{Kind: StreamOutput, Text: event.Delta.Text})
				}
			case "input_json_delta":
				if part := tools[event.Index]; part != nil {
					part.receivedDelta = true
					part.input += event.Delta.PartialJSON
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Response{}, err
	}
	for index := 0; index < len(tools); index++ {
		part := tools[index]
		if part == nil {
			continue
		}
		if !part.receivedDelta {
			part.input = part.initial
		}
		arguments := json.RawMessage(part.input)
		if !json.Valid(arguments) {
			arguments = json.RawMessage(`{}`)
		}
		result.ToolCalls = append(result.ToolCalls, ToolCall{ID: part.id, Name: part.name, Arguments: arguments})
	}
	anthropicFinalizeUsage(accumulated)
	return Response{Message: result, Usage: NormalizeUsage(accumulated)}, nil
}

// anthropicFinalizeUsage derives the complete logical InputTokens as the sum
// of the three separate Anthropic components (uncached + read + write) only
// when all three are known. Missing components stay unknown: there is no
// route-specific guarantee of zero on the OpenCode Go Messages route, so an
// input-only legacy payload keeps known uncached input with unknown complete
// input. The sum is computed only for non-negative components; negative or
// inconsistent values are left to NormalizeUsage sanitization.
func anthropicFinalizeUsage(acc *Usage) {
	if acc == nil {
		return
	}
	if acc.InputTokens != nil {
		return
	}
	if acc.UncachedInputTokens == nil || acc.CacheReadTokens == nil || acc.CacheWriteTokens == nil {
		return
	}
	uncached, read, write := *acc.UncachedInputTokens, *acc.CacheReadTokens, *acc.CacheWriteTokens
	if uncached < 0 || read < 0 || write < 0 {
		return
	}
	sum := uncached + read + write
	acc.InputTokens = &sum
}
