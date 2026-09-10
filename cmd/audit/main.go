// levara-audit operates the server's durable SIEM queue using operator SQL
// credentials. It does not create schema, change bounds or start a sender.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/audit"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, env func(string) string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: levara-audit status|dead-letter|replay|discard --destination ID [--event-ids ID,ID] [--after ID] [--limit 100]")
	}
	action := args[0]
	if action != "status" && action != "dead-letter" && action != "replay" && action != "discard" {
		return errors.New("unknown audit operation")
	}
	fs := flag.NewFlagSet("levara-audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	destination := fs.String("destination", "", "configured destination ID")
	ids := fs.String("event-ids", "", "explicit dead-letter event IDs")
	after := fs.String("after", "", "exclusive event ID cursor")
	limit := fs.Int("limit", 100, "maximum 100 events")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || *destination == "" || *limit < 1 || *limit > 100 {
		return errors.New("invalid audit arguments")
	}
	if (action == "replay" || action == "discard") != (*ids != "") {
		return errors.New("replay/discard require explicit --event-ids; read operations do not accept it")
	}
	dialect, driver := env("AUDIT_DB_DRIVER"), ""
	switch dialect {
	case "sqlite":
		driver = "sqlite3"
	case "postgres":
		driver = "pgx"
	default:
		return errors.New("AUDIT_DB_DRIVER must be sqlite or postgres")
	}
	dsn := env("AUDIT_DB_DSN")
	if dsn == "" {
		return errors.New("AUDIT_DB_DSN is required")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return errors.New("audit database unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	spool, err := audit.NewSQLSpool(db, audit.SpoolConfig{Dialect: dialect, DestinationID: *destination})
	if err != nil {
		return errors.New("invalid audit destination")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var result any
	switch action {
	case "status":
		result, err = spool.Stats(ctx)
	case "dead-letter":
		result, err = spool.DeadLetters(ctx, *after, *limit)
	default:
		var count int64
		count, err = spool.ResolveDeadLetters(ctx, strings.Split(*ids, ","), action == "discard")
		result = map[string]any{"operation": action, "affected": count}
	}
	if err != nil {
		return errors.New("audit operation failed; verify database, destination and event IDs")
	}
	return json.NewEncoder(out).Encode(result)
}
