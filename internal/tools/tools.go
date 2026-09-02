package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"qcode/internal/llm"
	"qcode/internal/prompt"
)

const maxOutput = 64 * 1024

type Handler func(context.Context, json.RawMessage) (string, error)

type ExecutionResult struct {
	Output string
	Diff   string
}

type Registry struct {
	root     string
	schemas  []llm.Tool
	handlers map[string]Handler
}

func New(root string) (*Registry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	r := &Registry{root: abs, handlers: map[string]Handler{}}
	r.add(llm.Tool{Name: "read", Description: prompt.ReadTool, Parameters: objectSchema(map[string]any{
		"path":   stringProperty(prompt.PathParameter),
		"offset": integerProperty(prompt.OffsetParameter),
		"limit":  integerProperty(prompt.LimitParameter),
	}, "path")}, r.read)
	r.add(llm.Tool{Name: "write", Description: prompt.WriteTool, Parameters: objectSchema(map[string]any{
		"path": stringProperty(prompt.PathParameter), "content": stringProperty(prompt.ContentParameter),
	}, "path", "content")}, r.write)
	r.add(llm.Tool{Name: "edit", Description: prompt.EditTool, Parameters: objectSchema(map[string]any{
		"path": stringProperty(prompt.PathParameter), "old_text": stringProperty(prompt.OldTextParameter), "new_text": stringProperty(prompt.NewTextParameter),
	}, "path", "old_text", "new_text")}, r.edit)
	r.add(llm.Tool{Name: "list", Description: prompt.ListTool, Parameters: objectSchema(map[string]any{
		"path": stringProperty(prompt.DirectoryParameter),
	})}, r.list)
	r.add(llm.Tool{Name: "search", Description: prompt.SearchTool, Parameters: objectSchema(map[string]any{
		"pattern": stringProperty(prompt.PatternParameter), "path": stringProperty(prompt.SearchPathParameter), "max_results": integerProperty(prompt.MaxResultsParameter),
	}, "pattern")}, r.search)
	r.add(llm.Tool{Name: "shell", Description: prompt.ShellTool, Parameters: objectSchema(map[string]any{
		"command": stringProperty(prompt.CommandParameter), "timeout_ms": integerProperty(prompt.TimeoutParameter),
	}, "command")}, r.shell)
	return r, nil
}

func (r *Registry) add(schema llm.Tool, handler Handler) {
	r.schemas = append(r.schemas, schema)
	r.handlers[schema.Name] = handler
}

func (r *Registry) Schemas() []llm.Tool { return append([]llm.Tool(nil), r.schemas...) }

func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	result, err := r.ExecuteDetailed(ctx, call)
	return result.Output, err
}

func (r *Registry) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (ExecutionResult, error) {
	switch call.Name {
	case "write":
		return r.writeDetailed(ctx, call.Arguments)
	case "edit":
		return r.editDetailed(ctx, call.Arguments)
	}
	handler, ok := r.handlers[call.Name]
	if !ok {
		return ExecutionResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	output, err := handler(ctx, call.Arguments)
	return ExecutionResult{Output: output}, err
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}
func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}
func integerProperty(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func decode(arguments json.RawMessage, target any) error {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid arguments: expected one JSON object")
	}
	return nil
}

// flexibleInteger tolerates integral JSON numbers encoded as numbers, decimal
// numbers, or strings. Some local models emit schema integers as "200.0".
type flexibleInteger int

func (value *flexibleInteger) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if len(text) > 0 && text[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("must be an integer: %w", err)
		}
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(number, 0) || math.IsNaN(number) || math.Trunc(number) != number {
		return fmt.Errorf("must be an integer, got %q", text)
	}
	maxInt := int64(^uint(0) >> 1)
	minInt := -maxInt - 1
	if number < float64(minInt) || number > float64(maxInt) {
		return fmt.Errorf("integer %q is out of range", text)
	}
	*value = flexibleInteger(int(number))
	return nil
}

