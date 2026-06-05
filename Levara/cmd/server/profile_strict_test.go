package main

import (
	"testing"

	"github.com/stek0v/levara/pkg/profile"
)

// TestEvaluateRuntimeProfileStrictFailFast pins the startup decision: strict
// mode fails fast on unsafe team/enterprise config, warn-only mode never does,
// and permissive profiles start regardless of strict mode.
func TestEvaluateRuntimeProfileStrictFailFast(t *testing.T) {
	unsafeTeam := profile.Config{Profile: profile.Team, DBProvider: "sqlite", HasDB: true}
	unsafeEnterprise := profile.Config{Profile: profile.Enterprise, DBProvider: "sqlite", HasDB: true}
	safeTeam := profile.Config{Profile: profile.Team, DBProvider: "postgres", HasDB: true, RequireAuth: true, JWTSecretSet: true}
	safeEnterprise := profile.Config{
		Profile: profile.Enterprise, DBProvider: "postgres", HasDB: true,
		RequireAuth: true, JWTSecretSet: true, TenantEnforced: true, AuditSinkSet: true,
	}

	cases := []struct {
		name      string
		cfg       profile.Config
		strict    bool
		wantFatal bool
	}{
		{"personal strict", profile.Config{Profile: profile.Personal}, true, false},
		{"solo_pro strict no sync", profile.Config{Profile: profile.SoloPro}, true, false},
		{"solo_pro strict sync without token", profile.Config{Profile: profile.SoloPro, SyncEnabled: true}, true, true},
		{"team warn-only stays up", unsafeTeam, false, false},
		{"team strict fails fast", unsafeTeam, true, true},
		{"team strict safe stays up", safeTeam, true, false},
		{"enterprise warn-only stays up", unsafeEnterprise, false, false},
		{"enterprise strict fails fast", unsafeEnterprise, true, true},
		{"enterprise strict safe stays up", safeEnterprise, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, fatal := evaluateRuntimeProfile(tc.cfg, tc.strict)
			if fatal != tc.wantFatal {
				t.Fatalf("evaluateRuntimeProfile(%s, strict=%v) fatal=%v, want %v", tc.name, tc.strict, fatal, tc.wantFatal)
			}
		})
	}
}

// TestEvaluateRuntimeProfileWarnFindingsPreserved confirms warn mode still
// surfaces the same findings (just non-fatal) so migration visibility is kept.
func TestEvaluateRuntimeProfileWarnFindingsPreserved(t *testing.T) {
	findings, fatal := evaluateRuntimeProfile(profile.Config{Profile: profile.Team, DBProvider: "sqlite", HasDB: true}, false)
	if fatal {
		t.Fatal("warn mode must never be fatal")
	}
	if len(findings) == 0 {
		t.Fatal("warn mode dropped team findings")
	}
	for _, f := range findings {
		if f.Level != profile.LevelWarn {
			t.Fatalf("warn mode finding %+v not warn-level", f)
		}
	}
}
