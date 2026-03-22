// Package temporal provides timestamp extraction from text and temporal search.
//
// Extracts dates/times from natural language text using regex patterns,
// then enables range queries on Neo4j graph nodes with date properties.
//
// Supports: ISO 8601, Russian dates, English dates, relative dates.
package temporal

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Event is a text span with an extracted timestamp.
type Event struct {
	Text      string
	Date      time.Time
	DateStr   string // original date string found in text
	Confidence float32 // 0-1, how confident we are in the extraction
	NodeID    string  // associated graph node ID (if any)
}

// ExtractTimestamps finds dates and timestamps in text.
// Returns events sorted by date.
func ExtractTimestamps(text string, referenceDate time.Time) []Event {
	if referenceDate.IsZero() {
		referenceDate = time.Now()
	}

	var events []Event

	// ISO 8601: 2024-03-15, 2024-03-15T10:30:00
	for _, m := range isoDateRe.FindAllStringIndex(text, -1) {
		dateStr := text[m[0]:m[1]]
		if t, err := time.Parse("2006-01-02", dateStr[:10]); err == nil {
			events = append(events, Event{
				Text: contextAround(text, m[0], m[1], 80),
				Date: t, DateStr: dateStr, Confidence: 1.0,
			})
		}
	}

	// Russian: "15 марта 2024", "март 2024", "5 января"
	for _, m := range ruDateRe.FindAllStringSubmatchIndex(text, -1) {
		if m == nil {
			continue
		}
		full := text[m[0]:m[1]]
		if t, ok := parseRussianDate(full, referenceDate); ok {
			events = append(events, Event{
				Text: contextAround(text, m[0], m[1], 80),
				Date: t, DateStr: full, Confidence: 0.9,
			})
		}
	}

	// English: "March 15, 2024", "15 March 2024", "Jan 2024"
	for _, m := range enDateRe.FindAllStringSubmatchIndex(text, -1) {
		if m == nil {
			continue
		}
		full := text[m[0]:m[1]]
		if t, ok := parseEnglishDate(full, referenceDate); ok {
			events = append(events, Event{
				Text: contextAround(text, m[0], m[1], 80),
				Date: t, DateStr: full, Confidence: 0.9,
			})
		}
	}

	// Slash dates: 15/03/2024, 03/15/2024, 2024/03/15
	for _, m := range slashDateRe.FindAllStringIndex(text, -1) {
		dateStr := text[m[0]:m[1]]
		if t, ok := parseSlashDate(dateStr); ok {
			events = append(events, Event{
				Text: contextAround(text, m[0], m[1], 80),
				Date: t, DateStr: dateStr, Confidence: 0.8,
			})
		}
	}

	// Standalone year: "in 1905", "1905 year", "год 1905" etc.
	// Only match years not already captured by ISO/slash/month patterns.
	for _, m := range yearOnlyRe.FindAllStringIndex(text, -1) {
		yearStr := strings.TrimSpace(text[m[0]:m[1]])
		// Extract just the 4-digit number
		digits := yearDigitRe.FindString(yearStr)
		if digits == "" {
			continue
		}
		y, err := strconv.Atoi(digits)
		if err != nil || y < 1000 || y > 2100 {
			continue
		}
		t := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		events = append(events, Event{
			Text:       contextAround(text, m[0], m[1], 80),
			Date:       t,
			DateStr:    digits,
			Confidence: 0.7,
		})
	}

	// Dedup by date (keep highest confidence)
	events = dedupEvents(events)

	// Sort by date
	sort.Slice(events, func(i, j int) bool {
		return events[i].Date.Before(events[j].Date)
	})

	return events
}

