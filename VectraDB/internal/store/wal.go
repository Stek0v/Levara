package store

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"unsafe"
)

const (
	OpInsert = 1
	OpDelete = 2
)

type WAL struct {
	file   *os.File
	writer *bufio.Writer
}

// OpenWal creates a new Write Ahead Log and returns a WAL struct
func OpenWal(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	return &WAL{
		file:   f,
		writer: bufio.NewWriter(f),
	}, nil
}

// WriterEntry saves an operation in the WAL

func (wal *WAL) WriteEntry(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	// Calculate sizes
	idBytes := []byte(id)
	idLen := uint32(len(idBytes))
	vectorLen := uint32(len(vector) * 4) // 4 bytes per float32
	metadataLen := uint32(len(metadata))

	// Total Payload Size: Op(1) + IDLen(4) + ID + VecLen(4) + Vec + MetaLen(4) + Meta
	totalPayloadSize := 1 + 4 + idLen + 4 + vectorLen + 4 + metadataLen + 8 + 4

	// Write Size Header
	if err := binary.Write(wal.writer, binary.LittleEndian, uint32(totalPayloadSize)); err != nil {
		return err
	}

	// Write operation type
	if err := wal.writer.WriteByte(op); err != nil {
		return err
	}

	// Write ID
	binary.Write(wal.writer, binary.LittleEndian, idLen)
	wal.writer.Write(idBytes)

	// Write Vector (bulk unsafe write — 1 call instead of dim calls)
	binary.Write(wal.writer, binary.LittleEndian, vectorLen)
	if vectorLen > 0 {
		vecPtr := unsafe.Pointer(&vector[0])
		vecBytes := unsafe.Slice((*byte)(vecPtr), len(vector)*4)
		wal.writer.Write(vecBytes)
	}

	// Write Metadata
	binary.Write(wal.writer, binary.LittleEndian, metadataLen)
	if metadataLen > 0 {
		wal.writer.Write(metadata)
	}

	// Write Offset (8 bytes)
	if err := binary.Write(wal.writer, binary.LittleEndian, int64(loc.Offset)); err != nil {
		return err
	}
	// Write Len (4 bytes)
	if err := binary.Write(wal.writer, binary.LittleEndian, uint32(loc.Length)); err != nil {
		return err
	}

	return wal.writer.Flush()
}

// WriteEntryNoFlush writes an entry without flushing — caller must call Flush().
// Use this in batch operations to amortize I/O across many records.
func (wal *WAL) WriteEntryNoFlush(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	idBytes := []byte(id)
	idLen := uint32(len(idBytes))
	vectorLen := uint32(len(vector) * 4)
	metadataLen := uint32(len(metadata))
	totalPayloadSize := 1 + 4 + idLen + 4 + vectorLen + 4 + metadataLen + 8 + 4

	if err := binary.Write(wal.writer, binary.LittleEndian, uint32(totalPayloadSize)); err != nil {
		return err
	}
	if err := wal.writer.WriteByte(op); err != nil {
		return err
	}
	binary.Write(wal.writer, binary.LittleEndian, idLen)
	wal.writer.Write(idBytes)
	binary.Write(wal.writer, binary.LittleEndian, vectorLen)
	if vectorLen > 0 {
		vecPtr := unsafe.Pointer(&vector[0])
		vecBytes := unsafe.Slice((*byte)(vecPtr), len(vector)*4)
		wal.writer.Write(vecBytes)
	}
	binary.Write(wal.writer, binary.LittleEndian, metadataLen)
	if metadataLen > 0 {
		wal.writer.Write(metadata)
	}
	if err := binary.Write(wal.writer, binary.LittleEndian, int64(loc.Offset)); err != nil {
		return err
	}
	return binary.Write(wal.writer, binary.LittleEndian, uint32(loc.Length))
}

// Flush flushes the buffered writer and fsyncs to ensure durability.
func (wal *WAL) Flush() error {
	if err := wal.writer.Flush(); err != nil {
		return err
	}
	return wal.file.Sync()
}

// Close ensures everything is written to disk and synced
func (w *WAL) Close() error {
	if err := w.writer.Flush(); err != nil {
		w.file.Close()
		return err
	}
	if err := w.file.Sync(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}

// Iterator function type
type WALIterator func(id string, vector []float32, meta []byte, loc FileLocation)

// WALIteratorEx receives the operation type so callers can handle deletes.
type WALIteratorEx func(op byte, id string, vector []float32, meta []byte, loc FileLocation)

// RecoverEx replays WAL entries with operation type awareness (insert + delete).
func (wal *WAL) RecoverEx(fn WALIteratorEx) error {
	return wal.recoverInternal(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		fn(op, id, vector, meta, loc)
	})
}

func (wal *WAL) Recover(fn WALIterator) error {
	return wal.recoverInternal(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		if op == OpInsert {
			fn(id, vector, meta, loc)
		}
	})
}

func (wal *WAL) recoverInternal(fn func(op byte, id string, vector []float32, meta []byte, loc FileLocation)) error {
	// Reset file pointer to start
	wal.file.Seek(0, 0)
	reader := bufio.NewReader(wal.file)

	for {
		// 1. Read Payload Size
		var size uint32
		if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break // End of file, we are done
			}
			return err
		}

		// 2. Read Operation
		op, err := reader.ReadByte()
		if err != nil {
			break // Truncated entry
		}

		// 3. Read ID
		var idLen uint32
		if err := binary.Read(reader, binary.LittleEndian, &idLen); err != nil {
			break
		}
		if idLen > size || idLen > 1<<20 {
			break // Sanity check: ID can't be larger than entry or 1MB
		}
		idBytes := make([]byte, idLen)
		if _, err := io.ReadFull(reader, idBytes); err != nil {
			break
		}
		id := string(idBytes)

		// 4. Read Vector (bulk unsafe read)
		var vecLen uint32
		if err := binary.Read(reader, binary.LittleEndian, &vecLen); err != nil {
			break
		}
		if vecLen > size || vecLen > 1<<26 {
			break // Sanity check: vector can't exceed 64MB
		}
		vecBytes := make([]byte, vecLen)
		if _, err := io.ReadFull(reader, vecBytes); err != nil {
			break
		}
		var vector []float32
		if vecLen > 0 {
			vector = unsafe.Slice((*float32)(unsafe.Pointer(&vecBytes[0])), vecLen/4)
		}

		// 5. Read Metadata
		var metaLen uint32
		if err := binary.Read(reader, binary.LittleEndian, &metaLen); err != nil {
			break
		}
		if metaLen > size || metaLen > 1<<20 {
			break // Sanity check: metadata can't exceed 1MB
		}
		meta := make([]byte, metaLen)
		if _, err := io.ReadFull(reader, meta); err != nil {
			break
		}

		// Read Offset
		var offset int64
		if err := binary.Read(reader, binary.LittleEndian, &offset); err != nil {
			break
		}

		// Read Length after offset
		var locLen int32
		if err := binary.Read(reader, binary.LittleEndian, &locLen); err != nil {
			break
		}

		// 6. Execute callback with operation type
		fn(op, id, vector, meta, FileLocation{Offset: offset, Length: locLen})
	}

	// Move pointer back to end for appending new writes
	wal.file.Seek(0, 2)
	return nil
}
