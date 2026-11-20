package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OllamaCloudKind is the built-in ID, independent of the profile name.
const OllamaCloudKind = "ollama-cloud"
const ollamaCloudBaseURL = "https://ollama.com"

func init() { Register(OllamaCloudKind, newOllamaCloud) }

type ollamaCloudProvider struct {
	name, baseURL, key string
	client             HTTPDoer
	mu                 sync.RWMutex
	catalogLoaded      bool
	models             map[string]ollamaCloudMetadata
}

// OllamaCloudBaseURL normalizes the configured origin and optional gateway
// prefix. Errors deliberately omit the user-supplied URL.
func OllamaCloudBaseURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return ollamaCloudBaseURL, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.ForceQuery || strings.ContainsAny(base, "?#") {
		return "", errors.New("ollama-cloud base URL must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	return "https://" + u.Host + strings.TrimSuffix(strings.TrimRight(u.EscapedPath(), "/"), "/api"), nil
}

func newOllamaCloud(cfg Config) (Provider, error) {
	base, err := OllamaCloudBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("ollama-cloud requires an API key: set OLLAMA_API_KEY or configure a key file in `qcode setup`")
	}
	client := httpClient(cfg)
	if httpClient, ok := client.(*http.Client); ok {
		// Preserve the configured transport, TLS settings and timeouts without
		// modifying the shared client or allowing even same-origin redirects.
		clone := *httpClient
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &clone
	}
	if cfg.RegisterSecrets != nil {
		cfg.RegisterSecrets(key)
	}
	name := cfg.InstanceName
	if name == "" {
		name = OllamaCloudKind
	}
	return &ollamaCloudProvider{name: name, baseURL: base, key: key, client: client, models: make(map[string]ollamaCloudMetadata)}, nil
}

func (p *ollamaCloudProvider) Name() string { return p.name }
func (*ollamaCloudProvider) Kind() string   { return OllamaCloudKind }

func ollamaCloudHTTPError(resp *http.Response) error {
	switch code := resp.StatusCode; {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("Ollama Cloud returned HTTP %d: check your API key and account access (`qcode setup` or OLLAMA_API_KEY)", code)
	case code >= 300 && code < 400:
		return fmt.Errorf("Ollama Cloud returned HTTP %d: redirects are not followed; configure the final HTTPS endpoint directly", code)
	default:
		return fmt.Errorf("Ollama Cloud returned HTTP %d", code)
	}
}

// Transport and in-stream errors may contain text controlled by the endpoint.
func (p *ollamaCloudProvider) safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	encoded, _ := json.Marshal(p.key)
	return errors.New(strings.NewReplacer(p.key, "[REDACTED]", string(encoded[1:len(encoded)-1]), "[REDACTED]").Replace(err.Error()))
}

func (p *ollamaCloudProvider) Complete(ctx context.Context, input Request, onText StreamCallback) (Response, error) {
	var thinking map[string]any
	meta := p.metadata(input.Model)
	if value, ok := meta.thinking.values[strings.ToLower(strings.TrimSpace(input.Thinking))]; ok && meta.thinking.capability.Adjustable {
		thinking = map[string]any{"think": value}
	}
	result, err := completeOllama(ctx, input, onText, p.baseURL, p.client, thinking, p.key)
	return result, p.safeError(err)
}

func (p *ollamaCloudProvider) requestJSON(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return p.safeError(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return p.safeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ollamaCloudHTTPError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return p.safeError(fmt.Errorf("decode Ollama Cloud metadata: %w", err))
	}
	return ctx.Err()
}

type ollamaCloudModel struct {
	Name          string          `json:"name"`
	Model         string          `json:"model"`
	Thinking      json.RawMessage `json:"thinking"`
	ContextLength json.RawMessage `json:"context_length"`
	Details       json.RawMessage `json:"details"`
	ModelInfo     json.RawMessage `json:"model_info"`
}

type ollamaCloudThinking struct {
	capability ThinkingCapability
	values     map[string]any
}

type ollamaCloudMetadata struct {
	thinking  ollamaCloudThinking
	capacity  int
	showTried bool
}

