package access

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBrowserSessionStorage(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			ctx := context.Background()
			if err := EnsureBrowserSessionSchema(ctx, s.DB, s.Q); err != nil {
				t.Fatal(err)
			}
			id, err := CreateBrowserSession(ctx, s.DB, s.Q, "alice", time.Now().Add(time.Hour).Unix())
			if err != nil {
				t.Fatal(err)
			}
			// Re-running schema initialization must preserve current sessions.
			if err := EnsureBrowserSessionSchema(ctx, s.DB, s.Q); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBrowserSession(ctx, s.DB, s.Q, "alice", id); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBrowserSession(ctx, s.DB, s.Q, "bob", id); err == nil {
				t.Fatal("other user accepted")
			}
			if err := RevokeBrowserSession(ctx, s.DB, s.Q, "bob", id); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBrowserSession(ctx, s.DB, s.Q, "alice", id); err != nil {
				t.Fatal("other user revoked session", err)
			}
			if err := RevokeBrowserSession(ctx, s.DB, s.Q, "alice", id); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBrowserSession(ctx, s.DB, s.Q, "alice", id); err == nil {
				t.Fatal("revoked session accepted")
			}
			if err := ValidateBrowserSession(ctx, nil, nil, "alice", id); err == nil {
				t.Fatal("missing database accepted")
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := ValidateBrowserSession(cancelled, s.DB, s.Q, "alice", id); err == nil {
				t.Fatal("cancelled query accepted")
			}
			if _, err := s.DB.Exec("DROP TABLE auth_sessions"); err != nil {
				t.Fatal(err)
			}
			if err := ValidateBrowserSession(ctx, s.DB, s.Q, "alice", id); err == nil {
				t.Fatal("SQL failure accepted")
			}
		})
	}
}

