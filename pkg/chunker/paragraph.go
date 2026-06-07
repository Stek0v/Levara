package chunker

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// runeLen returns character count (like Python len()), not byte count.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// chunkID returns a deterministic UUID5 when documentID is non-empty, using
// uuid5(NAMESPACE_OID, "{documentID}-{chunkIndex}") — identical to Python's
// uuid.uuid5(uuid.NAMESPACE_OID, f"{document_id}-{chunk_index}").
// Falls back to a random UUID4 when documentID is empty (backward compatible).
func chunkID(documentID string, chunkIndex int) string {
	if documentID == "" {
		return uuid.New().String()
	}
	name := fmt.Sprintf("%s-%d", documentID, chunkIndex)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

// ChunkByParagraphMerged splits text into paragraphs (by "\n\n"), merges
// small ones up to maxChunkChars, and discards chunks below minChunkChars.
// All lengths are in CHARACTERS (runes), matching Python len() behavior.
// When documentID is non-empty, chunk IDs are deterministic UUID5s matching
// Python's uuid.uuid5(uuid.NAMESPACE_OID, f"{document_id}-{chunk_index}").
func ChunkByParagraphMerged(text string, minChunkChars, maxChunkChars int, documentID string) []Chunk {
	// Normalize line endings: CRLF -> LF (Python reads in text mode)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rawParagraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	var bufParts []string
	bufRuneLen := 0
	chapter := 0

	for _, para := range rawParagraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Chapter detection: "Глава N" with total rune length < 20
		if strings.HasPrefix(para, "Глава ") && runeLen(para) < 20 {
			numStr := strings.TrimSpace(strings.TrimPrefix(para, "Глава "))
			if n, err := strconv.Atoi(numStr); err == nil {
				chapter = n
			}
		}

		paraRuneLen := runeLen(para)
		if bufRuneLen+paraRuneLen < maxChunkChars {
			bufParts = append(bufParts, para)
			if bufRuneLen > 0 {
				bufRuneLen += 2 // "\n\n" separator = 2 runes
			}
			bufRuneLen += paraRuneLen
		} else {
			if bufRuneLen >= minChunkChars && len(bufParts) > 0 {
				chunks = append(chunks, Chunk{
					ID:         chunkID(documentID, len(chunks)),
					Text:       strings.Join(bufParts, "\n\n"),
					Chapter:    chapter,
					ChunkIndex: len(chunks),
				})
			}
			bufParts = []string{para}
			bufRuneLen = paraRuneLen
		}
	}

	// Final buffer
	if bufRuneLen >= minChunkChars && len(bufParts) > 0 {
		chunks = append(chunks, Chunk{
			ID:         chunkID(documentID, len(chunks)),
			Text:       strings.Join(bufParts, "\n\n"),
			Chapter:    chapter,
			ChunkIndex: len(chunks),
		})
	}

	return chunks
}

// ChunkByParagraphSimple splits text into individual paragraphs (by "\n\n").
// Each paragraph >= minChunkChars becomes its own chunk (no merging).
// When documentID is non-empty, chunk IDs are deterministic UUID5s.
func ChunkByParagraphSimple(text string, minChunkChars int, documentID string) []Chunk {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rawParagraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	chapter := 0

	for _, para := range rawParagraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		if strings.HasPrefix(para, "Глава ") && runeLen(para) < 20 {
			numStr := strings.TrimSpace(strings.TrimPrefix(para, "Глава "))
			if n, err := strconv.Atoi(numStr); err == nil {
				chapter = n
			}
		}

		if runeLen(para) >= minChunkChars {
			chunks = append(chunks, Chunk{
				ID:         chunkID(documentID, len(chunks)),
				Text:       para,
				Chapter:    chapter,
				ChunkIndex: len(chunks),
				CutType:    "paragraph",
			})
		}
	}

	return chunks
}
