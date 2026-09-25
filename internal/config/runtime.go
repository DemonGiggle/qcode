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
)

// RuntimePreferenceWriter stores the preferences changed by the interactive
// main agent. It deliberately writes only the user configuration layer; the
// normal configuration precedence rules still decide whether those values
// are effective on a later startup.
type RuntimePreferenceWriter struct {
	mu   sync.Mutex
	path string
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
// level. An empty thinking value removes the user-level thinking override so
// lower-priority configuration can be inherited.
func (w *RuntimePreferenceWriter) PersistModel(model, thinking string) error {
	if w == nil {
		return errors.New("runtime preference writer is nil")
	}
	model = strings.TrimSpace(model)
	thinking = strings.TrimSpace(thinking)
	if model == "" {
		return errors.New("model must not be empty")
	}
	if !utf8.ValidString(model) || !utf8.ValidString(thinking) {
		return errors.New("runtime preference contains invalid UTF-8")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.persist(map[string]*string{
		"model":    stringPointer(tomlString(model)),
		"thinking": optionalTOMLString(thinking),
	})
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
	updated, changed, err := patchRuntimeConfig(data, changes)
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
// value ranges, which keeps comments, spacing, and unrelated settings intact.
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
				firstTable = lineStart(data, int(expression.Raw.Offset))
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
			if name != "model" && name != "thinking" && name != "max_steps" && name != "statusline_hidden" {
				continue
			}
			value := expression.Value()
			entries[name] = runtimeConfigEntry{
				keyValueStart: int(expression.Raw.Offset),
				keyValueEnd:   int(expression.Raw.Offset + expression.Raw.Length),
				valueStart:    int(value.Raw.Offset),
				valueEnd:      int(value.Raw.Offset + value.Raw.Length),
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
	for _, key := range []string{"model", "thinking", "max_steps", "statusline_hidden"} {
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
