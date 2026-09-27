package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenCodeGoModelMetaVersion identifies the JSON cache schema.
const (
	OpenCodeGoModelMetaVersion = 1
	modelsDevCatalogURL        = "https://models.dev/api.json"
	maxModelsDevCatalogSize    = 16 << 20
	maxModelMetaCacheSize      = 4 << 20
)

// OpenCodeGoModelOptions contains the selector controls Models.dev describes
// for one model. Only effort and toggle controls are currently understood.
type OpenCodeGoModelOptions struct {
	Effort []string `json:"effort,omitempty"`
	Toggle bool     `json:"toggle,omitempty"`
}

// OpenCodeGoModelMeta is a versioned snapshot of Models.dev's OpenCode Go
// reasoning options. Request formats are intentionally not stored here: qcode
// must have a local adapter before any of these choices can be used.
type OpenCodeGoModelMeta struct {
	Version   int                               `json:"version"`
	Source    string                            `json:"source"`
	FetchedAt string                            `json:"fetched_at,omitempty"`
	Models    map[string]OpenCodeGoModelOptions `json:"models"`
}

// EmptyOpenCodeGoModelMeta returns an explicit empty snapshot. Passing it to
// the provider suppresses built-in choices while retaining local wire adapters.
func EmptyOpenCodeGoModelMeta() *OpenCodeGoModelMeta {
	return &OpenCodeGoModelMeta{
		Version: OpenCodeGoModelMetaVersion,
		Source:  modelsDevCatalogURL,
		Models:  make(map[string]OpenCodeGoModelOptions),
	}
}

// OpenCodeGoModelMetaCachePath returns the per-user versioned metadata cache
// location under the operating system's cache directory.
func OpenCodeGoModelMetaCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(dir, "qcode", "opencode-go-model-meta-v1.json"), nil
}

// ParseOpenCodeGoModelMeta extracts supported effort and toggle choices from
// the Models.dev provider catalog. Unsupported option types are ignored.
func ParseOpenCodeGoModelMeta(data []byte) (*OpenCodeGoModelMeta, error) {
	var providers map[string]json.RawMessage
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("decode Models.dev catalog: %w", err)
	}
	if providers == nil {
		return nil, errors.New("decode Models.dev catalog: expected provider object")
	}
	providerData, ok := providers["opencode-go"]
	if !ok {
		return nil, errors.New("Models.dev catalog has no opencode-go provider")
	}
	var provider struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(providerData, &provider); err != nil {
		return nil, fmt.Errorf("decode Models.dev opencode-go provider: %w", err)
	}
	if provider.Models == nil {
		return nil, errors.New("Models.dev opencode-go provider has no models object")
	}
	if len(provider.Models) == 0 {
		return nil, errors.New("Models.dev opencode-go provider has an empty models object")
	}

	meta := EmptyOpenCodeGoModelMeta()
	for modelID, data := range provider.Models {
		if strings.TrimSpace(modelID) == "" {
			return nil, errors.New("Models.dev opencode-go provider contains an empty model ID")
		}
		var modelObject map[string]json.RawMessage
		if err := json.Unmarshal(data, &modelObject); err != nil || modelObject == nil {
			return nil, fmt.Errorf("decode Models.dev model %q: expected model object", modelID)
		}
		var model struct {
			ReasoningOptions []json.RawMessage `json:"reasoning_options"`
		}
		if err := json.Unmarshal(data, &model); err != nil {
			return nil, fmt.Errorf("decode Models.dev model %q: %w", modelID, err)
		}
		options := OpenCodeGoModelOptions{}
		for _, raw := range model.ReasoningOptions {
			var option struct {
				Type   string          `json:"type"`
				Values json.RawMessage `json:"values"`
			}
			if err := json.Unmarshal(raw, &option); err != nil {
				// A malformed or future option is not a usable selector control.
				continue
			}
			switch strings.ToLower(strings.TrimSpace(option.Type)) {
			case "effort":
				var values []string
				if len(option.Values) == 0 || json.Unmarshal(option.Values, &values) != nil {
					continue
				}
				for _, value := range values {
					if choice, ok := normalizeThinkingChoice(value); ok {
						options.Effort = appendUnique(options.Effort, choice)
					}
				}
			case "toggle":
				options.Toggle = true
			}
		}
		meta.Models[modelID] = options
	}
	return meta, nil
}

