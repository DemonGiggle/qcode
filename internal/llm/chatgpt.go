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

// chatGPTBaseURL is the Sign in with ChatGPT (token-sharing) Responses
// endpoint. Per https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference
// ChatGPT plan usage must go to https://api.openai.com/v1 (POST /responses,
// GET /models) with the OAuth access token as Bearer auth. Do not point it
// at ChatGPT's backend-api endpoints: backend-api/codex is a private Codex
// route whose access enforcement rejects third-party OAuth clients with
// 401 rejected_by_access_enforcement/no_matching_rule.
// The Responses wire format is shared with a subscription contract:
// top-level instructions, store:false, stream:true, no system role in input.
const chatGPTBaseURL = "https://api.openai.com/v1"

// chatGPTClientVersion is kept for custom baseURLs that still target the
// legacy Codex backend (chatgpt.com/backend-api/codex), which gates its
// catalog behind ?client_version=. It is not sent to api.openai.com/v1.
const chatGPTClientVersion = "0.166.0"

// isLegacyChatGPTBaseURL reports whether baseURL targets the deprecated
// chatgpt.com/backend-api Codex route, which needs the ChatGPT-Account-Id
// header and client_version query param. The supported SIWC route
// (api.openai.com/v1) needs neither.
func isLegacyChatGPTBaseURL(baseURL string) bool {
	return strings.Contains(baseURL, "chatgpt.com/backend-api")
}

type chatGPTProvider struct {
	baseURL         string
	tokensPath      string
	client          HTTPDoer
	registerSecrets func(...string)
}

var (
	_ ThinkingProvider      = (*chatGPTProvider)(nil)
	_ ContextWindowProvider = (*chatGPTProvider)(nil)
)

// chatGPTThinkingLevels are the Codex reasoning-effort tiers selectable through
// the SIWC Responses endpoint. They match the Codex CLI model_reasoning_effort
// knob (minimal/low/medium/high/xhigh) and the documented GPT-5.2-Codex effort
// support (low/medium/high/xhigh). Levels are sent as the Responses reasoning
// object, the same wire format OpenCode Go Responses models use.
var chatGPTThinkingLevels = []string{"minimal", "low", "medium", "high", "xhigh"}

