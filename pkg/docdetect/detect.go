// Package docdetect contains cheap document preflight heuristics used to route
// uploads before expensive extraction pipelines run.
package docdetect

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/stek0v/levara/pkg/extract"
)

// Pipeline is the recommended extraction path for a document.
type Pipeline string

const (
	PipelinePlainIngest    Pipeline = "plain_ingest"
	PipelineLiftStructured Pipeline = "lift_structured"
)

// TableSignal summarizes evidence that a document contains tables.
type TableSignal struct {
	HasTables      bool    `json:"has_tables"`
	Confidence     float64 `json:"confidence"`
	PagesWithTable []int   `json:"pages_with_table,omitempty"`
	Reason         string  `json:"reason,omitempty"`
}

// Analysis is the upload-time document routing result.
type Analysis struct {
	Filename          string      `json:"filename,omitempty"`
	Format            string      `json:"format"`
	TableSignal       TableSignal `json:"table_signal"`
	SchemaProvided    bool        `json:"schema_provided"`
	VisualOnly        bool        `json:"visual_only"`
	Recommended       Pipeline    `json:"recommended_pipeline"`
	StructuredReady   bool        `json:"structured_ready"`
	StructuredReason  string      `json:"structured_reason,omitempty"`
	ExtractedTextSize int         `json:"extracted_text_size,omitempty"`
}

// DetectPDFTables inspects a PDF using the existing extractor's layout chunk
// metadata. It is intentionally cheap compared with a vision model call.
func DetectPDFTables(data []byte, filename string) TableSignal {
	signal := detectPDFTableChunks(data, filename)
	if signal.HasTables || signal.Reason == "not_pdf" {
		return signal
	}
	result, err := extract.Extract(data, filename, "application/pdf")
	if err == nil {
		if textSignal := DetectTablesInText(result.Text); textSignal.HasTables {
			return textSignal
		}
	}
	return signal
}

func detectPDFTableChunks(data []byte, filename string) TableSignal {
	if !isPDF(data, filename) {
		return TableSignal{Reason: "not_pdf"}
	}

	chunks, err := extract.ExtractChunks(data, filename, 1200, 3000, 100, 100)
	if err != nil || len(chunks) == 0 {
		return TableSignal{Reason: "chunk_extract_failed"}
	}

	tableChunks := 0
	pages := make(map[int]bool)
	for _, ch := range chunks {
		if !ch.HasTable {
			continue
		}
		tableChunks++
		if ch.PageStart <= 0 && ch.PageEnd <= 0 {
			continue
		}
		start, end := ch.PageStart, ch.PageEnd
		if start <= 0 {
			start = end
		}
		if end <= 0 {
			end = start
		}
		if end < start {
			start, end = end, start
		}
		for p := start; p <= end; p++ {
			if p > 0 {
				pages[p] = true
			}
		}
	}

	if tableChunks == 0 {
		return TableSignal{Reason: "no_table_chunks"}
	}

	ratio := float64(tableChunks) / float64(len(chunks))
	conf := ratio
	if len(pages) > 0 {
		conf += 0.35
	}
	if tableChunks >= 2 {
		conf += 0.15
	}
	if conf > 1 {
		conf = 1
	}

	outPages := make([]int, 0, len(pages))
	for p := range pages {
		outPages = append(outPages, p)
	}
	sort.Ints(outPages)

	return TableSignal{
		HasTables:      conf >= 0.35,
		Confidence:     conf,
		PagesWithTable: outPages,
		Reason:         "extract_chunk_metadata",
	}
}

// DetectTablesInText catches simple tabular text after a PDF extractor has
// flattened table layout into aligned words. It intentionally favors precision:
// rows need numeric cells, so ordinary prose does not route to lift.
func DetectTablesInText(text string) TableSignal {
	lines := strings.Split(text, "\n")
	numericRows := 0
	headerRows := 0
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields) > 8 {
			continue
		}
		if countNumericFields(fields) > 0 {
			numericRows++
			continue
		}
		if len(fields) >= 2 && len(fields) <= 5 {
			headerRows++
		}
	}

	if numericRows >= 2 {
		conf := 0.4 + float64(numericRows-2)*0.1
		if headerRows > 0 {
			conf += 0.15
		}
		if conf > 0.85 {
			conf = 0.85
		}
		return TableSignal{
			HasTables:  true,
			Confidence: conf,
			Reason:     "text_table_heuristic",
		}
	}
	return TableSignal{Reason: "no_text_table_signal"}
}

// AnalyzePDFForStructuredExtraction combines table detection with the text
// extraction result to choose whether a schema-driven visual extractor is useful.
func AnalyzePDFForStructuredExtraction(data []byte, filename string, extractedText string, schemaProvided bool) Analysis {
	format := detectFormat(data, filename)
	table := TableSignal{Reason: "not_pdf"}
	if format == "pdf" {
		table = detectPDFTableChunks(data, filename)
		if !table.HasTables {
			if signal := DetectTablesInText(extractedText); signal.HasTables {
				table = signal
			}
		}
	}

	textSize := len(strings.TrimSpace(extractedText))
	visualOnly := format == "pdf" && textSize < 40
	recommended := ChoosePipeline(format, table, schemaProvided, visualOnly)

	reason := "schema_not_provided"
	if schemaProvided {
		switch {
		case table.HasTables:
			reason = "pdf_tables_detected"
		case visualOnly:
			reason = "pdf_visual_only"
		default:
			reason = "schema_provided_but_no_table_signal"
		}
	}

	return Analysis{
		Filename:          filename,
		Format:            format,
		TableSignal:       table,
		SchemaProvided:    schemaProvided,
		VisualOnly:        visualOnly,
		Recommended:       recommended,
		StructuredReady:   recommended == PipelineLiftStructured,
		StructuredReason:  reason,
		ExtractedTextSize: textSize,
	}
}

// ChoosePipeline is pure and separately tested so routing thresholds stay stable.
func ChoosePipeline(format string, table TableSignal, schemaProvided, visualOnly bool) Pipeline {
	if format == "pdf" && schemaProvided && (table.HasTables || visualOnly) {
		return PipelineLiftStructured
	}
	return PipelinePlainIngest
}

func detectFormat(data []byte, filename string) string {
	if isPDF(data, filename) {
		return "pdf"
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != "" {
		return strings.TrimPrefix(ext, ".")
	}
	return "unknown"
}

func isPDF(data []byte, filename string) bool {
	if strings.EqualFold(filepath.Ext(filename), ".pdf") {
		return true
	}
	return len(data) >= 4 && data[0] == '%' && data[1] == 'P' && data[2] == 'D' && data[3] == 'F'
}

func countNumericFields(fields []string) int {
	count := 0
	for _, field := range fields {
		if looksNumeric(field) {
			count++
		}
	}
	return count
}

func looksNumeric(s string) bool {
	s = strings.Trim(s, " \t,.$€£¥()%")
	if s == "" {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' || r == ',' || r == '-' || r == '/':
		default:
			return false
		}
	}
	return digits > 0
}
