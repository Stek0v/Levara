package main

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/audit"
)

func TestOperatorReplayAndDiscardAreExplicitAndIdempotent(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := audit.NewSQLSpool(db, audit.SpoolConfig{Dialect: "sqlite", DestinationID: "siem"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	e := audit.EnvelopeFromEntry(audit.Entry{Tool: "search", Outcome: audit.OutcomeOK})
	if err := s.Admit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE audit_spool_events SET state='dead'"); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "AUDIT_DB_DSN" {
			return dsn
		}
		if k == "AUDIT_DB_DRIVER" {
			return "sqlite"
		}
		return ""
	}
	var out bytes.Buffer
	if err := run([]string{"dead-letter", "--destination=siem"}, env, &out); err != nil || !strings.Contains(out.String(), e.EventID) {
		t.Fatalf("list %s %v", out.String(), err)
	}
	if err := run([]string{"discard", "--destination=siem"}, env, &out); err == nil {
		t.Fatal("implicit all-events discard accepted")
	}
	for _, want := range []string{`"affected":1`, `"affected":0`} {
		out.Reset()
		if err := run([]string{"replay", "--destination=siem", "--event-ids=" + e.EventID}, env, &out); err != nil || !strings.Contains(out.String(), want) {
			t.Fatalf("replay %s %v", out.String(), err)
		}
	}
	if _, err := db.Exec("UPDATE audit_spool_events SET state='dead'"); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"discard", "--destination=siem", "--event-ids=" + e.EventID}, env, &out); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Stats(context.Background())
	if err != nil || stats.Dead != 0 || stats.Pending != 0 {
		t.Fatalf("stats=%+v %v", stats, err)
	}
}
