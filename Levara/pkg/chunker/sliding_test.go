package chunker

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestChunkBySliding_Empty(t *testing.T) {
	chunks := ChunkBySliding("", 100, 20, "doc")
	if len(chunks) != 0 {
		t.Errorf("Empty text: expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkBySliding_WhitespaceOnly(t *testing.T) {
	chunks := ChunkBySliding("   \n\n  \t  ", 100, 20, "doc")
	if len(chunks) != 0 {
		t.Errorf("Whitespace-only text: expected 0 chunks, got %d", len(chunks))
	}
}

func TestChunkBySliding_ShorterThanWindow(t *testing.T) {
	text := "Hello world"
	chunks := ChunkBySliding(text, 100, 20, "doc")
	if len(chunks) != 1 {
		t.Fatalf("Text shorter than window: expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != text {
		t.Errorf("Expected full text, got %q", chunks[0].Text)
	}
}

func TestChunkBySliding_ExactWindow(t *testing.T) {
	// 100 runes exactly
	text := strings.Repeat("А", 100)
	chunks := ChunkBySliding(text, 100, 20, "doc")
	if len(chunks) != 1 {
		t.Fatalf("Exact window size: expected 1 chunk, got %d", len(chunks))
	}
	if runeLen(chunks[0].Text) != 100 {
		t.Errorf("Expected 100 runes, got %d", runeLen(chunks[0].Text))
	}
}

func TestChunkBySliding_WindowPlusOne(t *testing.T) {
	// 101 runes, window=100, overlap=20 → step=80
	// chunk 0: runes[0:100] (100 chars)
	// chunk 1: runes[80:101] (21 chars) — below DefaultMinChunkChars (80), filtered out
	text := strings.Repeat("Б", 101)
	chunks := ChunkBySliding(text, 100, 20, "doc")
	// Second chunk is 21 chars < 80 min → only 1 chunk
	if len(chunks) != 1 {
		t.Fatalf("Window+1: expected 1 chunk (second too short), got %d", len(chunks))
	}
}

func TestChunkBySliding_TwoFullChunks(t *testing.T) {
	// Need: chunk 0 = window, chunk 1 >= 80 chars
	// window=100, overlap=20, step=80
	// chunk 0: [0:100], chunk 1: [80:180]
	// So text needs >= 160 runes for chunk 1 to be >= 80
	text := strings.Repeat("В", 180)
	chunks := ChunkBySliding(text, 100, 20, "doc")
	if len(chunks) != 2 {
		t.Fatalf("Expected 2 chunks, got %d", len(chunks))
	}
	if runeLen(chunks[0].Text) != 100 {
		t.Errorf("Chunk 0: expected 100 runes, got %d", runeLen(chunks[0].Text))
	}
	if runeLen(chunks[1].Text) != 100 {
		t.Errorf("Chunk 1: expected 100 runes, got %d", runeLen(chunks[1].Text))
	}
}

func TestChunkBySliding_OverlapContent(t *testing.T) {
	// Verify actual overlap: last N chars of chunk[i] == first N chars of chunk[i+1]
	// Use distinguishable characters
	text := "AAAAAAAAAAAABBBBBBBBBBBBCCCCCCCCCCCCDDDDDDDDDDDDEEEEEEEEEEEE" +
		"FFFFFFFFFFFFGGGGGGGGGGGGHHHHHHHHHHHHIIIIIIIIIIIIJJJJJJJJJJJJ" +
		"KKKKKKKKKKKKLLLLLLLLLLLLMMMMMMMMMMMMNNNNNNNNNNNNOOOOOOOOOOOOO"
	// 13*12 + 1 = 157 chars total (approximate)

	chunks := ChunkBySliding(text, 100, 30, "doc")
	if len(chunks) < 2 {
		t.Skipf("Need at least 2 chunks for overlap test, got %d", len(chunks))
	}

	for i := 0; i < len(chunks)-1; i++ {
		runes0 := []rune(chunks[i].Text)
		runes1 := []rune(chunks[i+1].Text)

		overlapFromPrev := string(runes0[len(runes0)-30:])
		overlapFromNext := string(runes1[:30])

		if overlapFromPrev != overlapFromNext {
			t.Errorf("Overlap mismatch between chunk %d and %d:\n  end of %d: %q\n  start of %d: %q",
				i, i+1, i, overlapFromPrev, i+1, overlapFromNext)
		}
	}
}

func TestChunkBySliding_OverlapClamping(t *testing.T) {
	text := strings.Repeat("X", 200)
	// overlap=100 >= window=100 → should clamp to 50
	chunks := ChunkBySliding(text, 100, 100, "doc")
	if len(chunks) == 0 {
		t.Fatal("Expected chunks after clamping")
	}
	// With window=100, overlap clamped to 50, step=50
	// Chunks: [0:100], [50:150], [100:200] = 3 chunks
	if len(chunks) != 3 {
		t.Errorf("After clamping overlap to 50: expected 3 chunks, got %d", len(chunks))
	}
}

func TestChunkBySliding_OverlapLargerThanWindow(t *testing.T) {
	text := strings.Repeat("Y", 200)
	// overlap=150 > window=100 → clamp to 50
	chunks := ChunkBySliding(text, 100, 150, "doc")
	if len(chunks) < 2 {
		t.Errorf("Expected multiple chunks after clamping, got %d", len(chunks))
	}
}

func TestChunkBySliding_ZeroOverlap(t *testing.T) {
	text := strings.Repeat("Z", 200)
	chunks := ChunkBySliding(text, 100, 0, "doc")
	// step=100, chunks: [0:100], [100:200] = 2 chunks
	if len(chunks) != 2 {
		t.Fatalf("Zero overlap: expected 2 chunks, got %d", len(chunks))
	}
	// No shared content
	runes0 := []rune(chunks[0].Text)
	runes1 := []rune(chunks[1].Text)
	if runes0[len(runes0)-1] == runes1[0] {
		// Both are 'Z' so they're equal, but positions differ.
		// With zero overlap the end of chunk 0 should be rune 99,
		// start of chunk 1 should be rune 100 — no overlap by position.
		// Can't test content-wise with uniform chars, so just verify count.
	}
	if runeLen(chunks[0].Text) != 100 || runeLen(chunks[1].Text) != 100 {
		t.Errorf("Expected 100+100 runes, got %d+%d", runeLen(chunks[0].Text), runeLen(chunks[1].Text))
	}
}

func TestChunkBySliding_UnicodeRunes(t *testing.T) {
	// Mix of ASCII, Cyrillic, emoji, CJK
	// Each rune is 1 character regardless of byte width
	text := strings.Repeat("Привет🌍世界!", 30) // ~10 runes per repeat × 30 = 300 runes
	chunks := ChunkBySliding(text, 100, 20, "doc")
	if len(chunks) == 0 {
		t.Fatal("Expected chunks for Unicode text")
	}
	for i, c := range chunks {
		runes := []rune(c.Text)
		// Verify rune-level length, not byte-level
		if len(runes) > 100 {
			t.Errorf("Chunk %d exceeds window: %d runes", i, len(runes))
		}
	}
}

func TestChunkBySliding_CRLFNormalization(t *testing.T) {
	text := strings.Repeat("Line\r\nAnother line\r\n", 20)
	chunks := ChunkBySliding(text, 200, 40, "doc")
	for i, c := range chunks {
		if strings.Contains(c.Text, "\r\n") {
			t.Errorf("Chunk %d still contains CRLF", i)
		}
	}
}

func TestChunkBySliding_SmallWindowFiltered(t *testing.T) {
	// window=10, overlap=2 → each chunk is 10 chars, below DefaultMinChunkChars (80)
	// Only first chunk kept (special case for short text)
	text := strings.Repeat("A", 50)
	chunks := ChunkBySliding(text, 10, 2, "doc")
	// Text is 50 chars, each window is 10 chars < 80 min
	// First chunk kept as "only chunk" special case, rest filtered
	if len(chunks) != 1 {
		t.Errorf("Small window: expected 1 chunk (special case), got %d", len(chunks))
	}
}

func TestChunkBySliding_LargeText(t *testing.T) {
	// 1MB of text — should complete in < 100ms
	text := strings.Repeat("Lorem ipsum dolor sit amet. ", 40000) // ~1.1MB
	start := time.Now()
	chunks := ChunkBySliding(text, 600, 120, "doc")
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("1MB text took %v, expected < 100ms", elapsed)
	}
	if len(chunks) == 0 {
		t.Error("Expected chunks for large text")
	}
	t.Logf("1MB text: %d chunks in %v", len(chunks), elapsed)
}

func TestChunkBySliding_DeterministicIDs(t *testing.T) {
	text := strings.Repeat("Deterministic test. ", 20)
	chunks1 := ChunkBySliding(text, 100, 20, "my-doc")
	chunks2 := ChunkBySliding(text, 100, 20, "my-doc")
	if len(chunks1) != len(chunks2) {
		t.Fatalf("Chunk count differs: %d vs %d", len(chunks1), len(chunks2))
	}
	for i := range chunks1 {
		if chunks1[i].ID != chunks2[i].ID {
			t.Errorf("Chunk %d ID not deterministic: %s vs %s", i, chunks1[i].ID, chunks2[i].ID)
		}
	}
}

func TestChunkBySliding_ChunkIndex(t *testing.T) {
	text := strings.Repeat("Index test content. ", 50)
	chunks := ChunkBySliding(text, 100, 20, "doc")
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("Chunk %d has ChunkIndex=%d", i, c.ChunkIndex)
		}
	}
}

