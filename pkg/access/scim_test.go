package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func scimTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/scim.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newSCIMStore(t *testing.T) SCIMStore {
	t.Helper()
	s := SCIMStore{DB: scimTestDB(t), Q: pgToSQLiteForTests}
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// principals/users tables are needed by ProvisionCreate.
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS principals (id TEXT PRIMARY KEY, type TEXT NOT NULL DEFAULT 'user', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY REFERENCES principals(id), email TEXT NOT NULL UNIQUE, hashed_password TEXT NOT NULL, is_active BOOLEAN NOT NULL DEFAULT true, is_superuser BOOLEAN NOT NULL DEFAULT false, is_verified BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := s.DB.ExecContext(ctx, pgToSQLiteForTests(q)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// pgToSQLiteForTests rewrites $N placeholders for the sqlite test driver.
func pgToSQLiteForTests(q string) string {
	q = strings.ReplaceAll(q, "NOW()", "CURRENT_TIMESTAMP")
	q = strings.ReplaceAll(q, "now()", "CURRENT_TIMESTAMP")
	q = strings.ReplaceAll(q, "TIMESTAMPTZ", "TIMESTAMP")
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if q[i] == '$' && i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
			b.WriteByte('?')
			i++
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

func TestSCIMUserIDDeterministicAndDistinct(t *testing.T) {
	a1 := SCIMUserID("https://idp", "u1")
	a2 := SCIMUserID("https://idp", "u1")
	if a1 != a2 {
		t.Fatal("same identity mapped to different ids")
	}
	if a1 == SCIMUserID("https://idp", "u2") {
		t.Fatal("different identities collide")
	}
	if a1 == SCIMUserID("https://idp2", "u1") {
		t.Fatal("different issuers collide")
	}
	if !strings.HasPrefix(a1, "scim-") {
		t.Fatalf("unexpected id shape: %s", a1)
	}
}

func TestSCIMCreateThenIdempotentRecreate(t *testing.T) {
	s := newSCIMStore(t)
	ctx := context.Background()
	u := SCIMUser{Issuer: "iss", ExternalID: "ext-1", Email: "a@corp.test", Active: true}
	uid, created, err := s.ProvisionCreate(ctx, u)
	if err != nil || !created {
		t.Fatalf("first create: %v created=%v", err, created)
	}
	uid2, created2, err := s.ProvisionCreate(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if created2 || uid2 != uid {
		t.Fatalf("re-create diverged: id=%s created=%v", uid2, created2)
	}
}

func TestSCIMEmailCollisionRejected(t *testing.T) {
	s := newSCIMStore(t)
	ctx := context.Background()
	if _, _, err := s.ProvisionCreate(ctx, SCIMUser{Issuer: "iss", ExternalID: "e1", Email: "x@corp.test"}); err != nil {
		t.Fatal(err)
	}
	// Different externalId, same email → conflict (no silent merges).
	_, _, err := s.ProvisionCreate(ctx, SCIMUser{Issuer: "iss", ExternalID: "e2", Email: "x@corp.test"})
	if !errors.Is(err, ErrSCIMEmailConflict) {
		t.Fatalf("want ErrSCIMEmailConflict, got %v", err)
	}
	// A pre-existing LOCAL user with that email also blocks.
	if _, _, err := s.ProvisionCreate(ctx, SCIMUser{Issuer: "iss", ExternalID: "e3", Email: "x@corp.test"}); !errors.Is(err, ErrSCIMEmailConflict) {
		t.Fatalf("local-user collision not blocked: %v", err)
	}
}

func TestSCIMSoftDeleteAndReactivate(t *testing.T) {
	s := newSCIMStore(t)
	ctx := context.Background()
	uid, _, err := s.ProvisionCreate(ctx, SCIMUser{Issuer: "iss", ExternalID: "e1", Email: "d@corp.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProvisionDeactivate(ctx, "iss", "e1"); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := s.DB.QueryRowContext(ctx, pgToSQLiteForTests("SELECT is_active FROM users WHERE id = $1"), uid).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("deactivate did not flip is_active")
	}
	// Mapping row survives → re-provision reactivates.
	if _, created, err := s.ProvisionCreate(ctx, SCIMUser{Issuer: "iss", ExternalID: "e1", Email: "d@corp.test", Active: true}); err != nil || created {
		t.Fatalf("reactivate: err=%v created=%v", err, created)
	}
	if err := s.DB.QueryRowContext(ctx, pgToSQLiteForTests("SELECT is_active FROM users WHERE id = $1"), uid).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("re-provision did not reactivate")
	}
}

func TestSCIMLookupUnknown(t *testing.T) {
	s := newSCIMStore(t)
	if _, err := s.Lookup(context.Background(), "iss", "ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("want ErrUserNotFound, got %v", err)
	}
}

func TestSCIMCreateValidation(t *testing.T) {
	s := newSCIMStore(t)
	ctx := context.Background()
	for _, u := range []SCIMUser{
		{Issuer: "", ExternalID: "e", Email: "v@t"},
		{Issuer: "i", ExternalID: "", Email: "v@t"},
		{Issuer: "i", ExternalID: "e", Email: ""},
	} {
		if _, _, err := s.ProvisionCreate(ctx, u); err == nil {
			t.Fatalf("incomplete identity accepted: %+v", u)
		}
	}
}

func TestSCIMStoreNilDBDisabled(t *testing.T) {
	s := SCIMStore{}
	if err := s.EnsureSchema(context.Background()); !errors.Is(err, ErrSCIMDisabled) {
		t.Fatalf("nil db schema: %v", err)
	}
	if _, _, err := s.ProvisionCreate(context.Background(), SCIMUser{Issuer: "i", ExternalID: "e", Email: "x"}); !errors.Is(err, ErrSCIMDisabled) {
		t.Fatalf("nil db create: %v", err)
	}
}

func TestTokenCheck(t *testing.T) {
	if TokenCheck("x", "") {
		t.Fatal("empty configured token must never match")
	}
	if !TokenCheck("secret", "secret") {
		t.Fatal("valid token rejected")
	}
	if TokenCheck("secreT", "secret") {
		t.Fatal("case mismatch accepted")
	}
	if TokenCheck("", "secret") {
		t.Fatal("empty presented accepted")
	}
}

// Concurrent provision of distinct identities must not collide or fail.
func TestSCIMConcurrentCreates(t *testing.T) {
	s := newSCIMStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.ProvisionCreate(ctx, SCIMUser{
				Issuer: "iss", ExternalID: string(rune('a' + i)), Email: string(rune('a'+i)) + "@t",
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create failed: %v", err)
	}
}
