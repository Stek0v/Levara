package chunker

import (
	"testing"
)

func TestDetectSections_MarkdownH2(t *testing.T) {
	text := "## Authentication\n\nJWT tokens are used.\n\n## Authorization\n\nRBAC is implemented."
	sections := DetectSections(text)

	if len(sections) != 2 {
		t.Fatalf("Expected 2 sections, got %d: %+v", len(sections), sections)
	}
	if sections[0].Name != "Authentication" {
		t.Errorf("Section 0: got %q, want 'Authentication'", sections[0].Name)
	}
	if sections[1].Name != "Authorization" {
		t.Errorf("Section 1: got %q, want 'Authorization'", sections[1].Name)
	}
}

func TestDetectSections_MarkdownMultiLevel(t *testing.T) {
	text := "# Main Title\n\nIntro\n\n## Section One\n\nContent\n\n### Subsection\n\nDetail\n\n#### Deep\n\nMore"
	sections := DetectSections(text)

	if len(sections) < 4 {
		t.Fatalf("Expected 4 sections, got %d: %+v", len(sections), sections)
	}
	names := make([]string, len(sections))
	for i, s := range sections {
		names[i] = s.Name
	}
	t.Logf("Detected: %v", names)
}

func TestDetectSections_TitleCase(t *testing.T) {
	text := "Authentication\n\nJWT tokens are used for auth.\n\nSession Management\n\nSessions stored in Redis."
	sections := DetectSections(text)

	if len(sections) < 2 {
		t.Fatalf("Expected at least 2 sections, got %d: %+v", len(sections), sections)
	}
	if sections[0].Name != "Authentication" {
		t.Errorf("Section 0: got %q, want 'Authentication'", sections[0].Name)
	}
}

func TestDetectSections_NoSections(t *testing.T) {
	text := "This is just a plain paragraph without any section headers. It goes on and on without structure."
	sections := DetectSections(text)

	if len(sections) != 0 {
		t.Errorf("Expected 0 sections for plain text, got %d: %+v", len(sections), sections)
	}
}

func TestDetectSections_MultipleSections(t *testing.T) {
	text := "## Intro\n\nSome intro text.\n\n## Methods\n\nMethodology here.\n\n## Results\n\nFindings."
	sections := DetectSections(text)

	if len(sections) != 3 {
		t.Fatalf("Expected 3 sections, got %d", len(sections))
	}
	expected := []string{"Intro", "Methods", "Results"}
	for i, s := range sections {
		if s.Name != expected[i] {
			t.Errorf("Section %d: got %q, want %q", i, s.Name, expected[i])
		}
	}

	// Offsets should be monotonically increasing
	for i := 1; i < len(sections); i++ {
		if sections[i].Offset <= sections[i-1].Offset {
			t.Errorf("Offsets not increasing: %d <= %d", sections[i].Offset, sections[i-1].Offset)
		}
	}
}

func TestDetectSections_Cyrillic(t *testing.T) {
	text := "## Аутентификация\n\nJWT токены используются.\n\n## Авторизация\n\nRBAC реализован."
	sections := DetectSections(text)

	if len(sections) != 2 {
		t.Fatalf("Expected 2 Cyrillic sections, got %d: %+v", len(sections), sections)
	}
	if sections[0].Name != "Аутентификация" {
		t.Errorf("Section 0: got %q, want 'Аутентификация'", sections[0].Name)
	}
}

func TestDetectSections_CyrillicTitleCase(t *testing.T) {
	text := "Аутентификация\n\nJWT токены.\n\nАвторизация\n\nRBAC."
	sections := DetectSections(text)

	if len(sections) < 2 {
		t.Fatalf("Expected 2 Cyrillic title-case sections, got %d: %+v", len(sections), sections)
	}
}

func TestDetectSections_MixedFormats(t *testing.T) {
	text := "## Overview\n\nIntro text.\n\nImplementation Details\n\nCode lives here.\n\n### API Layer\n\nHTTP handlers."
	sections := DetectSections(text)

	// Should detect: "Overview" (markdown), "Implementation Details" (title-case), "API Layer" (markdown)
	if len(sections) < 3 {
		t.Fatalf("Expected at least 3 mixed sections, got %d: %+v", len(sections), sections)
	}

	names := make(map[string]bool)
	for _, s := range sections {
		names[s.Name] = true
	}
	if !names["Overview"] {
		t.Error("Missing 'Overview' section")
	}
	if !names["Implementation Details"] {
		t.Error("Missing 'Implementation Details' section")
	}
	if !names["API Layer"] {
		t.Error("Missing 'API Layer' section")
	}
}

func TestDetectSections_EmptyText(t *testing.T) {
	sections := DetectSections("")
	if sections != nil {
		t.Errorf("Expected nil for empty text, got %+v", sections)
	}
}

func TestDetectSections_ChapterHeaders(t *testing.T) {
	text := "Глава 1\n\nПервый параграф.\n\nГлава 2\n\nВторой параграф."
	sections := DetectSections(text)

	if len(sections) < 2 {
		t.Fatalf("Expected at least 2 chapter sections, got %d: %+v", len(sections), sections)
	}
}

func TestFindSectionForOffset(t *testing.T) {
	sections := []SectionBoundary{
		{Offset: 0, Name: "Intro"},
		{Offset: 50, Name: "Methods"},
		{Offset: 120, Name: "Results"},
	}

	tests := []struct {
		offset int
		want   string
	}{
		{0, "Intro"},
		{25, "Intro"},
		{50, "Methods"},
		{100, "Methods"},
		{120, "Results"},
		{200, "Results"},
	}

	for _, tc := range tests {
		got := FindSectionForOffset(sections, tc.offset)
		if got != tc.want {
			t.Errorf("FindSectionForOffset(%d) = %q, want %q", tc.offset, got, tc.want)
		}
	}
}

func TestIsTitleCaseLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Authentication", true},
		{"Session Management", true},
		{"API Layer Design", true},
		{"lowercase line", false},          // starts lowercase
		{"A", false},                        // too short
		{"This is a sentence.", false},      // ends with period
		{"What is this?", false},            // ends with ?
		{"Hello: world", true},              // colon mid-line, still looks like title
		{"ALLCAPS HEADER", true},            // all caps is title-case
		{"Аутентификация", true},            // Cyrillic
		{"Раздел Первый", true},             // Cyrillic multi-word
		{"{{{json stuff}}}", false},          // non-alpha
		{"", false},                          // empty
	}

	for _, tc := range tests {
		got := isTitleCaseLine(tc.line)
		if got != tc.want {
			t.Errorf("isTitleCaseLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}
