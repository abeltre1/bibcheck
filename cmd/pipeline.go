// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package cmd

import (
	"errors"

	"github.com/sandialabs/bibcheck/config"
	"github.com/sandialabs/bibcheck/documents"
	"github.com/sandialabs/bibcheck/elsevier"
	"github.com/sandialabs/bibcheck/entries"
	"github.com/sandialabs/bibcheck/lookup"
	"github.com/sandialabs/bibcheck/openrouter"
	"github.com/sandialabs/bibcheck/shirty"
)

type providerKind string

const (
	providerShirty     providerKind = "shirty"
	providerOpenRouter providerKind = "openrouter"
)

// analysisRuntime bundles the configured LLM provider and lookup dependencies
// behind the operations the analysis pipeline needs, so the CLI and the MCP
// server share one setup path. Shirty is preferred when both providers are
// configured, matching the historical CLI behavior.
type analysisRuntime struct {
	kind       providerKind
	classifier entries.Classifier
	parser     entries.Parser
	extractor  documents.EntryFromBibliographyExtractor
	meta       documents.MetaExtractor
	summarizer summarizer
	cfg        *lookup.EntryConfig

	prepare        func(path string) (*documents.Bibliography, error)
	prepareContent func(pdf []byte) (*documents.Bibliography, error)
	countEntries   func(*documents.Bibliography) (int, error)

	concurrency int
}

func newAnalysisRuntime(settings config.Settings) (*analysisRuntime, error) {
	var elsevierClient *elsevier.Client
	if settings.ElsevierAPIKey != "" {
		elsevierClient = elsevier.NewClient(settings.ElsevierAPIKey)
	}

	rt := &analysisRuntime{
		cfg:         &lookup.EntryConfig{ElsevierClient: elsevierClient},
		concurrency: settings.Concurrency,
	}

	if settings.OpenRouterAPIKey != "" && settings.OpenRouterBaseURL != "" {
		client := openrouter.NewClient(
			settings.OpenRouterAPIKey,
			openrouter.WithBaseURL(settings.OpenRouterBaseURL),
		)
		rt.kind = providerOpenRouter
		rt.classifier = client
		rt.parser = client
		rt.extractor = client
		rt.meta = client
		rt.summarizer = client
		rt.prepare = client.PrepareBibliography
		rt.prepareContent = client.PrepareBibliographyContent
		rt.countEntries = client.NumBibliographyEntries
	}

	if settings.ShirtyAPIKey != "" && settings.ShirtyBaseURL != "" {
		client := shirty.NewWorkflow(
			settings.ShirtyAPIKey,
			shirty.WithBaseUrl(settings.ShirtyBaseURL),
		)
		rt.kind = providerShirty
		rt.classifier = client
		rt.parser = client
		rt.extractor = client
		rt.meta = client
		rt.summarizer = client
		rt.prepare = client.PrepareBibliography
		rt.prepareContent = client.PrepareBibliographyContent
		rt.countEntries = client.NumBibEntries
	}

	if rt.kind == "" {
		return nil, errors.New("need shirty or openrouter config")
	}
	return rt, nil
}

// analyzeDocument analyzes entries [entryStart, entryStart+entryCount) of a
// prepared bibliography with the runtime's bounded concurrency.
func (rt *analysisRuntime) analyzeDocument(bib *documents.Bibliography, entryStart, entryCount int, mode string) []entryView {
	return analyzeEntries(entryStart, entryCount, rt.concurrency, func(i int) (*lookup.Result, error) {
		return lookup.EntryFromBibliography(bib, i, mode, rt.classifier, rt.extractor, rt.meta, rt.parser, rt.cfg)
	}, rt.summarizer)
}

// analyzeEntryText analyzes a single raw citation string.
func (rt *analysisRuntime) analyzeEntryText(text, mode string) []entryView {
	return analyzeEntries(1, 1, 1, func(int) (*lookup.Result, error) {
		return lookup.Entry(text, mode, rt.classifier, rt.meta, rt.parser, rt.cfg)
	}, rt.summarizer)
}
