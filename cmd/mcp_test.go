// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP wires an in-memory client to the bibcheck MCP server.
func connectMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()
	t.Setenv("SHIRTY_API_KEY", "sk-test")
	t.Setenv("OPENAI_AUDIT_ENABLED", "false")

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx := context.Background()
	if _, err := newMCPServer().Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "bibcheck-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestMCPServerListsTools(t *testing.T) {
	session := connectMCP(t)

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"check_bibliography", "check_entry"} {
		if !names[want] {
			t.Fatalf("tool %q missing from %v", want, names)
		}
	}
}

func TestMCPCheckBibliographyRequiresPDF(t *testing.T) {
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "check_bibliography",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for missing pdf input")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "pdf_path or pdf_base64") {
		t.Fatalf("unexpected error text: %q", text)
	}
}

func TestMCPCheckBibliographyRejectsBothInputs(t *testing.T) {
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "check_bibliography",
		Arguments: map[string]any{"pdf_path": "a.pdf", "pdf_base64": "aGk="},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for conflicting pdf inputs")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "not both") {
		t.Fatalf("unexpected error text: %q", text)
	}
}

func TestMCPCheckEntryRequiresText(t *testing.T) {
	session := connectMCP(t)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "check_entry",
		Arguments: map[string]any{"text": ""},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for empty text")
	}
}