// ThinkingCapability reports the Codex reasoning tiers for any ChatGPT model.
// The SIWC catalog only serves Codex reasoning models, so every non-empty
// model ID gets the same adjustable selector instead of falling back to
// "no thinking info". Future slugs keep working without a catalog update.
func (p *chatGPTProvider) ThinkingCapability(model string) ThinkingCapability {
	if strings.TrimSpace(model) == "" {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	return ThinkingCapability{
		Supported:     true,
		Adjustable:    true,
		Levels:        append([]string(nil), chatGPTThinkingLevels...),
		Default:       "medium",
		RequestFormat: ThinkingRequestResponsesReasoning,
		ReplayFormat:  ThinkingReplayReasoningDetails,
	}
}

// ContextWindow reports the active ChatGPT model's context capacity, or zero
// when unknown. Exact chatgpt entries win; otherwise the openai catalog is
// consulted for the same ID and for the base model behind Codex suffixes
// (gpt-5.6-codex -> gpt-5.6), so new Codex slugs inherit the matching API
// window instead of showing CTX unknown. Unlisted Codex slugs fall back to a
// conservative 400k estimate (the Codex product total); unlisted spark
// snapshots fall back to 128k.
func (p *chatGPTProvider) ContextWindow(_ context.Context, model string) (int, error) {
	var catalog map[string]map[string]int
	if err := json.Unmarshal(contextCatalog, &catalog); err != nil {
		return 0, err
	}
	if limit := catalog["chatgpt"][model]; limit != 0 {
		return limit, nil
	}
	if limit := catalog["openai"][model]; limit != 0 {
		return limit, nil
	}
	// Spark snapshots are lightweight chat variants (128k), regardless of base.
	if strings.Contains(strings.ToLower(model), "spark") {
		return 128000, nil
	}
	base := model
	for _, suffix := range []string{"-codex-spark", "-codex", "-spark"} {
		if strings.HasSuffix(base, suffix) {
			base = strings.TrimSuffix(base, suffix)
			break
		}
	}
	if base != model {
		if limit := catalog["chatgpt"][base]; limit != 0 {
			return limit, nil
		}
		if limit := catalog["openai"][base]; limit != 0 {
			return limit, nil
		}
	}
	// Future Codex slugs without a catalog entry still report a usable
	// estimate instead of CTX unknown; the override flags remain available.
	if strings.Contains(strings.ToLower(model), "codex") {
		return 400000, nil
	}
	return 0, nil
}

func init() {
	Register("chatgpt", newChatGPT)
}

func newChatGPT(config Config) (Provider, error) {
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = chatGPTBaseURL
	}
	return &chatGPTProvider{baseURL: baseURL, tokensPath: config.ChatGPTOAuthPath, client: httpClient(config), registerSecrets: config.RegisterSecrets}, nil
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

// Models lists models from the SIWC catalog (models[].slug), preserving
// server order. Entries with visibility set to anything other than "list"
// are hidden (per the SIWC models-and-inference example). A legacy platform
// shape (data[].id) is accepted as a fallback for custom baseURLs. Auth
// failures (401/403) are retried once after a serialized refresh before
// giving up.
func (p *chatGPTProvider) Models(ctx context.Context) ([]string, error) {
	creds, err := chatgptoauth.GetValidTokensWithSecrets(ctx, p.client, p.tokensPath, p.registerSecrets)
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
	fresh, err := chatgptoauth.RefreshAndSaveWithStaleWithSecrets(ctx, p.client, p.tokensPath, creds.AccessToken, p.registerSecrets)
	if err != nil {
		return nil, err
	}
	models, err = p.fetchModels(ctx, fresh)
	if err != nil {
		return nil, annotateChatGPTAuthError(err)
	}
	return models, nil
}

func (p *chatGPTProvider) fetchModels(ctx context.Context, creds chatgptoauth.Tokens) ([]string, error) {
	modelsURL := p.baseURL + "/models"
	if isLegacyChatGPTBaseURL(p.baseURL) {
		sep := "?"
		if strings.Contains(modelsURL, "?") {
			sep = "&"
		}
		modelsURL += sep + "client_version=" + chatGPTClientVersion
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if isLegacyChatGPTBaseURL(p.baseURL) && creds.AccountID != "" {
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
			Slug       string `json:"slug"`
			ID         string `json:"id"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &codex); err == nil && len(codex.Models) > 0 {
		models := make([]string, 0, len(codex.Models))
		for _, m := range codex.Models {
			// Per SIWC models-and-inference, only visibility=="list" models
			// are display candidates. Entries without a visibility field
			// (legacy Codex catalog) are kept.
			if m.Visibility != "" && m.Visibility != "list" {
				continue
			}
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
// SIWC Responses route rejects system roles inside input (use instructions
// or developer messages), so they must not be sent there.
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

// Complete sends a SIWC Responses request with the OAuth access token,
// refreshing once on 401 before giving up. An explicit thinking level is sent
// as the Responses reasoning object; empty preserves the provider default.
func (p *chatGPTProvider) Complete(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	creds, err := chatgptoauth.GetValidTokensWithSecrets(ctx, p.client, p.tokensPath, p.registerSecrets)
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
	fresh, err := chatgptoauth.RefreshAndSaveWithStaleWithSecrets(ctx, p.client, p.tokensPath, creds.AccessToken, p.registerSecrets)
	if err != nil {
		return Response{}, err
	}
	resp, err = p.doCodex(ctx, input, fresh, onText)
	if err != nil {
		return Response{}, annotateChatGPTAuthError(err)
	}
	return resp, nil
}

func (p *chatGPTProvider) doCodex(ctx context.Context, input Request, creds chatgptoauth.Tokens, onText StreamCallback) (Response, error) {
	// Cache policy (#81): the SIWC Responses route never emits
	// prompt_cache_key, even when Request carries a key.
	instructions, rest := splitChatGPTMessages(input.Messages)
	body := map[string]any{
		"model":  input.Model,
		"input":  responsesInput(rest),
		"stream": true,
		"store":  false,
	}
	for field, value := range thinkingFields(p.ThinkingCapability(input.Model), input.Thinking) {
		body[field] = value
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
	if isLegacyChatGPTBaseURL(p.baseURL) && creds.AccountID != "" {
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

// annotateChatGPTAuthError adds recovery guidance to a final 401/403 after
// the refresh retry. The legacy chatgpt.com/backend-api/codex route rejects
// third-party OAuth clients with
// rejected_by_access_enforcement/no_matching_rule; the supported SIWC route
// is https://api.openai.com/v1.
func annotateChatGPTAuthError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "rejected_by_access_enforcement") || strings.Contains(msg, "no_matching_rule") {
		return fmt.Errorf("%w; ChatGPT plan request rejected (no_matching_rule): use the default SIWC endpoint https://api.openai.com/v1 (remove any chatgpt base_url override pointing at chatgpt.com/backend-api) and run `qcode login chatgpt` again", err)
	}
	if isChatGPTUnauthorized(err) {
		return fmt.Errorf("%w; run `qcode login chatgpt` again and confirm ChatGPT plan access", err)
	}
	return err
}
