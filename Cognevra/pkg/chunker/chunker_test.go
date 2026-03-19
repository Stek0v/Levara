package chunker

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const testText = `Глава 1

Первый параграф первой главы. Это достаточно длинный текст, чтобы пройти минимальный порог в 80 символов для чанка.

Второй параграф. Он тоже должен быть достаточно длинным, чтобы пройти порог. Добавим ещё немного текста сюда.

Глава 2

Это параграф второй главы. Снова делаем его длинным для прохождения порога. Текст текст текст текст текст.

Короткий.

Ещё один длинный параграф второй главы. Мы проверяем, что короткие параграфы отбрасываются. Тест тест тест.`

func TestChunkByParagraphMerged(t *testing.T) {
	chunks := ChunkByParagraphMerged(testText, DefaultMinChunkChars, DefaultMaxChunkChars, "")

	if len(chunks) == 0 {
		t.Fatal("Expected at least 1 chunk")
	}

	// All chunks should be >= MIN
	for i, c := range chunks {
		if len(c.Text) < DefaultMinChunkChars {
			t.Errorf("Chunk %d too short: %d chars", i, len(c.Text))
		}
	}

	// Chapter detection: at least one chunk should have chapter > 0
	maxChapter := 0
	for _, c := range chunks {
		if c.Chapter > maxChapter {
			maxChapter = c.Chapter
		}
	}
	if maxChapter == 0 {
		t.Error("Chapter detection failed: no chapters found")
	}

	// ChunkIndex sequential
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("Chunk %d has ChunkIndex=%d", i, c.ChunkIndex)
		}
	}

	// IDs are unique
	ids := make(map[string]bool)
	for _, c := range chunks {
		if ids[c.ID] {
			t.Errorf("Duplicate ID: %s", c.ID)
		}
		ids[c.ID] = true
	}
}

func TestChunkByParagraphSimple(t *testing.T) {
	chunks := ChunkByParagraphSimple(testText, DefaultMinChunkChars, "")

	if len(chunks) == 0 {
		t.Fatal("Expected at least 1 chunk")
	}

	// "Короткий." should be discarded (< 80 chars)
	for _, c := range chunks {
		if strings.Contains(c.Text, "Короткий.") {
			t.Error("Short paragraph should be discarded")
		}
	}

	// Each chunk is a single paragraph (no "\n\n" inside)
	for i, c := range chunks {
		if strings.Contains(c.Text, "\n\n") {
			t.Errorf("Chunk %d contains paragraph separator", i)
		}
	}
}

func TestChunkBySentence(t *testing.T) {
	text := "Первое предложение. Второе предложение! Третье? Четвёртое предложение. " +
		"Пятое предложение здесь. Шестое предложение тоже длинное, чтобы пройти порог в 80 символов."

	chunks := ChunkBySentence(text, DefaultMinChunkChars, DefaultMaxChunkChars, "")

	if len(chunks) == 0 {
		t.Fatal("Expected at least 1 chunk")
	}

	// All chunks >= MIN
	for i, c := range chunks {
		if len(c.Text) < DefaultMinChunkChars {
			t.Errorf("Chunk %d too short: %d chars (%q)", i, len(c.Text), c.Text)
		}
	}

	// CutType should be "sentence"
	for i, c := range chunks {
		if c.CutType != "sentence" {
			t.Errorf("Chunk %d CutType=%q, want sentence", i, c.CutType)
		}
	}
}

func TestChunkEmptyText(t *testing.T) {
	chunks := ChunkByParagraphMerged("", DefaultMinChunkChars, DefaultMaxChunkChars, "")
	if len(chunks) != 0 {
		t.Errorf("Empty text should return 0 chunks, got %d", len(chunks))
	}
}