func (m ollamaCloudModel) metadata() ollamaCloudMetadata {
	positiveInt := func(raw json.RawMessage) int {
		var value int
		if json.Unmarshal(raw, &value) != nil {
			return 0
		}
		return max(0, value)
	}
	capacity := positiveInt(m.ContextLength)
	if capacity == 0 {
		var details struct {
			ContextLength json.RawMessage `json:"context_length"`
		}
		_ = json.Unmarshal(m.Details, &details)
		capacity = positiveInt(details.ContextLength)
	}
	if capacity == 0 {
		var info map[string]json.RawMessage
		_ = json.Unmarshal(m.ModelInfo, &info)
		for k, raw := range info {
			if k == "context_length" || strings.HasSuffix(k, ".context_length") {
				capacity = max(capacity, positiveInt(raw))
			}
		}
	}
	var controls struct {
		Values  []any `json:"values"`
		Default any   `json:"default"`
	}
	thinking := ollamaCloudThinking{}
	if json.Unmarshal(m.Thinking, &controls) == nil {
		thinking.values = make(map[string]any)
		for _, value := range controls.Values {
			label := ollamaCloudThinkingLabel(value)
			if _, exists := thinking.values[label]; label == "" || exists {
				continue
			}
			thinking.capability.Levels = append(thinking.capability.Levels, label)
			thinking.values[label] = value
			if value != false {
				thinking.capability.Supported = true
			}
		}
		if _, ok := thinking.values[ollamaCloudThinkingLabel(controls.Default)]; ok {
			thinking.capability.Default = ollamaCloudThinkingLabel(controls.Default)
		}
		// Even a single advertised setting can be selected instead of the
		// implicit default. A lone false value means thinking is unsupported.
		thinking.capability.Adjustable = thinking.capability.Supported
		thinking.capability.RequestFormat = ThinkingRequestOllamaThink
		thinking.capability.ReplayFormat = ThinkingReplayNone
	}
	return ollamaCloudMetadata{thinking: thinking, capacity: capacity}
}

func ollamaCloudThinkingLabel(value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "on"
		}
		return "off"
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func (m ollamaCloudMetadata) withFallback(old ollamaCloudMetadata) ollamaCloudMetadata {
	if len(m.thinking.values) == 0 {
		m.thinking = old.thinking
	}
	if m.capacity == 0 {
		m.capacity = old.capacity
	}
	m.showTried = m.showTried || old.showTried
	return m
}

func (p *ollamaCloudProvider) Models(ctx context.Context) ([]string, error) {
	var catalog struct {
		Models []ollamaCloudModel `json:"models"`
	}
	if err := p.requestJSON(ctx, http.MethodGet, "/api/tags", nil, &catalog); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.catalogLoaded = true
	models := make([]string, 0, len(catalog.Models))
	seen := make(map[string]bool)
	for _, model := range catalog.Models {
		id := model.Name
		if id == "" {
			id = model.Model
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
		p.models[id] = model.metadata().withFallback(p.models[id])
	}
	return models, nil
}

func (p *ollamaCloudProvider) metadata(model string) ollamaCloudMetadata {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.models[model]
}

func (p *ollamaCloudProvider) ThinkingCapability(model string) ThinkingCapability {
	capability := p.metadata(model).thinking.capability
	capability.Levels = append([]string(nil), capability.Levels...)
	return capability
}

func (p *ollamaCloudProvider) RefreshModelMetadata(ctx context.Context, model string) {
	p.refreshMetadata(ctx, model, true)
}

func (p *ollamaCloudProvider) ContextWindow(ctx context.Context, model string) (int, error) {
	p.refreshMetadata(ctx, model, false)
	return p.metadata(model).capacity, ctx.Err()
}

func (p *ollamaCloudProvider) refreshMetadata(ctx context.Context, model string, thinking bool) {
	if model == "" || ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	known := func(meta ollamaCloudMetadata) bool {
		if thinking {
			return len(meta.thinking.values) != 0
		}
		return meta.capacity > 0
	}
	p.mu.RLock()
	meta, catalogLoaded := p.models[model], p.catalogLoaded
	p.mu.RUnlock()
	if known(meta) || meta.showTried {
		return
	}
	if !catalogLoaded {
		_, _ = p.Models(ctx)
		meta = p.metadata(model)
	}
	if known(meta) || meta.showTried || ctx.Err() != nil {
		return
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	var result ollamaCloudModel
	err := p.requestJSON(ctx, http.MethodPost, "/api/show", body, &result)
	if ctx.Err() != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	meta = p.models[model]
	if err == nil {
		meta = result.metadata().withFallback(meta)
	}
	meta.showTried = true
	p.models[model] = meta
}
