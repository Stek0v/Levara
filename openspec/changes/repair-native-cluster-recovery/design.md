# Design

## Native Raft

Retain Hashicorp Raft TCP/Bolt/file-snapshot implementation. Set LeaderLeaseTimeout no larger than the existing heartbeat. Test the production constructor in actual children and three independent roots. Successful writes require quorum acknowledgements; timed-out writes are uncertain. Test acknowledged replacement after recovery rather than asserting uncertain writes absent.

## HTTP replication

HTTP primary/replica is a single-shard replication mode without quorum or failover. Use a versioned initial snapshot plus watermark on each live stream. Register the listener, capture snapshot and watermark under the existing server exclusive lock; DirectNode mutations and fanout use the same lock. Encode over network after unlock. Successful entries are ordered and contiguous. Failed mutations do not broadcast. Overflow or replaced listener invalidates its old stream; cleanup removes only its own listener.

The client validates initial frame/version, restores native snapshot, and then requires exact sequence increments and valid operation/application. EOF, malformed data, gaps or apply errors reconnect to a new snapshot. Use standard JSON decoding so a legitimate snapshot is not limited by the previous1MiB scanner line ceiling. Preserve initial Start readiness only after applied snapshot. No mixed-version fallback; experimental peers must upgrade together.

## Limits and acceptance

A global per-replication-server lock is the minimal correct fence; per-shard locks require measured throughput need. Direct store mutations outside DirectNode do not acquire this replication fence. Server HTTP bootstrap currently snapshots one shard: unsupported multi-shard replication must fail configuration validation rather than claim agreement. Collections, SQL sync and linearizable reads are distinct tasks.

Native proof includes offline insert/delete without rebroadcast, admission races, child SIGKILL and standalone WAL reopen. Raft proof includes actual constructor, three voters, leader death, restart catch-up, quorum loss without false acknowledgement, uncertain-write recovery, real file snapshots and network-free WAL reopen. No snapshot installation/compaction or power-loss proof is implied.

## Native WAL recovery and admission

Before rebuilding metadata, validate complete WAL frames and every visible structural field in an incomplete final frame. Only a structurally feasible physical tail is truncated and synced on the existing locked descriptor; public WAL readers remain nonmutating. Refuse visible corruption without changing WAL or metadata. A recovered root must accept a new acknowledged write that survives another independent process reopening it.

Use the existing durable format limits consistently: nonempty ID, ID and serialized metadata each at most 1 MiB, fixed finite vector dimension. Insert, batch admission and whole snapshot inventory validation precede metadata writes; the low-level writer applies format bounds before emitting a header. Exact limits remain supported. Previously produced empty-ID or oversized legacy WAL records now fail startup without modification; there is no automatic migration or silent discard. The unchecksummed legacy format cannot distinguish every arbitrary corruption from a feasible interrupted write.


Native snapshot enumeration must fail on unavailable vector/location/nonempty metadata rather than publish null or substitute {}. HTTP captures fail before listener admission/HTTP200; FSM snapshot fails before persistence. Checkpoint validates the complete inventory before opening the staged WAL, and production rebuild uses checked enumeration. Zero-length metadata is legitimate and survives native checkpoint/reopen. The legacy AllRecords no-error signature remains a compatibility wrapper; durable production callers use AllRecordsChecked.


Active per-record batch replication first serializes each metadata value with native BatchInsert JSON rules. The same RawMessage is used for local Insert and ordered broadcast, so listener admission cannot change stored JSON strings or byte-slice encoding. Deterministic marshal failures do not invalidate listeners or emit an entry.
