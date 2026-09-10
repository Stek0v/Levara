package access

import (
	"context"
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
