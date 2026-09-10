package http

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

type searchActorKey struct{}
type searchEvidenceKey struct{}
type searchReadPolicyKey struct{}

type searchEvidence struct {
	requiresAdmin bool
	mu            sync.Mutex
	sources       map[searchDocumentSource]struct{}
}

func trackSearchSource(ctx context.Context, source searchDocumentSource) {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		e.sources[source] = struct{}{}
		e.mu.Unlock()
	}
}

func searchSources(ctx context.Context) []searchDocumentSource {
	e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence)
	if !ok {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]searchDocumentSource, 0, len(e.sources))
	for source := range e.sources {
		out = append(out, source)
	}
	return out
}

func requireAdminSearchEvidence(ctx context.Context) {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		e.requiresAdmin = true
		e.mu.Unlock()
	}
}
func searchEvidenceRequiresAdmin(ctx context.Context) bool {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.requiresAdmin
	}
	return false
}

// Global graph features have no source-level provenance. Scoped requests use
// the document-aware graph path until those features can prove their sources.
func globalSearchGraphAllowed(ctx context.Context, cfg APIConfig) bool {
	actor, present := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	if !present || actor.UserID == "" {
		return !cfg.RequireAuth
	}
	p := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	active, err := p.IsActive(ctx, actor.UserID)
	if err != nil || !active {
		return false
	}
	admin, err := p.IsSuperuser(ctx, actor.UserID)
	return err == nil && admin && actor.TenantID == "" && accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionRead)
}

