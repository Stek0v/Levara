package trajectory

import (
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/audit"
)

func ev(id, ts, tool string) Event {
	return Event{ID: id, TS: ts, Tool: tool, Outcome: string(audit.OutcomeOK), ClientName: "codex", Collection: "levara"}
}

func ts(base time.Time, d time.Duration) string {
	return base.Add(d).UTC().Format(time.RFC3339Nano)
}

func TestBuildGroupsByTraceIDBeforeSession(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	a := ev("1", ts(base, time.Second), "search")
	a.TraceID = "tr"
	a.SessionID = "s1"
	b := ev("2", ts(base, 2*time.Second), "save_memory")
	b.TraceID = "tr"
	b.SessionID = "s2"
	got := Build([]Event{b, a}, true)
	if len(got) != 1 || got[0].ID != "trace:tr" {
		t.Fatalf("groups = %#v, want one trace group", got)
	}
	if got[0].Counters.SearchCount != 1 || got[0].Counters.SaveCount != 1 {
		t.Fatalf("counters = %+v", got[0].Counters)
	}
	if got[0].Events[0].ID != "1" || got[0].Events[1].ID != "2" {
		t.Fatalf("events not sorted: %+v", got[0].Events)
	}
}

func TestBuildGroupsBySessionID(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	a := ev("1", ts(base, 0), "recall_memory")
	a.SessionID = "s"
	b := ev("2", ts(base, time.Second), "save_memory")
	b.SessionID = "s"
	got := Build([]Event{a, b}, false)
	if len(got) != 1 || got[0].ID != "session:s" {
		t.Fatalf("groups = %#v", got)
	}
	if got[0].EventCount != 2 || len(got[0].Events) != 0 {
		t.Fatalf("event count/events = %d/%d", got[0].EventCount, len(got[0].Events))
	}
}

func TestBuildFallbackWindowDoesNotMixCollections(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	a := ev("1", ts(base, 0), "search")
	b := ev("2", ts(base, time.Minute), "search")
	b.Collection = "other"
	c := ev("3", ts(base, 31*time.Minute), "search")
	got := Build([]Event{a, b, c}, true)
	if len(got) != 3 {
		t.Fatalf("groups = %d, want 3", len(got))
	}
}

func TestBuildFallbackWindowGroupsSameClientCollectionWithinThirtyMinutes(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	a := ev("1", ts(base, 0), "search")
	b := ev("2", ts(base, 29*time.Minute), "save_memory")
	c := ev("3", ts(base, 29*time.Minute), "recall_memory")
	c.ClientName = "claude"
	got := Build([]Event{c, b, a}, true)
	if len(got) != 2 {
		t.Fatalf("groups=%d want 2", len(got))
	}
	var codex Trajectory
	for _, tr := range got {
		if tr.ClientName == "codex" {
			codex = tr
		}
	}
	if codex.ID == "" || codex.EventCount != 2 || codex.Counters.SearchCount != 1 || codex.Counters.SaveCount != 1 {
		t.Fatalf("codex fallback group=%+v", codex)
	}
}

func TestBuildOrdersTrajectoriesNewestFirstAndComputesDuration(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	old1 := ev("old1", ts(base, 0), "search")
	old1.TraceID = "old"
	old2 := ev("old2", ts(base, 1500*time.Millisecond), "save_memory")
	old2.TraceID = "old"
	newer := ev("new", ts(base, time.Hour), "recall_memory")
	newer.TraceID = "new"
	got := Build([]Event{old2, newer, old1}, true)
	if len(got) != 2 {
		t.Fatalf("groups=%d", len(got))
	}
	if got[0].ID != "trace:new" || got[1].ID != "trace:old" {
		t.Fatalf("order=%s,%s", got[0].ID, got[1].ID)
	}
	if got[1].DurationMS != 1500 {
		t.Fatalf("duration=%d want 1500", got[1].DurationMS)
	}
}

func TestBuildEmpty(t *testing.T) {
	if got := Build(nil, true); len(got) != 0 {
		t.Fatalf("got %d trajectories", len(got))
	}
}
