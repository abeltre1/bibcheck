// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause

// mockllm is an OpenRouter-compatible chat-completions server for end-to-end
// testing without a live LLM. It answers bibcheck's prompts by reading the
// text out of the PDF attached to each request (as produced by
// test/e2e/genpdf, which writes uncompressed content streams), so its answers
// are derived from the document rather than canned.
//
// It also records how many requests were in flight at once, so tests can
// assert that bibcheck's --concurrency bound is respected and exercised:
//
//	GET /stats        -> {"total": N, "in_flight": n, "max_in_flight": m}
//	POST /stats/reset -> zeroes the counters
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", "localhost:9800", "listen address")
	latency := flag.Duration("latency", 150*time.Millisecond, "artificial per-request latency, so concurrency is observable")
	flag.Parse()

	s := &server{latency: *latency}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /chat/completions", s.chatCompletions)
	mux.HandleFunc("GET /stats", s.stats)
	mux.HandleFunc("POST /stats/reset", s.reset)

	log.Printf("mock OpenRouter LLM listening on %s (latency %s)", *addr, *latency)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type server struct {
	latency time.Duration

	mu          sync.Mutex
	total       int
	inFlight    int
	maxInFlight int
}

func (s *server) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
}

func (s *server) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	json.NewEncoder(w).Encode(map[string]int{
		"total":         s.total,
		"in_flight":     s.inFlight,
		"max_in_flight": s.maxInFlight,
	})
}

func (s *server) reset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total, s.inFlight, s.maxInFlight = 0, 0, 0
}

// request mirrors just enough of the OpenRouter chat request.
type request struct {
	Messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			File struct {
				FileData string `json:"file_data"`
			} `json:"file"`
		} `json:"content"`
	} `json:"messages"`
}

func (s *server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	s.begin()
	defer s.end()
	time.Sleep(s.latency)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var system, user string
	var pdfLines []string
	for _, msg := range req.Messages {
		for _, content := range msg.Content {
			switch {
			case msg.Role == "system" && content.Type == "text":
				system += content.Text
			case msg.Role == "user" && content.Type == "text":
				user += content.Text
			case content.Type == "file":
				data := content.File.FileData
				if idx := strings.Index(data, "base64,"); idx >= 0 {
					data = data[idx+len("base64,"):]
				}
				if pdf, err := base64.StdEncoding.DecodeString(data); err == nil {
					pdfLines = pdfText(pdf)
				}
			}
		}
	}

	answer := route(system, user, pdfLines)
	log.Printf("%-28s -> %s", promptKind(system), answer)
	json.NewEncoder(w).Encode(map[string]any{
		"id": "mock",
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": answer}},
		},
	})
}

var (
	entryMarker = regexp.MustCompile(`^\[(\d+)\]\s*(.*)$`)
	entryReq    = regexp.MustCompile(`Extract bibliography entry (\d+)`)
	urlPattern  = regexp.MustCompile(`https?://[^\s")]+`)
	quotedTitle = regexp.MustCompile(`"([^"]+),?"`)
)

func promptKind(system string) string {
	switch {
	case strings.Contains(system, "contains any part of the paper's bibliography"):
		return "classify-bib-page"
	case strings.Contains(system, "number of entries in the bibliography"):
		return "count-entries"
	case strings.Contains(system, "extract ONLY THAT ENTRY"):
		return "extract-entry"
	case strings.Contains(system, "title, authors, and URL of the online resource"):
		return "parse-online"
	case strings.Contains(system, "matches the search results"):
		return "summarize"
	default:
		return "other"
	}
}

// route answers each of bibcheck's prompts from the document text.
func route(system, user string, pdfLines []string) string {
	marshal := func(v any) string {
		out, _ := json.Marshal(v)
		return string(out)
	}

	switch promptKind(system) {
	case "classify-bib-page":
		match := false
		for _, line := range pdfLines {
			if entryMarker.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "REFERENCES") {
				match = true
				break
			}
		}
		return marshal(map[string]any{"contains_bibliography": match})

	case "count-entries":
		max := 0
		for _, line := range pdfLines {
			if m := entryMarker.FindStringSubmatch(line); m != nil {
				var n int
				fmt.Sscanf(m[1], "%d", &n)
				if n > max {
					max = n
				}
			}
		}
		return marshal(map[string]any{"num_entries": max})

	case "extract-entry":
		m := entryReq.FindStringSubmatch(user)
		if m != nil {
			want := m[1]
			for _, line := range pdfLines {
				if em := entryMarker.FindStringSubmatch(line); em != nil && em[1] == want {
					return marshal(map[string]any{"entry_exists": true, "bibliography_entry": em[2]})
				}
			}
		}
		return marshal(map[string]any{"entry_exists": false, "bibliography_entry": ""})

	case "parse-online":
		title := ""
		if m := quotedTitle.FindStringSubmatch(user); m != nil {
			title = strings.TrimSuffix(m[1], ",")
		}
		return marshal(map[string]any{
			"title":   title,
			"authors": []string{},
			"url":     strings.TrimRight(urlPattern.FindString(user), "."),
		})

	case "summarize":
		return marshal(map[string]any{
			"explanation":       "mock summarizer: external sources are stubbed in this test environment",
			"possible_mismatch": false,
		})
	}

	// Generic fallbacks for prompts the E2E flow does not normally reach.
	switch {
	case strings.Contains(system, "contains a URL"):
		return marshal(map[string]any{"url": strings.TrimRight(urlPattern.FindString(user), ".")})
	case strings.Contains(system, "Extract authors"):
		return marshal(map[string]any{"authors": []string{}, "has_et_al": false})
	case strings.Contains(system, "Extract the title"):
		return marshal(map[string]any{"title": quotedTitle.FindString(user)})
	case strings.Contains(system, "publication venue"):
		return marshal(map[string]any{"pub": ""})
	case strings.Contains(system, "what kind of bibliography entry"):
		return marshal(map[string]any{"kind": "website"})
	default:
		return marshal(map[string]any{"title": "", "authors": []string{}, "publication_date": ""})
	}
}

var textShow = regexp.MustCompile(`\(((?:[^()\\]|\\.)*)\)\s*Tj`)

// pdfText recovers the text lines of a PDF whose content streams use one
// "(line) Tj" per line, as written by test/e2e/genpdf. Streams are inflated
// when compressed (e.g. after pdfcpu rewrites them during page slicing).
func pdfText(pdf []byte) []string {
	var lines []string
	rest := pdf
	for {
		start := bytes.Index(rest, []byte("stream"))
		if start < 0 {
			break
		}
		chunk := rest[start+len("stream"):]
		chunk = bytes.TrimPrefix(chunk, []byte("\r\n"))
		chunk = bytes.TrimPrefix(chunk, []byte("\n"))
		end := bytes.Index(chunk, []byte("endstream"))
		if end < 0 {
			break
		}
		data := chunk[:end]
		if inflated, err := inflate(data); err == nil {
			data = inflated
		}
		for _, m := range textShow.FindAllSubmatch(data, -1) {
			lines = append(lines, unescapeText(string(m[1])))
		}
		rest = chunk[end:]
	}
	return lines
}

func inflate(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func unescapeText(s string) string {
	s = strings.ReplaceAll(s, `\(`, `(`)
	s = strings.ReplaceAll(s, `\)`, `)`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}
