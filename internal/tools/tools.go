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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qcode/internal/llm"
	"qcode/internal/prompt"
)

const maxOutput = 64 * 1024

const maxImageSize = 20 * 1024 * 1024

type Handler func(context.Context, json.RawMessage) (string, error)

type ExecutionResult = llm.ToolResult

type Registry struct {
	root      string
	schemas   []llm.Tool
	handlers  map[string]Handler
	sandbox   *sandboxState
	grantMu   sync.RWMutex
	grants    []string
	approver  DirectoryApprover
	protected []string
	skills    SkillLoader
	disabled  map[string]bool
}

// DirectoryApprover asks the interactive host to approve an additional
// writable directory. selected is ignored when approved is false.
type DirectoryApprover func(ctx context.Context, requested, proposed string) (selected string, approved bool, err error)

func New(root string) (*Registry, error) {
	return NewWithOptions(root, Options{})
}

func NewWithOptions(root string, options Options) (*Registry, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if canonical, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = canonical
	}
	r := &Registry{root: abs, handlers: map[string]Handler{}, grants: []string{abs}, skills: options.Skills}
	if options.Sandbox {
		bwrap := options.BubblewrapPath
		if bwrap == "" {
			bwrap, err = exec.LookPath("bwrap")
			if err != nil {
				return nil, fmt.Errorf("locate bubblewrap: %w", err)
			}
		}
		home, _ := os.UserHomeDir()
		if canonical, evalErr := filepath.EvalSymlinks(home); evalErr == nil {
			home = canonical
		}
		for _, protected := range options.ProtectedPaths {
			if canonical, protectErr := filepath.EvalSymlinks(protected); protectErr == nil {
				r.protected = append(r.protected, canonical)
			}
		}
		r.sandbox = &sandboxState{bwrap: bwrap, home: home, allowNetwork: options.AllowNetwork, protected: r.protected}
	}
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
	r.add(llm.Tool{Name: "view_image", Description: prompt.ImageTool, Parameters: objectSchema(map[string]any{
		"path": stringProperty(prompt.PathParameter),
	}, "path")}, r.viewImage)
	if r.skills != nil {
		r.add(llm.Tool{Name: "skill", Description: prompt.SkillTool, Parameters: objectSchema(map[string]any{
			"name": stringProperty(prompt.SkillNameParameter),
		}, "name")}, r.loadSkill)
	}
	if r.sandbox != nil {
		r.add(llm.Tool{Name: "request_directory_access", Description: prompt.DirectoryAccessTool, Parameters: objectSchema(map[string]any{
			"path": stringProperty(prompt.AccessPathParameter),
		}, "path")}, r.requestDirectoryAccess)
	}
	return r, nil
}

func (r *Registry) loadSkill(_ context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	if args.Name == "" {
		return "", fmt.Errorf("skill name must not be empty")
	}
	return r.skills.Load(args.Name)
}

func (r *Registry) SetDirectoryApprover(approver DirectoryApprover) { r.approver = approver }

// ResetSession removes grants acquired after startup.
func (r *Registry) ResetSession() {
	r.grantMu.Lock()
	r.grants = []string{r.root}
	r.grantMu.Unlock()
}

func (r *Registry) add(schema llm.Tool, handler Handler) {
	r.schemas = append(r.schemas, schema)
	r.handlers[schema.Name] = handler
}

func (r *Registry) Schemas() []llm.Tool { return append([]llm.Tool(nil), r.schemas...) }

// EnabledSchemas returns only the tool schemas that are not disabled.
func (r *Registry) EnabledSchemas() []llm.Tool {
	result := make([]llm.Tool, 0, len(r.schemas))
	for _, tool := range r.schemas {
		if !r.disabled[tool.Name] {
			result = append(result, tool)
		}
	}
	return result
}

// EnableTool re-enables a previously disabled tool.
func (r *Registry) EnableTool(name string) {
	delete(r.disabled, name)
}

