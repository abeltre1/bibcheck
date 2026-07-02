// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sandialabs/bibcheck/documents"
	"github.com/sandialabs/bibcheck/entries"
	"github.com/sandialabs/bibcheck/lookup"
	"github.com/sandialabs/bibcheck/openrouter"
	"github.com/sandialabs/bibcheck/shirty"
)

// defaultConcurrency bounds how many entries are extracted/analyzed at once
// when Options.Concurrency is unset. Kept in sync with config.DefaultConcurrency
// (the CLI default); duplicated here to avoid importing the viper-based config
// package into the wasm bundle.
const defaultConcurrency = 4

type ProviderKind string

const (
	ProviderNone       ProviderKind = ""
	ProviderShirty     ProviderKind = "Shirty"
	ProviderOpenRouter ProviderKind = "OpenRouter"
)

type Keys struct {
	ShirtyAPIKey     string
	OpenRouterAPIKey string
}

type EntryState struct {
	ID             string
	TextStatus     string
	Text           string
	AnalysisStatus string
	Analysis       string
	Error          string
}

type State struct {
	Provider  ProviderKind
	Phase     string
	Total     int
	Completed int
	Entries   []EntryState
	Error     string
}

type Progress func(State)

type Options struct {
	Entry int
	// Concurrency bounds how many entries are processed at once. Values < 1 fall
	// back to defaultConcurrency.
	Concurrency int
}

type Provider interface {
	PrepareBibliographyContent([]byte) (*documents.Bibliography, error)
	EntryFromBibliography(*documents.Bibliography, int) (string, error)
	entries.Classifier
	entries.Parser
	documents.MetaExtractor
	Summarize(*lookup.Result) (bool, string, error)
}

type Counter interface {
	CountBibliographyEntries(*documents.Bibliography) (int, error)
}

type Runtime struct {
	Kind     ProviderKind
	Provider Provider
	Counter  Counter
}

type shirtyCounter struct {
	client *shirty.Workflow
}

func (c shirtyCounter) CountBibliographyEntries(b *documents.Bibliography) (int, error) {
	return c.client.NumBibEntries(b)
}

type openRouterCounter struct {
	client *openrouter.Client
}

func (c openRouterCounter) CountBibliographyEntries(b *documents.Bibliography) (int, error) {
	return c.client.NumBibliographyEntries(b)
}

func NewRuntime(keys Keys) (*Runtime, error) {
	shirtyKey := strings.TrimSpace(keys.ShirtyAPIKey)
	openRouterKey := strings.TrimSpace(keys.OpenRouterAPIKey)

	if shirtyKey != "" {
		client := shirty.NewWorkflow(shirtyKey, shirty.WithAuditEnabled(false))
		return &Runtime{
			Kind:     ProviderShirty,
			Provider: client,
			Counter:  shirtyCounter{client: client},
		}, nil
	}

	if openRouterKey != "" {
		client := openrouter.NewClient(openRouterKey)
		return &Runtime{
			Kind:     ProviderOpenRouter,
			Provider: client,
			Counter:  openRouterCounter{client: client},
		}, nil
	}

	return nil, errors.New("provide a Shirty or OpenRouter API key")
}

func AnalyzePDF(ctx context.Context, rt *Runtime, pdf []byte, progress Progress) State {
	return AnalyzePDFWithOptions(ctx, rt, pdf, Options{}, progress)
}

