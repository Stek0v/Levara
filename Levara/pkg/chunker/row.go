package chunker

import "strings"

// ChunkByRow splits CSV/tabular text by rows.
// Groups N rows per chunk (default: 20 rows per chunk).
// First row (header) is prepended to every chunk for context.
func ChunkByRow(text string, rowsPerChunk int, documentID string) []Chunk {
	if rowsPerChunk <= 0 {
		rowsPerChunk = 20
	}

	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return nil
	}

	header := lines[0]
	dataLines := lines[1:]

	var chunks []Chunk
	idx := 0
	for i := 0; i < len(dataLines); i += rowsPerChunk {
		end := i + rowsPerChunk
		if end > len(dataLines) {
			end = len(dataLines)
		}
		batch := dataLines[i:end]

		// Skip empty batches
		chunkText := header + "\n" + strings.Join(batch, "\n")
		chunkText = strings.TrimSpace(chunkText)
		if chunkText == "" || chunkText == header {
			continue
		}

		chunks = append(chunks, Chunk{
			ID:         chunkID(documentID, idx),
			Text:       chunkText,
			ChunkIndex: idx,
			CutType:    "row",
		})
		idx++
	}
	return chunks
}
