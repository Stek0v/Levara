package access

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSCIMConcurrentSameExternalIdentity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			u := SCIMUser{Issuer: "directory", ExternalID: "stable-sub", Email: "user@corp.test", Active: true}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var mu sync.Mutex
			created := 0
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					uid, fresh, err := s.ProvisionCreate(context.Background(), u)
					if err != nil {
						t.Errorf("same identity: %v", err)
						return
					}
					if uid != SCIMUserID(u.Issuer, u.ExternalID) {
						t.Errorf("wrong user %q", uid)
					}
					if fresh {
						mu.Lock()
						created++
						mu.Unlock()
					}
				}()
			}
			close(start)
			wg.Wait()
			if created != 1 {
				t.Errorf("created = %d, want 1", created)
			}
		})
	}
}

func TestSQLIdentityBridgeExactProvisionedMapping(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			ctx := context.Background()
			u := SCIMUser{Issuer: "Directory/Tenant", ExternalID: "CaseSensitive-Sub", Email: "stored@corp.test", Active: true}
			uid, _, err := s.ProvisionCreate(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			bridge := SQLIdentityBridge{DB: s.DB, Q: s.Q, TrustedIssuers: map[string]string{"https://idp.test/Tenant": u.Issuer, "https://idp.test/Other": "other-directory"}}
			ext := ExternalIdentity{Issuer: "https://idp.test/Tenant", Subject: u.ExternalID, Email: "untrusted@corp.test", Groups: []string{"superusers"}}
			p, err := bridge.ResolveExternal(ctx, ext)
			if err != nil || p.UserID != uid || p.Email != u.Email || p.Superuser || len(p.TenantIDs) != 0 {
				t.Fatalf("mapped principal=%+v, err=%v", p, err)
			}
			for _, bad := range []ExternalIdentity{
				{Issuer: "https://idp.test/tenant", Subject: u.ExternalID, Email: u.Email},
				{Issuer: ext.Issuer + "/", Subject: u.ExternalID, Email: u.Email},
				{Issuer: "https://idp.test/Other", Subject: u.ExternalID, Email: u.Email},
				{Issuer: ext.Issuer, Subject: "casesensitive-sub", Email: u.Email},
				{Issuer: ext.Issuer, Subject: u.ExternalID + " ", Email: u.Email},
				{Issuer: ext.Issuer, Subject: "unprovisioned", Email: u.Email},
			} {
				if p, err := bridge.ResolveExternal(ctx, bad); !errors.Is(err, ErrSubjectNotMapped) || p.UserID != "" {
					t.Errorf("accepted unmatched identity %+v: %+v %v", bad, p, err)
				}
			}
			if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.ResolveExternal(ctx, ext); !errors.Is(err, ErrSubjectNotMapped) {
				t.Fatalf("inactive identity: %v", err)
			}
			if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
				t.Fatal(err)
			}
			if p, err := bridge.ResolveExternal(ctx, ext); err != nil || p.UserID != uid {
				t.Fatalf("reactivated identity=%+v %v", p, err)
			}
			if _, err := s.DB.ExecContext(ctx, s.rewrite("DELETE FROM users WHERE id = $1"), uid); err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.ResolveExternal(ctx, ext); !errors.Is(err, ErrSubjectNotMapped) {
				t.Fatalf("dangling identity: %v", err)
			}
			drop := "DROP TABLE scim_identities"
			if dialect == "postgres" {
				drop += " CASCADE"
			}
			if _, err := s.DB.ExecContext(ctx, drop); err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.ResolveExternal(ctx, ext); err == nil {
				t.Fatal("database failure accepted")
			}
			bridge.DB = nil
			if _, err := bridge.ResolveExternal(ctx, ext); err == nil {
				t.Fatal("nil database accepted")
			}
		})
	}
}

