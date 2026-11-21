package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

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
// leading developer instructions in input, store:false, stream:true,
// no system role in input.
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
	instanceName    string
	baseURL         string
	tokensPath      string
	client          HTTPDoer
	registerSecrets func(...string)
	thinkingMu      sync.RWMutex
	thinkingModels  map[string]ThinkingCapability
}

var (
	_ ThinkingProvider      = (*chatGPTProvider)(nil)
	_ ContextWindowProvider = (*chatGPTProvider)(nil)
)

// ThinkingCapability uses account catalog metadata when available, then the
// bundled Codex catalog. Unknown models remain usable without invented tiers.
func (p *chatGPTProvider) ThinkingCapability(model string) ThinkingCapability {
	p.thinkingMu.RLock()
	capability, ok := p.thinkingModels[model]
	p.thinkingMu.RUnlock()
	if !ok {
		capability, ok = bundledChatGPTThinking[model]
	}
	if !ok {
		return ThinkingCapability{RequestFormat: ThinkingRequestNone, ReplayFormat: ThinkingReplayNone}
	}
	capability.Levels = append([]string(nil), capability.Levels...)
	return capability
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
	return &chatGPTProvider{instanceName: config.InstanceName, baseURL: baseURL, tokensPath: config.ChatGPTOAuthPath, client: httpClient(config), registerSecrets: config.RegisterSecrets}, nil
}

// Name returns the registry name.
func (p *chatGPTProvider) Name() string {
	if p.instanceName != "" {
		return p.instanceName
	}
	return "chatgpt"
}
func (p *chatGPTProvider) Kind() string { return "chatgpt" }

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
	models, err := decodeChatGPTModels(data)
	if err != nil {
		return nil, err
	}
	metadata, err := decodeChatGPTThinking(data)
	if err != nil {
		return nil, err
	}
	p.thinkingMu.Lock()
	p.thinkingModels = metadata
	p.thinkingMu.Unlock()
	return models, nil
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

// splitChatGPTMessages lifts system messages into durable instructions text,
// sent as the leading developer message in input. The SIWC Responses route
// rejects system roles inside input, so they must not be sent there.
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
	level := strings.ToLower(strings.TrimSpace(input.Thinking))
	if level != "" && !validThinkingLevel(p.ThinkingCapability(input.Model), level) {
		return Response{}, fmt.Errorf("thinking level %q is not supported by model %q", input.Thinking, input.Model)
	}
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

func (p *chatGPTProvider) buildCodexBody(input Request, instructions string, rest []Message, useCache bool) map[string]any {
	// Durable instructions lead the replayable history for implicit caching.
	items := chatGPTCacheInput(instructions, rest)
	topLevelInstructions := ""
	if input.RequestOnlyInstructions != "" {
		if IsOfficialOpenAIEndpoint(p.baseURL) && chatGPTInstructionsAfterInput(input.Model) {
			topLevelInstructions = input.RequestOnlyInstructions
		} else {
			items = append(items, responsesMessage("developer", input.RequestOnlyInstructions, nil))
		}
	}
	cacheKey := ""
	if useCache && IsOfficialOpenAIEndpoint(p.baseURL) {
		cacheKey = input.PromptCacheKey
	}
	// Shared Responses core (issues #115/#116): store, previous_response_id,
	// comparison, reasoning, tools, structured output, and lifecycle knobs.
	// Zero-value opt-ins are omitted in the shared builder so the opencode-go
	// route keeps its previous wire; the SIWC contract below re-adds
	// store:false to stay stateless.
	body := buildResponsesBody(input.Model, p.ThinkingCapability(input.Model), input, items, cacheKey, useCache && IsOfficialOpenAIEndpoint(p.baseURL))
	if _, ok := body["store"]; !ok {
		body["store"] = false
	}
	if topLevelInstructions != "" {
		body["instructions"] = topLevelInstructions
		// GPT-6.2+ renders top-level instructions after input (billed
		// uncached). The shared builder already placed request-only
		// guidance correctly; nothing else changes.
	}
	return body
}

// Starting with GPT-6.2, OpenAI renders top-level instructions after input,
// beyond eligible cache breakpoints. Preserve the trailing position for
// other models by encoding the guidance as the last input item.
func chatGPTInstructionsAfterInput(model string) bool {
	version, ok := strings.CutPrefix(model, "gpt-")
	if !ok {
		return false
	}
	version, _, _ = strings.Cut(version, "-")
	majorText, minorText, hasMinor := strings.Cut(version, ".")
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return false
	}
	minor := uint64(0)
	if hasMinor {
		minor, err = strconv.ParseUint(minorText, 10, 32)
		if err != nil {
			return false
		}
	}
	return major > 6 || major == 6 && minor >= 2
}

// isChatGPTCacheFieldRejection reports whether a non-2xx Responses body is a
// cache-control rejection (for example, "prompt_cache_key is not supported
// on this model"). A 400 means the request was rejected before execution,
// so retrying once without any prompt_cache_* fields is safe and cannot
// double-bill.
func isChatGPTCacheFieldRejection(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "prompt_cache") || strings.Contains(lower, "comparison_response") {
		return true
	}
	// Generic historical shape used in fixtures (no param name).
	if strings.Contains(lower, "cache") && (strings.Contains(lower, "unsupported") || strings.Contains(lower, "not supported") || strings.Contains(lower, "invalid_parameter")) {
		return true
	}
	return false
}

