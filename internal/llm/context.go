package llm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Context limits from https://models.dev/api.json, retrieved 2026-11-04.
// Exact IDs only: unknown models must not inherit a possibly incorrect limit.
//
//go:embed context_windows.json
var contextCatalog []byte

func (p *openAIProvider) ContextWindow(_ context.Context, model string) (int, error) {
	// A custom endpoint may impose different limits even for familiar model IDs.
	// Gemini official covers both the OpenAI-compat shim and the native root.
	if (p.name == "openai" && p.baseURL != "https://api.openai.com/v1") ||
		(p.name == "opencode-go" && p.baseURL != openCodeGoBaseURL) ||
		(p.name == "gemini" && !isGeminiOfficialBaseURL(p.baseURL)) {
		return 0, nil
	}
	var catalog map[string]map[string]int
	if err := json.Unmarshal(contextCatalog, &catalog); err != nil {
		return 0, err
	}
	if p.name == "gemini" {
		model = normalizeGeminiModelID(model)
	}
	return catalog[p.name][model], nil
}

func (p *ollamaProvider) ContextWindow(ctx context.Context, model string) (int, error) {
	// /api/ps reports the allocated window, unlike /api/show's training maximum.
	// When the model is not loaded /api/ps has no entry, so fall back to the
	// native maximum from /api/show (model_info.*.context_length) and then
	// /api/tags (details.context_length) so CTX keeps a capacity instead of
	// going unknown before the first completion.
	psLimit, psErr := p.ollamaContextFromPS(ctx, model)
	if psErr == nil && psLimit > 0 {
		return psLimit, nil
	}
	if showLimit, showErr := p.ollamaContextFromShow(ctx, model); showErr == nil && showLimit > 0 {
		return showLimit, nil
	}
	if tagsLimit, tagsErr := p.ollamaContextFromTags(ctx, model); tagsErr == nil && tagsLimit > 0 {
		return tagsLimit, nil
	}
	if psErr != nil {
		// Server unreachable: surface it so callers keep the previous
		// capacity instead of clearing to unknown.
		return 0, psErr
	}
	if psLimit > 0 {
		return psLimit, nil
	}
	// Model simply has no discoverable window yet (not loaded and no native
	// maximum reported); unknown, not an error.
	return 0, nil
}

func ollamaNormalizeModel(s string) string {
	if !strings.Contains(s, ":") {
		return s + ":latest"
	}
	return s
}

func (p *ollamaProvider) ollamaContextFromPS(ctx context.Context, model string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/ps", nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("context discovery: %s", resp.Status)
	}
	var payload struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	for _, item := range payload.Models {
		if ollamaNormalizeModel(item.Name) == ollamaNormalizeModel(model) || ollamaNormalizeModel(item.Model) == ollamaNormalizeModel(model) {
			return item.ContextLength, nil
		}
	}
	return 0, nil
}

func (p *ollamaProvider) ollamaContextFromShow(ctx context.Context, model string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/show", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("context discovery: %s", resp.Status)
	}
	var result struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	best := 0
	for key, value := range result.ModelInfo {
		if !strings.HasSuffix(key, "context_length") {
			continue
		}
		switch v := value.(type) {
		case float64:
			if int(v) > best {
				best = int(v)
			}
		case int:
			if v > best {
				best = v
			}
		case int64:
			if int(v) > best {
				best = int(v)
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && int(n) > best {
				best = int(n)
			}
		}
	}
	return best, nil
}

func (p *ollamaProvider) ollamaContextFromTags(ctx context.Context, model string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("context discovery: %s", resp.Status)
	}
	var payload struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				ContextLength int `json:"context_length"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	for _, item := range payload.Models {
		if ollamaNormalizeModel(item.Name) == ollamaNormalizeModel(model) || ollamaNormalizeModel(item.Model) == ollamaNormalizeModel(model) {
			return item.Details.ContextLength, nil
		}
	}
	return 0, nil
}