// DisableTool prevents a tool from being sent to the model.
func (r *Registry) DisableTool(name string) {
	if r.disabled == nil {
		r.disabled = make(map[string]bool)
	}
	r.disabled[name] = true
}

// IsToolEnabled reports whether a tool is currently enabled.
func (r *Registry) IsToolEnabled(name string) bool {
	return !r.disabled[name]
}

// ToolNames returns the names of all registered tools in registration order.
func (r *Registry) ToolNames() []string {
	names := make([]string, len(r.schemas))
	for i, tool := range r.schemas {
		names[i] = tool.Name
	}
	return names
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

// ResetSession clears all disabled tools so every tool is enabled again.
func (r *Registry) ResetSession() {
	r.disabled = nil
}

func (r *Registry) Execute(ctx context.Context, call llm.ToolCall) (string, error) {
	result, err := r.ExecuteDetailed(ctx, call)
	return result.Output, err
}

func (r *Registry) ExecuteDetailed(ctx context.Context, call llm.ToolCall) (ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	switch call.Name {
	case "write":
		return r.writeDetailed(ctx, call.Arguments)
	case "edit":
		return r.editDetailed(ctx, call.Arguments)
	case "view_image":
		return r.viewImageDetailed(ctx, call.Arguments)
	}
	handler, ok := r.handlers[call.Name]
	if !ok {
		return ExecutionResult{}, fmt.Errorf("unknown tool %q", call.Name)
	}
	output, err := handler(ctx, call.Arguments)
	return ExecutionResult{Output: output}, err
}

func (r *Registry) viewImage(ctx context.Context, arguments json.RawMessage) (string, error) {
	result, err := r.viewImageDetailed(ctx, arguments)
	return result.Output, err
}

func (r *Registry) viewImageDetailed(ctx context.Context, arguments json.RawMessage) (ExecutionResult, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decode(arguments, &args); err != nil {
		return ExecutionResult{}, err
	}
	path, err := r.resolveFor(ctx, args.Path)
	if err != nil {
		return ExecutionResult{}, err
	}
	file, err := r.openFile(path, os.O_RDONLY, 0)
	if err != nil {
		return ExecutionResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ExecutionResult{}, err
	}
	if !info.Mode().IsRegular() {
		return ExecutionResult{}, fmt.Errorf("image path %q is not a regular file", args.Path)
	}
	if info.Size() > maxImageSize {
		return ExecutionResult{}, fmt.Errorf("image %q is larger than the %d MiB limit", args.Path, maxImageSize/(1024*1024))
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImageSize+1))
	if err != nil {
		return ExecutionResult{}, err
	}
	if len(data) > maxImageSize {
		return ExecutionResult{}, fmt.Errorf("image %q is larger than the %d MiB limit", args.Path, maxImageSize/(1024*1024))
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	mediaType := http.DetectContentType(data)
	switch mediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return ExecutionResult{}, fmt.Errorf("unsupported image type %q; use PNG, JPEG, WEBP, or GIF", mediaType)
	}
	return ExecutionResult{
		Output: fmt.Sprintf("Loaded image %q (%s, %d bytes).", args.Path, mediaType, len(data)),
		Images: []llm.Image{{MediaType: mediaType, Data: data}},
	}, nil
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
	if r.sandbox == nil && (rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("path %q is outside workspace %q", name, r.root)
	}
	return path, nil
}

func (r *Registry) resolveFor(ctx context.Context, name string) (string, error) {
	path, err := r.resolve(name)
	if err != nil || r.sandbox == nil {
		return path, err
	}
	return r.authorizePath(ctx, path)
}

