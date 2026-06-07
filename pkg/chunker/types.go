package chunker

// Chunk represents a text segment with metadata.
type Chunk struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Chapter       int    `json:"chapter"`
	ChunkIndex    int    `json:"chunk_index"`
	CutType       string `json:"cut_type,omitempty"`        // "paragraph", "sentence", "sliding", "code", "row"
	ParentID      string `json:"parent_id,omitempty"`       // ID of parent chunk (for child chunks in parent-child mode)
	DocumentID    string `json:"document_id,omitempty"`     // stable document identifier
	DocumentTitle string `json:"document_title,omitempty"`  // human-readable document title/filename
	Section       string `json:"section,omitempty"`         // detected section header (Markdown ## or title-case)
}

const (
	DefaultMinChunkChars = 80
	DefaultMaxChunkChars = 600
)
