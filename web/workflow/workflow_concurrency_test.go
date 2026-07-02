// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sandialabs/bibcheck/documents"
	"github.com/sandialabs/bibcheck/entries"
	"github.com/sandialabs/bibcheck/lookup"
)

func TestRunPhaseRespectsBoundAndRunsEverything(t *testing.T) {
	const n, bound = 24, 3

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	seen := make([]int, n)

	runPhase(context.Background(), n, bound, func(i int) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		seen[i]++
		mu.Unlock()

		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
	})

	for i, count := range seen {
		if count != 1 {
			t.Fatalf("work(%d) ran %d times, want 1", i, count)
		}
	}
	if maxInFlight > bound {
		t.Fatalf("max in-flight = %d, want <= %d", maxInFlight, bound)
	}
	if maxInFlight < 2 {
		t.Fatalf("max in-flight = %d, expected parallel execution", maxInFlight)
	}
}

func TestRunPhaseStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	ran := 0

	runPhase(ctx, 10, 1, func(i int) {
		mu.Lock()
		ran++
		mu.Unlock()
		cancel()
	})

	if ran != 1 {
		t.Fatalf("ran = %d tasks after cancellation, want 1", ran)
	}
}

// fakeProvider satisfies Provider and Counter for hermetic pipeline tests.
// Extraction fails for every entry so the analyze phase never reaches
// lookup.Entry's network-backed sources.
type fakeProvider struct {
	entryCount int

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
}

func (f *fakeProvider) PrepareBibliographyContent([]byte) (*documents.Bibliography, error) {
	return &documents.Bibliography{Text: "fake bibliography"}, nil
}

func (f *fakeProvider) CountBibliographyEntries(*documents.Bibliography) (int, error) {
	return f.entryCount, nil
}

func (f *fakeProvider) EntryFromBibliography(_ *documents.Bibliography, id int) (string, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()

	time.Sleep(5 * time.Millisecond)

	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
	return "", fmt.Errorf("no text for entry %d", id)
}

func (f *fakeProvider) Classify(string) (string, error)             { return "", nil }
func (f *fakeProvider) ParseURL(string) (string, error)             { return "", nil }
func (f *fakeProvider) ParseOnline(string) (*entries.Online, error) { return &entries.Online{}, nil }
func (f *fakeProvider) ParseAuthors(string) (*entries.Authors, error) {
	return &entries.Authors{}, nil
}
func (f *fakeProvider) ParseTitle(string) (string, error) { return "", nil }
func (f *fakeProvider) ParsePub(string) (string, error)   { return "", nil }
func (f *fakeProvider) PDFMetadata([]byte) (*documents.Metadata, error) {
	return &documents.Metadata{}, nil
}
func (f *fakeProvider) HTMLMetadata([]byte) (*documents.Metadata, error) {
	return &documents.Metadata{}, nil
}
func (f *fakeProvider) Summarize(*lookup.Result) (bool, string, error) { return false, "", nil }

func TestAnalyzePDFRunsEntriesConcurrentlyInOrder(t *testing.T) {
	const entryCount, bound = 16, 4

	provider := &fakeProvider{entryCount: entryCount}
	rt := &Runtime{Kind: ProviderShirty, Provider: provider, Counter: provider}

	var mu sync.Mutex
	var snapshots []State

	final := AnalyzePDFWithOptions(context.Background(), rt, []byte("%PDF-fake"), Options{Concurrency: bound}, func(s State) {
		mu.Lock()
		snapshots = append(snapshots, s)
		mu.Unlock()
	})

	if final.Error != "" {
		t.Fatalf("unexpected workflow error: %s", final.Error)
	}
	if final.Phase != "Done" {
		t.Fatalf("phase = %q, want Done", final.Phase)
	}
	if len(final.Entries) != entryCount {
		t.Fatalf("len(entries) = %d, want %d", len(final.Entries), entryCount)
	}

	// Order is preserved: entry i occupies slot i regardless of completion order.
	for i, entry := range final.Entries {
		if want := fmt.Sprintf("%d", i+1); entry.ID != want {
			t.Fatalf("entries[%d].ID = %q, want %q", i, entry.ID, want)
		}
		if entry.TextStatus != "error" {
			t.Fatalf("entries[%d].TextStatus = %q, want error", i, entry.TextStatus)
		}
		if entry.AnalysisStatus != "error" {
			t.Fatalf("entries[%d].AnalysisStatus = %q, want error", i, entry.AnalysisStatus)
		}
		if want := fmt.Sprintf("extract entry: no text for entry %d", i+1); entry.Error != want {
			t.Fatalf("entries[%d].Error = %q, want %q", i, entry.Error, want)
		}
	}

	if provider.maxInFlight > bound {
		t.Fatalf("max in-flight extractions = %d, want <= %d", provider.maxInFlight, bound)
	}
	if provider.maxInFlight < 2 {
		t.Fatalf("max in-flight extractions = %d, expected parallel execution", provider.maxInFlight)
	}

	// Every progress snapshot must be internally consistent (entry slice sized
	// up front, IDs stable). The race detector validates snapshot isolation.
	for _, s := range snapshots {
		if s.Total != 0 && len(s.Entries) != 0 && len(s.Entries) != entryCount {
			t.Fatalf("snapshot with %d entries, want 0 or %d", len(s.Entries), entryCount)
		}
	}
}
