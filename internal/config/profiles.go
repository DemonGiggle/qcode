package config

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// SetLegacyProvider pins the original effective config provider for user files
// whose legacy connection fields have no root provider. Call before switching;
// do not pass the new selection or an environment/flag override. A provider
// explicitly present in the user file takes precedence over this fallback.
func (w *RuntimePreferenceWriter) SetLegacyProvider(provider string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.legacyProvider = provider
}

// SetLegacyConfig retains the user file's original layer-specific connection
// owner, even if an executable-adjacent config supplies a different default.
func (w *RuntimePreferenceWriter) SetLegacyConfig(cfg Config) {
	if w == nil {
		return
	}
	owner := cfg.legacyConnectionOwners[filepath.Clean(w.path)]
	if owner == "" {
		owner = cfg.Provider
	}
	if owner == "" {
		owner = "ollama"
	}
	w.SetLegacyProvider(owner)
}

// PersistProvider atomically stores the selected provider and its
// [providers.<name>] model/thinking preference so switching back restores
// them. Empty thinking removes the profile thinking override. Deprecated root
// model/thinking and provider_models/provider_thinking entries for this
// provider are removed. Legacy root connection fields in this file are first
// migrated to their original owner; connection values from other layers,
// flags or the environment are never saved.
func (w *RuntimePreferenceWriter) PersistProvider(provider, model, thinking string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	provider, model, thinking = strings.TrimSpace(provider), strings.TrimSpace(model), strings.TrimSpace(thinking)
	if !validProvider(provider) {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if model == "" {
		return errors.New("model must not be empty")
	}
	if !utf8.ValidString(model) || !utf8.ValidString(thinking) {
		return errors.New("runtime preference contains invalid UTF-8")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.persistWithProviderProfile(map[string]*string{
		"provider": stringPointer(tomlString(provider)),
	}, provider, stringPointer(tomlString(model)), optionalTOMLString(thinking))
}

func migrateLegacyConnection(data []byte, fallback string) ([]byte, error) {
	cfg, err := decodeConfig(data, false)
	if err != nil {
		return nil, err
	}
	entries, _, err := runtimeConfigEntries(data)
	if err != nil {
		return nil, err
	}
	_, base := entries["base_url"]
	_, key := entries["api_key"]
	if !base && !key {
		return data, nil
	}
	owner := cfg.Provider
	if owner == "" {
		owner = fallback
	}
	if !validProvider(owner) {
		return nil, errors.New("legacy connection owner is unknown; call SetLegacyProvider before switching")
	}
	profile := cfg.Providers[owner]
	for _, field := range []string{"base_url", "api_key"} {
		entry, exists := entries[field]
		if !exists {
			continue
		}
		explicit := profile.BaseURL
		if field == "api_key" {
			explicit = profile.APIKey
		}
		if explicit != nil {
			continue
		}
		// Copy the file's literal value, never a resolved connection or secret.
		literal := string(data[entry.valueStart:entry.valueEnd])
		// Reparse after each insertion, since offsets may have moved.
		data, err = insertProfileField(data, []string{"providers", owner, field}, literal)
		if err != nil {
			return nil, err
		}
		entries, _, err = runtimeConfigEntries(data)
		if err != nil {
			return nil, err
		}
	}
	data, _, err = patchRuntimeConfig(data, map[string]*string{"base_url": nil, "api_key": nil})
	if err != nil {
		return nil, err
	}
	var check map[string]any
	if err := toml.Unmarshal(data, &check); err != nil {
		return nil, err
	}
	return data, nil
}

func pathPrefix(prefix, path []string) bool {
	if len(prefix) >= len(path) {
		return false
	}
	for i := range prefix {
		if prefix[i] != path[i] {
			return false
		}
	}
	return true
}

// insertProfileField inserts a missing leaf without reserializing existing
// tables. It supports explicit tables, dotted assignments and inline ancestors.
func insertProfileField(data []byte, target []string, literal string) ([]byte, error) {
	parser := unstable.Parser{}
	parser.Reset(data)
	var table []string
	bestDepth, insertAt := 0, 0
	inline, inlineNonempty := false, false
	_, firstTable, err := runtimeConfigEntries(data)
	if err != nil {
		return nil, err
	}
	if firstTable < 0 {
		firstTable = len(data)
	}
	insertAt = firstTable
	var visit func(*unstable.Node, []string)
	visit = func(kv *unstable.Node, parent []string) {
		path := append(append([]string(nil), parent...), expressionKeyPath(kv)...)
		value := kv.Value()
		if value == nil || value.Kind != unstable.InlineTable || !pathPrefix(path, target) {
			return
		}
		if len(path) > bestDepth {
			bestDepth, insertAt = len(path), int(value.Raw.Offset)+1
			inline, inlineNonempty = true, value.Child() != nil
		}
		for child := value.Child(); child != nil; child = child.Next() {
			visit(child, path)
		}
	}
	for parser.NextExpression() {
		node := parser.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = expressionKeyPath(node)
			if node.Kind == unstable.Table && pathPrefix(table, target) && len(table) > bestDepth {
				bestDepth = len(table)
				insertAt = lineEnd(data, expressionTableOffset(data, node))
				inline = false
			}
		case unstable.KeyValue:
			visit(node, table)
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	parts := make([]string, 0, len(target)-bestDepth)
	for _, part := range target[bestDepth:] {
		parts = append(parts, tomlString(part))
	}
	text := strings.Join(parts, ".") + " = " + literal
	if inline {
		// Use nested inline tables rather than dotted map entries: the TOML
		// decoder cannot address pointer fields in map values on that path.
		text = parts[len(parts)-1] + " = " + literal
		for i := len(parts) - 2; i >= 0; i-- {
			text = parts[i] + " = {" + text + "}"
		}
		if inlineNonempty {
			text += ", "
		}
	} else {
		nl := detectedNewline(data)
		if insertAt > 0 && !bytes.HasSuffix(data[:insertAt], []byte("\n")) {
			text = nl + text
		}
		text += nl
	}
	return applyRuntimeConfigEdits(data, []runtimeConfigEdit{{start: insertAt, end: insertAt, replacement: []byte(text)}}), nil
}

// Decode profiles independently to avoid go-toml v2.3.1's unaddressable map
// struct value panic for dotted keys containing optional pointer fields.
func decodeConfig(data []byte, strict bool) (Config, error) {
	var document map[string]any
	var cfg Config
	if err := toml.Unmarshal(data, &document); err != nil {
		return cfg, err
	}
	profiles, present := document["providers"]
	delete(document, "providers")
	root, err := toml.Marshal(document)
	if err != nil {
		return cfg, err
	}
	decoder := toml.NewDecoder(bytes.NewReader(root))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, err
	}
	if !present {
		return cfg, nil
	}
	tables, ok := profiles.(map[string]any)
	if !ok {
		return cfg, errors.New("providers must be a table")
	}
	cfg.Providers = make(map[string]ProviderProfile, len(tables))
	for name, table := range tables {
		if _, ok := table.(map[string]any); !ok {
			return cfg, fmt.Errorf("providers.%s must be a table", name)
		}
		encoded, err := toml.Marshal(table)
		if err != nil {
			return cfg, err
		}
		var profile ProviderProfile
		decoder := toml.NewDecoder(bytes.NewReader(encoded))
		if strict {
			decoder.DisallowUnknownFields()
		}
		if err := decoder.Decode(&profile); err != nil {
			return cfg, fmt.Errorf("providers.%s: %w", name, err)
		}
		cfg.Providers[name] = profile
	}
	return cfg, nil
}
