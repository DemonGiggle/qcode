package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"qcode/internal/llm"
)

type ServerConfig struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
}
type entry struct {
	schema   llm.Tool
	original string
	session  *mcp.ClientSession
}
type Manager struct {
	mu       sync.RWMutex
	entries  map[string]entry
	sessions []*mcp.ClientSession
	cancels  []context.CancelFunc
	warnings io.Writer
	closed   bool
}

func New(config map[string]ServerConfig, root string, warnings io.Writer) *Manager {
	m := &Manager{entries: make(map[string]entry), warnings: warnings}
	names := make([]string, 0, len(config))
	for n := range config {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := config[name]
		if err := m.connect(name, cfg, root); err != nil {
			fmt.Fprintf(warnings, "WARNING: MCP server %q: %v\n", name, err)
		}
	}
	return m
}
func (m *Manager) connect(name string, cfg ServerConfig, root string) error {
	if strings.TrimSpace(name) == "" || (cfg.Command == "") == (cfg.URL == "") {
		return fmt.Errorf("specify exactly one of command or url")
	}
	var transport mcp.Transport
	if cfg.URL != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("invalid HTTP endpoint")
		}
		transport = &mcp.StreamableClientTransport{Endpoint: cfg.URL}
	} else {
		cmd := exec.Command(cfg.Command, cfg.Args...)
		cmd.Dir = root
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			if strings.ContainsAny(k, "=\x00") || k == "" || strings.ContainsRune(v, 0) {
				return fmt.Errorf("invalid environment variable")
			}
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	}
	var session *mcp.ClientSession
	var ready sync.Mutex
	client := mcp.NewClient(&mcp.Implementation{Name: "qcode", Version: "1"}, &mcp.ClientOptions{ToolListChangedHandler: func(_ context.Context, _ *mcp.ToolListChangedRequest) {
		go func() {
			ready.Lock()
			s := session
			ready.Unlock()
			if s != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if err := m.refresh(ctx, name, s); err != nil {
					fmt.Fprintf(m.warnings, "WARNING: MCP server %q refresh: %v\n", name, err)
				}
			}
		}()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(15*time.Second, cancel)
	s, err := client.Connect(ctx, transport, nil)
	timer.Stop()
	if err != nil {
		cancel()
		return err
	}
	ready.Lock()
	session = s
	ready.Unlock()
	refreshCtx, refreshCancel := context.WithTimeout(ctx, 15*time.Second)
	err = m.refresh(refreshCtx, name, s)
	refreshCancel()
	if err != nil {
		s.Close()
		cancel()
		return err
	}
	m.mu.Lock()
	m.sessions = append(m.sessions, s)
	m.cancels = append(m.cancels, cancel)
	m.mu.Unlock()
	return nil
}
func (m *Manager) refresh(ctx context.Context, server string, session *mcp.ClientSession) error {
	next := map[string]entry{}
	cursor := ""
	seen := map[string]bool{}
	for {
		result, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return err
		}
		for _, tool := range result.Tools {
			schema, ok := tool.InputSchema.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid schema for tool %q", tool.Name)
			}
			sum := sha256.Sum256([]byte(server + "\x00" + tool.Name))
			name := fmt.Sprintf("mcp_%x", sum[:16])
			next[name] = entry{schema: llm.Tool{Name: name, Description: fmt.Sprintf("MCP server %s, tool %s: %s", server, tool.Name, tool.Description), Parameters: schema}, original: tool.Name, session: session}
		}
		cursor = result.NextCursor
		if cursor == "" {
			break
		}
		if seen[cursor] {
			return fmt.Errorf("repeated pagination cursor")
		}
		seen[cursor] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	for name, e := range m.entries {
		if e.session == session {
			delete(m.entries, name)
		}
	}
	for name, e := range next {
		m.entries[name] = e
	}
	return nil
}
func (m *Manager) Schemas() []llm.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []llm.Tool
	for _, e := range m.entries {
		out = append(out, e.schema)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (m *Manager) Execute(ctx context.Context, name string, args json.RawMessage) (llm.ToolResult, bool, error) {
	m.mu.RLock()
	e, ok := m.entries[name]
	m.mu.RUnlock()
	if !ok {
		return llm.ToolResult{}, false, nil
	}
	res, err := e.session.CallTool(ctx, &mcp.CallToolParams{Name: e.original, Arguments: args})
	if err != nil {
		return llm.ToolResult{}, true, err
	}
	var out llm.ToolResult
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.ImageContent:
			out.Images = append(out.Images, llm.Image{MediaType: v.MIMEType, Data: v.Data})
		default:
			parts = append(parts, fmt.Sprintf("[Unsupported MCP content: %T]", c))
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		parts = append(parts, string(b))
	}
	out.Output = strings.Join(parts, "\n")
	if res.IsError {
		return out, true, fmt.Errorf("MCP tool error: %s", out.Output)
	}
	return out, true, nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	sessions := m.sessions
	cancels := m.cancels
	m.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	for _, cancel := range cancels {
		cancel()
	}
}
