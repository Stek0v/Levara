package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/chunker"
	"github.com/stek0v/levara/pkg/vectorstore"
)

const (
	DefaultChunkStrategy = "merged"
)

var markdownHeadingRe = regexp.MustCompile(`(?m)^(#{1,6})\s+(.+?)\s*$`)

type Embedder interface {
	EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
}

type LexicalIndex interface {
	Add(id, text, metadata string)
	Remove(id string)
}

type Indexer struct {
	Store    vectorstore.VectorStore
	Embedder Embedder
	Manifest *Manifest
	Lexical  LexicalIndex
	Now      func() time.Time
}

type IndexOptions struct {
	ProjectID          string
	Branch             string
	Generation         string
	Collection         string
	CommitHash         string
	ChunkStrategy      string
	MinChunkChars      int
	MaxChunkChars      int
	OverlapChars       int
	SnapToSentence     *bool
	ActivateGeneration bool
}

type MarkdownFile struct {
	Path       string
	Text       string
	FileDigest string
	DocumentID string
	Title      string
	Room       string
	Tags       []string
}

type IndexResult struct {
	ChunksCreated    int
	VectorIDs        []string
	DeletedVectorIDs []string
	Collection       string
	Generation       string
	Chunks           []IndexedChunk
}

type IndexedChunk struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Metadata map[string]any `json:"metadata"`
}

func (x *Indexer) IndexMarkdown(ctx context.Context, file MarkdownFile, opts IndexOptions) (IndexResult, error) {
	if x == nil || x.Store == nil {
		return IndexResult{}, errors.New("vector store required")
	}
	if x.Embedder == nil {
		return IndexResult{}, errors.New("embedder required")
	}
	if x.Manifest == nil {
		return IndexResult{}, errors.New("manifest required")
	}
	opts = normalizeIndexOptions(opts, x.Manifest)
	if err := validateIndexInput(file, opts); err != nil {
		return IndexResult{}, err
	}
	if file.DocumentID == "" {
		file.DocumentID = file.Path
	}
	if file.Title == "" {
		file.Title = filepath.Base(file.Path)
	}

	if err := x.Manifest.SetGeneration(opts.Generation, GenerationBuilding, ""); err != nil {
		return IndexResult{}, err
	}

	pathFilter := ChunkFilter{
		ProjectID:  opts.ProjectID,
		Branch:     opts.Branch,
		Generation: opts.Generation,
		Path:       file.Path,
		Collection: opts.Collection,
	}
	// Re-indexing the same path in the same generation replaces old vector IDs,
	// but only after the new vectors have been embedded and upserted.
	oldVectorIDs := x.Manifest.VectorIDs(pathFilter)

	chunks := chunkMarkdown(file, opts)
	if len(chunks) == 0 {
		x.Manifest.DeleteChunks(pathFilter)
		if len(oldVectorIDs) > 0 {
			if errs := x.Store.DeleteMany(opts.Collection, oldVectorIDs); len(errs) > 0 {
				return IndexResult{}, fmt.Errorf("delete old vectors: %v", errs)
			}
			x.removeLexical(oldVectorIDs)
		}
		return IndexResult{DeletedVectorIDs: oldVectorIDs, Collection: opts.Collection, Generation: opts.Generation}, nil
	}

	embedTexts := make([]string, len(chunks))
	for i, ch := range chunks {
		embedTexts[i] = buildWorkspaceEmbedText(ch.Text, file.Title, ch.HeadingPath)
	}
	vecs, err := x.Embedder.EmbedTexts(ctx, embedTexts)
	if err != nil {
		_ = x.Manifest.SetGeneration(opts.Generation, GenerationFailed, err.Error())
		return IndexResult{}, err
	}
	if len(vecs) != len(chunks) {
		err := fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(chunks))
		_ = x.Manifest.SetGeneration(opts.Generation, GenerationFailed, err.Error())
		return IndexResult{}, err
	}

	records := make([]vectorstore.UpsertRecord, 0, len(chunks))
	vectorIDs := make([]string, 0, len(chunks))
	manifestRecords := make([]ChunkRecord, 0, len(chunks))
	indexedChunks := make([]IndexedChunk, 0, len(chunks))
	now := x.now().UTC().Format(time.RFC3339)
	for i, ch := range chunks {
		chunkID := stableID("chk", opts.ProjectID, opts.Branch, file.Path, file.FileDigest, fmt.Sprint(ch.Index))
		vectorID := stableID("vec", opts.ProjectID, opts.Branch, opts.Generation, file.Path, file.FileDigest, fmt.Sprint(ch.Index))
		meta := workspaceChunkMetadata(file, opts, ch, chunkID, now)
		records = append(records, vectorstore.UpsertRecord{
			ID:       vectorID,
			Vector:   vecs[i],
			Metadata: meta,
		})
		vectorIDs = append(vectorIDs, vectorID)
		manifestRecords = append(manifestRecords, ChunkRecord{
			ProjectID:   opts.ProjectID,
			Branch:      opts.Branch,
			Generation:  opts.Generation,
			Path:        file.Path,
			FileDigest:  file.FileDigest,
			CommitHash:  opts.CommitHash,
			DocumentID:  file.DocumentID,
			HeadingPath: ch.HeadingPath,
			ChunkID:     chunkID,
			VectorID:    vectorID,
			Collection:  opts.Collection,
			UpdatedAt:   now,
		})
		indexedChunks = append(indexedChunks, IndexedChunk{
			ID:       vectorID,
			Text:     ch.Text,
			Metadata: meta,
		})
	}
	if errs := x.Store.BatchUpsert(opts.Collection, records); len(errs) > 0 {
		_ = x.Manifest.SetGeneration(opts.Generation, GenerationFailed, fmt.Sprint(errs))
		return IndexResult{}, fmt.Errorf("batch upsert: %v", errs)
	}

	staleVectorIDs := subtractStrings(oldVectorIDs, vectorIDs)
	if len(staleVectorIDs) > 0 {
		if errs := x.Store.DeleteMany(opts.Collection, staleVectorIDs); len(errs) > 0 {
			return IndexResult{}, fmt.Errorf("delete stale vectors: %v", errs)
		}
		x.removeLexical(staleVectorIDs)
	}
	x.Manifest.DeleteChunks(pathFilter)
	for _, rec := range manifestRecords {
		if err := x.Manifest.UpsertChunk(rec); err != nil {
			return IndexResult{}, err
		}
	}
	x.addLexical(indexedChunks)
	if opts.ActivateGeneration {
		if err := x.Manifest.ActivateGeneration(opts.Generation); err != nil {
			return IndexResult{}, err
		}
	}

	return IndexResult{
		ChunksCreated:    len(chunks),
		VectorIDs:        vectorIDs,
		DeletedVectorIDs: staleVectorIDs,
		Collection:       opts.Collection,
		Generation:       opts.Generation,
		Chunks:           indexedChunks,
	}, nil
}

