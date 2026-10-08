// Package workspace contains markdown-workspace indexing primitives.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const ManifestVersion = 1

type GenerationStatus string

const (
	GenerationBuilding  GenerationStatus = "building"
	GenerationActive    GenerationStatus = "active"
	GenerationFailed    GenerationStatus = "failed"
	GenerationGCPending GenerationStatus = "gc_pending"
)

var (
	ErrMissingProjectID  = errors.New("project_id required")
	ErrMissingBranch     = errors.New("branch required")
	ErrMissingGeneration = errors.New("generation required")
	ErrMissingPath       = errors.New("path required")
	ErrMissingChunkID    = errors.New("chunk_id required")
	ErrMissingVectorID   = errors.New("vector_id required")
)

// Generation records lifecycle state for an index generation.
type Generation struct {
	ID          string           `json:"id"`
	Status      GenerationStatus `json:"status"`
	CreatedAt   string           `json:"created_at,omitempty"`
	ActivatedAt string           `json:"activated_at,omitempty"`
	Error       string           `json:"error,omitempty"`
}

// ChunkRecord maps one markdown-derived chunk to one vector-store ID.
type ChunkRecord struct {
	ProjectID   string   `json:"project_id"`
	Branch      string   `json:"branch"`
	Generation  string   `json:"generation"`
	Path        string   `json:"path"`
	FileDigest  string   `json:"file_digest,omitempty"`
	CommitHash  string   `json:"commit_hash,omitempty"`
	DocumentID  string   `json:"document_id,omitempty"`
	HeadingPath []string `json:"heading_path,omitempty"`
	ChunkID     string   `json:"chunk_id"`
	VectorID    string   `json:"vector_id"`
	Collection  string   `json:"collection,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// ChunkFilter selects chunks from a manifest. Empty fields are wildcards.
type ChunkFilter struct {
	ProjectID  string
	Branch     string
	Generation string
	Path       string
	FileDigest string
	CommitHash string
	DocumentID string
	ChunkID    string
	VectorID   string
	Collection string
	ActiveOnly bool
}

// Manifest is the durable workspace-index manifest for one project/branch pair.
type Manifest struct {
	Version            int                          `json:"version"`
	ProjectID          string                       `json:"project_id"`
	Branch             string                       `json:"branch"`
	ActiveGeneration   string                       `json:"active_generation,omitempty"`
	Generations        map[string]Generation        `json:"generations"`
	Chunks             map[string]ChunkRecord       `json:"chunks"`
	Files              map[string]map[string]string `json:"files,omitempty"`
	PendingRetirements map[string]ChunkRecord       `json:"pending_retirements,omitempty"`
}

func NewManifest(projectID, branch string) *Manifest {
	return &Manifest{
		Version:            ManifestVersion,
		ProjectID:          projectID,
		Branch:             branch,
		Generations:        make(map[string]Generation),
		Chunks:             make(map[string]ChunkRecord),
		Files:              make(map[string]map[string]string),
		PendingRetirements: make(map[string]ChunkRecord),
	}
}

func LoadManifest(path string) (*Manifest, error) {
	root, err := OpenRoot(filepath.Dir(path), ".", false)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return LoadManifestRoot(root, filepath.Base(path))
}

func LoadManifestRoot(root *os.Root, name string) (*Manifest, error) {
	data, err := ReadFile(root, name)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m.ensureMaps()
	return &m, nil
}

func (m *Manifest) Save(path string) error {
	root, err := OpenRoot(filepath.Dir(path), ".", true)
	if err != nil {
		return err
	}
	defer root.Close()
	return m.SaveRoot(context.Background(), root, filepath.Base(path))
}

func (m *Manifest) SaveRoot(ctx context.Context, root *os.Root, name string) error {
	m.ensureMaps()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(ctx, root, name, append(data, '\n'), 0644)
}

func (m *Manifest) SetGeneration(id string, status GenerationStatus, errText string) error {
	if id == "" {
		return ErrMissingGeneration
	}
	m.ensureMaps()
	now := time.Now().UTC().Format(time.RFC3339)
	g := m.Generations[id]
	if g.ID == "" {
		g.ID = id
		g.CreatedAt = now
	}
	g.Status = status
	g.Error = errText
	if status == GenerationActive {
		g.ActivatedAt = now
	}
	m.Generations[id] = g
	return nil
}

func (m *Manifest) ActivateGeneration(id string) error {
	if id == "" {
		return ErrMissingGeneration
	}
	m.ensureMaps()
	now := time.Now().UTC().Format(time.RFC3339)
	for genID, g := range m.Generations {
		if g.Status == GenerationActive && genID != id {
			g.Status = GenerationGCPending
			m.Generations[genID] = g
		}
	}
	g := m.Generations[id]
	if g.ID == "" {
		g.ID = id
		g.CreatedAt = now
	}
	g.Status = GenerationActive
	g.ActivatedAt = now
	m.Generations[id] = g
	m.ActiveGeneration = id
	return nil
}

func (m *Manifest) UpsertChunk(rec ChunkRecord) error {
	m.ensureMaps()
	if rec.ProjectID == "" {
		rec.ProjectID = m.ProjectID
	}
	if rec.Branch == "" {
		rec.Branch = m.Branch
	}
	if rec.UpdatedAt == "" {
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := rec.Validate(); err != nil {
		return err
	}
	if _, ok := m.Generations[rec.Generation]; !ok {
		if err := m.SetGeneration(rec.Generation, GenerationBuilding, ""); err != nil {
			return err
		}
	}
	m.Chunks[rec.VectorID] = rec
	return nil
}

func (m *Manifest) ListChunks(filter ChunkFilter) []ChunkRecord {
	m.ensureMaps()
	out := make([]ChunkRecord, 0)
	for _, rec := range m.Chunks {
		if m.match(rec, filter) {
			out = append(out, rec)
		}
	}
	sortChunks(out)
	return out
}

func (m *Manifest) VectorIDs(filter ChunkFilter) []string {
	chunks := m.ListChunks(filter)
	ids := make([]string, 0, len(chunks))
	for _, rec := range chunks {
		ids = append(ids, rec.VectorID)
	}
	sort.Strings(ids)
	return ids
}

func (m *Manifest) DeleteChunks(filter ChunkFilter) []ChunkRecord {
	matches := m.ListChunks(filter)
	for _, rec := range matches {
		delete(m.Chunks, rec.VectorID)
	}
	return matches
}

func (r ChunkRecord) Validate() error {
	switch {
	case r.ProjectID == "":
		return ErrMissingProjectID
	case r.Branch == "":
		return ErrMissingBranch
	case r.Generation == "":
		return ErrMissingGeneration
	case r.Path == "":
		return ErrMissingPath
	case r.ChunkID == "":
		return ErrMissingChunkID
	case r.VectorID == "":
		return ErrMissingVectorID
	default:
		return nil
	}
}

func (m *Manifest) ensureMaps() {
	if m.Files == nil {
		m.Files = make(map[string]map[string]string)
	}
	if m.PendingRetirements == nil {
		m.PendingRetirements = make(map[string]ChunkRecord)
	}
	if m.Version == 0 {
		m.Version = ManifestVersion
	}
	if m.Generations == nil {
		m.Generations = make(map[string]Generation)
	}
	if m.Chunks == nil {
		m.Chunks = make(map[string]ChunkRecord)
	}
}

func (m *Manifest) match(rec ChunkRecord, f ChunkFilter) bool {
	if f.ActiveOnly && rec.Generation != m.ActiveGeneration {
		return false
	}
	return matchString(f.ProjectID, rec.ProjectID) &&
		matchString(f.Branch, rec.Branch) &&
		matchString(f.Generation, rec.Generation) &&
		matchString(f.Path, rec.Path) &&
		matchString(f.FileDigest, rec.FileDigest) &&
		matchString(f.CommitHash, rec.CommitHash) &&
		matchString(f.DocumentID, rec.DocumentID) &&
		matchString(f.ChunkID, rec.ChunkID) &&
		matchString(f.VectorID, rec.VectorID) &&
		matchString(f.Collection, rec.Collection)
}

func matchString(want, got string) bool {
	return want == "" || want == got
}

func sortChunks(chunks []ChunkRecord) {
	sort.Slice(chunks, func(i, j int) bool {
		a, b := chunks[i], chunks[j]
		ka := fmt.Sprintf("%s\x00%s\x00%s\x00%s", a.Generation, a.Path, a.ChunkID, a.VectorID)
		kb := fmt.Sprintf("%s\x00%s\x00%s\x00%s", b.Generation, b.Path, b.ChunkID, b.VectorID)
		return ka < kb
	})
}

// Clone prepares isolated candidate state without changing legacy unknown file
// inventories. A present empty inner map certifies an explicitly empty inventory.
func (m *Manifest) Clone() *Manifest {
	if m == nil {
		return nil
	}
	out := *m
	out.Generations = make(map[string]Generation, len(m.Generations))
	for id, generation := range m.Generations {
		out.Generations[id] = generation
	}
	out.Chunks = make(map[string]ChunkRecord, len(m.Chunks))
	for id, record := range m.Chunks {
		if record.HeadingPath != nil {
			record.HeadingPath = append([]string{}, record.HeadingPath...)
		}
		out.Chunks[id] = record
	}
	out.Files = make(map[string]map[string]string, len(m.Files))
	for generation, files := range m.Files {
		if files == nil {
			out.Files[generation] = nil
			continue
		}
		copyFiles := make(map[string]string, len(files))
		for path, digest := range files {
			copyFiles[path] = digest
		}
		out.Files[generation] = copyFiles
	}
	out.PendingRetirements = make(map[string]ChunkRecord, len(m.PendingRetirements))
	for id, record := range m.PendingRetirements {
		if record.HeadingPath != nil {
			record.HeadingPath = append([]string{}, record.HeadingPath...)
		}
		out.PendingRetirements[id] = record
	}
	return &out
}