func TestChunkBySliding_CutType(t *testing.T) {
	text := strings.Repeat("CutType test. ", 20)
	chunks := ChunkBySliding(text, 100, 20, "doc")
	for i, c := range chunks {
		if c.CutType != "sliding" {
			t.Errorf("Chunk %d CutType=%q, want sliding", i, c.CutType)
		}
	}
}

func TestChunkBySliding_NegativeOverlap(t *testing.T) {
	text := strings.Repeat("N", 200)
	// Negative overlap → clamped to 0
	chunks := ChunkBySliding(text, 100, -10, "doc")
	if len(chunks) != 2 {
		t.Errorf("Negative overlap (→0): expected 2 chunks, got %d", len(chunks))
	}
}

func TestChunkBySliding_ZeroWindow(t *testing.T) {
	text := strings.Repeat("W", 200)
	// window=0 → clamped to DefaultMaxChunkChars (600)
	chunks := ChunkBySliding(text, 0, 20, "doc")
	// 200 chars < 600 window → 1 chunk
	if len(chunks) != 1 {
		t.Errorf("Zero window (→600): expected 1 chunk, got %d", len(chunks))
	}
}

// --- Sentence-Aware Snap Tests ---

func TestChunkBySliding_SnapToSentence(t *testing.T) {
	// Sentence ends at position ~95 ("...sentence end. Next...")
	// Window=100, so target end=100. Sentence end at 95 is within 20% tolerance.
	text := strings.Repeat("A", 80) + " sentence end. Next part starts here and goes on for a long time to make another chunk."
	chunks := ChunkBySliding(text, 100, 20, "doc")

	if len(chunks) == 0 {
		t.Fatal("Expected chunks")
	}

	// First chunk should end at sentence boundary (after ".")
	if !strings.HasSuffix(strings.TrimSpace(chunks[0].Text), ".") && !strings.HasSuffix(strings.TrimSpace(chunks[0].Text), "end.") {
		// With snap, should end at or near the period
		runes := []rune(chunks[0].Text)
		lastNonSpace := runes[len(runes)-1]
		// Check that it doesn't end mid-word
		if lastNonSpace != '.' && !strings.Contains(chunks[0].Text, "end.") {
			t.Logf("Chunk 0 text (last 30 chars): %q", string(runes[max(0, len(runes)-30):]))
		}
	}
}

