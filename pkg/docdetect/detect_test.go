package docdetect

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestChoosePipeline(t *testing.T) {
	cases := []struct {
		name           string
		format         string
		table          TableSignal
		schemaProvided bool
		visualOnly     bool
		want           Pipeline
	}{
		{
			name:           "pdf with tables and schema routes to lift",
			format:         "pdf",
			table:          TableSignal{HasTables: true, Confidence: 0.6},
			schemaProvided: true,
			want:           PipelineLiftStructured,
		},
		{
			name:           "pdf with tables but no schema stays plain",
			format:         "pdf",
			table:          TableSignal{HasTables: true, Confidence: 0.6},
			schemaProvided: false,
			want:           PipelinePlainIngest,
		},
		{
			name:           "visual-only pdf with schema routes to lift",
			format:         "pdf",
			schemaProvided: true,
			visualOnly:     true,
			want:           PipelineLiftStructured,
		},
		{
			name:           "non-pdf never routes to lift",
			format:         "docx",
			table:          TableSignal{HasTables: true, Confidence: 0.9},
			schemaProvided: true,
			want:           PipelinePlainIngest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChoosePipeline(tc.format, tc.table, tc.schemaProvided, tc.visualOnly)
			if got != tc.want {
				t.Fatalf("ChoosePipeline = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAnalyzePDFForStructuredExtraction_VisualOnlyFallback(t *testing.T) {
	a := AnalyzePDFForStructuredExtraction([]byte("%PDF-1.4\n"), "scan.pdf", "", true)
	if !a.VisualOnly {
		t.Fatal("VisualOnly = false, want true for empty extracted text")
	}
	if a.Recommended != PipelineLiftStructured {
		t.Fatalf("Recommended = %q, want %q", a.Recommended, PipelineLiftStructured)
	}
	if a.StructuredReason != "pdf_visual_only" {
		t.Fatalf("StructuredReason = %q, want pdf_visual_only", a.StructuredReason)
	}
}

func TestDetectPDFTables_RealPDFWithTable(t *testing.T) {
	pdfPath := generatePDFWithReportLab(t, true)

	data := readFile(t, pdfPath)
	signal := DetectPDFTables(data, filepath.Base(pdfPath))
	if !signal.HasTables {
		t.Fatalf("HasTables = false, want true; signal=%+v", signal)
	}
	if signal.Confidence < 0.35 {
		t.Fatalf("Confidence = %.2f, want >= 0.35; signal=%+v", signal.Confidence, signal)
	}

	analysis := AnalyzePDFForStructuredExtraction(data, filepath.Base(pdfPath), "invoice table text", true)
	if analysis.Recommended != PipelineLiftStructured {
		t.Fatalf("Recommended = %q, want %q; analysis=%+v", analysis.Recommended, PipelineLiftStructured, analysis)
	}
}

func TestDetectPDFTables_RealNarrativePDF(t *testing.T) {
	pdfPath := generatePDFWithReportLab(t, false)

	data := readFile(t, pdfPath)
	signal := DetectPDFTables(data, filepath.Base(pdfPath))
	if signal.HasTables {
		t.Fatalf("HasTables = true, want false for narrative PDF; signal=%+v", signal)
	}

	analysis := AnalyzePDFForStructuredExtraction(data, filepath.Base(pdfPath), "This is a normal paragraph with enough text to avoid visual-only fallback.", true)
	if analysis.Recommended != PipelinePlainIngest {
		t.Fatalf("Recommended = %q, want %q; analysis=%+v", analysis.Recommended, PipelinePlainIngest, analysis)
	}
}

func generatePDFWithReportLab(t *testing.T, withTable bool) string {
	t.Helper()
	python := os.Getenv("LEVARA_TEST_PYTHON")
	if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not available for real PDF fixture generation")
		}
	}

	out := filepath.Join(t.TempDir(), "fixture.pdf")
	mode := "narrative"
	if withTable {
		mode = "table"
	}
	script := `
import sys
try:
    from reportlab.lib import colors
    from reportlab.lib.pagesizes import letter
    from reportlab.platypus import SimpleDocTemplate, Paragraph, Spacer, Table, TableStyle
    from reportlab.lib.styles import getSampleStyleSheet
except Exception as exc:
    print(exc, file=sys.stderr)
    sys.exit(17)

out, mode = sys.argv[1], sys.argv[2]
doc = SimpleDocTemplate(out, pagesize=letter)
styles = getSampleStyleSheet()
story = [Paragraph("Levara extraction fixture", styles["Title"]), Spacer(1, 12)]
if mode == "table":
    rows = [["Item", "Qty", "Amount"], ["Compute", "2", "120.00"], ["Storage", "1", "35.50"], ["Total", "", "155.50"]]
    table = Table(rows, colWidths=[220, 80, 100])
    table.setStyle(TableStyle([
        ("GRID", (0, 0), (-1, -1), 1, colors.black),
        ("BACKGROUND", (0, 0), (-1, 0), colors.lightgrey),
        ("ALIGN", (1, 1), (-1, -1), "RIGHT"),
    ]))
    story.append(table)
else:
    story.append(Paragraph("This page contains a regular paragraph without grid lines, repeated columns, or invoice style rows. It should remain on the ordinary plain ingest route.", styles["BodyText"]))
doc.build(story)
`
	cmd := exec.Command(python, "-c", script, out, mode)
	if output, err := cmd.CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 17 {
			t.Skipf("reportlab not available for real PDF fixture generation: %s", output)
		}
		t.Fatalf("generate pdf: %v\n%s", err, output)
	}
	return out
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
