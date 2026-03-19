package store

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// WAL operation types stored as a single byte at the start of each entry.
const (
	OpInsert = 1 // OpInsert records a vector + metadata upsert.
	OpDelete = 2 // OpDelete records a logical deletion by ID.
)

// flushRequest is sent by callers to the fsyncLoop goroutine.
type flushRequest struct {
	done chan error
}

// WAL is a write-ahead log that provides crash recovery for Cognevra.
//
// Each insert or delete is buffered via [WAL.WriteEntryNoFlush], then durably
// fsynced through a group-commit mechanism: multiple concurrent callers share
// a single fsync via [WAL.FlushAsync], reducing per-operation disk latency.
// On startup, [WAL.RecoverEx] replays all entries to rebuild in-memory state.
//
// The on-disk format is a sequence of length-prefixed binary records:
//
//	[4B size][1B op][4B idLen][id][4B vecLen][vec bytes][4B metaLen][meta][8B offset][4B length]
type WAL struct {
	file   *os.File
	writer *bufio.Writer

	mu        sync.Mutex       // protects writer (bufio.Writer) from concurrent access
	flushCh   chan flushRequest // group commit channel
	closeCh   chan struct{}     // signal fsyncLoop to stop
	closeOnce sync.Once        // ensure single close
	syncCount uint64           // atomic counter for testing coalescing
}

// OpenWal opens (or creates) the WAL file at path and starts the background
// fsyncLoop goroutine that coalesces flush requests. The WAL is ready for use
// immediately after this call returns.
func OpenWal(path string) (*WAL, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	wal := &WAL{
		file:    f,
		writer:  bufio.NewWriter(f),
		flushCh: make(chan flushRequest, 64),
		closeCh: make(chan struct{}),
	}
	go wal.fsyncLoop()
	return wal, nil
}

// fsyncLoop is the background goroutine that coalesces flush requests.
func (wal *WAL) fsyncLoop() {
	const maxWait = 2 * time.Millisecond
	const maxPending = 16

	pending := make([]flushRequest, 0, maxPending)
	timer := time.NewTimer(maxWait)
	timer.Stop()

	for {
		select {
		case req, ok := <-wal.flushCh:
			if !ok {
				// Channel closed — flush remaining and exit.
				wal.doGroupFlush(pending)
				timer.Stop()
				return
			}
			pending = append(pending, req)
			if len(pending) >= maxPending {
				timer.Stop()
				wal.doGroupFlush(pending)
				pending = pending[:0]
			} else if len(pending) == 1 {
				timer.Reset(maxWait)
			}
		case <-timer.C:
			if len(pending) > 0 {
				wal.doGroupFlush(pending)
				pending = pending[:0]
			}
		case <-wal.closeCh:
			timer.Stop()
			// Drain remaining requests from channel.
			for {
				select {
				case req := <-wal.flushCh:
					pending = append(pending, req)
				default:
					wal.doGroupFlush(pending)
					return
				}
			}
		}
	}
}

// doGroupFlush performs a single fsync for a batch of flush requests.
func (wal *WAL) doGroupFlush(reqs []flushRequest) {
	if len(reqs) == 0 {
		return
	}
	err := wal.flushSync()
	for _, r := range reqs {
		r.done <- err
	}
}

// flushSync flushes the buffered writer and fsyncs to disk under wal.mu.
func (wal *WAL) flushSync() error {
	wal.mu.Lock()
	defer wal.mu.Unlock()
	atomic.AddUint64(&wal.syncCount, 1)
	if err := wal.writer.Flush(); err != nil {
		return err
	}
	return wal.file.Sync()
}

// FlushAsync sends a flush request to fsyncLoop and blocks until completion.
// Multiple concurrent callers share a single fsync via group commit.
func (wal *WAL) FlushAsync() error {
	done := make(chan error, 1)
	wal.flushCh <- flushRequest{done: done}
	return <-done
}

// WriteEntry saves an operation in the WAL with immediate flush (inline).
// Uses wal.mu to protect the bufio.Writer and flushSync for durability.
func (wal *WAL) WriteEntry(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	wal.mu.Lock()
	err := wal.writeEntryLocked(op, id, vector, metadata, loc)
	wal.mu.Unlock()
	if err != nil {
		return err
	}
	return wal.flushSync()
}

