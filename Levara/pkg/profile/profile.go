// Package profile validates Levara runtime profile configuration.
package profile

import "strings"

const (
	Personal   = "personal"
	SoloPro    = "solo_pro"
	Team       = "team"
	Enterprise = "enterprise"
)

type Config struct {
	Profile        string
	DBProvider     string
	HasDB          bool
	RequireAuth    bool
	JWTSecretSet   bool
	SyncEnabled    bool
	SyncTokenSet   bool
	TenantEnforced bool
	AuditSinkSet   bool
}

// Finding levels. Warnings are advisory; errors are fatal in strict mode.
const (
	LevelWarn  = "warn"
	LevelError = "error"
)

type Finding struct {
	Level   string
	Code    string
	Message string
}

// HasError reports whether any finding is at error level. Callers use it to
// decide whether strict-mode startup must fail fast.
func HasError(findings []Finding) bool {
	for _, f := range findings {
		if f.Level == LevelError {
			return true
		}
	}
	return false
}

func Normalize(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", Personal:
		return Personal
	case "solo-pro", "solo_pro", "pro":
		return SoloPro
	case Team:
		return Team
	case Enterprise:
		return Enterprise
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// Validate returns advisory (warn-level) findings for the configured profile.
// This is the default, migration-friendly behavior: misconfiguration is
// surfaced but never blocks startup.
func Validate(cfg Config) []Finding {
	return validate(cfg, LevelWarn)
}

// ValidateStrict returns the same profile-requirement findings as Validate but
// at error level, so callers can fail fast on unsafe team/enterprise (and
// solo_pro sync) configurations. Personal — and solo_pro without sync — produce
// no findings and therefore never fail fast.
func ValidateStrict(cfg Config) []Finding {
	return validate(cfg, LevelError)
}

func validate(cfg Config, level string) []Finding {
	profile := Normalize(cfg.Profile)
	var findings []Finding
	require := func(code, msg string) Finding { return Finding{Level: level, Code: code, Message: msg} }
	switch profile {
	case Personal:
		return nil
	case SoloPro:
		if cfg.SyncEnabled && !cfg.SyncTokenSet {
			findings = append(findings, require("solo_pro_sync_without_token", "solo_pro sync is enabled without a stable sync token"))
		}
	case Team:
		if !isPostgres(cfg) {
			findings = append(findings, require("team_requires_postgres", "team profile should use Postgres"))
		}
		if !cfg.RequireAuth {
			findings = append(findings, require("team_requires_auth", "team profile should run with required auth"))
		}
		if !cfg.JWTSecretSet {
			findings = append(findings, require("team_requires_stable_jwt_secret", "team profile should set JWT_SECRET so tokens survive restarts"))
		}
	case Enterprise:
		if !isPostgres(cfg) {
			findings = append(findings, require("enterprise_requires_postgres", "enterprise profile should use Postgres or managed SQL"))
		}
		if !cfg.RequireAuth {
			findings = append(findings, require("enterprise_requires_auth", "enterprise profile should run with required auth or an SSO bridge"))
		}
		if !cfg.JWTSecretSet {
			findings = append(findings, require("enterprise_requires_stable_jwt_secret", "enterprise profile should set JWT_SECRET or equivalent stable signing config"))
		}
		if !cfg.TenantEnforced {
			findings = append(findings, require("enterprise_requires_tenant_enforcement", "enterprise profile should enforce tenant isolation"))
		}
		if !cfg.AuditSinkSet {
			findings = append(findings, require("enterprise_requires_audit_sink", "enterprise profile should configure an audit sink"))
		}
	default:
		// An unknown profile is advisory regardless of strict mode: a typo in
		// LEVARA_PROFILE should not hard-stop startup.
		findings = append(findings, warn("unknown_profile", "unknown LEVARA_PROFILE; using current runtime behavior"))
	}
	return findings
}

func isPostgres(cfg Config) bool {
	return cfg.HasDB && strings.EqualFold(cfg.DBProvider, "postgres")
}

func warn(code, msg string) Finding {
	return Finding{Level: LevelWarn, Code: code, Message: msg}
}
