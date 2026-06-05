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
