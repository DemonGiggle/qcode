package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testInput struct {
	Name string `json:"name"`
}

func testServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "greet", Description: "greeting"}, func(ctx context.Context, req *mcp.CallToolRequest, in testInput) (*mcp.CallToolResult, any, error) {
		if in.Name == "fail" {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "failed"}}}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hello " + in.Name}, &mcp.ImageContent{MIMEType: "image/png", Data: []byte{1, 2}}}}, nil, nil
	})
	return s
}
func TestHelperServer(t *testing.T) {
	if os.Getenv("QCODE_MCP_HELPER") != "1" {
		return
	}
	if err := testServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestConnections(t *testing.T) {
	server := testServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	var warnings bytes.Buffer
	manager := New(map[string]ServerConfig{"local": {Command: os.Args[0], Args: []string{"-test.run=TestHelperServer"}, Env: map[string]string{"QCODE_MCP_HELPER": "1"}}, "remote": {URL: httpServer.URL}, "broken": {Command: "/nonexistent/qcode-mcp"}}, t.TempDir(), &warnings)
	defer manager.Close()
	schemas := manager.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("schemas=%v warnings=%s", schemas, warnings.String())
	}
	if schemas[0].Name == schemas[1].Name || !strings.Contains(warnings.String(), "broken") {
		t.Fatalf("invalid names or warning: %v %s", schemas, warnings.String())
	}
	for _, schema := range schemas {
		out, ok, err := manager.Execute(context.Background(), schema.Name, json.RawMessage(`{"name":"world"}`))
		if !ok || err != nil || out.Output != "hello world" || len(out.Images) != 1 {
			t.Fatalf("result=%+v ok=%v err=%v", out, ok, err)
		}
		_, _, err = manager.Execute(context.Background(), schema.Name, json.RawMessage(`{"name":"fail"}`))
		if err == nil || !strings.Contains(err.Error(), "failed") {
			t.Fatalf("expected tool error: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	server.AddTool(&mcp.Tool{Name: "extra", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	deadline := time.Now().Add(3 * time.Second)
	for len(manager.Schemas()) != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(manager.Schemas()) != 3 {
		t.Fatal("tool change not refreshed")
	}
}

func TestPaginationAndCancellation(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "pages", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"one", "two"} {
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	defer httpServer.Close()
	var warnings bytes.Buffer
	manager := New(map[string]ServerConfig{"pages": {URL: httpServer.URL}}, t.TempDir(), &warnings)
	defer manager.Close()
	schemas := manager.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("pagination: got %d tools; %s", len(schemas), warnings.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, ok, err := manager.Execute(ctx, schemas[0].Name, json.RawMessage(`{}`))
	if !ok || err == nil || ctx.Err() == nil {
		t.Fatalf("cancellation: ok=%v err=%v", ok, err)
	}
}
