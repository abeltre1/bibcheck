// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package cmd

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sandialabs/bibcheck/lookup"
)

type fakeSummarizer struct {
	mu    sync.Mutex
	calls int
}

func (s *fakeSummarizer) Summarize(lr *lookup.Result) (bool, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return false, "summary of " + lr.Text, nil
}

func TestAnalyzeEntriesPreservesOrder(t *testing.T) {
	const start, count = 3, 17

	views := analyzeEntries(start, count, 5, func(i int) (*lookup.Result, error) {
		// Finish out of submission order to prove ordering comes from indexed
		// writes, not completion order.
		time.Sleep(time.Duration((count-i)%4) * time.Millisecond)
		return &lookup.Result{Text: fmt.Sprintf("entry %d", i)}, nil
	}, &fakeSummarizer{})

	if len(views) != count {
		t.Fatalf("len(views) = %d, want %d", len(views), count)
	}
	for offset, view := range views {
		want := start + offset
		if view.number != want {
			t.Fatalf("views[%d].number = %d, want %d", offset, view.number, want)
		}
		if wantText := fmt.Sprintf("entry %d", want); view.originalText != wantText {
			t.Fatalf("views[%d].originalText = %q, want %q", offset, view.originalText, wantText)
		}
		if wantSummary := fmt.Sprintf("summary of entry %d", want); view.summaryComment != wantSummary {
			t.Fatalf("views[%d].summaryComment = %q, want %q", offset, view.summaryComment, wantSummary)
		}
	}
}

func TestAnalyzeEntriesRespectsConcurrencyBound(t *testing.T) {
	const count, bound = 24, 3

	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0

	views := analyzeEntries(1, count, bound, func(i int) (*lookup.Result, error) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()

		time.Sleep(5 * time.Millisecond)

		mu.Lock()
		inFlight--
		mu.Unlock()
		return &lookup.Result{Text: fmt.Sprintf("entry %d", i)}, nil
	}, nil)

	if len(views) != count {
		t.Fatalf("len(views) = %d, want %d", len(views), count)
	}
	if maxInFlight > bound {
		t.Fatalf("max in-flight analyses = %d, want <= %d", maxInFlight, bound)
	}
	if maxInFlight < 2 {
		t.Fatalf("max in-flight analyses = %d, expected parallel execution", maxInFlight)
	}
}

func TestAnalyzeEntriesSkipsFailedEntries(t *testing.T) {
	views := analyzeEntries(1, 5, 2, func(i int) (*lookup.Result, error) {
		if i%2 == 0 {
			return nil, errors.New("boom")
		}
		return &lookup.Result{Text: fmt.Sprintf("entry %d", i)}, nil
	}, nil)

	if len(views) != 3 {
		t.Fatalf("len(views) = %d, want 3", len(views))
	}
	for offset, want := range []int{1, 3, 5} {
		if views[offset].number != want {
			t.Fatalf("views[%d].number = %d, want %d", offset, views[offset].number, want)
		}
	}
}

func TestAnalyzeEntriesClampsConcurrency(t *testing.T) {
	views := analyzeEntries(1, 2, 0, func(i int) (*lookup.Result, error) {
		return &lookup.Result{Text: fmt.Sprintf("entry %d", i)}, nil
	}, nil)
	if len(views) != 2 {
		t.Fatalf("len(views) = %d, want 2", len(views))
	}
}
