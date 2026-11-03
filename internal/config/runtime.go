package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
	"qcode/internal/theme"
)

// RuntimePreferenceWriter stores preferences changed by the interactive TUI.
// It deliberately writes only the user configuration layer; normal config
// precedence still decides whether those values are effective next startup.
type RuntimePreferenceWriter struct {
	mu             sync.Mutex
	path           string
	legacyProvider string
}

// RuntimePreferences is kept as a descriptive alias for callers that refer
// to the persisted values rather than the implementation detail of writing
// them.
type RuntimePreferences = RuntimePreferenceWriter

// NewRuntimePreferenceWriter returns a writer for the current user's qcode
// configuration file.
func NewRuntimePreferenceWriter() (*RuntimePreferenceWriter, error) {
	path, err := currentUserConfigPath()
	if err != nil {
		return nil, err
	}
	return newRuntimePreferenceWriter(path), nil
}

// NewRuntimePreferences is an alias constructor for callers that use the
// preference-oriented name.
func NewRuntimePreferences() (*RuntimePreferenceWriter, error) {
	return NewRuntimePreferenceWriter()
}

func newRuntimePreferenceWriter(path string) *RuntimePreferenceWriter {
	return &RuntimePreferenceWriter{path: path}
}

func currentUserConfigPath() (string, error) {
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		return userConfigPath(runtime.GOOS, home, ""), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return userConfigPath(runtime.GOOS, "", configDir), nil
}

// userConfigPath returns the user-level configuration path used for runtime
// preferences. Keep this in sync with the user-level candidate in
// candidatePaths, while leaving system and executable-adjacent layers alone.
func userConfigPath(goos, home, userConfigDir string) string {
	switch goos {
	case "linux":
		return filepath.Join(home, ".local", "etc", "qcode", fileName)
	case "darwin", "windows":
		return filepath.Join(userConfigDir, "qcode", fileName)
	default:
		return filepath.Join(userConfigDir, "qcode", fileName)
	}
}

// PersistModel records a model selection and its optional explicit thinking
// level. The model is associated with the given provider: the root model and
// thinking are updated and the provider's last-used entry in provider_models
// and provider_thinking is refreshed. An empty thinking value removes the
// user-level thinking override so lower-priority configuration can be
// inherited.
func (w *RuntimePreferenceWriter) PersistModel(provider, model, thinking string) error {
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
	return w.persistWithProviderMemory(map[string]*string{
		"model":    stringPointer(tomlString(model)),
		"thinking": optionalTOMLString(thinking),
	}, provider, stringPointer(tomlString(model)), optionalTOMLString(thinking))
}

// PersistMaxSteps records the positive model-turn limit in the user config.
func (w *RuntimePreferenceWriter) PersistMaxSteps(maxSteps int) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	if maxSteps <= 0 {
		return errors.New("max steps must be greater than zero")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	value := strconv.Itoa(maxSteps)
	return w.persist(map[string]*string{"max_steps": &value})
}

// PersistInteractive records the default question mode for future sessions.
func (w *RuntimePreferenceWriter) PersistInteractive(enabled bool) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	value := strconv.FormatBool(enabled)
	return w.persist(map[string]*string{"interactive": &value})
}

// statuslineHiddenOrder is the canonical order used when persisting the
// statusline_hidden denylist. It follows status bar priority from highest to
// lowest so the stored list stays deterministic.
var statuslineHiddenOrder = []string{"remote", "mode", "model", "think", "ws", "ctx", "step", "tok"}

// PersistStatuslineHidden records which status bar segments stay hidden in the
// user config. An empty list removes the override so later startups show every
// segment again.
func (w *RuntimePreferenceWriter) PersistStatuslineHidden(hidden []string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	normalized, err := normalizeStatuslineHidden(hidden)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if len(normalized) == 0 {
		return w.persist(map[string]*string{"statusline_hidden": nil})
	}
	value := encodeStatuslineHidden(normalized)
	return w.persist(map[string]*string{"statusline_hidden": &value})
}

