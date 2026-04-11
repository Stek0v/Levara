package chunker

import (
	"log"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkBySliding splits text into fixed-size windows with overlap.
// windowChars is the window size in characters (runes).
// overlapChars is the number of characters shared between adjacent chunks.
// Overlap is taken from the END of the previous chunk.
//
// When snapToSentence is true (default), window boundaries snap to the nearest
// sentence end (. ! ? followed by space) within ±20% of the target position.
// If no sentence end is found, snaps to the nearest word boundary (space).
// This prevents cutting words and sentences mid-way.
//
// Example: text="ABCDEFGHIJ", window=5, overlap=2, snapToSentence=false
//
//	chunk 0: "ABCDE"
//	chunk 1: "DEFGH"  (overlap: "DE")
//	chunk 2: "GHIJ"   (shorter than window — last chunk)
func ChunkBySliding(text string, windowChars, overlapChars int, documentID string) []Chunk {
	return ChunkBySlidingOpts(text, windowChars, overlapChars, true, documentID)
}

// ChunkBySlidingOpts is ChunkBySliding with explicit snapToSentence control.
func ChunkBySlidingOpts(text string, windowChars, overlapChars int, snapToSentence bool, documentID string) []Chunk {
	// Normalize CRLF → LF
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(text)

	if text == "" {
		return nil
	}

	// Clamp parameters
	if windowChars <= 0 {
		windowChars = DefaultMaxChunkChars
	}
	if overlapChars < 0 {
		overlapChars = 0
	}
	if overlapChars >= windowChars {
		clamped := windowChars / 2
		log.Printf("[chunker] overlap_chars (%d) >= window_chars (%d), clamped to %d", overlapChars, windowChars, clamped)
		overlapChars = clamped
	}

	runes := []rune(text)
	totalRunes := len(runes)

	var chunks []Chunk
	pos := 0

	for pos < totalRunes {
		end := pos + windowChars
		if end > totalRunes {
			end = totalRunes
		}

		// Snap end boundary to sentence/word if not at text end
		if snapToSentence && end < totalRunes {
			end = snapBoundary(runes, pos, end, windowChars)
		}

		chunkText := string(runes[pos:end])

		// Skip chunks below minimum threshold
		if utf8.RuneCountInString(chunkText) < DefaultMinChunkChars {
			if len(chunks) == 0 {
				// Exception: first/only chunk — keep even if short
				chunks = append(chunks, Chunk{
					ID:         chunkID(documentID, 0),
					Text:       chunkText,
					ChunkIndex: 0,
					CutType:    "sliding",
				})
			}
			break
		}

		chunks = append(chunks, Chunk{
			ID:         chunkID(documentID, len(chunks)),
			Text:       chunkText,
			ChunkIndex: len(chunks),
			CutType:    "sliding",
		})

		if end >= totalRunes {
			break
		}

		// Next position: step from pos, but adjusted if we snapped end
		// Overlap is calculated from the actual end, not the target
		actualChunkLen := end - pos
		nextPos := pos + actualChunkLen - overlapChars
		if nextPos <= pos {
			// Safety: always advance at least 1 rune
			nextPos = pos + 1
		}
		pos = nextPos
	}

	return chunks
}

// snapBoundary adjusts end position to a sentence or word boundary.
// Searches within ±20% of target end for sentence-ending punctuation.
// Falls back to nearest space. Falls back to original position if nothing found.
func snapBoundary(runes []rune, chunkStart, targetEnd, windowChars int) int {
	tolerance := windowChars / 5 // 20%
	searchStart := targetEnd - tolerance
	if searchStart < chunkStart+1 {
		searchStart = chunkStart + 1
	}
	searchEnd := targetEnd + tolerance
	if searchEnd > len(runes) {
		searchEnd = len(runes)
	}

	// Pass 1: find nearest sentence end (. ! ? followed by space or end)
	bestSentence := -1
	bestSentenceDist := tolerance + 1
	for i := searchStart; i < searchEnd-1; i++ {
		if isSentenceEnd(runes[i]) && (i+1 >= len(runes) || unicode.IsSpace(runes[i+1])) {
			dist := abs(i+1 - targetEnd) // snap AFTER the punctuation
			if dist < bestSentenceDist {
				bestSentenceDist = dist
				bestSentence = i + 1
			}
		}
	}
	if bestSentence > chunkStart {
		return bestSentence
	}

	// Pass 2: find nearest word boundary (space)
	bestSpace := -1
	bestSpaceDist := tolerance + 1
	for i := searchStart; i < searchEnd; i++ {
		if unicode.IsSpace(runes[i]) {
			dist := abs(i - targetEnd)
			if dist < bestSpaceDist {
				bestSpaceDist = dist
				bestSpace = i
			}
		}
	}
	if bestSpace > chunkStart {
		return bestSpace
	}

	// Pass 3: no good boundary found — hard cut
	return targetEnd
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
