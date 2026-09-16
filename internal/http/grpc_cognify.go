package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/runreg"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcDocumentScopeKey struct{}

func grpcCognifyContext(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor) context.Context {
	c := actor.Credential
	ctx = context.WithValue(ctx, grpcDocumentScopeKey{}, true)
	ctx = context.WithValue(ctx, searchActorKey{}, actor.Actor)
	ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
	return context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor.Actor,
		kind: c.Kind, keyID: c.KeyID, sessionID: c.SessionID, epoch: c.Epoch, issuedAt: c.IssuedAt, expiresAt: c.ExpiresAt})
}

// StartGRPCDocumentCognify is the trusted bridge from verified gRPC identity to
// the same detached runner and per-document publication workflow as HTTP.
func StartGRPCDocumentCognify(parent context.Context, cfg APIConfig, actor accesspkg.MetadataActor, req *pb.DocumentCognifyReq) (string, error) {
	if cfg.DB == nil || cfg.Runs == nil || cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return "", status.Error(codes.Unavailable, "document processing unavailable")
	}
	if req == nil || len(req.Documents) == 0 {
		return "", status.Error(codes.InvalidArgument, "documents required")
	}
	pipeCfg := baseCognifyConfig(cfg)
	if req.Mode == "graph" && pipeCfg.LLMEndpoint == "" && pipeCfg.LLMProvider == nil {
		return "", status.Error(codes.Unavailable, "graph processing unavailable")
	}
	ctx, cancel := context.WithTimeout(grpcCognifyContext(parent, cfg, actor), 30*time.Second)
	defer cancel()
	type locatedSource struct {
		source   cognifySource
		location string
	}
	pending := make([]locatedSource, len(req.Documents))
	for i, doc := range req.Documents {
		pending[i].source = cognifySource{datasetID: doc.DatasetId, documentID: doc.DocumentId}
		err := cfg.DB.QueryRowContext(ctx, Q(`SELECT d.name,d.raw_data_location FROM data d JOIN dataset_data dd ON dd.data_id=d.id
			WHERE dd.dataset_id=$1 AND dd.data_id=$2`), doc.DatasetId, doc.DocumentId).Scan(&pending[i].source.title, &pending[i].location)
		if err != nil {
			return "", grpcCognifyError(err)
		}
	}
	sources, texts, err := func() ([]cognifySource, []string, error) {
		fenced, release, err := beginSearchReadFence(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer release()
		p := documentSQLPolicy(cfg)
		if locked, ok := fenced.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy); ok {
			p = locked
		}
		// Check the complete batch before the first raw-byte transfer.
		for i := range pending {
			source, doc := &pending[i].source, req.Documents[i]
			ref := accesspkg.DocumentRef{DatasetID: source.datasetID, DataID: source.documentID}
			decision, err := p.AuthorizeDocument(fenced, actor.Actor, ref, accesspkg.ActionWrite)
			if err != nil {
				return nil, nil, err
			}
			if !decision.Allowed {
				return nil, nil, accesspkg.ErrDocumentForbidden
			}
			resource, err := p.GetDocumentResource(fenced, ref)
			if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
				return nil, nil, err
			}
			source.contentRevision = resource.ContentRevision
			source.sourceRevision, source.rawContentHash, err = p.SourceVersion(fenced, ref)
			if err != nil {
				return nil, nil, err
			}
			if source.sourceRevision != doc.SourceRevision || source.rawContentHash != doc.RawContentHash || (doc.ContentRevision > 0 && doc.ContentRevision != source.contentRevision) {
				return nil, nil, accesspkg.ErrDocumentVersionConflict
			}
		}
		sources, texts := make([]cognifySource, 0, len(pending)), make([]string, 0, len(pending))
		for _, item := range pending {
			raw, err := loadRawDataByLocation(fenced, cfg, item.location)
			if err != nil {
				return nil, nil, err
			}
			if fmt.Sprintf("%x", sha256.Sum256(raw)) != item.source.rawContentHash {
				return nil, nil, accesspkg.ErrDocumentVersionConflict
			}
			item.source.texts = []string{string(raw)}
			sources, texts = append(sources, item.source), append(texts, string(raw))
		}
		return sources, texts, nil
	}()
	if err != nil {
		return "", grpcCognifyError(err)
	}
	runID := uuid.NewString()
	if err := claimGRPCCognifySources(ctx, cfg, actor, sources, req.Collection, runID); err != nil {
		return "", grpcCognifyError(err)
	}
	proofs := make([]searchDocumentSource, len(sources))
	for i, source := range sources {
		proofs[i] = source.proof()
	}
	rawProofs, _ := json.Marshal(proofs)
	runStatus := &runreg.Status{OwnerID: actor.UserID, TenantID: actor.TenantID, SourcesJSON: string(rawProofs), RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now()}
	pipeCfg.Collection, pipeCfg.AttemptID = req.Collection, runID
	pipeCfg.SkipGraph, pipeCfg.GenerateTriplets = req.Mode != "graph", req.Mode == "graph"
	startCognifyRun(ctx, cfg, runStatus, pipeCfg, sources, texts, "", actor.UserID)
	return runID, nil
}

