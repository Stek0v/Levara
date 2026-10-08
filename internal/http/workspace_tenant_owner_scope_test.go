package http

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceSelectedTenantOwnerScope(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','b'),('root','a'),('root','b'),('owner','b')")
		f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('tenant-b-project','Tenant B Project','foreign')")
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('tenant-b-share','tenant-b-project','peer','editor')")
		for _, tc := range []struct {
			name, user, tenant, project string
			allowed                     bool
		}{
			{"shared_wrong_selection", "peer", "a", "tenant-b-project", false},
			{"superuser_wrong_selection", "root", "a", "tenant-b-project", false},
			{"shared_correct_selection", "peer", "b", "tenant-b-project", true},
			{"superuser_correct_selection", "root", "b", "tenant-b-project", true},
			{"legacy_empty_selection", "peer", "", "tenant-b-project", true},
			{"owner_original_membership", "owner", "a", "alpha", true},
			{"owner_second_membership", "owner", "b", "alpha", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				path := tc.name + ".md"
				file, _, err := workspaceFilePath(cfg, tc.project, "main", path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("original bytes"), 0600); err != nil {
					t.Fatal(err)
				}
				actor := accesspkg.MetadataActor{Actor: accesspkg.Actor{UserID: tc.user, TenantID: tc.tenant}, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
				_, err = writeWorkspaceMarkdownAuthorized(context.Background(), cfg, workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: tc.project, Branch: "main", Path: path, Text: "new bytes"}}, actor)
				if (err == nil) != tc.allowed {
					t.Errorf("effect allowed=%v want=%v error=%v", err == nil, tc.allowed, err)
				}
				body, readErr := os.ReadFile(file)
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := "original bytes"
				if tc.allowed {
					want = "new bytes"
				}
				if string(body) != want {
					t.Errorf("effect changed wrong-tenant bytes: %q", body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("effect retained SQL")
				}
			})
		}
		for _, surface := range []string{"watch", "context", "diagnostic"} {
			t.Run(surface+"_wrong_selection", func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/workspace?project_id=tenant-b-project&branch=main")
				if surface == "diagnostic" {
					raw.Request.Header.SetMethod("POST")
					raw.Request.Header.SetContentType("application/json")
					raw.Request.SetBodyString(`{"project_id":"tenant-b-project","access":"read"}`)
				}
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				defer func() {
					if s, ok := c.Response().BodyStream().(io.Closer); ok {
						_ = s.Close()
					}
				}()
				c.Locals("user_id", "peer")
				c.Locals("tenant_id", "a")
				c.Locals("verified_jwt", jwtPayload{Sub: "peer", Exp: time.Now().Add(time.Hour).Unix()})
				var err error
				switch surface {
				case "watch":
					err = workspaceWatchStatusHandler(cfg)(c)
				case "context":
					err = workspaceContextHandler(cfg)(c)
				case "diagnostic":
					err = workspaceAccessCheckHandler(cfg)(c)
				}
				if surface == "diagnostic" {
					if err != nil || c.Response().StatusCode() != 200 {
						t.Fatalf("negative diagnostic failed: %v", err)
					}
					var check workspaceAccessCheckResponse
					if err := json.Unmarshal(c.Response().Body(), &check); err != nil {
						t.Fatal(err)
					}
					if check.Allowed {
						t.Error("diagnostic ignored selected tenant")
					}
				} else if err == nil && c.Response().StatusCode() < 400 {
					t.Error("workspace read ignored selected tenant")
				}
				if s, ok := c.Response().BodyStream().(io.Closer); ok {
					_ = s.Close()
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("response retained SQL after close")
				}
			})
		}
	})
}
