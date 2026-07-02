// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package cmd

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/sandialabs/bibcheck/config"
	"github.com/sandialabs/bibcheck/documents"
	"github.com/sandialabs/bibcheck/version"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run bibcheck as an MCP server over stdio",
	Long: `Run bibcheck as a Model Context Protocol (MCP) server on stdin/stdout.

Exposes tools for checking bibliographies (whole PDFs or single citation
strings) to MCP clients such as Claude Code and Claude Desktop. Requires the
same provider configuration as the CLI (SHIRTY_API_KEY or OPENROUTER_API_KEY,
optionally ELSEVIER_API_KEY). Diagnostics go to stderr; stdout carries only
the MCP protocol.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Fail fast on missing provider config instead of erroring per tool call.
		if _, err := newAnalysisRuntime(config.Runtime()); err != nil {
			return err
		}

		return newMCPServer().Run(cmd.Context(), &mcp.StdioTransport{})
	},
}

func newMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "bibcheck",
		Title:   "bibcheck bibliography checker",
		Version: version.String(),
	}, &mcp.ServerOptions{
		Instructions: "Verify bibliography entries against doi.org, OSTI, arXiv, Crossref, Elsevier, and linked online resources. Use check_bibliography for a PDF, check_entry for a single citation string. Entries are analyzed concurrently; summary_state per entry is ok, review, error, or unknown.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_bibliography",
		Description: "Extract the bibliography from a PDF and verify every entry (or one entry) against scholarly metadata sources. Returns per-entry summary states and per-source lookup results.",
	}, checkBibliographyTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "check_entry",
		Description: "Verify a single bibliography entry given as raw citation text, without a PDF. Returns the entry's summary state and per-source lookup results.",
	}, checkEntryTool)

	return server
}

type checkBibliographyInput struct {
	PDFPath     string `json:"pdf_path,omitempty" jsonschema:"Path to a PDF file on the server's filesystem. Exactly one of pdf_path or pdf_base64 is required."`
	PDFBase64   string `json:"pdf_base64,omitempty" jsonschema:"Base64-encoded PDF content, for clients without a shared filesystem."`
	Entry       int    `json:"entry,omitempty" jsonschema:"Analyze only this 1-based entry number instead of the whole bibliography."`
	Concurrency int    `json:"concurrency,omitempty" jsonschema:"Maximum entries analyzed at once. Defaults to the server's configured concurrency."`
}

type checkEntryInput struct {
	Text string `json:"text" jsonschema:"The bibliography entry to verify, as plain citation text."`
}

func checkBibliographyTool(ctx context.Context, req *mcp.CallToolRequest, in checkBibliographyInput) (*mcp.CallToolResult, jsonDocumentView, error) {
	var zero jsonDocumentView

	rt, err := newAnalysisRuntime(config.Runtime())
	if err != nil {
		return nil, zero, err
	}
	if in.Concurrency > 0 {
		rt.concurrency = in.Concurrency
	}

	var bibliography *documents.Bibliography
	switch {
	case in.PDFPath != "" && in.PDFBase64 != "":
		return nil, zero, errors.New("provide either pdf_path or pdf_base64, not both")
	case in.PDFPath != "":
		bibliography, err = rt.prepare(in.PDFPath)
	case in.PDFBase64 != "":
		var pdf []byte
		if pdf, err = base64.StdEncoding.DecodeString(in.PDFBase64); err != nil {
			return nil, zero, fmt.Errorf("decode pdf_base64: %w", err)
		}
		bibliography, err = rt.prepareContent(pdf)
	default:
		return nil, zero, errors.New("provide pdf_path or pdf_base64")
	}
	if err != nil {
		return nil, zero, fmt.Errorf("prepare bibliography: %w", err)
	}

	entryStart, entryCount := 1, 0
	singleEntry := in.Entry > 0
	if singleEntry {
		entryStart, entryCount = in.Entry, 1
	} else {
		if entryCount, err = rt.countEntries(bibliography); err != nil {
			return nil, zero, fmt.Errorf("count bibliography entries: %w", err)
		}
	}

	views := rt.analyzeDocument(bibliography, entryStart, entryCount, "auto")
	doc := buildDocumentView(views, false)
	return nil, buildJSONDocument(doc, views, false, singleEntry), nil
}

func checkEntryTool(ctx context.Context, req *mcp.CallToolRequest, in checkEntryInput) (*mcp.CallToolResult, jsonDocumentView, error) {
	var zero jsonDocumentView

	if in.Text == "" {
		return nil, zero, errors.New("text is required")
	}

	rt, err := newAnalysisRuntime(config.Runtime())
	if err != nil {
		return nil, zero, err
	}

	views := rt.analyzeEntryText(in.Text, "auto")
	doc := buildDocumentView(views, false)
	return nil, buildJSONDocument(doc, views, false, true), nil
}