func TestChunkBookParity(t *testing.T) {
	// Load the actual test book if available
	// Try multiple paths (depends on where go test runs from)
	paths := []string{
		"../../../../Edvards_Dzanet_Uragan_r4_P61XH.txt",
		"/home/stek0v/src/new_db/Edvards_Dzanet_Uragan_r4_P61XH.txt",
	}
	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Skipf("Book file not found")
	}

	text := string(data)
	chunks := ChunkByParagraphMerged(text, DefaultMinChunkChars, DefaultMaxChunkChars, "")

	// Python produces ~1430 chunks with these settings
	if len(chunks) < 1000 || len(chunks) > 2000 {
		t.Errorf("Expected ~1430 chunks, got %d", len(chunks))
	}

	// Verify no empty chunks
	for i, c := range chunks {
		if len(c.Text) == 0 {
			t.Errorf("Chunk %d is empty", i)
		}
	}

	// Verify chapters detected
	maxChapter := 0
	for _, c := range chunks {
		if c.Chapter > maxChapter {
			maxChapter = c.Chapter
		}
	}
	if maxChapter < 40 {
		t.Errorf("Expected ~45 chapters, max found: %d", maxChapter)
	}

	t.Logf("Book chunked: %d chunks, max chapter=%d", len(chunks), maxChapter)
}

func TestUUID5MatchesPython(t *testing.T) {
	// Python: uuid.uuid5(uuid.NAMESPACE_OID, "test-doc-0")
	// Verify Go produces a non-empty deterministic UUID5 for this input.
	got := uuid.NewSHA1(uuid.NameSpaceOID, []byte("test-doc-0")).String()
	if got == "" {
		t.Error("UUID5 generation failed")
	}
	// The expected value was computed with Python:
	//   import uuid; str(uuid.uuid5(uuid.NAMESPACE_OID, "test-doc-0"))
	// which yields "ff501e71-5b83-59e4-b2b7-77c20fcc0ab3"
	const expected = "ff501e71-5b83-59e4-b2b7-77c20fcc0ab3"
	if got != expected {
		t.Errorf("UUID5 mismatch: got %s, want %s (Python parity check failed)", got, expected)
	}
	t.Logf("UUID5 for 'test-doc-0': %s", got)
}

func TestUUID5Deterministic(t *testing.T) {
	id1 := uuid.NewSHA1(uuid.NameSpaceOID, []byte("doc-42-7")).String()
	id2 := uuid.NewSHA1(uuid.NameSpaceOID, []byte("doc-42-7")).String()
	if id1 != id2 {
		t.Errorf("UUID5 not deterministic: %s != %s", id1, id2)
	}
	t.Logf("UUID5 for 'doc-42-7': %s", id1)
}

func TestChunkIDDeterministicWithDocumentID(t *testing.T) {
	// With the same documentID, chunking should yield identical chunk IDs
	chunks1 := ChunkByParagraphMerged(testText, DefaultMinChunkChars, DefaultMaxChunkChars, "my-doc-123")
	chunks2 := ChunkByParagraphMerged(testText, DefaultMinChunkChars, DefaultMaxChunkChars, "my-doc-123")
	if len(chunks1) != len(chunks2) {
		t.Fatalf("Chunk counts differ: %d vs %d", len(chunks1), len(chunks2))
	}
	for i := range chunks1 {
		if chunks1[i].ID != chunks2[i].ID {
			t.Errorf("Chunk %d ID not deterministic: %s != %s", i, chunks1[i].ID, chunks2[i].ID)
		}
	}
}

func TestChunkIDUniqueAcrossDocuments(t *testing.T) {
	// Different documentIDs should produce different chunk IDs for the same chunk index
	chunks1 := ChunkByParagraphMerged(testText, DefaultMinChunkChars, DefaultMaxChunkChars, "doc-A")
	chunks2 := ChunkByParagraphMerged(testText, DefaultMinChunkChars, DefaultMaxChunkChars, "doc-B")
	if len(chunks1) == 0 || len(chunks2) == 0 {
		t.Skip("No chunks produced")
	}
	if chunks1[0].ID == chunks2[0].ID {
		t.Errorf("Different documents should produce different chunk IDs: both got %s", chunks1[0].ID)
	}
}

func BenchmarkChunkBook(b *testing.B) {
	paths := []string{
		"../../../../Edvards_Dzanet_Uragan_r4_P61XH.txt",
		"/home/stek0v/src/new_db/Edvards_Dzanet_Uragan_r4_P61XH.txt",
	}
	var data []byte
	var err error
	for _, p := range paths {
		data, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		b.Skipf("Book file not found")
	}
	text := string(data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ChunkByParagraphMerged(text, DefaultMinChunkChars, DefaultMaxChunkChars, "")
	}
}
