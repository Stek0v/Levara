package workspace

import (
	"errors"
	"fmt"
	"sort"

	"github.com/stek0v/levara/pkg/vectorstore"
)

var ErrCannotGCActiveGeneration = errors.New("cannot GC active generation")

type GCResult struct {
	DryRun               bool     `json:"dry_run,omitempty"`
	Generations          []string `json:"generations"`
	DroppedCollections   []string `json:"dropped_collections"`
	DeletedVectorIDs     []string `json:"deleted_vector_ids"`
	ExclusiveCollections []string `json:"exclusive_collections,omitempty"`
	SharedCollections    []string `json:"shared_collections,omitempty"`
}

// PlanGCGenerations reports exact recorded retirements without changing state.
// One manifest cannot prove that a collection contains no other projects.
func PlanGCGenerations(manifest *Manifest) (GCResult, error) {
	if manifest == nil {
		return GCResult{}, errors.New("manifest required")
	}
	candidate := manifest.Clone()
	candidate.ensureMaps()
	targets := gcPendingGenerationIDs(candidate)
	if err := checkGCTargets(candidate, targets); err != nil {
		return GCResult{}, err
	}
	queueGCChunks(candidate, targets)
	collections, err := pendingRetirementCollections(candidate)
	if err != nil {
		return GCResult{}, err
	}
	result := GCResult{DryRun: true, Generations: targets}
	for _, collection := range sortedKeys(collections) {
		result.SharedCollections = append(result.SharedCollections, collection)
		result.DeletedVectorIDs = append(result.DeletedVectorIDs, collections[collection]...)
	}
	sort.Strings(result.DeletedVectorIDs)
	return result, nil
}

// RetirePendingChunks only cleans already queued exact IDs. Current manifest
// membership protects an ID even if an obsolete retirement records it too.
// Failed batches remain queued: DeleteMany may have partially succeeded.
func RetirePendingChunks(manifest *Manifest, store vectorstore.VectorStore) error {
	if manifest == nil {
		return errors.New("manifest required")
	}
	if store == nil {
		return errors.New("vector store required")
	}
	manifest.ensureMaps()
	collections, err := pendingRetirementCollections(manifest)
	if err != nil {
		return err
	}
	for _, collection := range sortedKeys(collections) {
		ids := collections[collection]
		if errs := store.DeleteMany(collection, ids); len(errs) > 0 {
			return fmt.Errorf("delete vectors from %s: %v", collection, errs)
		}
		for _, id := range ids {
			delete(manifest.PendingRetirements, id)
		}
	}
	return nil
}

// GCGenerations explicitly retires gc_pending generations. Publication callers
// use RetirePendingChunks instead, retaining older generations until explicit GC.
func GCGenerations(manifest *Manifest, store vectorstore.VectorStore) (GCResult, error) {
	if manifest == nil {
		return GCResult{}, errors.New("manifest required")
	}
	if store == nil {
		return GCResult{}, errors.New("vector store required")
	}
	plan, err := PlanGCGenerations(manifest)
	if err != nil {
		return plan, err
	}
	manifest.ensureMaps()
	removed := queueGCChunks(manifest, plan.Generations)
	before := make(map[string]struct{}, len(manifest.PendingRetirements))
	for id := range manifest.PendingRetirements {
		before[id] = struct{}{}
	}
	err = RetirePendingChunks(manifest, store)
	result := GCResult{SharedCollections: plan.SharedCollections}
	for id := range before {
		if _, pending := manifest.PendingRetirements[id]; !pending {
			result.DeletedVectorIDs = append(result.DeletedVectorIDs, id)
		}
	}
	// Preserve unresolved generation records for a later retry. Successfully
	// deleted chunk records stay removed even if another collection failed.
	for _, record := range removed {
		if _, pending := manifest.PendingRetirements[record.VectorID]; pending {
			manifest.Chunks[record.VectorID] = record
		}
	}
	for _, generation := range plan.Generations {
		unresolved := false
		for _, record := range manifest.PendingRetirements {
			if record.Generation == generation {
				unresolved = true
				break
			}
		}
		if !unresolved {
			delete(manifest.Generations, generation)
			delete(manifest.Files, generation)
			result.Generations = append(result.Generations, generation)
		}
	}
	sort.Strings(result.DeletedVectorIDs)
	sort.Strings(result.Generations)
	return result, err
}

func checkGCTargets(manifest *Manifest, targets []string) error {
	for _, id := range targets {
		if id == manifest.ActiveGeneration {
			return fmt.Errorf("%w: %s", ErrCannotGCActiveGeneration, id)
		}
	}
	return nil
}

func queueGCChunks(manifest *Manifest, targets []string) []ChunkRecord {
	targetSet := make(map[string]bool, len(targets))
	for _, id := range targets {
		targetSet[id] = true
	}
	var removed []ChunkRecord
	for id, record := range manifest.Chunks {
		if !targetSet[record.Generation] {
			continue
		}
		manifest.PendingRetirements[id] = record
		removed = append(removed, record)
		delete(manifest.Chunks, id)
	}
	return removed
}

func pendingRetirementCollections(manifest *Manifest) (map[string][]string, error) {
	collections := make(map[string][]string)
	for id, record := range manifest.PendingRetirements {
		if _, current := manifest.Chunks[id]; current {
			continue
		}
		if id == "" || record.VectorID != id || record.Collection == "" {
			return nil, fmt.Errorf("retirement %q lacks exact vector identity or collection", id)
		}
		collections[record.Collection] = append(collections[record.Collection], id)
	}
	for collection := range collections {
		sort.Strings(collections[collection])
	}
	return collections, nil
}

func gcPendingGenerationIDs(manifest *Manifest) []string {
	var out []string
	for id, generation := range manifest.Generations {
		if generation.Status == GenerationGCPending {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