func (r *Registry) authorizePath(ctx context.Context, path string) (string, error) {
	canonical, err := canonicalTarget(path)
	if err != nil {
		return "", err
	}
	for _, protected := range r.protected {
		if canonical == protected {
			return "", fmt.Errorf("path %q contains qcode configuration secrets and is not available to model tools", path)
		}
	}
	if r.isGranted(canonical) {
		return canonical, nil
	}
	proposed := nearestExistingDirectory(canonical)
	if r.approver == nil {
		return "", fmt.Errorf("path %q is outside approved directories; call request_directory_access first", path)
	}
	selected, approved, err := r.approver(ctx, canonical, proposed)
	if err != nil {
		return "", err
	}
	if !approved {
		return "", fmt.Errorf("directory access denied for %q", path)
	}
	grant := selected
	if !filepath.IsAbs(grant) {
		grant = filepath.Join(r.root, grant)
	}
	grant, err = filepath.Abs(grant)
	if err != nil {
		return "", err
	}
	grant, err = filepath.EvalSymlinks(grant)
	if err != nil {
		return "", fmt.Errorf("resolve granted directory: %w", err)
	}
	info, err := os.Stat(grant)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("granted path %q is not an existing directory", grant)
	}
	if filepath.Clean(grant) == string(filepath.Separator) {
		return "", errors.New("granting the filesystem root is not allowed")
	}
	if !pathContains(grant, canonical) {
		return "", fmt.Errorf("granted directory %q does not contain requested path %q", grant, canonical)
	}
	r.addGrant(grant)
	return canonical, nil
}

func nearestExistingDirectory(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

func canonicalTarget(path string) (string, error) {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var tail []string
	parent := path
	for {
		if resolved, resolveErr := filepath.EvalSymlinks(parent); resolveErr == nil {
			for index := len(tail) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, tail[index])
			}
			return resolved, nil
		} else if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", os.ErrNotExist
		}
		tail = append(tail, filepath.Base(parent))
		parent = next
	}
}

func (r *Registry) isGranted(path string) bool {
	r.grantMu.RLock()
	defer r.grantMu.RUnlock()
	for _, grant := range r.grants {
		if pathContains(grant, path) {
			return true
		}
	}
	return false
}

func (r *Registry) addGrant(path string) {
	r.grantMu.Lock()
	defer r.grantMu.Unlock()
	for _, grant := range r.grants {
		if pathContains(grant, path) {
			return
		}
	}
	kept := r.grants[:0]
	for _, grant := range r.grants {
		if !pathContains(path, grant) {
			kept = append(kept, grant)
		}
	}
	r.grants = append(kept, path)
}

func (r *Registry) grantPaths() []string {
	r.grantMu.RLock()
	defer r.grantMu.RUnlock()
	return append([]string(nil), r.grants...)
}

func (r *Registry) matchingGrant(path string) string {
	r.grantMu.RLock()
	defer r.grantMu.RUnlock()
	best := ""
	for _, grant := range r.grants {
		if pathContains(grant, path) && len(grant) > len(best) {
			best = grant
		}
	}
	return best
}

func (r *Registry) openFile(path string, flags int, perm os.FileMode) (*os.File, error) {
	if r.sandbox == nil {
		return os.OpenFile(path, flags, perm)
	}
	for _, protected := range r.protected {
		if path == protected {
			return nil, fmt.Errorf("path %q contains qcode configuration secrets and is not available to model tools", path)
		}
	}
	grant := r.matchingGrant(path)
	if grant == "" {
		return nil, os.ErrPermission
	}
	return secureOpen(grant, path, flags, perm)
}

func (r *Registry) mkdirAll(path string, perm os.FileMode) error {
	if r.sandbox == nil {
		return os.MkdirAll(path, perm)
	}
	grant := r.matchingGrant(path)
	if grant == "" {
		return os.ErrPermission
	}
	return secureMkdirAll(grant, path, perm)
}

