package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// WalkWALStrict validates complete native frames without mutating a WAL or store.
// Unlike crash recovery, a partial tail is an error. The callback must consume
// records synchronously; caller-owned logical state should have its own cap.
func WalkWALStrict(ctx context.Context, r io.Reader, dimension int, maxBytes int64, visit func(byte, SnapshotRecord) error) error {
	if dimension < 1 || dimension > 65536 || maxBytes < 0 || visit == nil {
		return errors.New("invalid strict WAL limits")
	}
	var consumed int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var header [4]byte
		n, err := io.ReadFull(r, header[:])
		if err == io.EOF && n == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("incomplete WAL frame header: %w", err)
		}
		size := int64(binary.LittleEndian.Uint32(header[:]))
		consumed += 4 + size
		if size < 25 || size > 25+2*(1<<20)+int64(dimension)*4 || consumed > maxBytes {
			return errors.New("WAL frame exceeds declared limits")
		}
		body := make([]byte, size)
		if _, err = io.ReadFull(r, body); err != nil {
			return fmt.Errorf("incomplete WAL frame: %w", err)
		}
		buf := bytes.NewReader(body)
		op, _ := buf.ReadByte()
		if op != OpInsert && op != OpDelete {
			return errors.New("unknown WAL operation")
		}
		readField := func(limit uint32) ([]byte, error) {
			var n uint32
			if err := binary.Read(buf, binary.LittleEndian, &n); err != nil {
				return nil, err
			}
			if n > limit || uint64(n) > uint64(buf.Len()) {
				return nil, errors.New("invalid WAL field size")
			}
			v := make([]byte, n)
			_, err := io.ReadFull(buf, v)
			return v, err
		}
		id, err := readField(1 << 20)
		if err != nil || len(id) == 0 {
			return errors.New("invalid WAL record ID")
		}
		vector, err := readField(uint32(dimension) * 4)
		if err != nil || op == OpInsert && len(vector) != dimension*4 || op == OpDelete && len(vector) != 0 {
			return errors.New("invalid WAL vector dimension")
		}
		metadata, err := readField(1 << 20)
		if err != nil || op == OpDelete && len(metadata) != 0 {
			return errors.New("invalid WAL metadata")
		}
		var offset int64
		var length uint32
		if binary.Read(buf, binary.LittleEndian, &offset) != nil || binary.Read(buf, binary.LittleEndian, &length) != nil || offset < 0 || buf.Len() != 0 {
			return errors.New("invalid WAL location or frame size")
		}
		record := SnapshotRecord{ID: string(id), Data: metadata, Vector: make([]float32, len(vector)/4)}
		for i := range record.Vector {
			record.Vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(vector[i*4:]))
			if math.IsNaN(float64(record.Vector[i])) || math.IsInf(float64(record.Vector[i]), 0) {
				return errors.New("non-finite WAL vector")
			}
		}
		if err = visit(op, record); err != nil {
			return err
		}
	}
}
