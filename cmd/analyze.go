// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause
package cmd

import (
	"log"
	"sync"

	"github.com/sandialabs/bibcheck/lookup"
)

// entryAnalyzer produces the lookup result for bibliography entry `i`.
type entryAnalyzer func(i int) (*lookup.Result, error)

// analyzeEntries analyzes entries [entryStart, entryStart+entryCount) with at
// most `concurrency` running at once. Entries are independent and their
// analysis is network-I/O bound, so the work fans out across a bounded worker
// pool. Results are written back by index to keep the original entry order; a
// nil slot means the entry errored and is skipped, matching the previous
// sequential behavior.
func analyzeEntries(entryStart, entryCount, concurrency int, analyze entryAnalyzer, summarize summarizer) []entryView {
	if concurrency < 1 {
		concurrency = 1
	}

	pending := make([]*entryView, entryCount)
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for offset := 0; offset < entryCount; offset++ {
		i := entryStart + offset
		slot := &pending[offset]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			lr, err := analyze(i)
			if err != nil {
				log.Printf("entry analysis error: %v", err)
				return
			}

			outcome := summaryOutcome{}
			if summarize != nil {
				mismatch, comment, err := summarize.Summarize(lr)
				outcome.mismatch = mismatch
				outcome.comment = comment
				outcome.err = err
				if err != nil {
					log.Printf("summarizer error: %v", err)
				}
			}

			view := buildEntryView(i, lr, outcome)
			*slot = &view
		}()
	}
	wg.Wait()

	views := make([]entryView, 0, entryCount)
	for _, v := range pending {
		if v != nil {
			views = append(views, *v)
		}
	}
	return views
}
