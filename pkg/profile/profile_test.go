package profile

import "testing"

func TestValidatePersonalIsPermissive(t *testing.T) {
	if got := Validate(Config{Profile: Personal}); len(got) != 0 {
		t.Fatalf("personal findings=%+v, want none", got)
	}
}

func TestValidateTeamWarnings(t *testing.T) {
	got := Validate(Config{Profile: Team, DBProvider: "sqlite", HasDB: true})
	want := map[string]bool{
		"team_requires_postgres":          true,
		"team_requires_auth":              true,
		"team_requires_stable_jwt_secret": true,
	}
	assertFindingCodes(t, got, want)
}

func TestValidateEnterpriseWarnings(t *testing.T) {
	got := Validate(Config{Profile: Enterprise, DBProvider: "postgres", HasDB: true, RequireAuth: true, JWTSecretSet: true})
	want := map[string]bool{
		"enterprise_requires_tenant_enforcement": true,
		"enterprise_requires_audit_sink":         true,
	}
	assertFindingCodes(t, got, want)
}

func TestValidateSoloProSyncTokenWarning(t *testing.T) {
	got := Validate(Config{Profile: SoloPro, SyncEnabled: true})
	assertFindingCodes(t, got, map[string]bool{"solo_pro_sync_without_token": true})
}

func TestValidateStrictPersonalAndSoloProAreNotFatal(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"personal", Config{Profile: Personal}},
		{"personal ignores missing db/auth", Config{Profile: Personal, DBProvider: "sqlite"}},
		{"solo_pro without sync", Config{Profile: SoloPro}},
		{"solo_pro sync with token", Config{Profile: SoloPro, SyncEnabled: true, SyncTokenSet: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateStrict(tc.cfg)
			if HasError(got) {
				t.Fatalf("ValidateStrict(%s) fatal findings=%+v, want none", tc.name, got)
			}
		})
	}
}

func TestValidateStrictTeamFailsFast(t *testing.T) {
	got := ValidateStrict(Config{Profile: Team, DBProvider: "sqlite", HasDB: true})
	if !HasError(got) {
		t.Fatalf("strict team with sqlite/no-auth not fatal: %+v", got)
	}
	for _, f := range got {
		if f.Level != LevelError {
			t.Fatalf("strict team finding %+v not error-level", f)
		}
	}

	// A fully-configured team is safe even in strict mode.
	safe := ValidateStrict(Config{Profile: Team, DBProvider: "postgres", HasDB: true, RequireAuth: true, JWTSecretSet: true})
	if HasError(safe) {
		t.Fatalf("strict team with postgres/auth/jwt unexpectedly fatal: %+v", safe)
	}
}

func TestValidateStrictEnterpriseFailsFast(t *testing.T) {
	// Missing tenant enforcement + audit sink despite postgres/auth/jwt.
	got := ValidateStrict(Config{Profile: Enterprise, DBProvider: "postgres", HasDB: true, RequireAuth: true, JWTSecretSet: true})
	if !HasError(got) {
		t.Fatalf("strict enterprise without tenant/audit not fatal: %+v", got)
	}

	safe := ValidateStrict(Config{
		Profile: Enterprise, DBProvider: "postgres", HasDB: true,
		RequireAuth: true, JWTSecretSet: true, TenantEnforced: true, AuditSinkSet: true,
	})
	if HasError(safe) {
		t.Fatalf("fully configured strict enterprise unexpectedly fatal: %+v", safe)
	}
}

func TestValidateEnterpriseSSOBridgeSatisfiesAuth(t *testing.T) {
	// An SSO-fronted enterprise authenticates at the bridge, so a configured
	// bridge satisfies the auth requirement even with RequireAuth off.
	safe := ValidateStrict(Config{
		Profile: Enterprise, DBProvider: "postgres", HasDB: true,
		RequireAuth: false, SSOBridgeConfigured: true,
		JWTSecretSet: true, TenantEnforced: true, AuditSinkSet: true,
	})
	if HasError(safe) {
		t.Fatalf("enterprise with SSO bridge in lieu of required auth unexpectedly fatal: %+v", safe)
	}

	// With neither required auth nor an SSO bridge, the auth finding fires.
	bad := ValidateStrict(Config{
		Profile: Enterprise, DBProvider: "postgres", HasDB: true,
		JWTSecretSet: true, TenantEnforced: true, AuditSinkSet: true,
	})
	if !HasError(bad) {
		t.Fatalf("enterprise without auth or SSO bridge not fatal: %+v", bad)
	}
	foundAuth := false
	for _, f := range bad {
		if f.Code == "enterprise_requires_auth" {
			foundAuth = true
		}
	}
	if !foundAuth {
		t.Fatalf("expected enterprise_requires_auth finding, got %+v", bad)
	}
}

func TestValidateStrictSoloProSyncWithoutTokenFailsFast(t *testing.T) {
	got := ValidateStrict(Config{Profile: SoloPro, SyncEnabled: true})
	if !HasError(got) {
		t.Fatalf("strict solo_pro sync without token not fatal: %+v", got)
	}
}

func TestValidateRemainsWarnOnly(t *testing.T) {
	// The default Validate must never produce error-level findings — strict
	// mode is opt-in and warn-only behavior stays available during migration.
	got := Validate(Config{Profile: Enterprise, DBProvider: "sqlite", HasDB: true})
	if len(got) == 0 || HasError(got) {
		t.Fatalf("Validate enterprise findings=%+v, want non-empty warn-only", got)
	}
}

func assertFindingCodes(t *testing.T, got []Finding, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("findings=%+v, want codes=%v", got, want)
	}
	for _, f := range got {
		if !want[f.Code] {
			t.Fatalf("unexpected finding %+v, want codes=%v", f, want)
		}
	}
}
