package main

import (
	"database/sql"
	"testing"

	"github.com/stek0v/levara/pkg/profile"
)

// TestBuildRuntimeProfileConfigSameFacts is the Phase 3B guard for the
// "ProfileConfig" group: extracting the profile validation input into a
// bootstrap helper must yield exactly the same profile.Config the inline
// literal in main() produced for the same runtime facts. The expected values
// re-encode that former literal field-by-field.
func TestBuildRuntimeProfileConfigSameFacts(t *testing.T) {
	// A populated, enterprise-grade environment exercises every derived field.
	t.Setenv("LEVARA_PROFILE", "enterprise")
	t.Setenv("DB_PROVIDER", "postgres")
	t.Setenv("JWT_SECRET", "shhh")
	t.Setenv("LEVARA_SYNC_REMOTE_URL", "http://10.23.0.53:8090/api/v1")
	t.Setenv("LEVARA_TOKEN", "lk_token")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	t.Setenv("LEVARA_WORKSPACE_AUDIT_EXPORT", "")

	// A non-nil *sql.DB is what main() passes once SQL runtime is up; the helper
	// only checks it for nil and reads DB_PROVIDER, so an empty handle is fine
	// (no connection is opened).
	db := &sql.DB{}

	got := buildRuntimeProfileConfig(db, true, "/var/log/levara/audit")
	want := profile.Config{
		Profile:        "enterprise",
		DBProvider:     "postgres",
		HasDB:          true,
		RequireAuth:    true,
		JWTSecretSet:   true,
		SyncEnabled:    true,
		SyncTokenSet:   true,
		TenantEnforced: true,
		AuditSinkSet:   true,
	}
	if got != want {
		t.Fatalf("buildRuntimeProfileConfig facts drifted\n got=%+v\nwant=%+v", got, want)
	}
}

// TestBuildRuntimeProfileConfigPersonalDefaults pins the permissive end: an
// empty env + nil DB collapses to a personal-shaped config with everything off.
func TestBuildRuntimeProfileConfigPersonalDefaults(t *testing.T) {
	t.Setenv("LEVARA_PROFILE", "")
	t.Setenv("DB_PROVIDER", "")
	t.Setenv("JWT_SECRET", "")
	t.Setenv("LEVARA_SYNC_REMOTE_URL", "")
	t.Setenv("LEVARA_TOKEN", "")
	t.Setenv("LEVARA_TENANT_ENFORCED", "")
	t.Setenv("LEVARA_WORKSPACE_AUDIT_EXPORT", "")

	got := buildRuntimeProfileConfig(nil, false, "-")
	want := profile.Config{} // all zero: no DB, no auth, no sync, audit disabled
	if got != want {
		t.Fatalf("personal defaults drifted\n got=%+v\nwant=%+v", got, want)
	}
}

// TestAuditSinkConfigured encodes the enabling rule shared with
// initMCPAuditSink so profile validation and the actual sink wiring can never
// silently disagree on whether an audit sink exists.
func TestAuditSinkConfigured(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wsExport string
		want     bool
	}{
		{"explicit dir is configured", "/var/log/audit", "", true},
		{"disabled dash is not", "-", "", false},
		{"disabled dash ignores ws export", "-", "1", false},
		{"default path alone is not", "", "", false},
		{"default path with ws export is", "", "1", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEVARA_WORKSPACE_AUDIT_EXPORT", tc.wsExport)
			if got := auditSinkConfigured(tc.path); got != tc.want {
				t.Fatalf("auditSinkConfigured(%q) with ws_export=%q = %v, want %v", tc.path, tc.wsExport, got, tc.want)
			}
		})
	}
}
