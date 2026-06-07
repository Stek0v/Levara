package classify

import "testing"

// T-9b: pure-fn smoke tests for the document classifier. Locks in the
// extension → DocType mapping that the cognify pipeline uses to pick a
// chunker (paragraph / sentence / row / code).

func TestClassify_ByExtension(t *testing.T) {
	cases := []struct {
		filename string
		wantType DocType
		wantChnk string
	}{
		{"data.csv", TypeTabularData, "row"},
		{"data.tsv", TypeTabularData, "row"},
		{"main.go", TypeCodeFile, "code"},
		{"app.py", TypeCodeFile, "code"},
		{"server.ts", TypeCodeFile, "code"},
		{"slides.pptx", TypePresentation, "paragraph"},
		{"book.xlsx", TypeSpreadsheet, "row"},
		{"README.md", TypeMarkdown, "paragraph"},
		{"sys.log", TypeLogFile, "sentence"},
		{"msg.eml", TypeEmail, "paragraph"},
		{"talk.mp3", TypeAudioFile, "sentence"},
		{"clip.wav", TypeAudioFile, "sentence"},
	}
	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			r := Classify(c.filename, nil)
			if r.Type != c.wantType {
				t.Errorf("Type = %s, want %s", r.Type, c.wantType)
			}
			if r.RecommendedChunker != c.wantChnk {
				t.Errorf("Chunker = %s, want %s", r.RecommendedChunker, c.wantChnk)
			}
			if r.MinChunkChars <= 0 || r.MaxChunkChars <= 0 {
				t.Errorf("chunk char bounds zero: min=%d max=%d", r.MinChunkChars, r.MaxChunkChars)
			}
		})
	}
}

func TestClassify_ContentBasedCSV(t *testing.T) {
	// Unknown extension, but content has consistent comma-delimited rows
	// → looksLikeCSV heuristic kicks in.
	csvContent := []byte("id,name,age\n1,Alice,30\n2,Bob,25\n3,Carol,40")
	r := Classify("anonymous", csvContent)
	if r.Type != TypeTabularData {
		t.Errorf("Type = %s, want TypeTabularData", r.Type)
	}
}

func TestClassify_ContentBasedCode(t *testing.T) {
	// 2+ code patterns → looksLikeCode triggers.
	codeContent := []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n    fmt.Println(\"hi\")\n}")
	r := Classify("anonymous", codeContent)
	if r.Type != TypeCodeFile {
		t.Errorf("Type = %s, want TypeCodeFile", r.Type)
	}
}

func TestClassify_DefaultsToTextDocument(t *testing.T) {
	// Plain prose with no extension and no heuristic match → fallback.
	prose := []byte("The quick brown fox jumps over the lazy dog. " +
		"Lorem ipsum dolor sit amet.")
	r := Classify("notes", prose)
	if r.Type != TypeTextDocument {
		t.Errorf("Type = %s, want TypeTextDocument", r.Type)
	}
}

func TestClassify_EmptyContent_NoCrash(t *testing.T) {
	r := Classify("", nil)
	if r.Type != TypeTextDocument {
		t.Errorf("Type = %s, want TypeTextDocument fallback", r.Type)
	}
}

func TestClassify_CaseInsensitiveExtension(t *testing.T) {
	r := Classify("DATA.CSV", nil)
	if r.Type != TypeTabularData {
		t.Errorf("uppercase .CSV not recognized: got %s", r.Type)
	}
}
