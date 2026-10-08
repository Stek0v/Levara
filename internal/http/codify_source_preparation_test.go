package http

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/ingest"
)

func TestCodifyNativeSourcePreparation(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx := protectedResponseTestContext(parent, h.cfg, time.Now().Add(time.Hour).Unix())
		requireAdminSearchEvidence(ctx)
		cfg := h.BaseCognifyConfig()
		cfg.DatasetID, cfg.Collection, cfg.DocumentTitle = uuid.NewString(), "code_knowledge", "main.go"
		prepared, err := h.PrepareCognify(ctx, []string{"package main\nfunc main() {}\n"}, cfg)
		if err != nil {
			t.Fatalf("native code source preparation: %v", err)
		}
		if prepared.DatasetID == "" || prepared.DocumentID == "" || prepared.SourceRevision <= 0 || len(prepared.RawContentHash) != 64 {
			t.Fatalf("missing immutable source binding: dataset=%q document=%q revision=%d hash=%q", prepared.DatasetID, prepared.DocumentID, prepared.SourceRevision, prepared.RawContentHash)
		}
	})
}