func (r *Registry) readFile(path string) ([]byte, error) {
	file, err := r.openFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (r *Registry) writeFile(path string, data []byte, perm os.FileMode) error {
	file, err := r.openFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (r *Registry) requestDirectoryAccess(ctx context.Context, arguments json.RawMessage) (string, error) {
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
	canonical, err := canonicalTarget(path)
	if err != nil {
		return "", err
	}
	if r.isGranted(canonical) {
		return fmt.Sprintf("%s is already approved", args.Path), nil
	}
	if _, err := r.authorizePath(ctx, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("granted read/write access to %s for this session", args.Path), nil
}

func (r *Registry) read(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Path   string          `json:"path"`
		Offset flexibleInteger `json:"offset"`
		Line   flexibleInteger `json:"line"` // Backward compatibility with qcode 0.1.
		Limit  flexibleInteger `json:"limit"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	path, err := r.resolveFor(ctx, args.Path)
	if err != nil {
		return "", err
	}
	data, err := r.readFile(path)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("file appears to be binary; use view_image for supported image files")
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "[showing lines %d-%d of %d; use offset=%d to continue]\n", offset, end, len(lines), end+1)
	}
	return truncate(out.String()), nil
}

func (r *Registry) write(ctx context.Context, arguments json.RawMessage) (string, error) {
	result, err := r.writeDetailed(ctx, arguments)
	return result.Output, err
}

func (r *Registry) writeDetailed(ctx context.Context, arguments json.RawMessage) (ExecutionResult, error) {
	var args struct{ Path, Content string }
	if err := decode(arguments, &args); err != nil {
		return ExecutionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	path, err := r.resolveFor(ctx, args.Path)
	if err != nil {
		return ExecutionResult{}, err
	}
	before, readErr := r.readFile(path)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ExecutionResult{}, readErr
	}
	if err := r.mkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ExecutionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	if err := r.writeFile(path, []byte(args.Content), 0o644); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{
		Output: fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path),
		Diff:   unifiedDiff(args.Path, before, []byte(args.Content), existed),
	}, nil
}

func (r *Registry) edit(ctx context.Context, arguments json.RawMessage) (string, error) {
	result, err := r.editDetailed(ctx, arguments)
	return result.Output, err
}

func (r *Registry) editDetailed(ctx context.Context, arguments json.RawMessage) (ExecutionResult, error) {
	var args struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := decode(arguments, &args); err != nil {
		return ExecutionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	if args.OldText == "" {
		return ExecutionResult{}, errors.New("old_text must not be empty")
	}
	path, err := r.resolveFor(ctx, args.Path)
	if err != nil {
		return ExecutionResult{}, err
	}
	data, err := r.readFile(path)
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
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, err
	}
	if err := r.writeFile(path, updated, info.Mode().Perm()); err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Output: fmt.Sprintf("edited %s", args.Path), Diff: unifiedDiff(args.Path, data, updated, true)}, nil
}

func (r *Registry) list(ctx context.Context, arguments json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decode(arguments, &args); err != nil {
		return "", err
	}
	path, err := r.resolveFor(ctx, args.Path)
	if err != nil {
		return "", err
	}
	directory, err := r.openFile(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return "", err
	}
	items := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		items = append(items, name)
	}
	sort.Strings(items)
	return truncate(strings.Join(items, "\n")), nil
}

func (r *Registry) search(ctx context.Context, arguments json.RawMessage) (string, error) {
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
	path, err := r.resolveFor(ctx, args.Path)
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
		if err := ctx.Err(); err != nil {
			return err
		}
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
		data, readErr := r.readFile(file)
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
	if r.sandbox != nil {
		cmd = r.sandbox.command(commandCtx, r.root, r.grantPaths(), args.Command)
	} else if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(commandCtx, "cmd.exe", "/d", "/s", "/c", args.Command)
	} else {
		cmd = exec.CommandContext(commandCtx, "/bin/sh", "-c", args.Command)
	}
	// --new-session conflicts with placing bubblewrap itself in a process
	// group. --die-with-parent handles sandbox child cleanup instead.
	if r.sandbox == nil {
		configureShellCancellation(cmd)
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
	if err := ctx.Err(); err != nil {
		return text, err
	}
	if err != nil {
		if r.sandbox != nil {
			return text, fmt.Errorf("command failed: %w; if it needs a path outside approved directories, call request_directory_access and retry", err)
		}
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
