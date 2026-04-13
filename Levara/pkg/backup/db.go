package backup

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
)

// PgDump runs pg_dump to export PostgreSQL database to a SQL file.
func PgDump(dsn, output string) error {
	// Parse DSN: postgres://user:pass@host:port/dbname
	args := parseDSNToArgs(dsn)
	args = append(args, "--format=plain", "--no-owner", "--no-acl", "-f", output)

	cmd := exec.Command("pg_dump", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump: %w\n%s", err, string(out))
	}
	log.Printf("[backup] pg_dump complete: %s", output)
	return nil
}

// PgRestore restores PostgreSQL database from a SQL file.
func PgRestore(dsn, input string) error {
	args := parseDSNToArgs(dsn)
	args = append(args, "-f", input)

	cmd := exec.Command("psql", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql restore: %w\n%s", err, string(out))
	}
	log.Printf("[backup] pg_restore complete from %s", input)
	return nil
}

// parseDSNToArgs converts postgres://user:pass@host:port/dbname to pg_dump args
func parseDSNToArgs(dsn string) []string {
	// Handle both formats:
	// postgres://user:pass@host:port/dbname
	// host=localhost port=5433 user=levara password=levara dbname=levara
	var args []string

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		// URI format — pass directly
		args = append(args, dsn)
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
				// Set via PGPASSWORD env (handled by caller)
			}
		}
	}
	return args
}
