package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
	"github.com/stek0v/levara/pkg/mcp"
)

type searchActorKey struct{}
type searchEvidenceKey struct{}
type searchReadPolicyKey struct{}
type workspaceSearchScopeKey struct{}

type searchEvidence struct {
	requiresAdmin     bool
	mu                sync.Mutex
	sources           map[searchDocumentSource]struct{}
	workspaceProjects map[string]struct{}
	workspaceScopes   map[searchDocumentSource]workspaceSearchTarget
	communities       map[mcp.CommunityEvidence]struct{}
}

func trackCommunityPublication(ctx context.Context, publication mcp.CommunityEvidence) {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		if e.communities == nil {
			e.communities = make(map[mcp.CommunityEvidence]struct{})
		}
		e.communities[publication] = struct{}{}
		e.requiresAdmin = true
		e.mu.Unlock()
	}
}
func searchCommunityPublications(ctx context.Context) []mcp.CommunityEvidence {
	e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence)
	if !ok {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]mcp.CommunityEvidence, 0, len(e.communities))
	for p := range e.communities {
		out = append(out, p)
	}
	return out
}
func communitySourceInvalid(err error) bool {
	return errors.Is(err, accesspkg.ErrDocumentInvalid) || errors.Is(err, accesspkg.ErrDocumentNotFound) || errors.Is(err, accesspkg.ErrDocumentVersionConflict)
}
func checkCommunityPublicationSources(ctx context.Context, cfg APIConfig, actor accesspkg.Actor, evidence mcp.CommunityEvidence) (bool, error) {
	if evidence.ID == "" || evidence.Generation == "" {
		return false, nil
	}
	sources, err := community.ParseSources(evidence.SourcesJSON)
	if err != nil {
		return false, nil
	}
	policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	if installed, ok := ctx.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); ok {
		policy = installed
	}
	if err := community.CheckSources(ctx, policy, sources); err != nil {
		if communitySourceInvalid(err) {
			return false, nil
		}
		return false, err
	}
	var tracked []searchDocumentSource
	for _, s := range sources {
		source := searchDocumentSource{DatasetID: s.DatasetID, DocumentID: s.DocumentID, ContentRevision: s.ContentRevision, Derived: s.Derived, Collection: s.Collection, Generation: s.Generation, SourceRevision: s.SourceRevision, RawContentHash: s.RawContentHash}
		allowed, err := searchDocumentAllowed(ctx, cfg, actor, source)
		if err != nil {
			if communitySourceInvalid(err) {
				return false, nil
			}
			return false, err
		}
		if !allowed {
			return false, nil
		}
		tracked = append(tracked, source)
	}
	for _, source := range tracked {
		trackSearchSource(ctx, source)
	}
	trackCommunityPublication(ctx, evidence)
	return true, nil
}

type communitySearchPublication struct {
	ID, Summary, Generation, SourcesJSON string
	MemberCount, Level                   int
}

