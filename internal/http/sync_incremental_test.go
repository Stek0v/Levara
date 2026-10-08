package http

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestSyncIncrementalNativeTimestampBoundary(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx := context.Background()
			for _, row := range []struct{ id, stamp string }{
				{"space-later", "2026-10-07 12:00:00+00:00"},
				{"equal", "2026-10-07T11:00:00Z"},
				{"fraction", "2026-10-07T11:00:00.000001Z"},
				{"offset-earlier", "2026-10-07T13:00:00+03:00"},
				{"earlier", "2026-10-07T10:00:00Z"},
			} {
				if _, err := cfg.DB.Exec(Q("INSERT INTO memories(id,key,value,created_at,updated_at) VALUES($1,$2,$3,$4,$5)"), row.id, row.id, "value", row.stamp, row.stamp); err != nil {
					t.Fatal(err)
				}
				if _, err := cfg.DB.Exec(Q("INSERT INTO interactions(id,query,created_at) VALUES($1,$2,$3)"), row.id, "query", row.stamp); err != nil {
					t.Fatal(err)
				}
			}
			wanted := []string{"equal", "fraction", "space-later"}
			for _, since := range []string{"2026-10-07T11:00:00Z", "2026-10-07T14:00:00+03:00"} {
				memories, err := exportSyncMemories(ctx, cfg, since)
				if err != nil {
					t.Fatal(err)
				}
				interactions, err := exportSyncInteractions(ctx, cfg, since)
				if err != nil {
					t.Fatal(err)
				}
				var mid, iid []string
				for _, m := range memories {
					mid = append(mid, m.ID)
				}
				for _, i := range interactions {
					iid = append(iid, i.ID)
				}
				sort.Strings(mid)
				sort.Strings(iid)
				if !reflect.DeepEqual(mid, wanted) || !reflect.DeepEqual(iid, wanted) {
					t.Fatalf("since %s: memories=%v interactions=%v", since, mid, iid)
				}
				repeated, err := importSyncInteractions(ctx, cfg, interactions)
				if err != nil || repeated["imported"] != 0 || repeated["skipped"] != len(wanted) {
					t.Fatalf("boundary replay %v %v", repeated, err)
				}
			}
			for _, since := range []string{"invalid", "2026-99-01T00:00:00Z"} {
				if _, err := exportSyncMemories(ctx, cfg, since); err == nil {
					t.Fatalf("invalid since accepted: %s", since)
				}
				if _, err := exportSyncInteractions(ctx, cfg, since); err == nil {
					t.Fatal("invalid interaction since accepted")
				}
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("incremental export leaked pool-one connection")
			}
		})
	}
}
