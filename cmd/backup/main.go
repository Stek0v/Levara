// levara-backup — CLI for backup/restore of Levara data.
//
// Usage:
//
//	levara-backup full     --data-dir /path --db-dsn postgres://... --output backup.tar.gz
//	levara-backup restore  --input backup.tar.gz --data-dir /path --db-dsn postgres://...
//	levara-backup export   --server http://localhost:8080 --collection "уца" --output col.json
//	levara-backup import   --server http://localhost:8080 --input col.json
//	levara-backup db-dump  --db-dsn postgres://... --output db.sql
//	levara-backup db-restore --db-dsn postgres://... --input db.sql
//	levara-backup list     --server http://localhost:8080
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/stek0v/levara/pkg/backup"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	if cmd == "verified" || cmd == "verify" || cmd == "status" {
		if err := runVerifiedCommand(context.Background(), cmd, os.Args[2:], os.Stdout); err != nil {
			log.Printf("%v", err)
			os.Exit(1)
		}
		return
	}

	// Common flags
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "Levara data directory")
	dbDSN := fs.String("db-dsn", "", "PostgreSQL DSN (postgres://user:pass@host:port/db)")
	output := fs.String("output", "", "Output file path")
	input := fs.String("input", "", "Input file path")
	server := fs.String("server", "http://localhost:8080", "Levara server URL")
	collection := fs.String("collection", "", "Collection name")

	_ = fs.Parse(os.Args[2:])

	switch cmd {
	case "full":
		if *dataDir == "" || *output == "" {
			log.Fatal("--data-dir and --output required")
		}
		if *output == "" {
			*output = fmt.Sprintf("levara-backup-%s.tar.gz", time.Now().Format("2006-01-02T150405"))
		}
		if err := backup.FullBackup(*dataDir, *dbDSN, *output); err != nil {
			log.Fatalf("backup failed: %v", err)
		}

	case "restore":
		if *input == "" || *dataDir == "" {
			log.Fatal("--input and --data-dir required")
		}
		if err := backup.FullRestore(*input, *dataDir, *dbDSN); err != nil {
			log.Fatalf("restore failed: %v", err)
		}

	case "export":
		if *collection == "" || *output == "" {
			log.Fatal("--collection and --output required")
		}
		if err := backup.ExportCollection(*server, *collection, *output); err != nil {
			log.Fatalf("export failed: %v", err)
		}

	case "import":
		if *input == "" {
			log.Fatal("--input required")
		}
		if err := backup.ImportCollection(*server, *input); err != nil {
			log.Fatalf("import failed: %v", err)
		}

	case "db-dump":
		if *dbDSN == "" || *output == "" {
			log.Fatal("--db-dsn and --output required")
		}
		if err := backup.PgDump(*dbDSN, *output); err != nil {
			log.Fatalf("db-dump failed: %v", err)
		}

	case "db-restore":
		if *dbDSN == "" || *input == "" {
			log.Fatal("--db-dsn and --input required")
		}
		if err := backup.PgRestore(*dbDSN, *input); err != nil {
			log.Fatalf("db-restore failed: %v", err)
		}

	case "list":
		if err := backup.ListData(*server); err != nil {
			log.Fatalf("list failed: %v", err)
		}

	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`levara-backup — Levara data backup/restore CLI

Commands:
  verified   Offline local snapshot + isolated restore verification
  verify     Verify an existing verified archive in fresh temporary resources
  status     JSON receipt of the last successful verified restore
  full       Legacy component archive (use verified for restore proof)
  restore    Restore a legacy component archive
  export     Export single collection (via API)
  import     Import collection (via API, re-embeds)
  db-dump    PostgreSQL dump only
  db-restore PostgreSQL restore only
  list       Show what data exists

Flags:
  --data-dir    Levara data directory
  --db-dsn      PostgreSQL connection string
  --output      Output file
  --input       Input file
  --server      Levara HTTP URL (default: http://localhost:8080)
  --collection  Collection name (for export/import)

Examples:
  levara-backup full --data-dir ./data --db-dsn "postgres://levara:levara@localhost:5433/levara" --output backup.tar.gz
  levara-backup restore --input backup.tar.gz --data-dir ./data-new --db-dsn "postgres://levara:levara@localhost:5433/levara"
  levara-backup export --collection "my_data" --output my_data.json
  levara-backup list`)
}

