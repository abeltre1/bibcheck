# End-to-end test: parallel pipeline + MCP server

`./test/e2e/run.sh` exercises the real `bibcheck` binary against a fixture
document whose bibliography contains real convergence-computing citations
(DOIs, arXiv IDs, and URLs drawn from the edge-computing/deep-learning survey
literature), with the LLM backend replaced by a local mock.

| Piece | Purpose |
|-------|---------|
| `convergence_computing.txt` | Source text: title page, body page, and a 12-entry REFERENCES section |
| `genpdf/` | Builds a 4-page PDF from the text, with uncompressed content streams |
| `mockllm/` | OpenRouter-compatible `/chat/completions` server that answers bibcheck's prompts by reading text back out of the attached PDF, and records max in-flight requests |
| `run.sh` | Orchestrates the runs and assertions |

The script asserts:

1. `--concurrency 1` and `--concurrency 8` produce **byte-identical JSON**
   (parallelism changes wall-clock time, never results or order);
2. the mock observes `max_in_flight == 1` for the serial run and
   `1 < max_in_flight <= 8` for the parallel run (the pool is real and bounded);
3. `bibcheck mcp` returns the same 12 entries in order via the
   `check_bibliography` tool, and `check_entry` works for a single citation.

Only the LLM is mocked. External metadata sources (doi.org, Crossref, arXiv,
...) are contacted for real; on a network-restricted machine those lookups
fail identically in every run, which the byte-identical assertion tolerates.
On an open network the same fixture verifies genuine DOI/arXiv/Crossref
resolution.

```bash
./test/e2e/run.sh
```