// Community text is SQL-authoritative; vectors only nominate an ID/generation.
func loadCommunitySearchPublication(ctx context.Context, cfg APIConfig, actor accesspkg.Actor, id, vectorGeneration string) (communitySearchPublication, bool, error) {
	var publication communitySearchPublication
	if id == "" {
		return publication, false, nil
	}
	if cfg.DB == nil {
		if cfg.RequireAuth || actor.UserID != "" {
			return publication, false, fiber.NewError(503, "community access unavailable")
		}
		return publication, false, nil
	}
	if actor.UserID == "" {
		if cfg.RequireAuth {
			return publication, false, nil
		}
	} else {
		if actor.TenantID != "" || !accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionRead) {
			return publication, false, nil
		}
		policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
		active, activeErr := policy.IsActive(ctx, actor.UserID)
		admin, adminErr := policy.IsSuperuser(ctx, actor.UserID)
		if activeErr != nil || adminErr != nil {
			return publication, false, fiber.NewError(503, "community access unavailable")
		}
		if !active || !admin {
			return publication, false, nil
		}
	}
	var verified int
	err := cfg.DB.QueryRowContext(ctx, Q("SELECT id,summary,generation,sources_json,lineage_verified,member_count,level FROM graph_communities WHERE id=$1"), id).Scan(&publication.ID, &publication.Summary, &publication.Generation, &publication.SourcesJSON, &verified, &publication.MemberCount, &publication.Level)
	if errors.Is(err, sql.ErrNoRows) {
		return publication, false, nil
	}
	if err != nil {
		return publication, false, fiber.NewError(503, "community access unavailable")
	}
	if actor.UserID == "" && !cfg.RequireAuth {
		return publication, true, nil
	}
	if verified != 1 || publication.Generation == "" || vectorGeneration != "" && publication.Generation != vectorGeneration {
		return publication, false, nil
	}
	allowed, err := checkCommunityPublicationSources(ctx, cfg, actor, mcp.CommunityEvidence{ID: publication.ID, Generation: publication.Generation, SourcesJSON: publication.SourcesJSON})
	if err != nil {
		return publication, false, fiber.NewError(503, "community access unavailable")
	}
	return publication, allowed, nil
}

func communitySearchMetadata(ctx context.Context, cfg APIConfig, actor accesspkg.Actor, raw any, collection string) (map[string]any, bool, bool, error) {
	expectedCommunity := (actor.UserID != "" || cfg.RequireAuth) && (collection == "_community_summaries" || collection == "_community_summaries_child")
	var metadata map[string]any
	switch value := raw.(type) {
	case map[string]any:
		metadata = value
	case fiber.Map:
		metadata = map[string]any(value)
	case json.RawMessage:
		if json.Unmarshal(value, &metadata) != nil {
			return nil, expectedCommunity, false, nil
		}
	case []byte:
		if json.Unmarshal(value, &metadata) != nil {
			return nil, expectedCommunity, false, nil
		}
	case string:
		if json.Unmarshal([]byte(value), &metadata) != nil {
			return nil, expectedCommunity, false, nil
		}
	default:
		return nil, expectedCommunity, false, nil
	}
	rawID, present := metadata["community_id"]
	if !present {
		return nil, expectedCommunity, false, nil
	}
	id, _ := rawID.(string)
	generation, _ := metadata["generation"].(string)
	if actor.UserID != "" || cfg.RequireAuth {
		if generation == "" {
			return nil, true, false, nil
		}
	}
	publication, allowed, err := loadCommunitySearchPublication(ctx, cfg, actor, id, generation)
	if err != nil || !allowed {
		return nil, true, false, err
	}
	return map[string]any{"community_id": publication.ID, "generation": publication.Generation, "text": publication.Summary, "member_count": publication.MemberCount, "level": publication.Level}, true, true, nil
}

func trackWorkspaceSearchProject(ctx context.Context, projectID string) {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		if e.workspaceProjects == nil {
			e.workspaceProjects = make(map[string]struct{})
		}
		e.workspaceProjects[projectID] = struct{}{}
		e.mu.Unlock()
	}
}