// WriteEntryNoFlush writes an entry without flushing — caller must call FlushAsync().
// Use this in batch operations to amortize I/O across many records.
// Acquires wal.mu to protect the bufio.Writer from concurrent access.
func (wal *WAL) WriteEntryNoFlush(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	wal.mu.Lock()
	defer wal.mu.Unlock()
	return wal.writeEntryLocked(op, id, vector, metadata, loc)
}

// writeEntryLocked writes a WAL entry. Caller must hold wal.mu.
func (wal *WAL) writeEntryLocked(op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	return writeWALEntryTo(wal.writer, op, id, vector, metadata, loc)
}

// writeWALEntryTo writes a single WAL entry to w. Used by both normal WAL writes
// and checkpoint compaction. The caller is responsible for any locking or flushing.
func writeWALEntryTo(w *bufio.Writer, op byte, id string, vector []float32, metadata []byte, loc FileLocation) error {
	idBytes := []byte(id)
	idLen := uint32(len(idBytes))
	vectorLen := uint32(len(vector) * 4) // 4 bytes per float32
	metadataLen := uint32(len(metadata))

	// Total Payload Size: Op(1) + IDLen(4) + ID + VecLen(4) + Vec + MetaLen(4) + Meta + Offset(8) + Len(4)
	totalPayloadSize := 1 + 4 + idLen + 4 + vectorLen + 4 + metadataLen + 8 + 4

	// Write Size Header
	if err := binary.Write(w, binary.LittleEndian, uint32(totalPayloadSize)); err != nil {
		return err
	}

	// Write operation type
	if err := w.WriteByte(op); err != nil {
		return err
	}

	// Write ID
	binary.Write(w, binary.LittleEndian, idLen)
	w.Write(idBytes)

	// Write Vector (bulk unsafe write — 1 call instead of dim calls)
	binary.Write(w, binary.LittleEndian, vectorLen)
	if vectorLen > 0 {
		vecPtr := unsafe.Pointer(&vector[0])
		vecBytes := unsafe.Slice((*byte)(vecPtr), len(vector)*4)
		w.Write(vecBytes)
	}

	// Write Metadata
	binary.Write(w, binary.LittleEndian, metadataLen)
	if metadataLen > 0 {
		w.Write(metadata)
	}

	// Write Offset (8 bytes)
	if err := binary.Write(w, binary.LittleEndian, int64(loc.Offset)); err != nil {
		return err
	}
	// Write Len (4 bytes)
	return binary.Write(w, binary.LittleEndian, uint32(loc.Length))
}

// Path returns the filesystem path of the WAL file.
func (wal *WAL) Path() string {
	return wal.file.Name()
}

// Flush flushes the buffered writer and fsyncs to ensure durability.
// Kept for backward compatibility — prefer FlushAsync for concurrent workloads.
func (wal *WAL) Flush() error {
	return wal.flushSync()
}

// SyncCount returns the number of fsyncs performed (for testing coalescing).
func (wal *WAL) SyncCount() uint64 {
	return atomic.LoadUint64(&wal.syncCount)
}

// Close ensures everything is written to disk and synced.
func (w *WAL) Close() error {
	w.closeOnce.Do(func() {
		close(w.closeCh)
	})
	// Give fsyncLoop time to drain pending flushes.
	// The closeCh signal causes fsyncLoop to drain flushCh and exit.
	// We wait briefly then do final flush under lock.
	time.Sleep(5 * time.Millisecond)

	w.mu.Lock()
	defer w.mu.Unlock()
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

// WALIterator is the callback signature for insert-only WAL replay via [WAL.Recover].
type WALIterator func(id string, vector []float32, meta []byte, loc FileLocation)

// WALIteratorEx receives the operation type so callers can handle both inserts and deletes.
type WALIteratorEx func(op byte, id string, vector []float32, meta []byte, loc FileLocation)

// RecoverEx replays all WAL entries (inserts and deletes) in order, invoking fn for each.
// Use this during startup to perform two-pass recovery that correctly handles deletions.
func (wal *WAL) RecoverEx(fn WALIteratorEx) error {
	return wal.recoverInternal(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		fn(op, id, vector, meta, loc)
	})
}

// Recover replays insert-only WAL entries, invoking fn for each. Delete entries are
// silently skipped. Prefer [WAL.RecoverEx] when delete-awareness is needed.
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