// FilterByRange returns events within [from, to] date range.
func FilterByRange(events []Event, from, to time.Time) []Event {
	var filtered []Event
	for _, e := range events {
		if !e.Date.Before(from) && !e.Date.After(to) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// --- Regex patterns ---

var (
	isoDateRe   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}(?::\d{2})?)?`)
	slashDateRe = regexp.MustCompile(`\d{1,4}[/\.]\d{1,2}[/\.]\d{2,4}`)

	ruMonths = map[string]time.Month{
		"январ": time.January, "феврал": time.February, "март": time.March,
		"апрел": time.April, "мая": time.May, "май": time.May, "июн": time.June,
		"июл": time.July, "август": time.August, "сентябр": time.September,
		"октябр": time.October, "ноябр": time.November, "декабр": time.December,
	}

	enMonths = map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March,
		"apr": time.April, "may": time.May, "jun": time.June,
		"jul": time.July, "aug": time.August, "sep": time.September,
		"oct": time.October, "nov": time.November, "dec": time.December,
	}

	ruDateRe = regexp.MustCompile(`(?i)(\d{1,2}\s+)?(январ[яьи]?|феврал[яьи]?|март[аеу]?|апрел[яьи]?|ма[яйю]|июн[яьи]?|июл[яьи]?|август[аеу]?|сентябр[яьи]?|октябр[яьи]?|ноябр[яьи]?|декабр[яьи]?)(\s+\d{4})?`)
	enDateRe = regexp.MustCompile(`(?i)((?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\w*)\s+(\d{1,2})(?:,?\s+(\d{4}))?|(\d{1,2})\s+((?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\w*)(?:\s+(\d{4}))?`)

	// Standalone year: matches "in 1905", "1905 year", "в 1905", "год 1905"
	// Negative lookbehind/lookahead for dash/slash to avoid matching ISO/slash dates.
	yearOnlyRe = regexp.MustCompile(`(?:^|[^\d/.\-])(\d{4})(?:[^\d/.\-]|$)`)
	yearDigitRe = regexp.MustCompile(`\d{4}`)
)

func parseRussianDate(s string, ref time.Time) (time.Time, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	parts := strings.Fields(s)

	var day int
	var month time.Month
	year := ref.Year()

	for _, p := range parts {
		// Try as number (day or year)
		if n, err := strconv.Atoi(p); err == nil {
			if n > 31 {
				year = n
			} else if day == 0 {
				day = n
			}
			continue
		}
		// Try as month
		for prefix, m := range ruMonths {
			if strings.HasPrefix(p, prefix) {
				month = m
				break
			}
		}
	}

	if month == 0 {
		return time.Time{}, false
	}
	if day == 0 {
		day = 1
	}

	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), true
}

func parseEnglishDate(s string, ref time.Time) (time.Time, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.'
	})

	var day int
	var month time.Month
	year := ref.Year()

	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil {
			if n > 31 {
				year = n
			} else if day == 0 {
				day = n
			}
			continue
		}
		for prefix, m := range enMonths {
			if strings.HasPrefix(p, prefix) {
				month = m
				break
			}
		}
	}

	if month == 0 {
		return time.Time{}, false
	}
	if day == 0 {
		day = 1
	}

	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC), true
}

func parseSlashDate(s string) (time.Time, bool) {
	sep := "/"
	if strings.Contains(s, ".") {
		sep = "."
	}
	parts := strings.Split(s, sep)
	if len(parts) != 3 {
		return time.Time{}, false
	}

	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return time.Time{}, false
		}
		nums[i] = n
	}

	// Detect format: YYYY/MM/DD or DD/MM/YYYY or MM/DD/YYYY
	var y, m, d int
	if nums[0] > 1000 { // YYYY/MM/DD
		y, m, d = nums[0], nums[1], nums[2]
	} else if nums[2] > 1000 { // DD/MM/YYYY
		d, m, y = nums[0], nums[1], nums[2]
	} else { // ambiguous — assume DD/MM/YY
		d, m, y = nums[0], nums[1], nums[2]+2000
	}

	if m < 1 || m > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}

	return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC), true
}

func contextAround(text string, start, end, radius int) string {
	// Work with runes to avoid breaking UTF-8
	runes := []rune(text)
	// Convert byte offsets to rune offsets (approximate)
	runeStart := 0
	runeEnd := len(runes)
	bytePos := 0
	for i, r := range runes {
		if bytePos >= start && runeStart == 0 {
			runeStart = i
		}
		if bytePos >= end {
			runeEnd = i
			break
		}
		bytePos += len(string(r))
	}

	s := runeStart - radius
	if s < 0 {
		s = 0
	}
	e := runeEnd + radius
	if e > len(runes) {
		e = len(runes)
	}
	return strings.TrimSpace(string(runes[s:e]))
}

func dedupEvents(events []Event) []Event {
	seen := make(map[string]int) // dateStr → index in result
	var result []Event
	for _, e := range events {
		if idx, ok := seen[e.DateStr]; ok {
			if e.Confidence > result[idx].Confidence {
				result[idx] = e
			}
		} else {
			seen[e.DateStr] = len(result)
			result = append(result, e)
		}
	}
	return result
}

// TemporalNode represents a temporal event node to be added to the graph.
type TemporalNode struct {
	ID          string
	Name        string // date string
	Type        string // "TemporalEvent"
	Description string // context text around the date
	DateISO     string // ISO 8601 date string for Neo4j property
}

// TemporalEdge represents a HAPPENED_AT edge linking an entity to a temporal node.
type TemporalEdge struct {
	SourceID         string // entity node ID
	TargetID         string // temporal node ID
	RelationshipName string // "HAPPENED_AT"
	EdgeText         string // context
}

// LinkEventsToEntities creates temporal nodes from events and HAPPENED_AT edges
// linking each provided entity node ID to temporal events extracted from the same chunk.
// entityIDs are the IDs of entities extracted from the same text chunk.
// Returns temporal nodes and edges to add to the dedup result.
func LinkEventsToEntities(events []Event, entityIDs []string) ([]TemporalNode, []TemporalEdge) {
	if len(events) == 0 || len(entityIDs) == 0 {
		return nil, nil
	}

	var nodes []TemporalNode
	var edges []TemporalEdge
	seenNodes := make(map[string]bool)

	for _, ev := range events {
		nodeID := fmt.Sprintf("temporal_%s", ev.Date.Format("2006-01-02"))

		if !seenNodes[nodeID] {
			seenNodes[nodeID] = true
			nodes = append(nodes, TemporalNode{
				ID:          nodeID,
				Name:        ev.DateStr,
				Type:        "TemporalEvent",
				Description: ev.Text,
				DateISO:     ev.Date.Format("2006-01-02"),
			})
		}

		for _, entID := range entityIDs {
			edges = append(edges, TemporalEdge{
				SourceID:         entID,
				TargetID:         nodeID,
				RelationshipName: "HAPPENED_AT",
				EdgeText:         fmt.Sprintf("%s happened at %s", entID, ev.DateStr),
			})
		}
	}

	return nodes, edges
}

// DateRangeFromEvents computes the min/max date range from extracted events.
// If only one event, returns that date as both from and to (with to = end of year for year-only).
func DateRangeFromEvents(events []Event) (from, to time.Time, ok bool) {
	if len(events) == 0 {
		return time.Time{}, time.Time{}, false
	}

	from = events[0].Date
	to = events[0].Date

	for _, e := range events[1:] {
		if e.Date.Before(from) {
			from = e.Date
		}
		if e.Date.After(to) {
			to = e.Date
		}
	}

	// If from == to and it's Jan 1 (year-only match), expand to full year
	if from.Equal(to) && from.Month() == time.January && from.Day() == 1 {
		to = time.Date(from.Year(), 12, 31, 23, 59, 59, 0, time.UTC)
	}

	return from, to, true
}