// LoadOpenCodeGoModelMeta reuses a valid cache without network access. If there
// is no valid cache, it fetches and stores a new snapshot. On failure, an empty
// snapshot and the error are returned so callers can continue without choices.
func LoadOpenCodeGoModelMeta(ctx context.Context, cachePath string, client HTTPDoer) (*OpenCodeGoModelMeta, error) {
	if cachePath == "" {
		var err error
		cachePath, err = OpenCodeGoModelMetaCachePath()
		if err != nil {
			return EmptyOpenCodeGoModelMeta(), err
		}
	}
	if cached, err := readOpenCodeGoModelMeta(cachePath); err == nil {
		return cached, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// Invalid or unreadable cache data is treated as a cache miss. A failed
		// fetch will leave the original file in place for diagnosis/recovery.
	}
	meta, err := FetchOpenCodeGoModelMeta(ctx, client)
	if err != nil {
		return EmptyOpenCodeGoModelMeta(), err
	}
	if err := writeOpenCodeGoModelMeta(cachePath, meta); err != nil {
		return meta, fmt.Errorf("write OpenCode Go model metadata cache: %w", err)
	}
	return meta, nil
}

// RefreshOpenCodeGoModelMeta always fetches and validates a new snapshot before
// atomically replacing the existing cache. A failed refresh preserves the last
// good cache.
func RefreshOpenCodeGoModelMeta(ctx context.Context, cachePath string, client HTTPDoer) (*OpenCodeGoModelMeta, error) {
	if cachePath == "" {
		var err error
		cachePath, err = OpenCodeGoModelMetaCachePath()
		if err != nil {
			return nil, err
		}
	}
	meta, err := FetchOpenCodeGoModelMeta(ctx, client)
	if err != nil {
		return nil, err
	}
	if err := writeOpenCodeGoModelMeta(cachePath, meta); err != nil {
		return nil, fmt.Errorf("write OpenCode Go model metadata cache: %w", err)
	}
	return meta, nil
}

// FetchOpenCodeGoModelMeta retrieves and parses the public Models.dev catalog.
func FetchOpenCodeGoModelMeta(ctx context.Context, client HTTPDoer) (*OpenCodeGoModelMeta, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevCatalogURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch Models.dev catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("fetch Models.dev catalog: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsDevCatalogSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Models.dev catalog: %w", err)
	}
	if len(data) > maxModelsDevCatalogSize {
		return nil, fmt.Errorf("Models.dev catalog exceeds %d bytes", maxModelsDevCatalogSize)
	}
	meta, err := ParseOpenCodeGoModelMeta(data)
	if err != nil {
		return nil, err
	}
	meta.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return meta, nil
}

func readOpenCodeGoModelMeta(path string) (*OpenCodeGoModelMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxModelMetaCacheSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxModelMetaCacheSize {
		return nil, fmt.Errorf("OpenCode Go model metadata cache exceeds %d bytes", maxModelMetaCacheSize)
	}
	var meta OpenCodeGoModelMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("decode OpenCode Go model metadata cache: %w", err)
	}
	if err := validateOpenCodeGoModelMeta(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func writeOpenCodeGoModelMeta(path string, meta *OpenCodeGoModelMeta) error {
	if err := validateOpenCodeGoModelMeta(meta); err != nil {
		return err
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".opencode-go-model-meta-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func validateOpenCodeGoModelMeta(meta *OpenCodeGoModelMeta) error {
	if meta == nil || meta.Version != OpenCodeGoModelMetaVersion {
		return fmt.Errorf("unsupported OpenCode Go model metadata cache version")
	}
	if meta.Models == nil {
		return errors.New("OpenCode Go model metadata cache has no models object")
	}
	for modelID, options := range meta.Models {
		if strings.TrimSpace(modelID) == "" {
			return errors.New("OpenCode Go model metadata cache contains an empty model ID")
		}
		seen := make(map[string]bool, len(options.Effort))
		for _, value := range options.Effort {
			choice, ok := normalizeThinkingChoice(value)
			if !ok || choice != value || seen[value] {
				return fmt.Errorf("OpenCode Go model metadata cache has an invalid effort choice for %q", modelID)
			}
			seen[value] = true
		}
	}
	return nil
}

func normalizeThinkingChoice(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return value, true
}

func appendUnique(values []string, candidate string) []string {
	for _, value := range values {
		if value == candidate {
			return values
		}
	}
	return append(values, candidate)
}
