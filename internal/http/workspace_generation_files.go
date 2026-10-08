package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/vectorstore"
	"github.com/stek0v/levara/pkg/workspace"
)

// One retained attempt per branch: callers hold the native project lock.
type workspaceGenerationAttempt struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Branch     string `json:"branch"`
	Generation string `json:"generation"`
	Collection string `json:"collection"`
}

func workspaceGenerationAttemptPath(cfg APIConfig, projectID, branch string) string {
	return workspaceManifestPath(cfg, projectID, branch) + ".attempt.json"
}

func removeWorkspaceGenerationAttempt(cfg APIConfig, projectID, branch string) error {
	root, name, err := openWorkspaceFileParent(cfg, workspaceGenerationAttemptPath(cfg, projectID, branch), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func recoverWorkspaceGenerationAttempt(cfg APIConfig, manifest *workspace.Manifest, store vectorstore.VectorStore) error {
	data, err := readWorkspaceFile(cfg, workspaceGenerationAttemptPath(cfg, manifest.ProjectID, manifest.Branch))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var attempt workspaceGenerationAttempt
	if err := json.Unmarshal(data, &attempt); err != nil {
		return err
	}
	if _, err := uuid.Parse(attempt.ID); err != nil {
		return errors.New("invalid workspace attempt identity")
	}
	if attempt.ProjectID != manifest.ProjectID || attempt.Branch != manifest.Branch || attempt.Generation == "" || attempt.Collection == "" {
		return errors.New("workspace attempt target mismatch")
	}
	if err := validateWorkspaceCollection(attempt.Collection); err != nil {
		return err
	}
	if store.Has(attempt.Collection) {
		records, err := store.Scan(attempt.Collection)
		if err != nil {
			return err
		}
		var ids []string
		for _, record := range records {
			var metadata map[string]any
			if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
				continue
			}
			if metadata["attempt_id"] != attempt.ID || metadata["project_id"] != attempt.ProjectID || metadata["branch"] != attempt.Branch || metadata["generation"] != attempt.Generation {
				continue
			}
			if current, ok := manifest.Chunks[record.ID]; ok && current.Collection == attempt.Collection {
				continue
			}
			ids = append(ids, record.ID)
		}
		if len(ids) > 0 {
			if errs := store.DeleteMany(attempt.Collection, ids); len(errs) > 0 {
				return fmt.Errorf("workspace attempt cleanup: %v", errs)
			}
		}
	}
	return removeWorkspaceGenerationAttempt(cfg, manifest.ProjectID, manifest.Branch)
}

// Candidate chunks stay ineligible until the one authoritative manifest save.
func publishWorkspaceMarkdownBatchLocked(ctx context.Context, cfg APIConfig, req workspaceReindexRequest, files []workspace.MarkdownFile, removed map[string]bool, verifyFiles bool, fullInventory bool) (workspaceReindexResponse, error) {
	manifest, manifestPath, err := loadWorkspaceManifest(cfg, req.ProjectID, defaultBranch(req.Branch))
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	collection := req.Collection
	if collection == "" {
		collection = workspace.DefaultCollectionName(req.ProjectID, defaultBranch(req.Branch), req.Generation)
	}
	if err := validateWorkspaceCollection(collection); err != nil {
		return workspaceReindexResponse{}, err
	}
	candidate := manifest.Clone()
	oldInventory, oldInventoryKnown := manifest.Files[req.Generation]
	oldInventoryKnown = oldInventoryKnown && oldInventory != nil
	_, oldGenerationExists := manifest.Generations[req.Generation]
	keepInventoryUnknown := oldGenerationExists && !oldInventoryKnown && !fullInventory
	indexer, err := newWorkspaceIndexer(cfg, candidate, collection)
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	indexer.Lexical = nil
	if err := recoverWorkspaceGenerationAttempt(cfg, manifest, indexer.Store); err != nil {
		return workspaceReindexResponse{}, err
	}
	attempt := workspaceGenerationAttempt{ID: uuid.NewString(), ProjectID: req.ProjectID, Branch: defaultBranch(req.Branch), Generation: req.Generation, Collection: collection}
	raw, err := json.Marshal(attempt)
	if err != nil {
		return workspaceReindexResponse{}, err
	}
	if err := writeWorkspaceFile(ctx, cfg, workspaceGenerationAttemptPath(cfg, req.ProjectID, req.Branch), raw); err != nil {
		return workspaceReindexResponse{}, err
	}
	// Failed compensation remains recorded for the next write/recovery attempt.
	published := false
	defer func() {
		if !published {
			if err := recoverWorkspaceGenerationAttempt(cfg, manifest, indexer.Store); err != nil {
				log.Printf("[workspace-index] attempt cleanup retained: %v", err)
			}
		}
	}()
	if candidate.Files[req.Generation] == nil {
		candidate.Files[req.Generation] = map[string]string{}
		for _, record := range candidate.ListChunks(workspace.ChunkFilter{Generation: req.Generation}) {
			candidate.Files[req.Generation][record.Path] = record.FileDigest
		}
	}
	for id, record := range candidate.Chunks {
		if record.Generation == req.Generation && removed[record.Path] {
			candidate.PendingRetirements[id] = record
			delete(candidate.Chunks, id)
		}
	}
	for path := range removed {
		delete(candidate.Files[req.Generation], path)
	}
	results := make([]workspace.IndexResult, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return workspaceReindexResponse{}, err
		}
		for id, record := range candidate.Chunks {
			if record.Generation == req.Generation && record.Path == file.Path {
				candidate.PendingRetirements[id] = record
				delete(candidate.Chunks, id)
			}
		}
		result, err := indexer.IndexMarkdown(ctx, file, workspace.IndexOptions{
			ProjectID: req.ProjectID, Branch: defaultBranch(req.Branch), Generation: req.Generation, Collection: collection,
			CommitHash: req.CommitHash, ChunkStrategy: req.ChunkStrategy, MinChunkChars: req.MinChunkChars,
			MaxChunkChars: req.MaxChunkChars, OverlapChars: req.OverlapChars, SnapToSentence: req.SnapToSentence,
			AttemptID: attempt.ID, DeferRetirement: true,
		})
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		candidate.Files[req.Generation][file.Path] = file.FileDigest
		results = append(results, result)
	}
	if verifyFiles {
		for _, file := range files {
			absolute, _, err := workspaceFilePath(cfg, req.ProjectID, req.Branch, file.Path)
			if err != nil {
				return workspaceReindexResponse{}, err
			}
			current, err := readWorkspaceFile(cfg, absolute)
			if err != nil {
				return workspaceReindexResponse{}, err
			}
			if digestBytes(current) != file.FileDigest {
				return workspaceReindexResponse{}, errors.New("workspace source changed during indexing")
			}
		}
	}
	for name := range removed {
		absolute, _, err := workspaceFilePath(cfg, req.ProjectID, req.Branch, name)
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		if _, err := readWorkspaceFile(cfg, absolute); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return workspaceReindexResponse{}, err
			}
			return workspaceReindexResponse{}, errors.New("workspace removed source reappeared during indexing")
		}
	}
	if fullInventory {
		current, err := listWorkspaceMarkdownPaths(cfg, workspaceProjectRoot(cfg, req.ProjectID, req.Branch))
		if err != nil {
			return workspaceReindexResponse{}, err
		}
		expected := map[string]bool{}
		for _, file := range files {
			expected[file.Path] = true
		}
		if len(current) != len(expected) {
			return workspaceReindexResponse{}, errors.New("workspace source inventory changed during indexing")
		}
		for _, name := range current {
			if !expected[name] {
				return workspaceReindexResponse{}, errors.New("workspace source inventory changed during indexing")
			}
		}
	}
	if keepInventoryUnknown {
		delete(candidate.Files, req.Generation)
	}
	if req.ActivateGeneration || manifest.ActiveGeneration == req.Generation {
		if err := candidate.ActivateGeneration(req.Generation); err != nil {
			return workspaceReindexResponse{}, err
		}
	} else if len(files) == 0 {
		if err := candidate.SetGeneration(req.Generation, workspace.GenerationBuilding, ""); err != nil {
			return workspaceReindexResponse{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return workspaceReindexResponse{}, err
	}
	if err := saveWorkspaceManifest(ctx, cfg, manifestPath, candidate); err != nil {
		return workspaceReindexResponse{}, err
	}
	published = true
	if fullInventory && candidate.ActiveGeneration == req.Generation {
		if err := supersedeWorkspaceIndexFailures(cfg, req.ProjectID, defaultBranch(req.Branch), req.Generation); err != nil {
			log.Printf("[workspace-index] terminal job supersession retained: %v", err)
		}
	}
	for _, result := range results {
		if lexical := workspaceLexicalIndex(cfg, result.Collection); lexical != nil {
			for _, chunk := range result.Chunks {
				metadata, err := json.Marshal(chunk.Metadata)
				if err == nil {
					lexical.Add(chunk.ID, chunk.Text, string(metadata))
				}
			}
		}
	}
	// Retirement failure cannot undo a published generation. Persisted IDs retry
	// on the next publication or explicit GC; search checks membership meanwhile.
	pending := make(map[string]workspace.ChunkRecord, len(candidate.PendingRetirements))
	for id, record := range candidate.PendingRetirements {
		pending[id] = record
	}
	cleanupErr := ctx.Err()
	if cleanupErr == nil {
		for _, record := range pending {
			if err := validateWorkspaceCollection(record.Collection); err != nil {
				cleanupErr = err
				break
			}
		}
	}
	if cleanupErr == nil {
		cleanupErr = workspace.RetirePendingChunks(candidate, indexer.Store)
	}
	if cleanupErr == nil {
		for id, record := range pending {
			if _, remains := candidate.PendingRetirements[id]; !remains {
				if lexical := workspaceLexicalIndex(cfg, record.Collection); lexical != nil {
					lexical.Remove(id)
				}
				for i, file := range files {
					if record.Generation == req.Generation && record.Path == file.Path {
						results[i].DeletedVectorIDs = append(results[i].DeletedVectorIDs, id)
					}
				}
			}
		}
		for i := range results {
			sort.Strings(results[i].DeletedVectorIDs)
		}
		if err := saveWorkspaceManifest(ctx, cfg, manifestPath, candidate); err != nil {
			log.Printf("[workspace-index] retirement state persistence retained: %v", err)
		}
	} else {
		log.Printf("[workspace-index] retirement cleanup retained: %v", cleanupErr)
	}

	if err := removeWorkspaceGenerationAttempt(cfg, req.ProjectID, req.Branch); err != nil {
		// Published membership protects current IDs during recovery of this sidecar.
		log.Printf("[workspace-index] published attempt cleanup retained: %v", err)
	}
	return workspaceReindexResponse{workspaceResponse: workspaceBaseResponse(candidate, manifestPath), Results: results}, nil
}

func workspaceMarkdownFile(cfg APIConfig, projectID, branch, name string) (workspace.MarkdownFile, error) {
	absolute, relative, err := workspaceFilePath(cfg, projectID, branch, name)
	if err != nil {
		return workspace.MarkdownFile{}, err
	}
	data, err := readWorkspaceFile(cfg, absolute)
	if err != nil {
		return workspace.MarkdownFile{}, err
	}
	if !utf8.Valid(data) {
		return workspace.MarkdownFile{}, errors.New("workspace text must be valid UTF-8")
	}
	return workspace.MarkdownFile{Path: relative, Text: string(data), FileDigest: digestBytes(data), Title: filepath.Base(relative)}, nil
}

func refreshWorkspaceLexical(cfg APIConfig, manifest *workspace.Manifest, collection string) error {
	lexical := workspaceLexicalIndex(cfg, collection)
	if lexical == nil {
		return nil
	}
	store, err := workspaceVectorStore(cfg)
	if err != nil {
		return err
	}
	// ponytail: scan this collection's lexical snapshot per workspace search;
	// cache by manifest digest only if measured search latency requires it.
	for _, document := range lexical.Documents() {
		var source searchDocumentSource
		if json.Unmarshal([]byte(document.Metadata), &source) != nil || source.ProjectID != manifest.ProjectID || source.Branch != manifest.Branch || source.ChunkID == "" {
			continue
		}
		current, ok := manifest.Chunks[document.ID]
		if !ok || current.Collection != collection {
			lexical.Remove(document.ID)
		}
	}
	for _, record := range manifest.ListChunks(workspace.ChunkFilter{Collection: collection}) {
		stored, found, err := store.Get(collection, record.VectorID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("workspace published vector missing")
		}
		var metadata map[string]any
		if err := json.Unmarshal(stored.Metadata, &metadata); err != nil {
			return err
		}
		source, err := decodeSearchDocumentSource(metadata)
		if err != nil {
			return err
		}
		if source.ProjectID != record.ProjectID || source.Branch != record.Branch || source.Generation != record.Generation || source.ChunkID != record.ChunkID || source.Path != record.Path || source.FileDigest != record.FileDigest || source.DocumentID != record.DocumentID {
			return errors.New("workspace published vector identity mismatch")
		}
		text, ok := metadata["text"].(string)
		if !ok {
			return errors.New("workspace published vector text missing")
		}
		lexical.Add(record.VectorID, text, string(stored.Metadata))
	}
	return nil
}
