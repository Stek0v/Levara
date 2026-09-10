package backup

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// PgDump runs pg_dump to export PostgreSQL database to a SQL file.
func PgDump(dsn, output string) error {
	args, password, err := parseDSNToArgs(dsn)
	if err != nil {
		return err
	}
	args = append(args, "--format=plain", "--no-owner", "--no-acl", "-f", output)

	cmd := exec.Command("pg_dump", args...)
	setPgPassword(cmd, password)
	// Client diagnostics can echo connection credentials; do not relay them.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}
	log.Printf("[backup] pg_dump complete: %s", output)
	return nil
}

// PgRestore restores PostgreSQL database from a SQL file.
func PgRestore(dsn, input string) error {
	args, password, err := parseDSNToArgs(dsn)
	if err != nil {
		return err
	}
	// Plain pg_dump output is transaction-compatible (no --create). Ignore a
	// local psqlrc so it cannot disable error handling or add side effects.
	args = append(args, "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--single-transaction", "-f", input)

	cmd := exec.Command("psql", args...)
	setPgPassword(cmd, password)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("psql restore: %w", err)
	}
	log.Printf("[backup] pg_restore complete from %s", input)
	return nil
}

// setPgPassword preserves an explicit empty password as well as a nonempty one.
func setPgPassword(cmd *exec.Cmd, password *string) {
	if password == nil {
		return
	}
	cmd.Env = append(os.Environ(), "PGPASSWORD="+*password)
}

// parseDSNToArgs converts postgres://user:***@host:port/dbname to pg_dump args
// and returns the DSN password (nil when absent) for PGPASSWORD.
func parseDSNToArgs(dsn string) ([]string, *string, error) {
	// Handle both formats:
	// postgres://user:pass@host:port/dbname
	// host=localhost port=5433 user=levara password=<change-me> dbname=levara
	var args []string
	var password *string

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		// libpq authorities allow encoded socket paths and mixed host/port lists
		// that net/url rejects. Parse the other URI components with a placeholder
		// host, and keep the original authority for libpq to interpret.
		scheme, rest, _ := strings.Cut(dsn, "://")
		authority, suffix := rest, ""
		if end := strings.IndexAny(rest, "/?#"); end >= 0 {
			authority, suffix = rest[:end], rest[end:]
		}
		userinfo, host := "", authority
		if at := strings.LastIndexByte(authority, '@'); at >= 0 {
			userinfo, host = authority[:at+1], authority[at+1:]
		}
		u, err := url.Parse(scheme + "://" + userinfo + "localhost" + suffix)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid PostgreSQL connection URI")
		}
		if u.User != nil {
			if value, ok := u.User.Password(); ok {
				password = &value
				username, _, _ := strings.Cut(userinfo, ":")
				authority = username + "@" + host
			}
		}
		// libpq uses percent decoding, not form decoding: '+' is literal.
		// Preserve other parameters byte-for-byte, including duplicate order.
		var query []string
		for _, part := range strings.Split(u.RawQuery, "&") {
			key, value, hasValue := strings.Cut(part, "=")
			key, err = url.PathUnescape(key)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid PostgreSQL connection URI query")
			}
			if key == "password" {
				value, err = url.PathUnescape(value)
				if err != nil || !hasValue {
					return nil, nil, fmt.Errorf("invalid PostgreSQL connection URI password")
				}
				password = &value
			} else {
				query = append(query, part)
			}
		}
		pathAndQuery, fragment, hasFragment := strings.Cut(suffix, "#")
		rawPath, _, _ := strings.Cut(pathAndQuery, "?")
		sanitized := scheme + "://" + authority + rawPath
		if rawQuery := strings.Join(query, "&"); rawQuery != "" || u.ForceQuery {
			sanitized += "?" + rawQuery
		}
		if hasFragment {
			sanitized += "#" + fragment
		}
		args = append(args, "--dbname", sanitized)
	} else {
		// Key=value format
		parts := strings.Fields(dsn)
		for _, p := range parts {
			kv := strings.SplitN(p, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "host":
				args = append(args, "-h", kv[1])
			case "port":
				args = append(args, "-p", kv[1])
			case "user", "username":
				args = append(args, "-U", kv[1])
			case "dbname":
				args = append(args, "-d", kv[1])
			case "password":
				password = &kv[1]
			}
		}
	}
	return args, password, nil
}
