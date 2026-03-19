package chunker

// Chunk represents a text segment with metadata.
type Chunk struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	Chapter    int    `json:"chapter"`
	ChunkIndex int    `json:"chunk_index"`
	CutType    string `json:"cut_type,omitempty"` // "paragraph", "sentence"
}

const (
	DefaultMinChunkChars = 80
	DefaultMaxChunkChars = 600
)