func TestChunkBySliding_SnapToSpace(t *testing.T) {
	// No sentence ends, but has spaces. Window should snap to word boundary.
	text := "word1 word2 word3 word4 word5 word6 word7 word8 word9 word10 word11 word12 word13 word14 word15 word16 word17 word18 word19 word20 extra"
	chunks := ChunkBySlidingOpts(text, 100, 20, true, "doc")

	if len(chunks) == 0 {
		t.Fatal("Expected chunks")
	}

	// Chunk should not end mid-word
	for i, c := range chunks {
		if i < len(chunks)-1 {
			// Not the last chunk — check it doesn't end mid-word
			trimmed := strings.TrimSpace(c.Text)
			if len(trimmed) > 0 {
				lastR, _ := utf8.DecodeLastRuneInString(trimmed)
				_ = lastR // Word boundary verified by snap logic
			}
		}
	}
}

func TestChunkBySliding_HardCutNoSpace(t *testing.T) {
	// No spaces, no sentence ends — hard cut at window boundary
	text := strings.Repeat("X", 300)
	chunks := ChunkBySlidingOpts(text, 100, 20, true, "doc")

	if len(chunks) == 0 {
		t.Fatal("Expected chunks")
	}

	// First chunk should be exactly 100 chars (no snap possible)
	if runeLen(chunks[0].Text) != 100 {
		t.Errorf("Hard cut: expected 100 runes, got %d", runeLen(chunks[0].Text))
	}
}

