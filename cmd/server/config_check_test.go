package main

import (
	"strings"
	"testing"
)

// profileEnvKeys are every env var the profile-fact derivation reads. Tests
// reset all of them so an ambient value in the developer's shell cannot leak
// into a case and flip an assertion.
var profileEnvKeys = []string{
	"LEVARA_PROFILE",
	"DB_PROVIDER",
	"JWT_SECRET",
	"LEVARA_SYNC_REMOTE_URL",
	"LEVARA_TOKEN",
	"LEVARA_TENANT_ENFORCED",
	"LEVARA_WORKSPACE_AUDIT_EXPORT",
	"LEVARA_SSO_BRIDGE",
	"STORAGE_ENCRYPTION", "KMS_KEY_ARN", "KMS_READ_KEY_ARNS", "KMS_REGION", "KMS_ENDPOINT", "KMS_TIMEOUT", "KMS_CACHE_TTL",
	"STORAGE_ENCRYPTION_MAX_OBJECT_BYTES", "STORAGE_ENCRYPTION_MAX_SPOOL_BYTES", "STORAGE_ENCRYPTION_MAX_IN_FLIGHT", "STORAGE_ENCRYPTION_MAX_WAITERS", "STORAGE_ENCRYPTION_TIMEOUT", "STORAGE_ENCRYPTION_SPOOL_DIRECTORY",
	"AUDIT_WEBHOOK_URL", "AUDIT_WEBHOOK_TOKEN_FILE", "AUDIT_DESTINATION_ID", "AUDIT_QUEUE_MAX_EVENTS", "AUDIT_QUEUE_MAX_BYTES", "AUDIT_MAX_ATTEMPTS", "AUDIT_ADMISSION_TIMEOUT", "AUDIT_WEBHOOK_TIMEOUT",
}

func TestRunConfigCheckValidatesIntegrationsOffline(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want int
	}{
		{"valid", map[string]string{"STORAGE_ENCRYPTION": "aws-kms", "KMS_KEY_ARN": "arn:aws:kms:eu-central-1:123456789012:key/test-key", "KMS_ENDPOINT": "https://unreachable.invalid", "AUDIT_WEBHOOK_URL": "https://siem.invalid/events"}, 0},
		{"bad-key", map[string]string{"STORAGE_ENCRYPTION": "aws-kms", "KMS_KEY_ARN": "alias/current"}, 1},
		{"bad-ttl", map[string]string{"STORAGE_ENCRYPTION": "aws-kms", "KMS_KEY_ARN": "arn:aws:kms:eu-central-1:123456789012:key/test-key", "KMS_CACHE_TTL": "6m"}, 1},
		{"bad-capacity", map[string]string{"AUDIT_WEBHOOK_URL": "https://siem.invalid/events", "AUDIT_QUEUE_MAX_EVENTS": "1000001"}, 1},
		{"bad-timeout", map[string]string{"AUDIT_WEBHOOK_URL": "https://siem.invalid/events", "AUDIT_WEBHOOK_TIMEOUT": "30s"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			setProfileEnv(t, test.env)
			var out strings.Builder
			if code := runConfigCheck(&out, false, "", false); code != test.want {
				t.Fatalf("code=%d want=%d %s", code, test.want, out.String())
			}
		})
	}
}

func setProfileEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	for _, k := range profileEnvKeys {
		t.Setenv(k, "")
	}
	for k, v := range overrides {
		t.Setenv(k, v)
	}
}

func TestRunConfigCheckPersonalOK(t *testing.T) {
	setProfileEnv(t, map[string]string{
		"LEVARA_PROFILE": "personal",
		"DB_PROVIDER":    "sqlite",
	})
	var out strings.Builder
	// Personal has no requirements, so even strict mode is clean.
	if code := runConfigCheck(&out, false, "", true); code != 0 {
		t.Fatalf("personal strict: exit code = %d, want 0\noutput:\n%s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "profile: personal") {
		t.Errorf("output missing profile line:\n%s", s)
	}
	if !strings.Contains(s, "config-check: OK") {
		t.Errorf("output missing OK line:\n%s", s)
	}
}

func TestRunConfigCheckTeamStrictFailsWithoutAuth(t *testing.T) {
	setProfileEnv(t, map[string]string{
		"LEVARA_PROFILE": "team",
		"DB_PROVIDER":    "postgres",
		"JWT_SECRET":     "stable-secret",
	})
	var out strings.Builder
	// Team strict requires required auth; -require-auth flag is off here.
	if code := runConfigCheck(&out, false, "", true); code != 1 {
		t.Fatalf("team strict without auth: exit code = %d, want 1\noutput:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "team_requires_auth") {
		t.Errorf("expected team_requires_auth finding:\n%s", out.String())
	}
}

func TestRunConfigCheckTeamStrictOKWithAuth(t *testing.T) {
	setProfileEnv(t, map[string]string{
		"LEVARA_PROFILE": "team",
		"DB_PROVIDER":    "postgres",
		"JWT_SECRET":     "stable-secret",
	})
	var out strings.Builder
	if code := runConfigCheck(&out, true, "", true); code != 0 {
		t.Fatalf("team strict with auth: exit code = %d, want 0\noutput:\n%s", code, out.String())
	}
}

func TestRunConfigCheckTeamNonStrictNeverFatal(t *testing.T) {
	setProfileEnv(t, map[string]string{
		"LEVARA_PROFILE": "team",
		"DB_PROVIDER":    "postgres",
		"JWT_SECRET":     "stable-secret",
	})
	var out strings.Builder
	// Warn-only mode surfaces the missing-auth finding but never fails.
	if code := runConfigCheck(&out, false, "", false); code != 0 {
		t.Fatalf("team non-strict: exit code = %d, want 0\noutput:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "config-check: OK") {
		t.Errorf("non-strict run should report OK:\n%s", out.String())
	}
}

func TestBuildConfigCheckProfileConfigDerivesFromEnv(t *testing.T) {
	setProfileEnv(t, map[string]string{"DB_PROVIDER": "sqlite"})
	cfg := buildConfigCheckProfileConfig(false, "")
	if !cfg.HasDB {
		t.Error("config-check should assume a DB is declared (HasDB=true)")
	}
	if cfg.DBProvider != "sqlite" {
		t.Errorf("DBProvider = %q, want sqlite", cfg.DBProvider)
	}

	setProfileEnv(t, map[string]string{"DB_PROVIDER": ""})
	cfg = buildConfigCheckProfileConfig(false, "")
	if cfg.DBProvider != "postgres" {
		t.Errorf("DBProvider = %q, want postgres (bootstrap default)", cfg.DBProvider)
	}
}
