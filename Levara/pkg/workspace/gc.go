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

// PlanGCGenerations reports the same generations, collections, and vector IDs
// GCGenerations would remove, without mutating the manifest or vector store.
func PlanGCGenerations(manifest *Manifest) (GCResult, error) {
	if manifest == nil {
		return GCResult{}, errors.New("manifest required")
	}
	manifest.ensureMaps()
	targets := gcPendingGenerationIDs(manifest)
	targetSet := make(map[string]struct{}, len(targets))
	for _, id := range targets {
		if id == manifest.ActiveGeneration {
			return GCResult{}, fmt.Errorf("%w: %s", ErrCannotGCActiveGeneration, id)
		}
		targetSet[id] = struct{}{}
	}
	result := GCResult{DryRun: true, Generations: append([]string(nil), targets...)}
	collections := targetCollections(manifest, targetSet)
	for _, coll := range sortedKeys(collections) {
		ids := collections[coll]
		if collectionUsedOutside(manifest, coll, targetSet) {
			result.SharedCollections = append(result.SharedCollections, coll)
			result.DeletedVectorIDs = append(result.DeletedVectorIDs, ids...)
			continue
		}
		result.ExclusiveCollections = append(result.ExclusiveCollections, coll)
		result.DroppedCollections = append(result.DroppedCollections, coll)
	}
	for _, gen := range targets {
		for _, rec := range manifest.ListChunks(ChunkFilter{Generation: gen}) {
			if rec.Collection == "" {
				result.DeletedVectorIDs = append(result.DeletedVectorIDs, rec.VectorID)
			}
		}
	}
	sort.Strings(result.DeletedVectorIDs)
	sort.Strings(result.DroppedCollections)
	sort.Strings(result.ExclusiveCollections)
	sort.Strings(result.SharedCollections)
	sort.Strings(result.Generations)
	return result, nil
}

// GCGenerations removes generations marked gc_pending from the manifest and
// vector store. Collections used only by those generations are dropped whole;
// shared collections are cleaned by exact vector IDs.
func GCGenerations(manifest *Manifest, store vectorstore.VectorStore) (GCResult, error) {
	if manifest == nil {
		return GCResult{}, errors.New("manifest required")
	}
	if store == nil {
		return GCResult{}, errors.New("vector store required")
	}
	manifest.ensureMaps()

	targets := gcPendingGenerationIDs(manifest)
	targetSet := make(map[string]struct{}, len(targets))
	for _, id := range targets {
		if id == manifest.ActiveGeneration {
			return GCResult{}, fmt.Errorf("%w: %s", ErrCannotGCActiveGeneration, id)
		}
		targetSet[id] = struct{}{}
	}

	plan, err := PlanGCGenerations(manifest)
	if err != nil {
		return plan, err
	}
	result := GCResult{
		ExclusiveCollections: append([]string(nil), plan.ExclusiveCollections...),
		SharedCollections:    append([]string(nil), plan.SharedCollections...),
	}
	collections := targetCollections(manifest, targetSet)
	for _, coll := range sortedKeys(collections) {
		ids := collections[coll]
		if collectionUsedOutside(manifest, coll, targetSet) {
			if errs := store.DeleteMany(coll, ids); len(errs) > 0 {
				return result, fmt.Errorf("delete vectors from %s: %v", coll, errs)
			}
			result.DeletedVectorIDs = append(result.DeletedVectorIDs, ids...)
			continue
		}
		if store.Has(coll) {
			if err := store.Drop(coll); err != nil {
				return result, fmt.Errorf("drop collection %s: %w", coll, err)
			}
		}
		result.DroppedCollections = append(result.DroppedCollections, coll)
	}

	for _, gen := range targets {
		deleted := manifest.DeleteChunks(ChunkFilter{Generation: gen})
		for _, rec := range deleted {
			if rec.Collection == "" {
				result.DeletedVectorIDs = append(result.DeletedVectorIDs, rec.VectorID)
			}
		}
		delete(manifest.Generations, gen)
		result.Generations = append(result.Generations, gen)
	}
	sort.Strings(result.DeletedVectorIDs)
	sort.Strings(result.DroppedCollections)
	sort.Strings(result.Generations)
	return result, nil
}

func gcPendingGenerationIDs(manifest *Manifest) []string {
	var out []string
	for id, gen := range manifest.Generations {
		if gen.Status == GenerationGCPending {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func targetCollections(manifest *Manifest, targetSet map[string]struct{}) map[string][]string {
	out := make(map[string][]string)
	for _, rec := range manifest.Chunks {
		if _, ok := targetSet[rec.Generation]; !ok {
			continue
		}
		if rec.Collection == "" {
			continue
		}
		out[rec.Collection] = append(out[rec.Collection], rec.VectorID)
	}
	for coll := range out {
		sort.Strings(out[coll])
	}
	return out
}

func collectionUsedOutside(manifest *Manifest, collection string, targetSet map[string]struct{}) bool {
	for _, rec := range manifest.Chunks {
		if rec.Collection != collection {
			continue
		}
		if _, target := targetSet[rec.Generation]; !target {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