func claimGRPCCognifySources(ctx context.Context, cfg APIConfig, actor accesspkg.MetadataActor, sources []cognifySource, collection, runID string) error {
	tx, locked, err := documentSQLPolicy(cfg).BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	claims := make([]pipelineAttemptSource, len(sources))
	for i, source := range sources {
		ref := accesspkg.DocumentRef{DatasetID: source.datasetID, DataID: source.documentID}
		decision, err := locked.AuthorizeDocument(ctx, actor.Actor, ref, accesspkg.ActionWrite)
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return accesspkg.ErrDocumentForbidden
		}
		resource, err := locked.GetDocumentResource(ctx, ref)
		if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
			return err
		}
		revision, hash, err := locked.SourceVersion(ctx, ref)
		if err != nil {
			return err
		}
		if resource.ContentRevision != source.contentRevision || revision != source.sourceRevision || hash != source.rawContentHash {
			return accesspkg.ErrDocumentVersionConflict
		}
		claims[i] = pipelineAttemptSource{datasetID: source.datasetID, dataID: source.documentID, sourceRevision: revision, rawContentHash: hash}
	}
	if err := claimPipelineAttemptsTx(ctx, tx, claims, collection, runID); err != nil {
		return err
	}
	if !actor.TrustedLocal {
		c := actor.Credential
		if err := locked.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WatchGRPCDocumentCognify sends immutable snapshots. Each Send holds the live
// credential/source fence; disconnect cancels this observer, not accepted jobs.
func WatchGRPCDocumentCognify(parent context.Context, cfg APIConfig, actor accesspkg.MetadataActor, runID string, send func(*pb.DocumentCognifyStatus) error) error {
	if cfg.DB == nil || cfg.Runs == nil {
		return status.Error(codes.Unavailable, "document processing unavailable")
	}
	ctx := grpcCognifyContext(parent, cfg, actor)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return grpcCognifyError(err)
		}
		val, ok := cfg.Runs.Load(runID)
		if !ok || val.SourcesJSON == "" || authorizeRunStatus(ctx, cfg, val) != nil {
			return status.Error(codes.NotFound, "run not found")
		}
		var sources []searchDocumentSource
		if json.Unmarshal([]byte(val.SourcesJSON), &sources) != nil || len(sources) == 0 {
			return status.Error(codes.NotFound, "run not found")
		}
		for _, source := range sources {
			if source.DatasetID == "" || source.DocumentID == "" || source.SourceRevision <= 0 || len(source.RawContentHash) != 64 {
				return status.Error(codes.NotFound, "run not found")
			}
		}
		_, release, err := beginSearchTransferFence(ctx)
		if err != nil {
			return status.Error(codes.NotFound, "run not found")
		}
		frame := &pb.DocumentCognifyStatus{PipelineRunId: val.RunID, Status: val.Status, Stage: val.Stage, ChunksCreated: int32(val.Chunks), EntitiesExtracted: int32(val.Entities), EdgesExtracted: int32(val.Edges), ElapsedMs: val.ElapsedMs, StartedAtUnixMs: val.StartedAt.UnixMilli()}
		if val.Status == "FAILED" {
			frame.Message = "document processing failed"
		}
		for _, source := range sources {
			frame.Sources = append(frame.Sources, &pb.DocumentCognifySource{DatasetId: source.DatasetID, DocumentId: source.DocumentID, SourceRevision: source.SourceRevision, RawContentHash: source.RawContentHash, ContentRevision: source.ContentRevision})
		}
		err = send(frame)
		release()
		if err != nil {
			return grpcCognifyError(err)
		}
		if val.Status != "RUNNING" {
			return nil
		}
		select {
		case <-ctx.Done():
			return grpcCognifyError(ctx.Err())
		case <-ticker.C:
		}
	}
}

func grpcCognifyError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	case errors.Is(err, accesspkg.ErrRevokedCredential):
		return status.Error(codes.Unauthenticated, "credential revoked")
	case errors.Is(err, accesspkg.ErrDocumentForbidden):
		return status.Error(codes.PermissionDenied, "document processing denied")
	case errors.Is(err, accesspkg.ErrDocumentInvalid):
		return status.Error(codes.InvalidArgument, "invalid document request")
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, accesspkg.ErrDocumentNotFound):
		return status.Error(codes.NotFound, "document not found")
	case errors.Is(err, accesspkg.ErrDocumentVersionConflict):
		return status.Error(codes.FailedPrecondition, "document source changed")
	}
	var httpErr *fiber.Error
	if errors.As(err, &httpErr) {
		switch httpErr.Code {
		case 401:
			return status.Error(codes.Unauthenticated, "credential revoked")
		case 403:
			return status.Error(codes.PermissionDenied, "document processing denied")
		case 409:
			return status.Error(codes.FailedPrecondition, "document source changed")
		}
	}
	return status.Error(codes.Unavailable, "document processing unavailable")
}