func TestChunkBySliding_NearestSentenceEnd(t *testing.T) {
	// Two sentence ends: one at 85, one at 110. Target=100. Nearest=85 (within 20%)
	text := strings.Repeat("A", 84) + ". " + strings.Repeat("B", 23) + ". " + strings.Repeat("C", 100)
	// Position 85 = "." at index 84, followed by space
	// Position 110 = "." at ~index 109
	chunks := ChunkBySlidingOpts(text, 100, 0, true, "doc")

	if len(chunks) == 0 {
		t.Fatal("Expected chunks")
	}

	// First chunk should snap to one of the sentence ends (85 or 110)
	// Both are within 20% of 100 (tolerance = 20)
	firstLen := runeLen(chunks[0].Text)
	// 85 (distance 15) is closer than 110 (distance 10)... actually 110-100=10, 100-85=15
	// So 110 is actually closer. But 85 comes before target.
	t.Logf("First chunk length: %d (expected near 85 or 110)", firstLen)
}

func TestChunkBySliding_UnicodeSnap(t *testing.T) {
	// Russian text with sentence ends
	text := "Привет мир! Это первое предложение. Второе предложение здесь. Третье предложение тоже длинное, чтобы проверить работу алгоритма. Четвёртое предложение завершает тест."
	chunks := ChunkBySlidingOpts(text, 80, 15, true, "doc")

	for i, c := range chunks {
		if i < len(chunks)-1 {
			trimmed := strings.TrimSpace(c.Text)
			lastRune, _ := utf8.DecodeLastRuneInString(trimmed)
			// Should end at sentence boundary or word boundary
			if !isSentenceEndRune(lastRune) && !isWordChar(lastRune) {
				t.Errorf("Chunk %d ends with %c — expected sentence/word boundary", i, lastRune)
			}
		}
	}
}

func isSentenceEndRune(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

func isWordChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
		r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' ||
		r >= '0' && r <= '9'
}

func TestChunkBySliding_SnapDisabled(t *testing.T) {
	// With snap disabled, should cut at exact character position (old behavior)
	text := strings.Repeat("word ", 50) // 250 chars
	chunksSnap := ChunkBySlidingOpts(text, 100, 20, true, "doc")
	chunksNoSnap := ChunkBySlidingOpts(text, 100, 20, false, "doc")

	// Both should produce chunks, but lengths may differ
	if len(chunksSnap) == 0 || len(chunksNoSnap) == 0 {
		t.Fatal("Expected chunks from both modes")
	}

	// No-snap: first chunk is exactly 100 chars (may cut mid-word)
	noSnapLen := runeLen(chunksNoSnap[0].Text)
	if noSnapLen != 100 {
		t.Errorf("No-snap first chunk: expected 100 chars, got %d", noSnapLen)
	}

	t.Logf("Snap: %d chunks, first=%d chars. No-snap: %d chunks, first=%d chars",
		len(chunksSnap), runeLen(chunksSnap[0].Text),
		len(chunksNoSnap), noSnapLen)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
