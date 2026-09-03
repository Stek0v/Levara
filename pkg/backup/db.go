package backup

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// PgDump runs pg_dump to export PostgreSQL database to a SQL file.
func PgDump(dsn, output string) error {
	args, password := parseDSNToArgs(dsn)
	args = append(args, "--format=plain", "--no-owner", "--no-acl", "-f", output)

	cmd := exec.Command("pg_dump", args...)
	setPgPassword(cmd, password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump: %w\n%s", err, string(out))
	}
	log.Printf("[backup] pg_dump complete: %s", output)
	return nil
}

// PgRestore restores PostgreSQL database from a SQL file.
func PgRestore(dsn, input string) error {
	args, password := parseDSNToArgs(dsn)
	args = append(args, "-f", input)

	cmd := exec.Command("psql", args...)
	setPgPassword(cmd, password)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("psql restore: %w\n%s", err, string(out))
	}
	log.Printf("[backup] pg_restore complete from %s", input)
	return nil
}

// setPgPassword exports PGPASSWORD on the command environment when the DSN
	// carried one (finding L6, 2026-09-03 review).
func setPgPassword(cmd *exec.Cmd, password string) {
	if password == "" {
		return
	}
	cmd.Env = append(os.Environ(), "PGPASSWORD="+password)
}

// parseDSNToArgs converts postgres://user:***@host:port/dbname to pg_dump args
// and returns the DSN password ("" when absent) for PGPASSWORD.
func parseDSNToArgs(dsn string) ([]string, string) {
	// Handle both formats:
	// postgres://user:pass@host:port/dbname
	// host=localhost port=5433 user=levara password=<change-me> dbname=levara
	var args []string
	var password string

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
				password = kv[1]
			}
		}
	}
	return args, password
}