func AnalyzePDFWithOptions(ctx context.Context, rt *Runtime, pdf []byte, options Options, progress Progress) State {
	if rt == nil || rt.Provider == nil || rt.Counter == nil {
		state := State{Phase: "Starting"}
		return fail(progress, state, errors.New("missing analysis runtime"))
	}

	state := State{
		Provider: rt.Kind,
		Phase:    "Preparing bibliography",
	}
	emit(progress, state)

	if err := ctx.Err(); err != nil {
		return fail(progress, state, err)
	}
	if len(pdf) == 0 {
		return fail(progress, state, errors.New("selected PDF is empty"))
	}

	bibliography, err := rt.Provider.PrepareBibliographyContent(pdf)
	if err != nil {
		return fail(progress, state, fmt.Errorf("prepare bibliography: %w", err))
	}

	entryIDs := []int{options.Entry}
	if options.Entry < 1 {
		state.Phase = "Counting entries"
		emit(progress, state)
		count, err := rt.Counter.CountBibliographyEntries(bibliography)
		if err != nil {
			return fail(progress, state, fmt.Errorf("count bibliography entries: %w", err))
		}
		if count < 1 {
			return fail(progress, state, fmt.Errorf("expected at least one bibliography entry, found %d", count))
		}

		entryIDs = make([]int, count)
		for i := range entryIDs {
			entryIDs[i] = i + 1
		}
	}

	state.Total = len(entryIDs)
	state.Entries = make([]EntryState, len(entryIDs))
	for i, id := range entryIDs {
		state.Entries[i] = EntryState{
			ID:             fmt.Sprintf("%d", id),
			TextStatus:     "pending",
			AnalysisStatus: "pending",
		}
	}
	concurrency := options.Concurrency
	if concurrency < 1 {
		concurrency = defaultConcurrency
	}

	// Entries are independent, so both phases fan work out across a bounded
	// worker pool. state is shared, so all mutations and progress snapshots go
	// through report, which serializes them under mu. Each worker writes only
	// its own state.Entries[i], preserving order without an explicit merge.
	var mu sync.Mutex
	report := func(mutate func()) {
		mu.Lock()
		mutate()
		snapshot := cloneState(state)
		mu.Unlock()
		if progress != nil {
			progress(snapshot)
		}
	}

	state.Phase = "Extracting entries"
	emit(progress, state)

	runPhase(ctx, len(state.Entries), concurrency, func(i int) {
		report(func() { state.Entries[i].TextStatus = "active" })

		text, err := rt.Provider.EntryFromBibliography(bibliography, entryIDs[i])
		if err != nil {
			report(func() {
				state.Entries[i].TextStatus = "error"
				state.Entries[i].Error = fmt.Sprintf("extract entry: %v", err)
			})
			return
		}

		report(func() {
			state.Entries[i].TextStatus = "completed"
			state.Entries[i].Text = text
		})
	})
	if err := ctx.Err(); err != nil {
		return fail(progress, state, err)
	}

	state.Phase = "Analyzing entries"
	emit(progress, state)

	runPhase(ctx, len(state.Entries), concurrency, func(i int) {
		if state.Entries[i].TextStatus != "completed" {
			report(func() {
				state.Entries[i].AnalysisStatus = "error"
				if state.Entries[i].Error == "" {
					state.Entries[i].Error = "entry text was not extracted"
				}
			})
			return
		}

		report(func() { state.Entries[i].AnalysisStatus = "active" })

		result, err := lookup.Entry(state.Entries[i].Text, "auto", rt.Provider, rt.Provider, rt.Provider, nil)
		if err != nil {
			report(func() {
				state.Entries[i].AnalysisStatus = "error"
				state.Entries[i].Error = fmt.Sprintf("analyze entry: %v", err)
			})
			return
		}

		mismatch, comment, summaryErr := rt.Provider.Summarize(result)
		if summaryErr != nil {
			result.Summary.Error = summaryErr
		} else {
			result.Summary.Status = lookup.SearchStatusDone
			result.Summary.Matches = !mismatch
			result.Summary.Comment = comment
		}

		report(func() {
			state.Entries[i].AnalysisStatus = "completed"
			state.Entries[i].Analysis = FormatAnalysis(result)
			state.Completed++
		})
	})
	if err := ctx.Err(); err != nil {
		return fail(progress, state, err)
	}

	state.Phase = "Done"
	emit(progress, state)
	return state
}

// runPhase runs work(i) for each i in [0, n) across at most concurrency
// goroutines, returning once every task has finished. Tasks are skipped once
// ctx is cancelled so an aborted run stops launching new work promptly.
func runPhase(ctx context.Context, n, concurrency int, work func(i int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		i := i
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			work(i)
		}()
	}
	wg.Wait()
}

func FormatAnalysis(result *lookup.Result) string {
	if result == nil {
		return ""
	}

	var b strings.Builder
	if result.Summary.Status == lookup.SearchStatusDone {
		status := "possible mismatch"
		if result.Summary.Matches {
			status = "looks okay"
		}
		fmt.Fprintf(&b, "summary: %s", status)
		if result.Summary.Comment != "" {
			fmt.Fprintf(&b, " - %s", result.Summary.Comment)
		}
		b.WriteString("\n")
	} else if result.Summary.Error != nil {
		fmt.Fprintf(&b, "summary error: %v\n", result.Summary.Error)
	}

	if result.Arxiv.Status == lookup.SearchStatusDone && result.Arxiv.Entry != nil {
		fmt.Fprintf(&b, "arxiv: %s\n", result.Arxiv.Entry.ToString())
	}
	if result.OSTI.Status == lookup.SearchStatusDone && result.OSTI.Record != nil {
		fmt.Fprintf(&b, "OSTI: %s\n", result.OSTI.Record.ToString())
	}
	if result.Crossref.Status == lookup.SearchStatusDone && result.Crossref.Work != nil {
		fmt.Fprintf(&b, "crossref: %s\n", result.Crossref.Work.ToString())
	}
	if result.DOIOrg.Status == lookup.SearchStatusDone && result.DOIOrg.Found {
		fmt.Fprintf(&b, "doi.org: exists\n")
	}
	if result.Online.Status == lookup.SearchStatusDone && result.Online.Metadata != nil {
		fmt.Fprintf(&b, "URL: %s\n", result.Online.Metadata.ToString())
	}
	if b.Len() == 0 {
		return "No matching metadata found."
	}
	return strings.TrimSpace(b.String())
}

func emit(progress Progress, state State) {
	if progress != nil {
		progress(cloneState(state))
	}
}

func fail(progress Progress, state State, err error) State {
	state.Phase = "Error"
	state.Error = err.Error()
	emit(progress, state)
	return state
}

func cloneState(state State) State {
	entries := make([]EntryState, len(state.Entries))
	copy(entries, state.Entries)
	state.Entries = entries
	return state
}