// isChatGPTContinuationRejection reports whether a non-2xx Responses body
// rejects an opt-in stateful field (previous_response_id / conversation).
// A 400/404 here happens before execution, so retrying once with the same
// full-history input and no continuation id cannot double-bill.
func isChatGPTContinuationRejection(statusCode int, body string) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "previous_response") || strings.Contains(lower, "conversation")
}

// chatGPTStatusError preserves the HTTP status for retry decisions while
// keeping the user-facing "provider returned ..." message unchanged.
type chatGPTStatusError struct {
	statusCode int
	status     string
	body       string
}

func (e *chatGPTStatusError) Error() string {
	return fmt.Sprintf("provider returned %s: %s", e.status, e.body)
}

func asChatGPTStatusError(err error) (chatGPTStatusError, bool) {
	if err == nil {
		return chatGPTStatusError{}, false
	}
	// errors.As via manual unwrap to avoid importing errors for one call site.
	for cur := err; cur != nil; {
		if v, ok := cur.(*chatGPTStatusError); ok {
			return *v, true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return chatGPTStatusError{}, false
}

func (p *chatGPTProvider) postCodexRaw(ctx context.Context, creds chatgptoauth.Tokens, payload []byte, onText StreamCallback) (Response, error) {
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
		body := strings.TrimSpace(string(data))
		return Response{}, &chatGPTStatusError{statusCode: resp.StatusCode, status: resp.Status, body: body}
	}
	return parseResponsesStream(resp.Body, onText)
}

func (p *chatGPTProvider) doCodex(ctx context.Context, input Request, creds chatgptoauth.Tokens, onText StreamCallback) (Response, error) {
	instructions, rest := splitChatGPTMessages(input.Messages)
	body := p.buildCodexBody(input, instructions, rest, true)
	// The implicit-only default sends no prompt_cache_options and no explicit
	// breakpoints, so only opt-in cache/continuation fields can trigger the
	// safe keyless/delta-less retry below.
	_, usedCache := body["prompt_cache_key"]
	_, usedComparison := body["prompt_cache_options"]
	_, usedContinuation := body["previous_response_id"]
	if _, ok := body["conversation"]; ok {
		usedContinuation = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	resp, err := p.postCodexRaw(ctx, creds, payload, onText)
	if err == nil {
		return resp, nil
	}
	var statusCode int
	var bodyText string
	if se, ok := asChatGPTStatusError(err); ok {
		statusCode = se.statusCode
		bodyText = se.body
	} else {
		return resp, err
	}
	// Fall back once without the rejected prompt_cache_* field. A key
	// rejection strips all cache fields; a comparison-only rejection keeps
	// the stable key so accounting separation survives. Stripping cannot
	// double-bill (a 400 rejects before execution) and keeps the reusable
	// prefix byte-identical.
	if (usedCache || usedComparison) && isChatGPTCacheFieldRejection(statusCode, bodyText) {
		lower := strings.ToLower(bodyText)
		mentionsKey := strings.Contains(lower, "prompt_cache_key")
		mentionsComparison := strings.Contains(lower, "prompt_cache_options") || strings.Contains(lower, "comparison_response")
		cleanInput := input
		cleanInput.ComparisonResponseID = ""
		useCache := true
		if mentionsKey || !mentionsComparison {
			// Key rejection, or generic cache rejection without a field
			// name: strip all cache fields to avoid looping into the same
			// 400 (previously the keyless fallback always ran).
			cleanInput.PromptCacheKey = ""
			useCache = false
		}
		clean := p.buildCodexBody(cleanInput, instructions, rest, useCache)
		cleanPayload, err := json.Marshal(clean)
		if err != nil {
			return Response{}, err
		}
		return p.postCodexRaw(ctx, creds, cleanPayload, onText)
	}
	// Continuation fallback: same full-history input without the
	// previous_response_id/conversation delta. The server never executed,
	// so resending the full history cannot double-bill.
	if usedContinuation && isChatGPTContinuationRejection(statusCode, bodyText) {
		cleanInput := input
		cleanInput.PreviousResponseID = ""
		cleanInput.ConversationID = ""
		clean := p.buildCodexBody(cleanInput, instructions, rest, true)
		cleanPayload, err := json.Marshal(clean)
		if err != nil {
			return resp, err
		}
		return p.postCodexRaw(ctx, creds, cleanPayload, onText)
	}
	return resp, err
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
		return fmt.Errorf("%w; ChatGPT plan request rejected (no_matching_rule): use the default SIWC endpoint https://api.openai.com/v1 (remove any chatgpt base_url override pointing at chatgpt.com/backend-api) and re-authenticate with ChatGPT in `qcode setup`", err)
	}
	if isChatGPTUnauthorized(err) {
		return fmt.Errorf("%w; re-authenticate with ChatGPT in `qcode setup` and confirm ChatGPT plan access", err)
	}
	return err
}
