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
	"flag"
	"fmt"
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
  full       Full backup (collections + uploads + DB → tar.gz)
  restore    Restore from full backup
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
