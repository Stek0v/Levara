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

type Finding struct {
	Level   string
	Code    string
	Message string
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

func Validate(cfg Config) []Finding {
	profile := Normalize(cfg.Profile)
	var findings []Finding
	switch profile {
	case Personal:
		return nil
	case SoloPro:
		if cfg.SyncEnabled && !cfg.SyncTokenSet {
			findings = append(findings, warn("solo_pro_sync_without_token", "solo_pro sync is enabled without a stable sync token"))
		}
	case Team:
		if !isPostgres(cfg) {
			findings = append(findings, warn("team_requires_postgres", "team profile should use Postgres"))
		}
		if !cfg.RequireAuth {
			findings = append(findings, warn("team_requires_auth", "team profile should run with required auth"))
		}
		if !cfg.JWTSecretSet {
			findings = append(findings, warn("team_requires_stable_jwt_secret", "team profile should set JWT_SECRET so tokens survive restarts"))
		}
	case Enterprise:
		if !isPostgres(cfg) {
			findings = append(findings, warn("enterprise_requires_postgres", "enterprise profile should use Postgres or managed SQL"))
		}
		if !cfg.RequireAuth {
			findings = append(findings, warn("enterprise_requires_auth", "enterprise profile should run with required auth or an SSO bridge"))
		}
		if !cfg.JWTSecretSet {
			findings = append(findings, warn("enterprise_requires_stable_jwt_secret", "enterprise profile should set JWT_SECRET or equivalent stable signing config"))
		}
		if !cfg.TenantEnforced {
			findings = append(findings, warn("enterprise_requires_tenant_enforcement", "enterprise profile should enforce tenant isolation"))
		}
		if !cfg.AuditSinkSet {
			findings = append(findings, warn("enterprise_requires_audit_sink", "enterprise profile should configure an audit sink"))
		}
	default:
		findings = append(findings, warn("unknown_profile", "unknown LEVARA_PROFILE; using current runtime behavior"))
	}
	return findings
}

func isPostgres(cfg Config) bool {
	return cfg.HasDB && strings.EqualFold(cfg.DBProvider, "postgres")
}

func warn(code, msg string) Finding {
	return Finding{Level: "warn", Code: code, Message: msg}
}
