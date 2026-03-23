package chunker

import (
	"regexp"
	"strings"
)

// funcBoundary matches function/class/method boundaries at the start of a line.
// Covers: Go (func), Python (def, class), JS/TS (function, class, export),
// Java/C#/Kotlin (public, private, protected), Ruby (def, class).
var funcBoundary = regexp.MustCompile(`(?m)^(func |def |class |function |export (?:default )?(?:function |class )|public |private |protected )`)

// ChunkByFunction splits source code by function/class boundaries.
//
// Algorithm:
//  1. Extract the preamble (imports, package declarations) — all lines before the
//     first function/class boundary.
//  2. Split remaining text at each function/class boundary.
//  3. Each function/class block becomes one chunk, with the preamble prepended.
//  4. If a chunk exceeds maxChunkChars, it is split by lines.
//  5. Chunks smaller than a minimal threshold (10 chars) are discarded.
func ChunkByFunction(text string, maxChunkChars int, documentID string) []Chunk {
	if maxChunkChars <= 0 {
		maxChunkChars = 3000
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Find all function/class boundary positions
	locs := funcBoundary.FindAllStringIndex(text, -1)

	// No boundaries found — return as single chunk (or paragraph fallback)
	if len(locs) == 0 {
		if runeLen(text) < 10 {
			return nil
		}
		return []Chunk{{
			ID:         chunkID(documentID, 0),
			Text:       strings.TrimSpace(text),
			ChunkIndex: 0,
			CutType:    "code",
		}}
	}

	// Extract preamble: everything before first function/class boundary
	preamble := ""
	if locs[0][0] > 0 {
		preamble = strings.TrimRight(text[:locs[0][0]], "\n")
	}

	// Split into function/class blocks
	var blocks []string
	for i, loc := range locs {
		var end int
		if i+1 < len(locs) {
			end = locs[i+1][0]
		} else {
			end = len(text)
		}
		block := strings.TrimRight(text[loc[0]:end], "\n")
		if strings.TrimSpace(block) != "" {
			blocks = append(blocks, block)
		}
	}

	// Build chunks: preamble + each block
	var chunks []Chunk
	for _, block := range blocks {
		var chunkText string
		if preamble != "" {
			chunkText = preamble + "\n\n" + block
		} else {
			chunkText = block
		}
		chunkText = strings.TrimSpace(chunkText)

		if runeLen(chunkText) <= maxChunkChars {
			if runeLen(chunkText) >= 10 {
				chunks = append(chunks, Chunk{
					ID:         chunkID(documentID, len(chunks)),
					Text:       chunkText,
					ChunkIndex: len(chunks),
					CutType:    "code",
				})
			}
		} else {
			// Oversized block: split by lines
			subChunks := splitCodeByLines(chunkText, maxChunkChars, preamble, documentID, len(chunks))
			chunks = append(chunks, subChunks...)
		}
	}

	return chunks
}

// splitCodeByLines splits an oversized code block into line-based chunks,
// each prefixed with the preamble for context.
func splitCodeByLines(text string, maxChunkChars int, preamble string, documentID string, startIdx int) []Chunk {
	lines := strings.Split(text, "\n")

	// If preamble is already in text, don't double-prepend
	preamblePrefix := ""
	if preamble != "" && !strings.HasPrefix(text, preamble) {
		preamblePrefix = preamble + "\n\n"
	}

	var chunks []Chunk
	var buf []string
	bufLen := runeLen(preamblePrefix)

	for _, line := range lines {
		lineLen := runeLen(line) + 1 // +1 for newline
		if bufLen+lineLen > maxChunkChars && len(buf) > 0 {
			chunkText := strings.TrimSpace(preamblePrefix + strings.Join(buf, "\n"))
			if runeLen(chunkText) >= 10 {
				chunks = append(chunks, Chunk{
					ID:         chunkID(documentID, startIdx+len(chunks)),
					Text:       chunkText,
					ChunkIndex: startIdx + len(chunks),
					CutType:    "code",
				})
			}
			buf = nil
			bufLen = runeLen(preamblePrefix)
		}
		buf = append(buf, line)
		bufLen += lineLen
	}

	if len(buf) > 0 {
		chunkText := strings.TrimSpace(preamblePrefix + strings.Join(buf, "\n"))
		if runeLen(chunkText) >= 10 {
			chunks = append(chunks, Chunk{
				ID:         chunkID(documentID, startIdx+len(chunks)),
				Text:       chunkText,
				ChunkIndex: startIdx + len(chunks),
				CutType:    "code",
			})
		}
	}

	return chunks
}