func TestSessionCredentialCombined(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			ctx := context.Background()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := s.DB.ExecContext(ctx, s.rewrite(query), args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, user := range []string{"alice", "bob", "inactive"} {
				exec("INSERT INTO principals(id,type) VALUES($1,'user')", user)
				exec("INSERT INTO users(id,email,hashed_password,is_active) VALUES($1,$2,'locked',$3)", user, user+"@test.invalid", user != "inactive")
			}
			exec("CREATE TABLE IF NOT EXISTS tenants(id TEXT PRIMARY KEY,name TEXT,owner_id TEXT)")
			exec("CREATE TABLE IF NOT EXISTS user_tenant(user_id TEXT,tenant_id TEXT,PRIMARY KEY(user_id,tenant_id))")
			exec("INSERT INTO tenants(id,name,owner_id) VALUES('tenant-a','Tenant A','alice')")
			exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('alice','tenant-a')")
			if err := EnsureBrowserSessionSchema(ctx, s.DB, s.Q); err != nil {
				t.Fatal(err)
			}
			now := time.Now().Unix()
			exec("INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES($1,$2,$3,$4)", "live", "alice", now+3600, false)
			exec("INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES($1,$2,$3,$4)", "foreign", "bob", now+3600, false)
			exec("INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES($1,$2,$3,$4)", "revoked", "alice", now+3600, true)
			exec("INSERT INTO auth_sessions(id,user_id,expires_at,revoked) VALUES($1,$2,$3,$4)", "expired", "alice", now-1, false)
			calls := 0
			tenant, err := ValidateSessionCredentialTenant(ctx, s.DB, func(query string) string {
				calls++
				return s.rewrite(query)
			}, "alice", 0, "live")
			if err != nil || tenant != "tenant-a" || calls != 1 {
				t.Fatalf("combined tenant credential: tenant=%q calls=%d err=%v", tenant, calls, err)
			}
			calls = 0
			tenant, superuser, err := ValidateSessionCredentialAuthorization(ctx, s.DB, func(query string) string {
				calls++
				return s.rewrite(query)
			}, "alice", 0, "live")
			if err != nil || tenant != "tenant-a" || superuser || calls != 1 {
				t.Fatalf("combined authorization: tenant=%q superuser=%v calls=%d err=%v", tenant, superuser, calls, err)
			}
			exec("UPDATE users SET is_superuser=TRUE WHERE id='alice'")
			if _, superuser, err = ValidateSessionCredentialAuthorization(ctx, s.DB, s.Q, "alice", 0, "live"); err != nil || !superuser {
				t.Fatalf("fresh superuser grant: superuser=%v err=%v", superuser, err)
			}
			exec("UPDATE users SET is_superuser=FALSE WHERE id='alice'")
			if _, superuser, err = ValidateSessionCredentialAuthorization(ctx, s.DB, s.Q, "alice", 0, "live"); err != nil || superuser {
				t.Fatalf("fresh superuser revoke: superuser=%v err=%v", superuser, err)
			}
			if tenant, err = ValidateSessionCredentialTenant(ctx, s.DB, s.Q, "alice", 0, "revoked"); !errors.Is(err, ErrRevokedCredential) || tenant != "" {
				t.Fatalf("revoked credential returned tenant=%q err=%v", tenant, err)
			}
			for _, tc := range []struct {
				name, user, sid string
				epoch           int64
				want            error
			}{
				{"missing_epoch_zero", "alice", "live", 0, nil},
				{"legacy", "alice", "", 0, nil},
				{"negative", "alice", "live", -1, ErrRevokedCredential},
				{"stale", "alice", "live", 1, ErrRevokedCredential},
				{"missing_user", "missing", "live", 0, ErrInactiveIdentity},
				{"inactive_user", "inactive", "live", 0, ErrInactiveIdentity},
				{"foreign_sid", "alice", "foreign", 0, ErrRevokedCredential},
				{"missing_sid", "alice", "missing", 0, ErrRevokedCredential},
				{"revoked_sid", "alice", "revoked", 0, ErrRevokedCredential},
				{"expired_sid", "alice", "expired", 0, ErrRevokedCredential},
			} {
				t.Run(tc.name, func(t *testing.T) {
					calls := 0
					q := func(query string) string {
						calls++
						return s.rewrite(query)
					}
					err := ValidateSessionCredential(ctx, s.DB, q, tc.user, tc.epoch, tc.sid)
					if !errors.Is(err, tc.want) {
						t.Fatalf("got %v, want %v", err, tc.want)
					}
					if tc.epoch >= 0 && calls != 1 {
						t.Fatalf("rewritten queries=%d, want one fresh query", calls)
					}
				})
			}
			if err := ValidateSessionCredential(ctx, nil, s.Q, "alice", 0, "live"); !errors.Is(err, ErrProvisioningNoDB) {
				t.Fatalf("nil DB: %v", err)
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := ValidateSessionCredential(cancelled, s.DB, s.Q, "alice", 0, "live"); !errors.Is(err, context.Canceled) {
				t.Fatalf("raw canceled SQL error: %v", err)
			}

			// The same transaction must be used while it occupies the only
			// connection; a hidden DB query would wait until this deadline.
			s.DB.SetMaxOpenConns(1)
			fencedCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			tx, err := s.DB.BeginTx(fencedCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			lock := "LOCK TABLE users,credential_epochs,auth_sessions IN SHARE MODE"
			if dialect == "sqlite" {
				lock = "UPDATE users SET id=id WHERE 1=0"
			}
			if _, err := tx.ExecContext(fencedCtx, lock); err != nil {
				t.Fatal(err)
			}
			p := (SQLPolicy{DB: s.DB, Q: s.Q}).WithReadTransaction(tx)
			if err := p.RecheckCredential(fencedCtx, "alice", "jwt", "", "", "live", 0, 0, now+3600); err != nil {
				t.Fatalf("fenced pool1 live credential: %v", err)
			}
			if err := p.RecheckCredential(fencedCtx, "alice", "jwt", "", "", "foreign", 0, 0, now+3600); !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("fenced foreign SID: %v", err)
			}
			if err := p.RecheckCredential(fencedCtx, "alice", "jwt", "", "", "live", 0, 0, now-1); !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("expired JWT: %v", err)
			}
			waitCtx, endWait := context.WithTimeout(ctx, 20*time.Millisecond)
			err = ValidateSessionCredential(waitCtx, s.DB, s.Q, "alice", 0, "live")
			endWait()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pool wait must return raw deadline: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}

			// Waiting for a connection must not let JWT expiry pass unnoticed.
			blockedTx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			expires := time.Now().Unix() + 1
			released := make(chan struct{})
			go func() {
				defer close(released)
				for time.Now().Unix() < expires {
					time.Sleep(10 * time.Millisecond)
				}
				_ = blockedTx.Rollback()
			}()
			expiryCtx, endExpiry := context.WithTimeout(ctx, 3*time.Second)
			err = (SQLPolicy{DB: s.DB, Q: s.Q}).RecheckCredential(expiryCtx, "alice", "jwt", "", "", "live", 0, 0, expires)
			<-released
			if expiryCtx.Err() != nil {
				t.Fatal("expiry regression hit its observer deadline", expiryCtx.Err())
			}
			endExpiry()
			if !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("JWT expired during SQL wait: %v", err)
			}

			// Every boundary re-reads SQL; neither epoch nor SID is cached.
			exec("INSERT INTO credential_epochs(user_id,epoch) VALUES('alice',1)")
			if err := ValidateSessionCredential(ctx, s.DB, s.Q, "alice", 0, "live"); !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("fresh epoch revoke: %v", err)
			}
			if err := ValidateSessionCredential(ctx, s.DB, s.Q, "alice", 1, "live"); err != nil {
				t.Fatal(err)
			}
			if err := RevokeBrowserSession(ctx, s.DB, s.Q, "alice", "live"); err != nil {
				t.Fatal(err)
			}
			if err := ValidateSessionCredential(ctx, s.DB, s.Q, "alice", 1, "live"); !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("fresh SID revoke: %v", err)
			}
			exec("DROP TABLE auth_sessions")
			if err := ValidateSessionCredential(ctx, s.DB, s.Q, "alice", 1, ""); err != nil {
				t.Fatalf("legacy JWT without auth_sessions: %v", err)
			}
			if err := ValidateSessionCredential(ctx, s.DB, s.Q, "alice", 1, "live"); err == nil || errors.Is(err, ErrRevokedCredential) || errors.Is(err, ErrInactiveIdentity) {
				t.Fatalf("missing session table must retain raw SQL error: %v", err)
			}
		})
	}
}