func TestSCIMCredentialEpochAtomicity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			ctx := context.Background()
			u := SCIMUser{Issuer: "directory", ExternalID: "user", Email: "user@corp.test", Active: true}
			uid, _, err := s.ProvisionCreate(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.ExecContext(ctx, s.rewrite("INSERT INTO api_keys (id, user_id) VALUES ('old-key', $1)"), uid); err != nil {
				t.Fatal(err)
			}
			assert := func(epoch int64, revoked bool) {
				t.Helper()
				got, err := CurrentCredentialEpoch(ctx, s.DB, s.Q, uid)
				if err != nil || got != epoch {
					t.Fatalf("active epoch=%d, %v, want %d", got, err, epoch)
				}
				var gotRevoked bool
				if err := s.DB.QueryRowContext(ctx, "SELECT revoked FROM api_keys WHERE id = 'old-key'").Scan(&gotRevoked); err != nil || gotRevoked != revoked {
					t.Fatalf("key revoked=%v err=%v want=%v", gotRevoked, err, revoked)
				}
			}
			assert(0, false)
			u.Active = false
			if err := s.ProvisionUpdate(ctx, u, "blocked@corp.test"); err == nil {
				t.Fatal("constraint failure accepted")
			}
			assert(0, false)
			for i, deactivate := range []func() error{
				func() error { return s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID) },
				func() error { return s.ProvisionUpdate(ctx, u, "") },
				func() error { _, _, err := s.ProvisionCreate(ctx, u); return err },
				func() error { return (SQLProvisioner{DB: s.DB, Q: s.Q}).DeactivateUser(ctx, uid) },
				func() error {
					return (SQLProvisioner{DB: s.DB, Q: s.Q}).ProvisionUser(ctx, ProvisionedUser{UserID: uid, Active: false})
				},
			} {
				if err := deactivate(); err != nil {
					t.Fatal(err)
				}
				if err := ValidateCredential(ctx, s.DB, s.Q, uid, int64(i)); !errors.Is(err, ErrInactiveIdentity) {
					t.Fatalf("inactive credential: %v", err)
				}
				u.Active = true
				if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
					t.Fatal(err)
				}
				u.Active = false
				assert(int64(i+1), true)
				if err := ValidateCredential(ctx, s.DB, s.Q, uid, int64(i)); !errors.Is(err, ErrRevokedCredential) {
					t.Fatalf("old credential revived: %v", err)
				}
				if err := ValidateCredential(ctx, s.DB, s.Q, uid, int64(i+1)); err != nil {
					t.Fatalf("fresh credential denied: %v", err)
				}
			}
		})
	}
}

func TestExternalCredentialRevocationWatermark(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := scimStoreForDialect(t, dialect)
			ctx := context.Background()
			u := SCIMUser{Issuer: "directory", ExternalID: "subject", Email: "user@corp.test", Active: true}
			uid, _, err := s.ProvisionCreate(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateExternalCredential(ctx, s.DB, s.Q, uid, 0); err != nil {
				t.Fatalf("initial missing iat=%v", err)
			}
			old := time.Now().Add(-time.Minute).Unix()
			if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
				t.Fatal(err)
			}
			var watermark int64
			if err := s.DB.QueryRowContext(ctx, s.rewrite("SELECT revoked_before FROM credential_epochs WHERE user_id = $1"), uid).Scan(&watermark); err != nil {
				t.Fatal(err)
			}
			if watermark < old {
				t.Fatalf("invalid revoke watermark=%d", watermark)
			}
			for _, issued := range []int64{0, old, watermark} {
				if err := ValidateExternalCredential(ctx, s.DB, s.Q, uid, issued); !errors.Is(err, ErrRevokedCredential) {
					t.Errorf("iat=%d revived: %v", issued, err)
				}
			}
			if err := ValidateExternalCredential(ctx, s.DB, s.Q, uid, watermark+1); err != nil {
				t.Fatalf("fresh token denied: %v", err)
			}
			// A subsequent deactivation cannot move the watermark backwards,
			// including when the server clock has moved backwards.
			future := watermark + 3600
			if _, err := s.DB.ExecContext(ctx, s.rewrite("UPDATE credential_epochs SET revoked_before = $1 WHERE user_id = $2"), future, uid); err != nil {
				t.Fatal(err)
			}
			if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
				t.Fatal(err)
			}
			if err := ValidateExternalCredential(ctx, s.DB, s.Q, uid, future); !errors.Is(err, ErrRevokedCredential) {
				t.Fatalf("watermark moved backwards: %v", err)
			}
		})
	}
}

func TestSSOResolvesProvisionedSCIMIdentity(t *testing.T) {
	s := newSCIMStore(t)
	u := SCIMUser{Issuer: "https://directory.test/tenant", ExternalID: "stable-sub", Email: "stored@corp.test", Active: true}
	uid, _, err := s.ProvisionCreate(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	bridge := SQLIdentityBridge{DB: s.DB, Q: s.Q, TrustedIssuers: map[string]string{u.Issuer: u.Issuer}}
	p, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{Issuer: u.Issuer, Subject: u.ExternalID, Email: "claim@other.test"})
	if err != nil {
		t.Fatal(err)
	}
	if p.UserID != uid {
		t.Errorf("SSO resolves %q, want provisioned %q", p.UserID, uid)
	}
	if p.Email != u.Email {
		t.Errorf("claim email overrides provisioned identity: %q", p.Email)
	}
}