func (x *Indexer) DeleteMarkdown(path string, opts IndexOptions) ([]string, error) {
	if x == nil || x.Store == nil || x.Manifest == nil {
		return nil, errors.New("store and manifest required")
	}
	opts = normalizeIndexOptions(opts, x.Manifest)
	if path == "" {
		return nil, ErrMissingPath
	}
	filter := ChunkFilter{
		ProjectID:  opts.ProjectID,
		Branch:     opts.Branch,
		Generation: opts.Generation,
		Path:       path,
		Collection: opts.Collection,
	}
	ids := x.Manifest.VectorIDs(filter)
	if len(ids) == 0 {
		return nil, nil
	}
	if errs := x.Store.DeleteMany(opts.Collection, ids); len(errs) > 0 {
		return nil, fmt.Errorf("delete vectors: %v", errs)
	}
	x.removeLexical(ids)
	x.Manifest.DeleteChunks(filter)
	return ids, nil
}

func (x *Indexer) now() time.Time {
	if x != nil && x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Indexer) addLexical(chunks []IndexedChunk) {
	if x == nil || x.Lexical == nil {
		return
	}
	for _, ch := range chunks {
		x.Lexical.Add(ch.ID, ch.Text, metadataString(ch.Metadata))
	}
}

func (x *Indexer) removeLexical(ids []string) {
	if x == nil || x.Lexical == nil {
		return
	}
	for _, id := range ids {
		x.Lexical.Remove(id)
	}
}

func metadataString(meta map[string]any) string {
	data, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(data)
}

type markdownChunk struct {
	Index       int
	Text        string
	Section     string
	HeadingPath []string
}

func chunkMarkdown(file MarkdownFile, opts IndexOptions) []markdownChunk {
	text := strings.ReplaceAll(file.Text, "\r\n", "\n")
	documentKey := stableID("doc", opts.ProjectID, opts.Branch, file.Path, file.FileDigest)
	minChars := opts.MinChunkChars
	if minChars <= 0 {
		minChars = chunker.DefaultMinChunkChars
	}
	maxChars := opts.MaxChunkChars
	if maxChars <= 0 {
		maxChars = chunker.DefaultMaxChunkChars
	}

	var chunks []chunker.Chunk
	switch opts.ChunkStrategy {
	case "sliding":
		overlap := opts.OverlapChars
		if overlap <= 0 {
			overlap = maxChars / 5
		}
		snap := true
		if opts.SnapToSentence != nil {
			snap = *opts.SnapToSentence
		}
		chunks = chunker.ChunkBySlidingOpts(text, maxChars, overlap, snap, documentKey)
	case "paragraph":
		chunks = chunker.ChunkByParagraphSimple(text, minChars, documentKey)
	case "sentence":
		chunks = chunker.ChunkBySentence(text, minChars, maxChars, documentKey)
	default:
		chunks = chunker.ChunkByParagraphMerged(text, minChars, maxChars, documentKey)
	}

	headingIndex := parseMarkdownHeadings(text)
	cursorByte := 0
	out := make([]markdownChunk, 0, len(chunks))
	for i, ch := range chunks {
		offset, nextByte := findChunkOffset(text, ch.Text, cursorByte)
		if offset >= 0 {
			cursorByte = nextByte
		} else {
			offset = i * maxChars
		}
		path := headingIndex.PathAt(offset)
		section := ""
		if len(path) > 0 {
			section = path[len(path)-1]
		}
		out = append(out, markdownChunk{
			Index:       i,
			Text:        ch.Text,
			Section:     section,
			HeadingPath: path,
		})
	}
	return out
}

