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

// validateWALRecord keeps acknowledged writes inside the native recovery format.
func validateWALRecord(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	if len(id) == 0 || len(id) > 1<<20 || len(metadata) > 1<<20 {
		return errors.New("native WAL requires a nonempty ID and ID/metadata at most 1 MiB")
	}
	if op != OpInsert && op != OpDelete || op == OpInsert && (len(vector) == 0 || len(vector) > 65536) || op == OpDelete && (len(vector) != 0 || len(metadata) != 0) || loc.Offset < 0 {
		return errors.New("invalid native WAL record")
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return errors.New("non-finite WAL vector")
		}
	}
	return nil
}

// partialWALLength checks whether visible low bytes admit any valid completion.
func partialWALLength(body []byte, minimum, maximum uint32) bool {
	if minimum > maximum {
		return false
	}
	var low uint64
	for i, b := range body {
		low |= uint64(b) << (8 * i)
	}
	step := uint64(1) << (8 * len(body))
	if low < uint64(minimum) {
		low += (uint64(minimum) - low + step - 1) / step * step
	}
	return low <= uint64(maximum)
}

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
		if size < 26 || size > 25+2*(1<<20)+int64(dimension)*4 || consumed > maxBytes {
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

// prepareRecovery is startup-only: validate before rebuilding metadata and repair
// only an incomplete physical tail before this WAL accepts new appends.
// ponytail: strict validation precedes replay; share its decoder if startup CPU becomes material.
func (wal *WAL) prepareRecovery(dimension int) error {
	if dimension < 1 || dimension > 65536 {
		return errors.New("invalid WAL dimension")
	}
	wal.mu.Lock()
	defer wal.mu.Unlock()
	info, err := wal.file.Stat()
	if err != nil {
		return err
	}
	var completeEnd int64
	repair := func() error {
		if err := wal.file.Truncate(completeEnd); err != nil {
			return err
		}
		return wal.file.Sync()
	}
	maxFrame := int64(25+2*(1<<20)) + int64(dimension)*4
	for completeEnd < info.Size() {
		remaining := info.Size() - completeEnd
		if remaining < 4 {
			header := make([]byte, remaining)
			if _, err := wal.file.ReadAt(header, completeEnd); err != nil {
				return err
			}
			if !partialWALLength(header, 26, uint32(maxFrame)) {
				return errors.New("invalid incomplete WAL frame header")
			}
			return repair()
		}
		var header [4]byte
		if _, err := wal.file.ReadAt(header[:], completeEnd); err != nil {
			return err
		}
		size := int64(binary.LittleEndian.Uint32(header[:]))
		if size < 26 || size > maxFrame {
			return errors.New("WAL frame exceeds declared limits")
		}
		if remaining-4 < size {
			body := make([]byte, remaining-4)
			if len(body) > 0 {
				if _, err := wal.file.ReadAt(body, completeEnd+4); err != nil {
					return err
				}
			}
			if err := validateIncompleteWALFrame(body, size, dimension); err != nil {
				return err
			}
			return repair()
		}
		frame := io.NewSectionReader(wal.file, completeEnd, 4+size)
		if err := WalkWALStrict(context.Background(), frame, dimension, 4+size, func(byte, SnapshotRecord) error { return nil }); err != nil {
			return err
		}
		completeEnd += 4 + size
	}
	return nil
}

// Validate every available structural field before classifying a short final
// frame as an interrupted write. Visible corruption is never repaired as EOF.
func validateIncompleteWALFrame(body []byte, declared int64, dimension int) error {
	if len(body) == 0 {
		return nil
	}
	op := body[0]
	if op != OpInsert && op != OpDelete {
		return errors.New("unknown WAL operation")
	}
	body = body[1:]
	var fieldBytes int64
	for field, limit := range []uint32{1 << 20, uint32(dimension) * 4, 1 << 20} {
		if len(body) < 4 {
			minimum, maximum := uint32(0), limit
			switch field {
			case 0:
				minimum = 1
				space := declared - 25
				if op == OpInsert {
					space -= int64(dimension) * 4
				}
				if space < 1 {
					return errors.New("invalid incomplete WAL frame size")
				}
				if space < int64(maximum) {
					maximum = uint32(space)
				}
				if op == OpDelete {
					if space > int64(limit) {
						return errors.New("invalid incomplete WAL frame size")
					}
					minimum, maximum = uint32(space), uint32(space)
				} else if space-(1<<20) > int64(minimum) {
					minimum = uint32(space - (1 << 20))
				}
			case 1:
				if op == OpDelete {
					limit = 0
				}
				minimum, maximum = limit, limit
			case 2:
				space := declared - 25 - fieldBytes
				if space < 0 || space > int64(limit) || op == OpDelete && space != 0 {
					return errors.New("invalid incomplete WAL frame size")
				}
				minimum, maximum = uint32(space), uint32(space)
			}
			if !partialWALLength(body, minimum, maximum) {
				return errors.New("invalid incomplete WAL field length")
			}
			return nil
		}
		n := binary.LittleEndian.Uint32(body[:4])
		body = body[4:]
		if n > limit || field == 0 && n == 0 || field == 1 && (op == OpInsert && n != uint32(dimension)*4 || op == OpDelete && n != 0) || field == 2 && op == OpDelete && n != 0 {
			return errors.New("invalid incomplete WAL field")
		}
		fieldBytes += int64(n)
		minimum := int64(25) + fieldBytes
		if field == 0 && op == OpInsert {
			minimum += int64(dimension) * 4
		}
		maximum := minimum
		if op == OpInsert && field < 2 {
			maximum += 1 << 20
		}
		if minimum > declared || maximum < declared {
			return errors.New("invalid incomplete WAL frame size")
		}
		available := len(body)
		if uint64(available) > uint64(n) {
			available = int(n)
		}
		if field == 1 {
			for i := 0; i+4 <= available; i += 4 {
				value := math.Float32frombits(binary.LittleEndian.Uint32(body[i : i+4]))
				if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
					return errors.New("non-finite WAL vector")
				}
			}
		}
		if uint64(len(body)) < uint64(n) {
			return nil
		}
		body = body[n:]
	}
	if len(body) >= 8 && int64(binary.LittleEndian.Uint64(body[:8])) < 0 {
		return errors.New("invalid WAL location")
	}
	return nil
}