func trackSearchSource(ctx context.Context, source searchDocumentSource) {
	if e, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); ok {
		e.mu.Lock()
		e.sources[source] = struct{}{}
		if scope, scoped := ctx.Value(workspaceSearchScopeKey{}).(workspaceSearchTarget); scoped {
			if e.workspaceScopes == nil {
				e.workspaceScopes = make(map[searchDocumentSource]workspaceSearchTarget)
			}
			e.workspaceScopes[source] = scope
		}
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

func workspaceSearchSource(source searchDocumentSource) bool {
	return source.ProjectID != "" && (source.ChunkID != "" || source.Branch != "" && source.Generation != "" && source.Path != "")
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
	if scope, ok := ctx.Value(workspaceSearchScopeKey{}).(workspaceSearchTarget); ok {
		if !workspaceSearchSource(source) || source.ProjectID != scope.Manifest.ProjectID || source.Branch != scope.Branch || source.Generation != scope.Generation || source.Collection != scope.Collection {
			return false, nil
		}
	}
	if cfg.RequireAuth && actor.UserID == "" {
		return false, nil
	}
	// Workspace vectors are derived even in trusted-local mode. Pending records
	// must never reach context merely because SQL authorization is disabled.
	if workspaceSearchSource(source) {
		if source.DatasetID != source.ProjectID || source.ChunkID == "" || source.Branch == "" || source.Path == "" || source.FileDigest == "" || source.DocumentID == "" || source.Generation == "" || source.VectorID == "" || source.Collection == "" {
			return false, nil
		}
		if actor.UserID != "" || cfg.RequireAuth {
			if cfg.DB == nil {
				return false, fiber.NewError(503, "document authorization unavailable")
			}
			policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
			if locked, ok := ctx.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); ok && locked.DB == cfg.DB {
				policy = locked
			}
			decision, err := policy.AuthorizeDataset(ctx, actor, source.ProjectID, accesspkg.ActionRead)
			if err != nil || !decision.Allowed {
				return false, err
			}
		}
		manifest, _, err := loadWorkspaceManifest(cfg, source.ProjectID, source.Branch)
		if err != nil {
			return false, err
		}
		generation := manifest.ActiveGeneration
		if scope, scoped := ctx.Value(workspaceSearchScopeKey{}).(workspaceSearchTarget); scoped {
			generation = scope.Generation
		}
		record, ok := manifest.Chunks[source.VectorID]
		return ok && record.Collection == source.Collection && record.ChunkID == source.ChunkID && record.ProjectID == source.ProjectID && record.Branch == source.Branch && record.DocumentID == source.DocumentID && record.Generation == source.Generation && record.Generation == generation && record.Path == source.Path && record.FileDigest == source.FileDigest, nil
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
	out := make([]fiber.Map, 0, len(results))
	actor := workspaceActorFromFiber(c)
	for _, result := range results {
		collection, _ := result["collection"].(string)
		metadata, isCommunity, admitted, communityErr := communitySearchMetadata(c.UserContext(), cfg, actor, result["metadata"], collection)
		if communityErr != nil {
			return nil, communityErr
		}
		if isCommunity {
			if admitted {
				result["metadata"] = metadata
				result["text"] = metadata["text"]
				out = append(out, result)
			}
			continue
		}

		source, err := decodeSearchDocumentSource(result["metadata"])
		source.VectorID, _ = result["id"].(string)
		if workspaceSearchSource(source) {
			source.Collection = collection
		}
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
	out := make([]pipeline.ScoredResult, 0, len(results))
	actor := workspaceActorFromFiber(c)
	for _, result := range results {
		metadata, isCommunity, admitted, communityErr := communitySearchMetadata(c.UserContext(), cfg, actor, result.Metadata, result.Collection)
		if communityErr != nil {
			return nil, communityErr
		}
		if isCommunity {
			if admitted {
				result.Metadata, _ = json.Marshal(metadata)
				out = append(out, result)
			}
			continue
		}

		source, err := decodeSearchDocumentSource(result.Metadata)
		source.VectorID = result.ID
		if workspaceSearchSource(source) {
			source.Collection = result.Collection
		}
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
		metadata, isCommunity, admitted, communityErr := communitySearchMetadata(ctx, cfg, actor, result.Metadata, result.Collection)
		if communityErr != nil {
			return nil, communityErr
		}
		if isCommunity {
			if admitted {
				result.Metadata, _ = json.Marshal(metadata)
				out = append(out, result)
			}
			continue
		}

		source, err := decodeSearchDocumentSource(result.Metadata)
		if err != nil {
			continue
		}
		source.Derived, source.VectorID = true, result.ID
		if workspaceSearchSource(source) {
			source.Collection = result.Collection
		}
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
