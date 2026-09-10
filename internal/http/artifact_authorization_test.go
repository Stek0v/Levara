package http

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestArtifactVerificationRequiresLiveSourceAccess(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg := f.cfg
		cfg.RequireAuth = true
		cfg.WorkspacePath = t.TempDir()
		h := &mcpHandler{cfg: cfg}
		contextFor := func(user string) context.Context {
			actor := accesspkg.Actor{UserID: user, TenantID: "a"}
			return context.WithValue(context.Background(), searchEgressKey{}, searchEgress{cfg: cfg, actor: actor, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		}
		var uri string
		if err := f.db.QueryRow(`SELECT raw_data_location FROM data WHERE id='blob'`).Scan(&uri); err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("blob bytes")))
		if err := h.VerifyArtifact(contextFor("viewer"), uri, digest); err == nil {
			t.Error("restricted source verified without grant")
		}
		if err := h.VerifyArtifact(contextFor("owner"), uri, digest); err != nil {
			t.Errorf("owner source: %v", err)
		}
		f.exec(`UPDATE users SET is_active=false WHERE id='owner'`)
		if err := h.VerifyArtifact(contextFor("owner"), uri, digest); err == nil {
			t.Error("revoked user verified source")
		}
		f.exec(`UPDATE users SET is_active=true WHERE id='owner'`)
		path := filepath.Join(cfg.WorkspacePath, "projects", "alpha", "main", "proof.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("workspace evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		digest = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("workspace evidence")))
		if err := h.VerifyArtifact(contextFor("owner"), "file://"+path, digest); err != nil {
			t.Errorf("workspace owner: %v", err)
		}
		if err := h.VerifyArtifact(contextFor("foreign"), "file://"+path, digest); err == nil {
			t.Error("foreign workspace source verified")
		}
		f.exec(`INSERT INTO datasets(id,name,owner_id) VALUES('alpha!','Collision','foreign')`)
		if err := h.VerifyArtifact(contextFor("owner"), "file://"+path, digest); err == nil {
			t.Error("ambiguous workspace project verified")
		}
	})
}

type unboundedArtifactReader struct {
	read   int64
	closed bool
}

func (r *unboundedArtifactReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}
func (r *unboundedArtifactReader) Close() error { r.closed = true; return nil }

type largeArtifactStorage struct {
	*memStorage
	r *unboundedArtifactReader
}

func (s *largeArtifactStorage) Load(context.Context, string) (io.ReadCloser, error) { return s.r, nil }
func TestArtifactVerificationBoundsRemoteReader(t *testing.T) {
	r := &unboundedArtifactReader{}
	h := &mcpHandler{cfg: APIConfig{FileStorage: &largeArtifactStorage{newMemStorage(), r}}}
	err := h.VerifyArtifact(context.Background(), "storage://large", strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), "64 MiB") || !r.closed || r.read != (64<<20)+1 {
		t.Fatalf("remote bound: read=%d closed=%v err=%v", r.read, r.closed, err)
	}
}
