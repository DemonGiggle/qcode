package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"qcode/internal/chatgptoauth"
)

// chatGPTBaseURL is the subscription (Codex) endpoint, not the platform
// api.openai.com API. The Responses wire format is reused with a Codex
// request contract: top-level instructions, store:false, and the
// ChatGPT-Account-Id header.
const chatGPTBaseURL = "https://chatgpt.com/backend-api/codex"

// chatGPTClientVersion is sent as ?client_version= on the Codex models
// catalog request. The backend gates the catalog behind this query param
// (400 without it) and keys defaults to the requesting Codex version.
const chatGPTClientVersion = "0.166.0"

type chatGPTProvider struct {
	baseURL    string
	tokensPath string
	client     HTTPDoer
}

func init() {
	Register("chatgpt", newChatGPT)
}

func newChatGPT(config Config) (Provider, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = chatGPTBaseURL
	}
	return &chatGPTProvider{baseURL: baseURL, tokensPath: config.ChatGPTOAuthPath, client: httpClient(config)}, nil
}

// Name returns the registry name.
func (p *chatGPTProvider) Name() string { return "chatgpt" }

// ValidateModel accepts any non-empty model; the Codex catalog moves fast.
func (p *chatGPTProvider) ValidateModel(model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("model is required")
	}
	return nil
}

// Models lists models from the Codex catalog (models[].slug), preserving
// server order. A legacy platform shape (data[].id) is accepted as a fallback
// for custom baseURLs. Auth failures (401/403) are retried once after a
// serialized refresh before giving up.
func (p *chatGPTProvider) Models(ctx context.Context) ([]string, error) {
	creds, err := chatgptoauth.GetValidTokens(ctx, p.client, p.tokensPath)
	if err != nil {
		return nil, err
	}
	models, err := p.fetchModels(ctx, creds)
	if err == nil {
		return models, nil
	}
	if !isChatGPTUnauthorized(err) {
		return nil, err
	}
	fresh, err := chatgptoauth.RefreshAndSaveWithStale(ctx, p.client, p.tokensPath, creds.AccessToken)
	if err != nil {
		return nil, err
	}
	return p.fetchModels(ctx, fresh)
}

func (p *chatGPTProvider) fetchModels(ctx context.Context, creds chatgptoauth.Tokens) ([]string, error) {
	modelsURL := p.baseURL + "/models"
	sep := "?"
	if strings.Contains(modelsURL, "?") {
		sep = "&"
	}
	modelsURL += sep + "client_version=" + chatGPTClientVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if creds.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", creds.AccountID)
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return decodeChatGPTModels(data)
}

func decodeChatGPTModels(data []byte) ([]string, error) {
	var codex struct {
		Models []struct {
			Slug string `json:"slug"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &codex); err == nil && len(codex.Models) > 0 {
		models := make([]string, 0, len(codex.Models))
		for _, m := range codex.Models {
			id := m.Slug
			if id == "" {
				id = m.ID
			}
			if id != "" {
				models = append(models, id)
			}
		}
		if len(models) > 0 {
			return models, nil
		}
	}
	var legacy struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]string, 0, len(legacy.Data))
	for _, m := range legacy.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// splitChatGPTMessages lifts system messages into top-level instructions. The
// Codex backend rejects system roles inside input, so they must not be sent
// there.
func splitChatGPTMessages(messages []Message) (string, []Message) {
	var systems []string
	rest := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == "system" {
			if strings.TrimSpace(m.Content) != "" {
				systems = append(systems, m.Content)
			}
			continue
		}
		rest = append(rest, m)
	}
	return strings.Join(systems, "\n\n"), rest
}

// Complete sends a Codex Responses request with the OAuth access token,
// refreshing once on 401 before giving up. Thinking levels are intentionally
// unsupported in this first integration: the request omits optional thinking
// fields and preserves the provider default.
func (p *chatGPTProvider) Complete(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	creds, err := chatgptoauth.GetValidTokens(ctx, p.client, p.tokensPath)
	if err != nil {
		return Response{}, err
	}
	resp, err := p.doCodex(ctx, input, creds, onText)
	if err == nil {
		return resp, nil
	}
	if !isChatGPTUnauthorized(err) {
		return Response{}, err
	}
	fresh, err := chatgptoauth.RefreshAndSaveWithStale(ctx, p.client, p.tokensPath, creds.AccessToken)
	if err != nil {
		return Response{}, err
	}
	return p.doCodex(ctx, input, fresh, onText)
}

func (p *chatGPTProvider) doCodex(ctx context.Context, input Request, creds chatgptoauth.Tokens, onText StreamCallback) (Response, error) {
	instructions, rest := splitChatGPTMessages(input.Messages)
	body := map[string]any{
		"model":  input.Model,
		"input":  responsesInput(rest),
		"stream": true,
		"store":  false,
	}
	if strings.TrimSpace(instructions) != "" {
		body["instructions"] = instructions
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
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if creds.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", creds.AccountID)
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

func isChatGPTUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, " 401") || strings.Contains(msg, "401 Unauthorized")
}