// Verified commands require explicit runtime inventory. PostgreSQL credentials
// come from the environment and are never echoed or persisted in receipts.
func runVerifiedCommand(ctx context.Context, command string, args []string, out io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(out, `Verified offline commands:
  verified --data-dir PATH --output-dir PATH --db-provider sqlite|postgres --standalone=true --node-id ID --shards N --dim N
  verify --input ARCHIVE
  status --output-dir PATH

Optional inventory flags: --workspace-path PATH, --uploads-path PATH, --sqlite-path PATH,
  --storage-backend local, --neo4j-url URL (external Neo4j is rejected).
PostgreSQL: DATABASE_URL or POSTGRES_DSN environment variable; --postgres-bin-dir PATH.
Limits: --timeout 15m, --max-bytes 4294967296, --max-files 100000, --max-rows 100000.
The output directory must be outside every source root. A running writer is rejected.
Verification restores fresh resources; it never targets an existing database.
Only successful verified captures advance last_successful_restore.json.`)
		return err
	}

	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o backup.VerifiedOptions
	var input string
	fs.StringVar(&o.DataDir, "data-dir", os.Getenv("LEVARA_DATA_DIR"), "Local data root")
	fs.StringVar(&o.WorkspacePath, "workspace-path", "", "Workspace root (default data-dir/workspace)")
	fs.StringVar(&o.UploadsPath, "uploads-path", "", "Artifact root (default data-dir/uploads)")
	fs.StringVar(&o.SQLitePath, "sqlite-path", "", "SQLite file (default data-dir/levara.db)")
	fs.StringVar(&o.DBProvider, "db-provider", os.Getenv("DB_PROVIDER"), "sqlite or postgres")
	fs.StringVar(&o.NodeID, "node-id", "", "Exact server node ID")
	fs.IntVar(&o.Shards, "shards", 0, "Exact server shard count")
	fs.IntVar(&o.Dimension, "dim", 0, "Exact default vector dimension")
	fs.BoolVar(&o.Standalone, "standalone", false, "Confirm one standalone node")
	fs.StringVar(&o.StorageBackend, "storage-backend", os.Getenv("STORAGE_BACKEND"), "Must be local")
	fs.StringVar(&o.Neo4jURL, "neo4j-url", os.Getenv("NEO4J_URL"), "Configured external Neo4j (unsupported)")
	fs.StringVar(&o.OutputDir, "output-dir", "", "Dedicated archive and status directory outside all sources")
	fs.StringVar(&input, "input", "", "Verified archive path")
	fs.StringVar(&o.PostgresBinDir, "postgres-bin-dir", "", "Directory containing PostgreSQL native tools")
	fs.DurationVar(&o.Timeout, "timeout", 15*time.Minute, "Total operation deadline")
	fs.Int64Var(&o.MaxBytes, "max-bytes", 4<<30, "Maximum uncompressed inventory bytes")
	fs.IntVar(&o.MaxFiles, "max-files", 100000, "Maximum inventory files/directories")
	fs.IntVar(&o.MaxRows, "max-rows", 100000, "Maximum SQL rows per table and index operations")
	if err := fs.Parse(args); err != nil {
		return errors.New("backup: invalid verified command arguments; use the documented flags")
	}
	if fs.NArg() != 0 {
		return errors.New("backup: unexpected positional argument")
	}
	o.PostgresDSN = os.Getenv("DATABASE_URL")
	other := os.Getenv("POSTGRES_DSN")
	if o.PostgresDSN != "" && other != "" && other != o.PostgresDSN {
		return errors.New("backup: conflicting DATABASE_URL and POSTGRES_DSN")
	}
	if o.PostgresDSN == "" {
		o.PostgresDSN = other
	}
	var receipt backup.RestoreReceipt
	var err error
	switch command {
	case "verified":
		receipt, err = backup.CreateVerifiedBackup(ctx, o)
	case "verify":
		if input == "" {
			return errors.New("backup: --input required")
		}
		receipt, err = backup.VerifyArchive(ctx, input, o)
	case "status":
		if o.OutputDir == "" {
			return errors.New("backup: --output-dir required")
		}
		receipt, err = backup.ReadLastSuccessfulRestore(o.OutputDir)
	default:
		return errors.New("backup: unknown verified command")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(receipt)
}
