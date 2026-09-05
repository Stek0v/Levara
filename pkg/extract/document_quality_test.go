package extract

import (
	"os"
	"strings"
	"testing"
)

// Synthetic source documents contain known facts, numbers, and Unicode.
// This checks actual parsing rather than extension recognition or LLM quality.
func TestDocumentQualityFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"report.pdf", []string{"Quarterly report", "Revenue is 123", "Storage costs 35.50"}},
		{"report.docx", []string{"Revenue is 123", "Привет, команда", "Storage", "35.50"}},
		{"report.pptx", []string{"Revenue is 123", "Привет, команда"}},
		{"report.xlsx", []string{"Storage", "35.5", "Выручка", "123"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + tc.name)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Extract(data, tc.name, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got.Text, want) {
					t.Errorf("missing %q in %q", want, got.Text)
				}
			}
			if strings.Contains(got.Text, "<?xml") || strings.ContainsRune(got.Text, 0) {
				t.Errorf("raw container leaked: %q", got.Text)
			}
			t.Logf("format=%s bytes=%d pages=%d warnings=%v", got.Format, len(got.Text), got.Pages, got.Warnings)
		})
	}
	for name, body := range map[string]string{
		"report.html": "<html><body><h1>Отчёт</h1><p>Revenue is 123.</p></body></html>",
		"report.csv":  "Item,Amount\nВыручка,123\n",
		"report.md":   "# Отчёт\n\nRevenue is 123.\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Extract([]byte(body), name, "")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Text, "123") || strings.Contains(got.Text, "<html>") {
				t.Fatalf("text=%q", got.Text)
			}
		})
	}
}
