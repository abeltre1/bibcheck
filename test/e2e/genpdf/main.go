// Copyright 2025 National Technology and Engineering Solutions of Sandia
// SPDX-License-Identifier: BSD-3-Clause

// genpdf builds a simple multi-page PDF from a plain-text description, for
// end-to-end testing. Pages are separated by lines containing only "===PAGE===";
// every other input line becomes one text line on the page. Content streams
// are written uncompressed so test tooling (e.g. test/e2e/mockllm) can read
// the text back out of the PDF without a full PDF renderer.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s input.txt output.pdf\n", os.Args[0])
		os.Exit(2)
	}

	input, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var pages [][]string
	var current []string
	for _, line := range strings.Split(strings.ReplaceAll(string(input), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "===PAGE===" {
			pages = append(pages, current)
			current = nil
			continue
		}
		current = append(current, line)
	}
	pages = append(pages, current)

	if err := os.WriteFile(os.Args[2], render(pages), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d pages)\n", os.Args[2], len(pages))
}

func escapeText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

func contentStream(lines []string) []byte {
	var b bytes.Buffer
	b.WriteString("BT\n/F1 9 Tf\n50 770 Td\n12 TL\n")
	for _, line := range lines {
		fmt.Fprintf(&b, "(%s) Tj\nT*\n", escapeText(line))
	}
	b.WriteString("ET\n")
	return b.Bytes()
}

// render emits a minimal PDF 1.4 file: catalog, page tree, one font, and one
// uncompressed content stream per page, with a correct xref table.
func render(pages [][]string) []byte {
	type object struct {
		id   int
		body []byte
	}
	var objects []object
	add := func(body string) int {
		objects = append(objects, object{id: len(objects) + 1, body: []byte(body)})
		return len(objects)
	}

	catalogID := add("<< /Type /Catalog /Pages 2 0 R >>")
	pagesID := add("") // placeholder, filled below
	fontID := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")

	var pageIDs []int
	for _, lines := range pages {
		stream := contentStream(lines)
		contentID := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream))
		pageID := add(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesID, fontID, contentID))
		pageIDs = append(pageIDs, pageID)
	}

	kids := make([]string, len(pageIDs))
	for i, id := range pageIDs {
		kids[i] = fmt.Sprintf("%d 0 R", id)
	}
	objects[pagesID-1].body = []byte(fmt.Sprintf(
		"<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pageIDs)))

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for _, obj := range objects {
		offsets[obj.id] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", obj.id, obj.body)
	}

	xrefStart := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for id := 1; id <= len(objects); id++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, catalogID, xrefStart)
	return out.Bytes()
}