func (r *Registry) resolve(name string) (string, error) {
	if name == "" {
		name = "."
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(r.root, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside workspace %q", name, r.root)
	}
	return path, nil
}

func (r *Registry) read(_ context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Path   string          `json:"path"`
		Offset flexibleInteger `json:"offset"`
		Line   flexibleInteger `json:"line"` // Backward compatibility with qcode 0.1.
		Limit  flexibleInteger `json:"limit"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("file appears to be binary")
	}
	offset := int(args.Offset)
	if offset < 1 {
		offset = int(args.Line)
	}
	if offset < 1 {
		offset = 1
	}
	limit := int(args.Limit)
	if limit <= 0 {
		limit = 200
	}
	if limit > 2000 {
		limit = 2000
	}
	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d is beyond end of file (%d lines total)", offset, len(lines))
	}
	end := offset - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var out strings.Builder
	for i := offset - 1; i < end; i++ {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "[showing lines %d-%d of %d; use offset=%d to continue]\n", offset, end, len(lines), end+1)
	}
	return truncate(out.String()), nil
}

func (r *Registry) write(_ context.Context, arguments json.RawMessage) (string, error) {
	result, err := r.writeDetailed(context.Background(), arguments)
	return result.Output, err
}

func (r *Registry) writeDetailed(_ context.Context, arguments json.RawMessage) (ExecutionResult, error) {
	var args struct{ Path, Content string }
	if err := decode(arguments, &args); err != nil {
		return ExecutionResult{}, err
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return ExecutionResult{}, err
	}
	before, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ExecutionResult{}, readErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ExecutionResult{}, err
	}
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{
		Output: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path),
		Diff:   unifiedDiff(args.Path, before, []byte(args.Content), existed),
	}, nil
}

func (r *Registry) edit(_ context.Context, arguments json.RawMessage) (string, error) {
	result, err := r.editDetailed(context.Background(), arguments)
	return result.Output, err
}

func (r *Registry) editDetailed(_ context.Context, arguments json.RawMessage) (ExecutionResult, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := decode(arguments, &args); err != nil {
		return ExecutionResult{}, err
	}
	if args.OldText == "" {
		return ExecutionResult{}, errors.New("old_text must not be empty")
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return ExecutionResult{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ExecutionResult{}, err
	}
	count := bytes.Count(data, []byte(args.OldText))
	if count != 1 {
		return ExecutionResult{}, fmt.Errorf("old_text must occur exactly once; found %d occurrences", count)
	}
	updated := bytes.Replace(data, []byte(args.OldText), []byte(args.NewText), 1)
	info, err := os.Stat(path)
	if err != nil {
		return ExecutionResult{}, err
	}
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Output: fmt.Sprintf("edited %s", args.Path), Diff: unifiedDiff(args.Path, data, updated, true)}, nil
}

func (r *Registry) list(_ context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		items = append(items, name)
	}
	sort.Strings(items)
	return truncate(strings.Join(items, "\n")), nil
}

func (r *Registry) search(_ context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Pattern, Path string
		MaxResults    int `json:"max_results"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return "", err
	}
	if args.MaxResults <= 0 {
		args.MaxResults = 100
	}
	if args.MaxResults > 1000 {
		args.MaxResults = 1000
	}
	var matches []string
	err = filepath.WalkDir(path, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if file != path && (entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if len(matches) >= args.MaxResults {
			return fs.SkipAll
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > 2*1024*1024 {
			return nil
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for number, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(r.root, file)
				matches = append(matches, rel+":"+strconv.Itoa(number+1)+":"+line)
				if len(matches) >= args.MaxResults {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	return truncate(strings.Join(matches, "\n")), nil
}

func (r *Registry) shell(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Command   string `json:"command"`
		TimeoutMS int    `json:"timeout_ms"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	if args.TimeoutMS <= 0 {
		args.TimeoutMS = 120000
	}
	if args.TimeoutMS > 600000 {
		args.TimeoutMS = 600000
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutMS)*time.Millisecond)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(commandCtx, "cmd.exe", "/d", "/s", "/c", args.Command)
	} else {
		cmd = exec.CommandContext(commandCtx, "/bin/sh", "-c", args.Command)
	}
	cmd.Dir = r.root
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	text := output.String()
	if commandCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("command timed out after %dms", args.TimeoutMS)
	}
	if err != nil {
		return text, fmt.Errorf("command failed: %w", err)
	}
	if text == "" {
		text = "command completed with no output"
	}
	return text, nil
}

type limitedBuffer struct {
	data    []byte
	omitted int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	n := len(data)
	remaining := maxOutput - len(b.data)
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		b.data = append(b.data, data[:remaining]...)
	}
	b.omitted += n - remaining
	return n, nil
}
func (b *limitedBuffer) String() string {
	result := string(b.data)
	if b.omitted > 0 {
		result += fmt.Sprintf("\n[truncated: %d bytes omitted]", b.omitted)
	}
	return result
}
func truncate(value string) string {
	if len(value) <= maxOutput {
		return value
	}
	return value[:maxOutput] + fmt.Sprintf("\n[truncated: %d bytes omitted]", len(value)-maxOutput)
}
