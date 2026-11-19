package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGreeter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := newServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "greeter-test", Version: "v0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "greet" {
		t.Fatalf("expected exactly one greet tool, got %+v", tools.Tools)
	}

	call := func(args map[string]any) (*mcp.CallToolResult, error) {
		return session.CallTool(ctx, &mcp.CallToolParams{Name: "greet", Arguments: args})
	}

	t.Run("greet", func(t *testing.T) {
		result, err := call(map[string]any{"name": "  Ada\t"})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError || len(result.Content) != 1 {
			t.Fatalf("expected one successful greeting, got %+v", result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || text.Text != "Hello, Ada!" {
			t.Fatalf("expected Hello, Ada!, got %+v", result.Content[0])
		}
	})

	t.Run("missing name", func(t *testing.T) {
		if _, err := call(map[string]any{}); err == nil || !strings.Contains(err.Error(), `"name"`) {
			t.Fatalf("expected a schema error for missing name, got %v", err)
		}
	})

	t.Run("blank name", func(t *testing.T) {
		result, err := call(map[string]any{"name": " \t"})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || len(result.Content) != 1 {
			t.Fatalf("expected one tool error, got %+v", result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || text.Text != "name must not be blank" {
			t.Fatalf("expected a blank name error, got %+v", result.Content[0])
		}
	})
}