type headingPoint struct {
	Offset int
	Level  int
	Title  string
	Path   []string
}

type headingIndex []headingPoint

func parseMarkdownHeadings(text string) headingIndex {
	var points []headingPoint
	stack := make([]string, 0, 6)
	for _, match := range markdownHeadingRe.FindAllStringSubmatchIndex(text, -1) {
		level := match[3] - match[2]
		title := strings.TrimSpace(text[match[4]:match[5]])
		title = strings.Trim(title, "# \t")
		if title == "" {
			continue
		}
		if level > len(stack) {
			for len(stack) < level-1 {
				stack = append(stack, "")
			}
			stack = append(stack, title)
		} else {
			stack = stack[:level-1]
			stack = append(stack, title)
		}
		path := make([]string, 0, len(stack))
		for _, item := range stack {
			if item != "" {
				path = append(path, item)
			}
		}
		points = append(points, headingPoint{
			Offset: byteToRuneOffset(text, match[0]),
			Level:  level,
			Title:  title,
			Path:   path,
		})
	}
	return points
}

func (idx headingIndex) PathAt(offset int) []string {
	var path []string
	for _, point := range idx {
		if point.Offset <= offset {
			path = point.Path
			continue
		}
		break
	}
	out := make([]string, len(path))
	copy(out, path)
	return out
}

func findChunkOffset(text, chunkText string, cursorByte int) (runeOffset int, nextByte int) {
	if chunkText == "" {
		return byteToRuneOffset(text, cursorByte), cursorByte
	}
	if cursorByte < 0 || cursorByte > len(text) {
		cursorByte = 0
	}
	if idx := strings.Index(text[cursorByte:], chunkText); idx >= 0 {
		start := cursorByte + idx
		return byteToRuneOffset(text, start), start + len(chunkText)
	}
	if idx := strings.Index(text, chunkText); idx >= 0 {
		return byteToRuneOffset(text, idx), idx + len(chunkText)
	}
	return -1, cursorByte
}

func byteToRuneOffset(text string, byteOffset int) int {
	if byteOffset <= 0 {
		return 0
	}
	if byteOffset > len(text) {
		byteOffset = len(text)
	}
	return len([]rune(text[:byteOffset]))
}

func buildWorkspaceEmbedText(text, title string, headingPath []string) string {
	var parts []string
	if title != "" {
		parts = append(parts, "Document: "+title)
	}
	if len(headingPath) > 0 {
		parts = append(parts, "Section: "+strings.Join(headingPath, " > "))
	}
	parts = append(parts, text)
	return strings.Join(parts, "\n\n")
}

func workspaceChunkMetadata(file MarkdownFile, opts IndexOptions, ch markdownChunk, chunkID, updatedAt string) map[string]any {
	meta := map[string]any{
		"text":           ch.Text,
		"dataset_id":     opts.ProjectID,
		"project_id":     opts.ProjectID,
		"branch":         opts.Branch,
		"generation":     opts.Generation,
		"path":           file.Path,
		"file_digest":    file.FileDigest,
		"commit_hash":    opts.CommitHash,
		"document_id":    file.DocumentID,
		"document_title": file.Title,
		"heading_path":   ch.HeadingPath,
		"section":        ch.Section,
		"chunk_id":       chunkID,
		"room":           file.Room,
		"tags":           file.Tags,
		"updated_at":     updatedAt,
	}
	return meta
}

func stableID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:32]
}

func normalizeIndexOptions(opts IndexOptions, manifest *Manifest) IndexOptions {
	if opts.ProjectID == "" && manifest != nil {
		opts.ProjectID = manifest.ProjectID
	}
	if opts.Branch == "" && manifest != nil {
		opts.Branch = manifest.Branch
	}
	if opts.ChunkStrategy == "" {
		opts.ChunkStrategy = DefaultChunkStrategy
	}
	if opts.Collection == "" {
		opts.Collection = DefaultCollectionName(opts.ProjectID, opts.Branch, opts.Generation)
	}
	return opts
}

func DefaultCollectionName(projectID, branch, generation string) string {
	return fmt.Sprintf("kb_%s_%s_%s", sanitizeID(projectID), sanitizeID(branch), sanitizeID(generation))
}

func validateIndexInput(file MarkdownFile, opts IndexOptions) error {
	switch {
	case opts.ProjectID == "":
		return ErrMissingProjectID
	case opts.Branch == "":
		return ErrMissingBranch
	case opts.Generation == "":
		return ErrMissingGeneration
	case file.Path == "":
		return ErrMissingPath
	default:
		return nil
	}
}

func sanitizeID(s string) string {
	if s == "" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "default"
	}
	return out
}

func subtractStrings(all, keep []string) []string {
	if len(all) == 0 {
		return nil
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		keepSet[id] = struct{}{}
	}
	var out []string
	for _, id := range all {
		if _, ok := keepSet[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