func graphSourcesAllowed(ctx context.Context, cfg APIConfig, sources []searchDocumentSource, allowedDatasets []string) bool {
	actor, present := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	for i := range sources {
		sources[i].Derived = true
		source := sources[i]
		if present || cfg.RequireAuth {
			allowed, err := searchDocumentAllowed(ctx, cfg, actor, source)
			if err != nil || !allowed {
				return false
			}
		} else if allowedDatasets != nil {
			found := false
			for _, id := range allowedDatasets {
				if id == source.DatasetID {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	for _, source := range sources {
		trackSearchSource(ctx, source)
	}
	return len(sources) > 0
}

type searchDocumentSource struct {
	DatasetMetadataOnly bool   `json:"dataset_metadata_only,omitempty"` // server evidence only, never trusted from indexed metadata
	DatasetID           string `json:"dataset_id"`
	Derived             bool   `json:"derived,omitempty"`
	Collection          string `json:"collection,omitempty"`
	ProjectID           string `json:"project_id"`
	DocumentID          string `json:"document_id"`
	ContentRevision     int64  `json:"content_revision"`
	SourceRevision      int64  `json:"source_revision,omitempty"`
	RawContentHash      string `json:"raw_content_hash,omitempty"`
	Generation          string `json:"generation"`
	Branch              string `json:"branch"`
	ChunkID             string `json:"chunk_id"`
	Path                string `json:"path"`
	FileDigest          string `json:"file_digest"`
	VectorID            string `json:"vector_id,omitempty"`
}

func decodeSearchDocumentSource(value any) (searchDocumentSource, error) {
	var raw []byte
	switch v := value.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(value)
		if err != nil {
			return searchDocumentSource{}, err
		}
	}
	var source searchDocumentSource
	if err := json.Unmarshal(raw, &source); err != nil {
		return source, err
	}
	// Indexed metadata can originate from a client. It cannot assert the
	// weaker proof used solely for server-generated dataset names.
	source.DatasetMetadataOnly = false
	if source.DatasetID == "" {
		source.DatasetID = source.ProjectID
	}
	return source, nil
}

// A registered document requires a current generation as well as a live grant.
// Dataset-only legacy derivatives remain readable through the live dataset ACL
// until the dataset contains registered documents.
func searchDocumentAllowed(ctx context.Context, cfg APIConfig, actor accesspkg.Actor, source searchDocumentSource) (bool, error) {
	remaining := 256
	return searchDocumentAllowedWithLineage(ctx, cfg, actor, source, make(map[searchDocumentSource]bool), &remaining)
}

func searchDocumentAllowedWithLineage(ctx context.Context, cfg APIConfig, actor accesspkg.Actor, source searchDocumentSource, path map[searchDocumentSource]bool, remaining *int) (bool, error) {
	if *remaining <= 0 || len(path) >= 16 || path[source] {
		return false, nil
	}
	*remaining -= 1
	path[source] = true
	defer delete(path, source)
	if cfg.RequireAuth && actor.UserID == "" {
		return false, nil
	}
	if cfg.DB == nil {
		if cfg.RequireAuth {
			return false, fiber.NewError(503, "document authorization unavailable")
		}
		return true, nil
	}
	policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	if locked, ok := ctx.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); ok && locked.DB == cfg.DB {
		policy = locked
	}
	if source.DatasetMetadataOnly {
		if source.DatasetID == "" || source.DocumentID != "" || source.ProjectID != "" || source.Derived || source.Generation != "" || source.Collection != "" {
			return false, accesspkg.ErrDocumentInvalid
		}
		decision, err := policy.AuthorizeDataset(ctx, actor, source.DatasetID, accesspkg.ActionRead)
		return decision.Allowed, err
	}
	if source.ProjectID != "" && source.ProjectID == source.DatasetID && source.Generation != "" && source.ChunkID != "" {
		decision, err := policy.AuthorizeDataset(ctx, actor, source.ProjectID, accesspkg.ActionRead)
		if err != nil || !decision.Allowed {
			return false, err
		}
		manifest, _, err := loadWorkspaceManifest(cfg, source.ProjectID, source.Branch)
		if err != nil {
			return false, err
		}
		record, ok := manifest.Chunks[source.VectorID]
		return ok && record.ChunkID == source.ChunkID && record.ProjectID == source.ProjectID && record.Branch == source.Branch && record.DocumentID == source.DocumentID && record.Generation == source.Generation && record.Generation == manifest.ActiveGeneration && record.Path == source.Path && record.FileDigest == source.FileDigest, nil
	}
	if source.DocumentID != "" && source.DatasetID != "" {
		ref := accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID}
		resource, err := policy.GetDocumentResource(ctx, ref)
		if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
			return false, err
		}
		if err == nil && (resource.Tombstoned || source.ContentRevision <= 0 || source.ContentRevision != resource.ContentRevision) {
			return false, nil
		}
		decision, err := policy.AuthorizeDocument(ctx, actor, ref, accesspkg.ActionRead)
		if err != nil || !decision.Allowed {
			return false, err
		}
		if source.SourceRevision != 0 || source.RawContentHash != "" {
			version, hash, err := policy.SourceVersion(ctx, ref)
			if err != nil || source.SourceRevision <= 0 || version != source.SourceRevision || hash != source.RawContentHash {
				return false, err
			}
		}
		if source.Derived {
			lineage, published, err := policy.DocumentIndexLineage(ctx, ref, source.ContentRevision, source.Collection, source.Generation)
			if err != nil || !published {
				return false, err
			}
			if lineage.RequiresAdmin {
				admin, err := policy.IsSuperuser(ctx, actor.UserID)
				if err != nil || !admin || actor.TenantID != "" {
					return false, err
				}
				requireAdminSearchEvidence(ctx)
			}
			var dependencies []searchDocumentSource
			if len(lineage.SourcesJSON) > 128<<10 || json.Unmarshal([]byte(lineage.SourcesJSON), &dependencies) != nil || dependencies == nil || len(dependencies) > 256 {
				return false, accesspkg.ErrDocumentInvalid
			}
			for _, dependency := range dependencies {
				if dependency.DatasetID == "" {
					return false, accesspkg.ErrDocumentInvalid
				}
				if dependency.DocumentID != "" && !dependency.Derived && dependency.SourceRevision <= 0 {
					_, err := policy.GetDocumentResource(ctx, accesspkg.DocumentRef{DatasetID: dependency.DatasetID, DataID: dependency.DocumentID})
					if errors.Is(err, accesspkg.ErrDocumentNotFound) {
						return false, nil
					}
					if err != nil {
						return false, err
					}
				}
				allowed, err := searchDocumentAllowedWithLineage(ctx, cfg, actor, dependency, path, remaining)
				if err != nil || !allowed {
					return false, err
				}
				trackSearchSource(ctx, dependency)
			}
		}
		return true, nil
	}
	if source.DatasetID != "" {
		registered, err := policy.HasRegisteredDocuments(ctx, source.DatasetID)
		if err != nil {
			return false, err
		}
		if registered {
			return false, nil
		}
		decision, err := policy.AuthorizeDataset(ctx, actor, source.DatasetID, accesspkg.ActionRead)
		return decision.Allowed, err
	}
	if actor.UserID == "" && !cfg.RequireAuth {
		return true, nil
	}
	active, err := policy.IsActive(ctx, actor.UserID)
	if err != nil || !active {
		return false, err
	}
	admin, err := policy.IsSuperuser(ctx, actor.UserID)
	allowed := err == nil && admin && actor.TenantID == "" && accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionRead)
	if allowed {
		requireAdminSearchEvidence(ctx)
	}
	return allowed, err
}

