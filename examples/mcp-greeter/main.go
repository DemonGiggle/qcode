package main

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type greetInput struct {
	Name string `json:"name" jsonschema:"The name of the person to greet"`
}

func newServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-greeter", Version: "v0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "Greet someone by name"}, greet)
	return server
}

func greet(_ context.Context, _ *mcp.CallToolRequest, input greetInput) (*mcp.CallToolResult, any, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, nil, errors.New("name must not be blank")
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Hello, " + name + "!"}},
	}, nil, nil
}

func main() {
	// StdioTransport owns stdout; log writes errors to stderr.
	if err := newServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