// PersistTheme records the selected terminal palette in the user config.
func (w *RuntimePreferenceWriter) PersistTheme(id string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if !theme.ValidID(id) {
		return fmt.Errorf("unknown theme %q", id)
	}
	if !utf8.ValidString(id) {
		return errors.New("theme contains invalid UTF-8")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	value := tomlString(id)
	return w.persist(map[string]*string{"theme": &value})
}

// PersistSelectedSkills records the explicit /skill selection in the user
// config's [skills] table. Both lists are normalized (trimmed, deduped,
// sorted); every always name is also treated as selected. Empty lists are
// stored as explicit [] so a cleared selection overrides lower layers.
func (w *RuntimePreferenceWriter) PersistSelectedSkills(selected, always []string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	normalizedSelected, normalizedAlways, err := normalizeSelectedSkillsPreference(selected, always)
	if err != nil {
		return err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.persistSkills(normalizedSelected, normalizedAlways)
}

func normalizeSelectedSkillsPreference(selected, always []string) ([]string, []string, error) {
	for _, raw := range append(append([]string(nil), selected...), always...) {
		if !utf8.ValidString(raw) {
			return nil, nil, errors.New("skill selection contains invalid UTF-8")
		}
	}
	normalizedSelected, invalid, ok := normalizeSkillSelection(selected)
	if !ok {
		return nil, nil, fmt.Errorf("invalid skill name %q (want 1-64 lowercase letters, digits, hyphens, or underscores)", invalid)
	}
	normalizedAlways, invalid, ok := normalizeSkillSelection(always)
	if !ok {
		return nil, nil, fmt.Errorf("invalid skill name %q (want 1-64 lowercase letters, digits, hyphens, or underscores)", invalid)
	}
	// Every always name stays selected for a valid [skills] document.
	selectedSet := map[string]bool{}
	for _, name := range normalizedSelected {
		selectedSet[name] = true
	}
	for _, name := range normalizedAlways {
		if !selectedSet[name] {
			normalizedSelected = append(normalizedSelected, name)
			selectedSet[name] = true
		}
	}
	for i := 1; i < len(normalizedSelected); i++ {
		for j := i; j > 0 && normalizedSelected[j] < normalizedSelected[j-1]; j-- {
			normalizedSelected[j], normalizedSelected[j-1] = normalizedSelected[j-1], normalizedSelected[j]
		}
	}
	return normalizedSelected, normalizedAlways, nil
}

func encodeSkillSelectionList(names []string) string {
	if len(names) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, tomlString(name))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func normalizeStatuslineHidden(hidden []string) ([]string, error) {
	seen := make(map[string]bool, len(hidden))
	for _, segment := range hidden {
		clean := strings.ToLower(strings.TrimSpace(segment))
		if clean == "" {
			continue
		}
		if !utf8.ValidString(clean) {
			return nil, errors.New("statusline segment contains invalid UTF-8")
		}
		known := false
		for _, candidate := range statuslineHiddenOrder {
			if clean == candidate {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unknown statusline segment %q", segment)
		}
		seen[clean] = true
	}
	normalized := make([]string, 0, len(seen))
	for _, candidate := range statuslineHiddenOrder {
		if seen[candidate] {
			normalized = append(normalized, candidate)
		}
	}
	return normalized, nil
}

func encodeStatuslineHidden(hidden []string) string {
	parts := make([]string, 0, len(hidden))
	for _, segment := range hidden {
		parts = append(parts, tomlString(segment))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func stringPointer(value string) *string { return &value }

func optionalTOMLString(value string) *string {
	if value == "" {
		return nil
	}
	quoted := tomlString(value)
	return &quoted
}

func (w *RuntimePreferenceWriter) persist(changes map[string]*string) error {
	if strings.TrimSpace(w.path) == "" {
		return errors.New("runtime preference path is empty")
	}
	path := filepath.Clean(w.path)
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create user config directory %s: %w", parent, err)
	}

	data, mode, exists, err := readRuntimeConfig(path)
	if err != nil {
		return err
	}
	original := append([]byte(nil), data...)
	if _, switching := changes["provider"]; switching {
		data, err = migrateLegacyConnection(data, w.legacyProvider)
		if err != nil {
			return fmt.Errorf("update config %s: %w", path, err)
		}
	}
	updated, changed, err := patchRuntimeConfig(data, changes)
	changed = changed || !bytes.Equal(original, data)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	if !changed {
		return nil
	}
	if !exists {
		mode = 0o600
	}
	if err := writeRuntimeConfigAtomically(path, updated, mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// persistWithProviderMemory writes root preference changes and refreshes the
// provider's last-used model/thinking in provider_models and provider_thinking.
// modelLiteral and thinkingLiteral are TOML values (nil removes the thinking
// entry). The caller must hold w.mu.
func (w *RuntimePreferenceWriter) persistWithProviderMemory(changes map[string]*string, provider string, modelLiteral, thinkingLiteral *string) error {
	if strings.TrimSpace(w.path) == "" {
		return errors.New("runtime preference path is empty")
	}
	path := filepath.Clean(w.path)
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create user config directory %s: %w", parent, err)
	}
	data, mode, exists, err := readRuntimeConfig(path)
	if err != nil {
		return err
	}
	original := append([]byte(nil), data...)
	if _, switching := changes["provider"]; switching {
		data, err = migrateLegacyConnection(data, w.legacyProvider)
		if err != nil {
			return fmt.Errorf("update config %s: %w", path, err)
		}
	}
	updated, changed, err := patchRuntimeConfig(data, changes)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	data = updated
	if modelLiteral != nil {
		updated, modelChanged, err := patchProviderMemoryTable(data, "provider_models", provider, modelLiteral)
		if err != nil {
			return fmt.Errorf("update config %s: %w", path, err)
		}
		data = updated
		changed = changed || modelChanged
	}
	updated, thinkingChanged, err := patchProviderMemoryTable(data, "provider_thinking", provider, thinkingLiteral)
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	data = updated
	changed = changed || thinkingChanged
	changed = changed || !bytes.Equal(original, data)
	if !changed {
		return nil
	}
	if !exists {
		mode = 0o600
	}
	if err := writeRuntimeConfigAtomically(path, data, mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// patchProviderMemoryTable upserts or removes a single provider entry in a
// [provider_models] or [provider_thinking] table while preserving comments.
// A nil literal removes the entry; otherwise the TOML value replaces it.
func patchProviderMemoryTable(data []byte, table, key string, literal *string) ([]byte, bool, error) {
	if len(data) == 0 {
		if literal == nil {
			return data, false, nil
		}
		newline := "\n"
		var output bytes.Buffer
		output.WriteString("[" + table + "]")
		output.WriteString(newline)
		output.WriteString(providerMemoryKey(key))
		output.WriteString(" = ")
		output.WriteString(*literal)
		output.WriteString(newline)
		return output.Bytes(), true, nil
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, false, err
	}
	entries, hasTable, tableEnd, err := providerMemoryEntries(data, table)
	if err != nil {
		return nil, false, err
	}
	if entry, found := entries[key]; found {
		if literal == nil {
			removeStart, removeEnd := lineStart(data, entry.keyValueStart), lineEnd(data, entry.keyValueEnd)
			return applyRuntimeConfigEdits(data, []runtimeConfigEdit{{start: removeStart, end: removeEnd}}), true, nil
		}
		if bytes.Equal(data[entry.valueStart:entry.valueEnd], []byte(*literal)) {
			return append([]byte(nil), data...), false, nil
		}
		return applyRuntimeConfigEdits(data, []runtimeConfigEdit{{start: entry.valueStart, end: entry.valueEnd, replacement: []byte(*literal)}}), true, nil
	}
	if literal == nil {
		return append([]byte(nil), data...), false, nil
	}
	if !hasTable {
		newline := detectedNewline(data)
		var inserted bytes.Buffer
		if len(bytes.TrimSpace(data)) > 0 {
			if !endsInNewline(data) {
				inserted.WriteString(newline)
			}
			inserted.WriteString(newline)
		}
		inserted.WriteString("[" + table + "]")
		inserted.WriteString(newline)
		inserted.WriteString(providerMemoryKey(key))
		inserted.WriteString(" = ")
		inserted.WriteString(*literal)
		inserted.WriteString(newline)
		return append(data, inserted.Bytes()...), true, nil
	}
	insertAt := tableEnd
	if insertAt < 0 {
		insertAt = len(data)
	}
	newline := detectedNewline(data)
	var inserted bytes.Buffer
	inserted.WriteString(providerMemoryKey(key))
	inserted.WriteString(" = ")
	inserted.WriteString(*literal)
	inserted.WriteString(newline)
	if insertAt > 0 && data[insertAt-1] != '\n' && data[insertAt-1] != '\r' {
		prefix := []byte(newline)
		prefix = append(prefix, inserted.Bytes()...)
		inserted.Reset()
		inserted.Write(prefix)
	}
	return applyRuntimeConfigEdits(data, []runtimeConfigEdit{{start: insertAt, end: insertAt, replacement: inserted.Bytes()}}), true, nil
}

func providerMemoryKey(key string) string {
	// Provider IDs use only bare-key characters; keep the file readable.
	// Quote anything unexpected so the table stays valid TOML.
	for _, r := range key {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return tomlString(key)
		}
	}
	if key == "" {
		return tomlString(key)
	}
	return key
}

func providerMemoryEntries(data []byte, table string) (map[string]runtimeConfigEntry, bool, int, error) {
	parser := unstable.Parser{}
	parser.Reset(data)
	entries := make(map[string]runtimeConfigEntry)
	hasTable := false
	inTable := false
	inRoot := true
	tableEnd := -1
	for parser.NextExpression() {
		expression := parser.Expression()
		switch expression.Kind {
		case unstable.Table, unstable.ArrayTable:
			path := expressionKeyPath(expression)
			if expression.Kind == unstable.Table && len(path) == 1 && stripKeyQuotes(path[0]) == table {
				hasTable = true
				inTable = true
				inRoot = false
				continue
			}
			if hasTable && tableEnd < 0 && (inTable || inRoot) {
				if offset := expressionTableOffset(data, expression); offset >= 0 {
					tableEnd = lineStart(data, offset)
				}
			}
			inTable = false
			inRoot = false
		case unstable.KeyValue:
			path := expressionKeyPath(expression)
			var name string
			var isMemoryKey bool
			if inTable && len(path) == 1 {
				name = stripKeyQuotes(path[0])
				isMemoryKey = true
			} else if inRoot && len(path) == 2 && stripKeyQuotes(path[0]) == table {
				name = stripKeyQuotes(path[1])
				isMemoryKey = true
			}
			if !isMemoryKey {
				continue
			}
			if _, exists := entries[name]; exists {
				continue
			}
			exprStart := int(expression.Raw.Offset)
			exprEnd := exprStart + int(expression.Raw.Length)
			if exprStart < 0 || exprEnd > len(data) || exprStart >= exprEnd {
				continue
			}
			eq := bytes.IndexByte(data[exprStart:exprEnd], '=')
			if eq < 0 {
				continue
			}
			valueStart := exprStart + eq + 1
			for valueStart < exprEnd && (data[valueStart] == ' ' || data[valueStart] == '\t') {
				valueStart++
			}
			entries[name] = runtimeConfigEntry{
				keyValueStart: exprStart,
				keyValueEnd:   exprEnd,
				valueStart:    valueStart,
				valueEnd:      exprEnd,
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, false, -1, err
	}
	return entries, hasTable, tableEnd, nil
}

func stripKeyQuotes(key string) string {
	if len(key) >= 2 {
		if (key[0] == '"' && key[len(key)-1] == '"') || (key[0] == '\'' && key[len(key)-1] == '\'') {
			// Best effort: quoted TOML keys decode to their inner value; the
			// parser preserves the quotes in Data for some forms.
			return key[1 : len(key)-1]
		}
	}
	return key
}

func (w *RuntimePreferenceWriter) persistSkills(selected, always []string) error {
	if strings.TrimSpace(w.path) == "" {
		return errors.New("runtime preference path is empty")
	}
	path := filepath.Clean(w.path)
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create user config directory %s: %w", parent, err)
	}
	data, mode, exists, err := readRuntimeConfig(path)
	if err != nil {
		return err
	}
	updated, changed, err := patchSkillsSelection(data, encodeSkillSelectionList(selected), encodeSkillSelectionList(always))
	if err != nil {
		return fmt.Errorf("update config %s: %w", path, err)
	}
	if !changed {
		return nil
	}
	if !exists {
		mode = 0o600
	}
	if err := writeRuntimeConfigAtomically(path, updated, mode); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

func readRuntimeConfig(path string) ([]byte, os.FileMode, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, false, nil
	}
	if err != nil {
		return nil, 0, true, fmt.Errorf("read config %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, true, fmt.Errorf("stat config %s: %w", path, err)
	}
	return data, info.Mode().Perm(), true, nil
}

type runtimeConfigEdit struct {
	start       int
	end         int
	replacement []byte
}

type runtimeConfigEntry struct {
	keyValueStart int
	keyValueEnd   int
	valueStart    int
	valueEnd      int
}

// patchRuntimeConfig validates the complete existing document first, then
// changes only the managed root-level values. The TOML AST supplies exact
// assignment ranges, which keep comments, spacing, and unrelated settings intact.
func patchRuntimeConfig(data []byte, changes map[string]*string) ([]byte, bool, error) {
	if len(data) == 0 {
		rendered := renderRuntimeKeys(changes, "\n")
		return append([]byte(nil), rendered...), len(rendered) != 0, nil
	}

	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, false, err
	}

	entries, firstTable, err := runtimeConfigEntries(data)
	if err != nil {
		return nil, false, err
	}

	var edits []runtimeConfigEdit
	missing := make(map[string]*string)
	for key, value := range changes {
		entry, found := entries[key]
		if value == nil {
			if found {
				removeStart, removeEnd := entry.keyValueStart, entry.keyValueEnd
				endOfLine := lineEnd(data, entry.keyValueEnd)
				if len(bytes.TrimSpace(data[entry.keyValueEnd:endOfLine])) == 0 {
					removeStart, removeEnd = lineStart(data, entry.keyValueStart), endOfLine
				}
				edits = append(edits, runtimeConfigEdit{
					start:       removeStart,
					end:         removeEnd,
					replacement: nil,
				})
			}
			continue
		}
		if found {
			if bytes.Equal(data[entry.valueStart:entry.valueEnd], []byte(*value)) {
				continue
			}
			edits = append(edits, runtimeConfigEdit{
				start:       entry.valueStart,
				end:         entry.valueEnd,
				replacement: []byte(*value),
			})
			continue
		}
		missing[key] = value
	}

	if len(missing) != 0 {
		insertAt := firstTable
		if insertAt < 0 {
			insertAt = len(data)
		}
		newline := detectedNewline(data)
		inserted := renderRuntimeKeys(missing, newline)
		if firstTable < 0 && len(data) > 0 && !endsInNewline(data) {
			inserted = append([]byte(newline), inserted...)
		}
		edits = append(edits, runtimeConfigEdit{start: insertAt, end: insertAt, replacement: inserted})
	}

	if len(edits) == 0 {
		return append([]byte(nil), data...), false, nil
	}
	return applyRuntimeConfigEdits(data, edits), true, nil
}

func runtimeConfigEntries(data []byte) (map[string]runtimeConfigEntry, int, error) {
	parser := unstable.Parser{}
	parser.Reset(data)
	entries := make(map[string]runtimeConfigEntry)
	firstTable := -1
	inRoot := true
	for parser.NextExpression() {
		expression := parser.Expression()
		switch expression.Kind {
		case unstable.Table, unstable.ArrayTable:
			if firstTable < 0 {
				// Table nodes do not carry a Raw range, but their keys do.
				keys := expression.Key()
				keys.Next()
				firstTable = lineStart(data, int(keys.Node().Raw.Offset))
			}
			inRoot = false
		case unstable.KeyValue:
			if !inRoot {
				continue
			}
			keyIterator := expression.Key()
			keyIterator.Next()
			key := keyIterator.Node()
			if key == nil || keyIterator.Node().Next() != nil {
				continue
			}
			name := string(key.Data)
			if name != "provider" && name != "base_url" && name != "api_key" && name != "model" && name != "thinking" && name != "max_steps" && name != "interactive" && name != "statusline_hidden" && name != "theme" {
				continue
			}
			// Array nodes do not carry a Raw range. Derive the value span
			// from the key and the complete assignment, including closing
			// brackets and any whitespace or comments inside the array.
			keyValueEnd := int(expression.Raw.Offset + expression.Raw.Length)
			valueStart := int(key.Raw.Offset + key.Raw.Length)
			valueStart += bytes.IndexByte(data[valueStart:keyValueEnd], '=') + 1
			for valueStart < keyValueEnd && (data[valueStart] == ' ' || data[valueStart] == '\t') {
				valueStart++
			}
			entries[name] = runtimeConfigEntry{
				keyValueStart: int(expression.Raw.Offset),
				keyValueEnd:   keyValueEnd,
				valueStart:    valueStart,
				valueEnd:      keyValueEnd,
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, -1, err
	}
	return entries, firstTable, nil
}

func renderRuntimeKeys(changes map[string]*string, newline string) []byte {
	var output bytes.Buffer
	for _, key := range []string{"provider", "model", "thinking", "max_steps", "interactive", "statusline_hidden", "theme"} {
		value, ok := changes[key]
		if !ok || value == nil {
			continue
		}
		output.WriteString(key)
		output.WriteString(" = ")
		output.WriteString(*value)
		output.WriteString(newline)
	}
	return output.Bytes()
}

func patchSkillsSelection(data []byte, selectedValue, alwaysValue string) ([]byte, bool, error) {
	if len(data) == 0 {
		newline := "\n"
		var output bytes.Buffer
		output.WriteString("[skills]")
		output.WriteString(newline)
		output.WriteString("selected = ")
		output.WriteString(selectedValue)
		output.WriteString(newline)
		output.WriteString("selected_always = ")
		output.WriteString(alwaysValue)
		output.WriteString(newline)
		return output.Bytes(), true, nil
	}
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, false, err
	}
	entries, hasSkills, skillsEnd, err := skillsConfigEntries(data)
	if err != nil {
		return nil, false, err
	}
	desired := map[string]string{
		"selected":        selectedValue,
		"selected_always": alwaysValue,
	}
	var edits []runtimeConfigEdit
	var missing []string
	for _, key := range []string{"selected", "selected_always"} {
		want := desired[key]
		entry, found := entries[key]
		if found {
			if bytes.Equal(data[entry.valueStart:entry.valueEnd], []byte(want)) {
				continue
			}
			edits = append(edits, runtimeConfigEdit{
				start:       entry.valueStart,
				end:         entry.valueEnd,
				replacement: []byte(want),
			})
			continue
		}
		missing = append(missing, key)
	}
	if len(missing) != 0 {
		newline := detectedNewline(data)
		var inserted bytes.Buffer
		if !hasSkills {
			if len(data) > 0 && !endsInNewline(data) {
				inserted.WriteString(newline)
			}
			if len(bytes.TrimSpace(data)) > 0 {
				inserted.WriteString(newline)
			}
			inserted.WriteString("[skills]")
			inserted.WriteString(newline)
			for _, key := range []string{"selected", "selected_always"} {
				inserted.WriteString(key)
				inserted.WriteString(" = ")
				inserted.WriteString(desired[key])
				inserted.WriteString(newline)
			}
			edits = append(edits, runtimeConfigEdit{start: len(data), end: len(data), replacement: inserted.Bytes()})
		} else {
			insertAt := skillsEnd
			if insertAt < 0 {
				insertAt = len(data)
			}
			for _, key := range missing {
				inserted.WriteString(key)
				inserted.WriteString(" = ")
				inserted.WriteString(desired[key])
				inserted.WriteString(newline)
			}
			// Ensure the inserted block starts on its own line.
			if insertAt > 0 && insertAt < len(data) && data[insertAt-1] != '\n' && data[insertAt-1] != '\r' {
				prefix := []byte(newline)
				prefix = append(prefix, inserted.Bytes()...)
				inserted.Reset()
				inserted.Write(prefix)
			}
			edits = append(edits, runtimeConfigEdit{start: insertAt, end: insertAt, replacement: inserted.Bytes()})
		}
	}
	if len(edits) == 0 {
		return append([]byte(nil), data...), false, nil
	}
	return applyRuntimeConfigEdits(data, edits), true, nil
}

func skillsConfigEntries(data []byte) (map[string]runtimeConfigEntry, bool, int, error) {
	parser := unstable.Parser{}
	parser.Reset(data)
	entries := make(map[string]runtimeConfigEntry)
	hasSkills := false
	inSkills := false
	inRoot := true
	skillsEnd := -1
	for parser.NextExpression() {
		expression := parser.Expression()
		switch expression.Kind {
		case unstable.Table, unstable.ArrayTable:
			path := expressionKeyPath(expression)
			if expression.Kind == unstable.Table && len(path) == 1 && path[0] == "skills" {
				hasSkills = true
				inSkills = true
				inRoot = false
				continue
			}
			if hasSkills && skillsEnd < 0 && (inSkills || inRoot) {
				if offset := expressionTableOffset(data, expression); offset >= 0 {
					skillsEnd = lineStart(data, offset)
				}
			}
			inSkills = false
			inRoot = false
		case unstable.KeyValue:
			path := expressionKeyPath(expression)
			var name string
			var isSkillsKey bool
			if inSkills && len(path) == 1 && (path[0] == "selected" || path[0] == "selected_always") {
				name = path[0]
				isSkillsKey = true
			} else if inRoot && len(path) == 2 && path[0] == "skills" && (path[1] == "selected" || path[1] == "selected_always") {
				name = path[1]
				isSkillsKey = true
			}
			if !isSkillsKey {
				continue
			}
			if _, exists := entries[name]; exists {
				continue
			}
			exprStart := int(expression.Raw.Offset)
			exprEnd := exprStart + int(expression.Raw.Length)
			if exprStart < 0 || exprEnd > len(data) || exprStart >= exprEnd {
				continue
			}
			eq := bytes.IndexByte(data[exprStart:exprEnd], '=')
			if eq < 0 {
				continue
			}
			valueStart := exprStart + eq + 1
			for valueStart < exprEnd && (data[valueStart] == ' ' || data[valueStart] == '\t') {
				valueStart++
			}
			entries[name] = runtimeConfigEntry{
				keyValueStart: exprStart,
				keyValueEnd:   exprEnd,
				valueStart:    valueStart,
				valueEnd:      exprEnd,
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, false, -1, err
	}
	return entries, hasSkills, skillsEnd, nil
}

func expressionKeyPath(expression *unstable.Node) []string {
	var path []string
	iter := expression.Key()
	for iter.Next() {
		node := iter.Node()
		if node == nil {
			continue
		}
		path = append(path, string(node.Data))
	}
	return path
}

func expressionTableOffset(data []byte, expression *unstable.Node) int {
	iter := expression.Key()
	if !iter.Next() {
		return -1
	}
	node := iter.Node()
	if node == nil {
		return -1
	}
	offset := int(node.Raw.Offset)
	if offset < 0 || offset > len(data) {
		return -1
	}
	return offset
}

func applyRuntimeConfigEdits(data []byte, edits []runtimeConfigEdit) []byte {
	// Edits are applied from right to left, so offsets remain valid.
	for i := 0; i < len(edits); i++ {
		for j := i + 1; j < len(edits); j++ {
			if edits[j].start > edits[i].start {
				edits[i], edits[j] = edits[j], edits[i]
			}
		}
	}
	updated := append([]byte(nil), data...)
	for _, edit := range edits {
		updated = append(updated[:edit.start], append(append([]byte(nil), edit.replacement...), updated[edit.end:]...)...)
	}
	return updated
}

func lineStart(data []byte, offset int) int {
	if offset > len(data) {
		offset = len(data)
	}
	if newline := bytes.LastIndexByte(data[:offset], '\n'); newline >= 0 {
		return newline + 1
	}
	return 0
}

func lineEnd(data []byte, offset int) int {
	if offset > len(data) {
		offset = len(data)
	}
	if newline := bytes.IndexByte(data[offset:], '\n'); newline >= 0 {
		return offset + newline + 1
	}
	return len(data)
}

func detectedNewline(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func endsInNewline(data []byte) bool {
	return len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r')
}

func tomlString(value string) string {
	var output strings.Builder
	output.Grow(len(value) + 2)
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '\\':
			output.WriteString(`\\`)
		case '"':
			output.WriteString(`\"`)
		case '\b':
			output.WriteString(`\b`)
		case '\t':
			output.WriteString(`\t`)
		case '\n':
			output.WriteString(`\n`)
		case '\f':
			output.WriteString(`\f`)
		case '\r':
			output.WriteString(`\r`)
		default:
			if character < 0x20 || character == 0x7f {
				output.WriteString(fmt.Sprintf(`\u%04x`, character))
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
	return output.String()
}

func writeRuntimeConfigAtomically(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".config.toml.tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}