func filterSearchDocuments(c *fiber.Ctx, cfg APIConfig, results []fiber.Map) ([]fiber.Map, error) {
	if cfg.DB == nil && !cfg.RequireAuth {
		return results, nil
	}
	out := make([]fiber.Map, 0, len(results))
	actor := workspaceActorFromFiber(c)
	for _, result := range results {
		source, err := decodeSearchDocumentSource(result["metadata"])
		source.VectorID, _ = result["id"].(string)
		source.Derived = true
		if err != nil {
			continue
		}
		allowed, err := searchDocumentAllowed(c.UserContext(), cfg, actor, source)
		if err != nil {
			return nil, fiber.NewError(503, "document access check failed")
		}
		if allowed {
			trackSearchSource(c.UserContext(), source)
			out = append(out, result)
		}
	}
	return out, nil
}

func filterScoredSearchDocuments(c *fiber.Ctx, cfg APIConfig, results []pipeline.ScoredResult) ([]pipeline.ScoredResult, error) {
	if cfg.DB == nil && !cfg.RequireAuth {
		return results, nil
	}
	out := make([]pipeline.ScoredResult, 0, len(results))
	actor := workspaceActorFromFiber(c)
	for _, result := range results {
		source, err := decodeSearchDocumentSource(result.Metadata)
		source.VectorID = result.ID
		source.Derived = true
		if err != nil {
			continue
		}
		allowed, err := searchDocumentAllowed(c.UserContext(), cfg, actor, source)
		if err != nil {
			return nil, fiber.NewError(503, "document access check failed")
		}
		if allowed {
			trackSearchSource(c.UserContext(), source)
			out = append(out, result)
		}
	}
	return out, nil
}

func scopedQueryExpansion(ctx context.Context, cfg APIConfig, query string) string {
	if !globalSearchGraphAllowed(ctx, cfg) {
		return ""
	}
	return expandQueryFromGraph(ctx, cfg.DB, query)
}

func filterMCPDocumentResults(ctx context.Context, cfg APIConfig, results []pipeline.ScoredResult) ([]pipeline.ScoredResult, error) {
	actor, _ := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	out := make([]pipeline.ScoredResult, 0, len(results))
	for _, result := range results {
		source, err := decodeSearchDocumentSource(result.Metadata)
		if err != nil {
			continue
		}
		source.Derived, source.VectorID = true, result.ID
		allowed, err := searchDocumentAllowed(ctx, cfg, actor, source)
		if err != nil {
			return nil, err
		}
		if allowed {
			trackSearchSource(ctx, source)
			out = append(out, result)
		}
	}
	return out, nil
}
