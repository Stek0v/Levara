package trajectory

import (
	"fmt"
	"sort"
	"time"

	"github.com/stek0v/levara/pkg/audit"
)

const fallbackWindow = 30 * time.Minute

type Event = audit.EventRow

type Counters struct {
	SearchCount     int `json:"search_count"`
	RecallCount     int `json:"recall_count"`
	SaveCount       int `json:"save_count"`
	ZeroResultCount int `json:"zero_result_count"`
	ErrorCount      int `json:"error_count"`
	RequestBytes    int `json:"request_bytes"`
	ResponseBytes   int `json:"response_bytes"`
}

type Trajectory struct {
	ID          string    `json:"id"`
	StartedAt   string    `json:"started_at"`
	EndedAt     string    `json:"ended_at"`
	DurationMS  int64     `json:"duration_ms"`
	ClientName  string    `json:"client_name,omitempty"`
	Toolset     string    `json:"toolset,omitempty"`
	Collection  string    `json:"collection,omitempty"`
	EventCount  int       `json:"event_count"`
	Counters    Counters  `json:"counters"`
	Events      []Event   `json:"events,omitempty"`
	startParsed time.Time `json:"-"`
	endParsed   time.Time `json:"-"`
}

func Build(events []Event, includeEvents bool) []Trajectory {
	if len(events) == 0 {
		return []Trajectory{}
	}
	sort.SliceStable(events, func(i, j int) bool {
		return parseTS(events[i].TS).Before(parseTS(events[j].TS))
	})
	groups := map[string]*Trajectory{}
	order := []string{}
	for _, event := range events {
		id := groupID(event)
		tr, ok := groups[id]
		if !ok {
			ts := parseTS(event.TS)
			tr = &Trajectory{ID: id, StartedAt: event.TS, EndedAt: event.TS, ClientName: event.ClientName, Toolset: event.Toolset, Collection: event.Collection, startParsed: ts, endParsed: ts}
			groups[id] = tr
			order = append(order, id)
		}
		applyEvent(tr, event, includeEvents)
	}
	out := make([]Trajectory, 0, len(groups))
	for _, id := range order {
		tr := groups[id]
		tr.EventCount = len(tr.Events)
		if !includeEvents {
			tr.EventCount = tr.Counters.SearchCount + tr.Counters.RecallCount + tr.Counters.SaveCount
			// EventCount must be all events, not just memory/search counters.
			tr.EventCount = countGroupEvents(events, id)
		}
		if tr.endParsed.After(tr.startParsed) {
			tr.DurationMS = tr.endParsed.Sub(tr.startParsed).Milliseconds()
		}
		out = append(out, *tr)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].startParsed.After(out[j].startParsed)
	})
	return out
}

func countGroupEvents(events []Event, id string) int {
	n := 0
	for _, event := range events {
		if groupID(event) == id {
			n++
		}
	}
	return n
}

func applyEvent(tr *Trajectory, event Event, includeEvents bool) {
	ts := parseTS(event.TS)
	if tr.StartedAt == "" || ts.Before(tr.startParsed) {
		tr.StartedAt = event.TS
		tr.startParsed = ts
	}
	if tr.EndedAt == "" || ts.After(tr.endParsed) {
		tr.EndedAt = event.TS
		tr.endParsed = ts
	}
	if tr.ClientName == "" {
		tr.ClientName = event.ClientName
	}
	if tr.Toolset == "" {
		tr.Toolset = event.Toolset
	}
	if tr.Collection == "" {
		tr.Collection = event.Collection
	}
	switch event.Tool {
	case "search", "workspace_search", "cross_search":
		tr.Counters.SearchCount++
	case "recall_memory", "list_memories", "wake_up":
		tr.Counters.RecallCount++
	case "save_memory":
		tr.Counters.SaveCount++
	}
	if event.ZeroResult {
		tr.Counters.ZeroResultCount++
	}
	if event.Outcome != "" && event.Outcome != string(audit.OutcomeOK) {
		tr.Counters.ErrorCount++
	}
	tr.Counters.RequestBytes += event.RequestBytes
	tr.Counters.ResponseBytes += event.ResponseBytes
	if includeEvents {
		tr.Events = append(tr.Events, event)
	}
}

func groupID(event Event) string {
	if event.TraceID != "" {
		return "trace:" + event.TraceID
	}
	if event.SessionID != "" {
		return "session:" + event.SessionID
	}
	ts := parseTS(event.TS)
	bucket := ts.Unix() / int64(fallbackWindow.Seconds())
	return fmt.Sprintf("window:%s:%s:%d", event.ClientName, event.Collection, bucket)
}

func parseTS(raw string) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return ts
}

func Page(items []Trajectory, limit, offset int) []Trajectory {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []Trajectory{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}
