package bm25

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// LoadSnapshotStrict is the bounded verification path. Tolerant runtime recovery
// remains unchanged. Invalid records and partial JSON are never skipped here.
func LoadSnapshotStrict(ctx context.Context, r io.Reader, maxBytes int64, maxDocuments int) (*Index, error) {
	if maxBytes < 0 || maxDocuments < 1 {
		return nil, errors.New("invalid strict BM25 limits")
	}
	bounded := &io.LimitedReader{R: r, N: maxBytes + 1}
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 64<<10), 10<<20)
	idx := NewIndex()
	count := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if bounded.N <= 0 {
			return nil, errors.New("BM25 snapshot exceeds byte limit")
		}
		var doc diskDoc
		if err := json.Unmarshal(scanner.Bytes(), &doc); err != nil {
			return nil, err
		}
		switch doc.Op {
		case "", "put":
			if doc.ID == "" {
				return nil, errors.New("BM25 document ID required")
			}
			idx.Add(doc.ID, doc.Text, doc.Metadata)
		case "delete":
			if doc.ID == "" {
				return nil, errors.New("BM25 delete ID required")
			}
			idx.Remove(doc.ID)
		case "clear":
			idx.Clear()
		default:
			return nil, errors.New("unknown BM25 operation")
		}
		count++
		if count > maxDocuments {
			return nil, errors.New("BM25 operation count limit exceeded")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if bounded.N <= 0 {
		return nil, errors.New("BM25 snapshot exceeds byte limit")
	}
	return idx, ctx.Err()
}
