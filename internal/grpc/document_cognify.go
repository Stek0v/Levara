package grpc

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stek0v/levara/pkg/access"
	pb "github.com/stek0v/levara/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetDocumentCognify binds the shared document runner before serving traffic.
// The adapter must recheck live credentials and source permissions at startup
// and at every progress transfer. Canceling an observer never cancels its job.
func (s *Service) SetDocumentCognify(
	start func(context.Context, access.MetadataActor, *pb.DocumentCognifyReq) (string, error),
	watch func(context.Context, access.MetadataActor, string, func(*pb.DocumentCognifyStatus) error) error,
) {
	s.documentCognifyStart, s.documentCognifyWatch = start, watch
}

func (s *Service) CognifyDocuments(req *pb.DocumentCognifyReq, stream pb.LevaraService_CognifyDocumentsServer) error {
	validated, err := validateDocumentCognify(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(stream.Context(), 30*time.Minute)
	defer cancel()
	actor, err := s.documentCognifyActor(ctx)
	if err != nil {
		return err
	}
	if s.documentCognifyStart == nil || s.documentCognifyWatch == nil {
		return status.Error(codes.Unavailable, "document processing unavailable")
	}
	if err := ctx.Err(); err != nil {
		return documentCognifyError(err)
	}
	runID, err := s.documentCognifyStart(ctx, actor, validated)
	if err != nil {
		return documentCognifyError(err)
	}
	if !validDocumentCognifyID(runID) {
		return status.Error(codes.Internal, "document processing returned invalid run")
	}
	return s.watchDocumentCognify(ctx, actor, runID, stream.Send)
}

func (s *Service) CognifyDocumentsStatus(req *pb.DocumentCognifyStatusReq, stream pb.LevaraService_CognifyDocumentsStatusServer) error {
	if req == nil || !validDocumentCognifyID(req.PipelineRunId) {
		return status.Error(codes.InvalidArgument, "valid pipeline_run_id required")
	}
	ctx, cancel := context.WithTimeout(stream.Context(), 30*time.Minute)
	defer cancel()
	actor, err := s.documentCognifyActor(ctx)
	if err != nil {
		return err
	}
	if s.documentCognifyWatch == nil {
		return status.Error(codes.Unavailable, "document processing unavailable")
	}
	if err := ctx.Err(); err != nil {
		return documentCognifyError(err)
	}
	return s.watchDocumentCognify(ctx, actor, req.PipelineRunId, stream.Send)
}

// Returning on the observer deadline lets gRPC close the underlying transport
// and unblock Send: a context wrapper alone does not cancel transport flow
// control. The adapter sends sequentially, respects cancellation between frames,
// and holds its SQL fence until any in-flight Send has actually returned.
func (s *Service) watchDocumentCognify(ctx context.Context, actor access.MetadataActor, runID string, send func(*pb.DocumentCognifyStatus) error) error {
	done := make(chan error, 1)
	go func() {
		done <- s.documentCognifyWatch(ctx, actor, runID, func(frame *pb.DocumentCognifyStatus) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return send(frame)
		})
	}()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return status.FromContextError(ctx.Err()).Err()
		}
		return documentCognifyError(err)
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}
}

func (s *Service) documentCognifyActor(ctx context.Context) (access.MetadataActor, error) {
	if s.ingestDB == nil {
		return access.MetadataActor{}, status.Error(codes.Unavailable, "metadata storage unavailable")
	}
	actor, verified := ctx.Value(ctxMetadataActorKey{}).(access.MetadataActor)
	if s.ingestRequireAuth {
		if !verified || actor.TrustedLocal || actor.UserID == "" || actor.Credential.Kind != "jwt" {
			return access.MetadataActor{}, status.Error(codes.Unauthenticated, "verified identity required")
		}
	} else if !verified {
		actor = access.MetadataActor{Actor: access.Actor{UserID: UserIDFromContext(ctx)}, TrustedLocal: true}
	}
	return actor, nil
}

func validateDocumentCognify(req *pb.DocumentCognifyReq) (*pb.DocumentCognifyReq, error) {
	if req == nil || len(req.Documents) < 1 || len(req.Documents) > 100 {
		return nil, status.Error(codes.InvalidArgument, "1 to 100 documents required")
	}
	if !validDocumentCognifyID(req.Collection) {
		return nil, status.Error(codes.InvalidArgument, "valid collection required")
	}
	mode := req.Mode
	if mode == "" {
		mode = "rag"
	}
	if mode != "rag" && mode != "graph" {
		return nil, status.Error(codes.InvalidArgument, "mode must be rag or graph")
	}
	// Copy only the public fields we understand. No caller-owned pointers or
	// unknown proto fields reach the runner or supply model/endpoint settings.
	validated := &pb.DocumentCognifyReq{Collection: req.Collection, Mode: mode}
	type sourceKey struct{ dataset, document string }
	seen := make(map[sourceKey]*pb.DocumentCognifySource, len(req.Documents))
	for _, doc := range req.Documents {
		if doc == nil || !validDocumentCognifyID(doc.DatasetId) || !validDocumentCognifyID(doc.DocumentId) || doc.SourceRevision <= 0 || doc.ContentRevision < 0 || len(doc.RawContentHash) != 64 {
			return nil, status.Error(codes.InvalidArgument, "valid document reference and source proof required")
		}
		if _, err := hex.DecodeString(doc.RawContentHash); err != nil {
			return nil, status.Error(codes.InvalidArgument, "raw_content_hash must be SHA256 hex")
		}
		copy := &pb.DocumentCognifySource{DatasetId: doc.DatasetId, DocumentId: doc.DocumentId, SourceRevision: doc.SourceRevision, RawContentHash: strings.ToLower(doc.RawContentHash), ContentRevision: doc.ContentRevision}
		key := sourceKey{copy.DatasetId, copy.DocumentId}
		if previous, exists := seen[key]; exists {
			if previous.SourceRevision != copy.SourceRevision || previous.RawContentHash != copy.RawContentHash || previous.ContentRevision != copy.ContentRevision {
				return nil, status.Error(codes.InvalidArgument, "conflicting document source proofs")
			}
			continue
		}
		seen[key] = copy
		validated.Documents = append(validated.Documents, copy)
	}
	return validated, nil
}

func validDocumentCognifyID(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "." && value != ".." && !strings.ContainsAny(value, "/\\") && strings.IndexFunc(value, unicode.IsControl) < 0
}

func documentCognifyError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	// The trusted HTTP adapter maps policy errors to safe gRPC status messages.
	// Unexpected infrastructure errors must never disclose internal addresses,
	// paths, queries or provider responses.
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unavailable, "document processing unavailable")
}
