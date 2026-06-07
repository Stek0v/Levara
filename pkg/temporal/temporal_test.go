package temporal

import (
	"testing"
	"time"
)

var ref = time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

func TestExtractISO(t *testing.T) {
	events := ExtractTimestamps("Released on 2024-03-15 and updated 2024-06-01", ref)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Date.Month() != time.March || events[0].Date.Day() != 15 {
		t.Errorf("first event: expected March 15, got %v", events[0].Date)
	}
	if events[1].Date.Month() != time.June {
		t.Errorf("second event: expected June, got %v", events[1].Date)
	}
}

func TestExtractRussian(t *testing.T) {
	events := ExtractTimestamps("В марте 2024 выпустили v1.0. 5 января произошёл инцидент.", ref)
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d: %+v", len(events), events)
	}

	hasJan := false
	hasMar := false
	for _, e := range events {
		if e.Date.Month() == time.January {
			hasJan = true
		}
		if e.Date.Month() == time.March && e.Date.Year() == 2024 {
			hasMar = true
		}
	}
	if !hasMar {
		t.Error("expected March 2024 event")
	}
	if !hasJan {
		t.Error("expected January event")
	}
}

func TestExtractEnglish(t *testing.T) {
	events := ExtractTimestamps("Launched March 15, 2024. Updated on June 1, 2024.", ref)
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(events))
	}
}

func TestExtractSlashDates(t *testing.T) {
	events := ExtractTimestamps("Date: 15/03/2024 and 2024/06/01", ref)
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(events))
	}
}

func TestFilterByRange(t *testing.T) {
	events := []Event{
		{Date: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Text: "jan"},
		{Date: time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC), Text: "mar"},
		{Date: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), Text: "jun"},
		{Date: time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), Text: "dec"},
	}

	from := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)

	filtered := FilterByRange(events, from, to)
	if len(filtered) != 2 {
		t.Errorf("expected 2 events in range, got %d", len(filtered))
	}
}

func TestExtractMixed(t *testing.T) {
	text := `В марте 2024 Levara выпустила v1.0 с HNSW индексом.
On 2024-06-15 gRPC was added to the server.
15/09/2024 — добавлен BM25 поиск.
December 2024 brought the LLM proxy feature.`

	events := ExtractTimestamps(text, ref)
	if len(events) < 3 {
		t.Errorf("expected >= 3 events from mixed text, got %d", len(events))
	}

	// Should be sorted by date
	for i := 1; i < len(events); i++ {
		if events[i].Date.Before(events[i-1].Date) {
			t.Errorf("events not sorted: %v before %v", events[i].Date, events[i-1].Date)
		}
	}

	t.Logf("Extracted %d events:", len(events))
	for _, e := range events {
		t.Logf("  %s → %q", e.Date.Format("2006-01-02"), e.DateStr)
	}
}

func TestNoEvents(t *testing.T) {
	events := ExtractTimestamps("No dates in this text at all", ref)
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func BenchmarkExtract(b *testing.B) {
	text := "Released 2024-03-15. Updated in марте 2024. Version from June 1, 2024. Date 15/09/2024."
	for i := 0; i < b.N; i++ {
		ExtractTimestamps(text, ref)
	}
}
