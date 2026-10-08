package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/stek0v/levara/pkg/memoryindex"
)

type syncMemoryIncarnation struct {
	MemoryID            string `json:"memory_id"`
	OwnerID             string `json:"owner_id"`
	CollectionName      string `json:"collection_name"`
	LogicalKey          string `json:"logical_key"`
	OriginalKeyResolved int    `json:"original_key_resolved"`
	Generation          int64  `json:"generation"`
	StateRevision       int64  `json:"state_revision"`
	State               string `json:"state"`
}
type syncMemoryAlias struct {
	AliasID  string `json:"alias_id"`
	MemoryID string `json:"memory_id"`
}
type syncMemoryGenerationKey struct {
	Owner, Collection, Key string
	Generation             int64
}

func (i syncMemoryIncarnation) identity() syncMemoryGenerationKey {
	return syncMemoryGenerationKey{i.OwnerID, i.CollectionName, i.LogicalKey, i.Generation}
}
func syncMemoryStateRank(s string) int {
	if s == "deleted" {
		return 2
	}
	if s == "retired" {
		return 1
	}
	return 0
}
func syncMemoryStateWins(in, current syncMemoryIncarnation) bool {
	if current.State == "deleted" {
		return in.State == "deleted" && in.StateRevision > current.StateRevision
	}
	return in.StateRevision > current.StateRevision || in.StateRevision == current.StateRevision && syncMemoryStateRank(in.State) > syncMemoryStateRank(current.State)
}

const syncGenerationMemoryColumns = `id,key,value,type,owner_id,collection_name,COALESCE(room,''),COALESCE(hall,''),is_pinned,pin_priority,CAST(created_at AS TEXT),CAST(updated_at AS TEXT),COALESCE(CAST(valid_until AS TEXT),''),COALESCE(superseded_by,''),COALESCE(supersedes_memory_id,''),COALESCE(supersession_reason,''),tier,consolidated_from,consolidation_run_id`

func scanSyncGenerationMemory(row interface{ Scan(...any) error }) (m syncMemory, err error) {
	err = row.Scan(&m.ID, &m.Key, &m.Value, &m.Type, &m.OwnerID, &m.CollectionName, &m.Room, &m.Hall, &m.IsPinned, &m.PinPriority, &m.CreatedAt, &m.UpdatedAt, &m.ValidUntil, &m.SupersededBy, &m.SupersedesMemoryID, &m.SupersessionReason, &m.Tier, &m.ConsolidatedFrom, &m.ConsolidationRunID)
	if err != nil {
		return
	}
	m.CreatedAt, err = normalizeSyncTimestamp(m.CreatedAt, false)
	if err != nil {
		return
	}
	m.UpdatedAt, err = normalizeSyncTimestamp(m.UpdatedAt, false)
	if err != nil {
		return
	}
	m.ValidUntil, err = normalizeSyncTimestamp(m.ValidUntil, true)
	return
}

// ponytail: a complete fenced snapshot avoids wall-clock cursor gaps.
func exportSyncMemoryGenerationBatch(ctx context.Context, cfg APIConfig, since string) (batch syncMemoryBatch, err error) {
	batch = syncMemoryBatch{ProtocolVersion: syncMemoryProtocolVersion, Memories: []syncMemory{}, Deletions: []syncMemoryDeletion{}, Incarnations: []syncMemoryIncarnation{}, Aliases: []syncMemoryAlias{}}
	if _, err = syncExportBoundary(since); err != nil {
		return
	}
	if cfg.DB == nil {
		return batch, errors.New("database not configured")
	}
	tx, err := beginSyncImportTx(ctx, cfg.DB, "memories")
	if err != nil {
		return batch, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, Q("SELECT "+syncGenerationMemoryColumns+" FROM memories ORDER BY id"))
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		m, e := scanSyncGenerationMemory(rows)
		if e != nil {
			rows.Close()
			return batch, e
		}
		batch.Memories = append(batch.Memories, m)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return batch, err
	}
	rows, err = tx.QueryContext(ctx, Q(`SELECT memory_id,key,owner_id,collection_name,deleted_at FROM memory_sync_deletions ORDER BY memory_id`))
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		var d syncMemoryDeletion
		if err = rows.Scan(&d.ID, &d.Key, &d.OwnerID, &d.CollectionName, &d.DeletedAt); err != nil {
			rows.Close()
			return batch, err
		}
		batch.Deletions = append(batch.Deletions, d)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return batch, err
	}
	rows, err = tx.QueryContext(ctx, Q(`SELECT memory_id,owner_id,collection_name,logical_key,original_key_resolved,generation,state_revision,state FROM memory_sync_incarnations ORDER BY generation,memory_id`))
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		var i syncMemoryIncarnation
		if err = rows.Scan(&i.MemoryID, &i.OwnerID, &i.CollectionName, &i.LogicalKey, &i.OriginalKeyResolved, &i.Generation, &i.StateRevision, &i.State); err != nil {
			rows.Close()
			return batch, err
		}
		if i.OriginalKeyResolved != 1 {
			rows.Close()
			return batch, errors.New("sync original logical key history unavailable")
		}
		batch.Incarnations = append(batch.Incarnations, i)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return batch, err
	}
	rows, err = tx.QueryContext(ctx, Q(`SELECT alias_id,memory_id FROM memory_sync_aliases ORDER BY alias_id`))
	if err != nil {
		return batch, err
	}
	for rows.Next() {
		var a syncMemoryAlias
		if err = rows.Scan(&a.AliasID, &a.MemoryID); err != nil {
			rows.Close()
			return batch, err
		}
		batch.Aliases = append(batch.Aliases, a)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return batch, err
	}
	if err = batch.validateGeneration(); err != nil {
		return batch, err
	}
	return batch, tx.Commit()
}
func (b syncMemoryBatch) validateGeneration() error {
	byID := map[string]syncMemoryIncarnation{}
	tuples := map[syncMemoryGenerationKey]bool{}
	for _, i := range b.Incarnations {
		if i.MemoryID == "" || i.OriginalKeyResolved != 1 || i.Generation < 0 || i.StateRevision < 0 || (i.State != "active" && i.State != "retired" && i.State != "deleted") {
			return errors.New("invalid sync incarnation history")
		}
		if _, ok := byID[i.MemoryID]; ok || tuples[i.identity()] {
			return errors.New("duplicate sync incarnation identity")
		}
		byID[i.MemoryID] = i
		tuples[i.identity()] = true
	}
	aliases := map[string]string{}
	for _, a := range b.Aliases {
		if a.AliasID == "" || a.MemoryID == "" {
			return errors.New("invalid sync alias")
		}
		if _, ok := byID[a.MemoryID]; !ok {
			return errors.New("sync alias unresolved incarnation")
		}
		if _, ok := aliases[a.AliasID]; ok {
			return errors.New("duplicate sync alias")
		}
		aliases[a.AliasID] = a.MemoryID
	}
	rows := map[string]bool{}
	for _, m := range b.Memories {
		i, ok := byID[m.ID]
		if !ok || rows[m.ID] || aliases[m.ID] != m.ID || m.OwnerID != i.OwnerID || m.CollectionName != i.CollectionName || i.State == "deleted" {
			return errors.New("sync memory incarnation binding conflict")
		}
		if (m.ValidUntil != "" || m.SupersededBy != "") != (i.State == "retired") {
			return errors.New("sync memory lifecycle state conflict")
		}
		if m.Key != i.LogicalKey && (i.State != "retired" || m.Key != i.LogicalKey+"#superseded:"+m.ID) {
			return errors.New("sync logical key binding conflict")
		}
		rows[m.ID] = true
	}
	deletions := map[string]bool{}
	for _, d := range b.Deletions {
		i, ok := byID[d.ID]
		if !ok || deletions[d.ID] || i.State != "deleted" || d.OwnerID != i.OwnerID || d.CollectionName != i.CollectionName {
			return errors.New("sync deletion incarnation binding conflict")
		}
		if d.Key != i.LogicalKey && d.Key != i.LogicalKey+"#superseded:"+d.ID {
			return errors.New("sync deletion logical key binding conflict")
		}
		if _, err := normalizeSyncTimestamp(d.DeletedAt, false); err != nil {
			return err
		}
		deletions[d.ID] = true
	}
	for _, i := range b.Incarnations {
		if aliases[i.MemoryID] != i.MemoryID {
			return errors.New("sync canonical alias missing")
		}
		if i.State != "deleted" && !rows[i.MemoryID] || i.State == "deleted" && !deletions[i.MemoryID] {
			return errors.New("sync incarnation payload history missing")
		}
	}
	return nil
}
func importSyncMemoryGenerations(ctx context.Context, cfg APIConfig, b syncMemoryBatch) (counts map[string]int, err error) {
	total := len(b.Memories) + len(b.Deletions)
	counts = map[string]int{"imported": 0, "skipped": 0, "failed": 0, "total": total}
	fail := func(e error) (map[string]int, error) {
		counts["imported"], counts["skipped"], counts["failed"] = 0, 0, total
		return counts, e
	}
	if err = b.validateGeneration(); err != nil {
		return fail(err)
	}
	if cfg.DB == nil {
		return fail(errors.New("database not configured"))
	}
	tx, err := beginSyncImportTx(ctx, cfg.DB, "memories")
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	local := map[string]syncMemoryIncarnation{}
	byTuple := map[syncMemoryGenerationKey]string{}
	rows, err := tx.QueryContext(ctx, Q(`SELECT memory_id,owner_id,collection_name,logical_key,original_key_resolved,generation,state_revision,state FROM memory_sync_incarnations`))
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var i syncMemoryIncarnation
		if err = rows.Scan(&i.MemoryID, &i.OwnerID, &i.CollectionName, &i.LogicalKey, &i.OriginalKeyResolved, &i.Generation, &i.StateRevision, &i.State); err != nil {
			rows.Close()
			return fail(err)
		}
		local[i.MemoryID] = i
		if i.OriginalKeyResolved == 1 {
			if _, ok := byTuple[i.identity()]; ok {
				rows.Close()
				return fail(errors.New("ambiguous local incarnation"))
			}
			byTuple[i.identity()] = i.MemoryID
		}
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return fail(err)
	}
	aliases := map[string]string{}
	rows, err = tx.QueryContext(ctx, Q(`SELECT alias_id,memory_id FROM memory_sync_aliases`))
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var a syncMemoryAlias
		if err = rows.Scan(&a.AliasID, &a.MemoryID); err != nil {
			rows.Close()
			return fail(err)
		}
		aliases[a.AliasID] = a.MemoryID
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return fail(err)
	}
	canonical := map[string]string{}
	incoming := map[string]syncMemoryIncarnation{}
	chosen := map[string]syncMemoryIncarnation{}
	ordered := append([]syncMemoryIncarnation(nil), b.Incarnations...)
	sort.Slice(ordered, func(a, c int) bool { return ordered[a].Generation < ordered[c].Generation })
	for _, i := range ordered {
		incoming[i.MemoryID] = i
		id := aliases[i.MemoryID]
		if id != "" {
			old := local[id]
			if old.OriginalKeyResolved != 1 || old.identity() != i.identity() {
				return fail(errors.New("sync immutable alias identity conflict"))
			}
		}
		if id == "" {
			id = byTuple[i.identity()]
		}
		if id == "" {
			id = i.MemoryID
			if _, ok := local[id]; ok {
				return fail(errors.New("sync physical ID identity conflict"))
			}
			for _, old := range local {
				if old.OwnerID == i.OwnerID && old.CollectionName == i.CollectionName && old.OriginalKeyResolved != 1 {
					return fail(errors.New("sync original logical key history unavailable"))
				}
			}
		}
		canonical[i.MemoryID] = id
		candidate := i
		candidate.MemoryID = id
		if old, ok := local[id]; ok && !syncMemoryStateWins(i, old) {
			candidate = old
		}
		chosen[id] = candidate
		byTuple[i.identity()] = id
	}
	for _, i := range chosen {
		if i.Generation > int64(len(local)+len(chosen)) {
			return fail(errors.New("sync generation predecessor history unavailable"))
		}
		for g := int64(0); g < i.Generation; g++ {
			k := i.identity()
			k.Generation = g
			id := byTuple[k]
			old, ok := chosen[id]
			if !ok {
				old, ok = local[id]
			}
			if !ok || old.State == "active" {
				return fail(errors.New("sync generation predecessor history unavailable"))
			}
		}
	}
	for _, a := range b.Aliases {
		id := canonical[a.MemoryID]
		if old, ok := aliases[a.AliasID]; ok && old != id {
			return fail(errors.New("sync immutable alias identity conflict"))
		}
		if old, ok := local[a.AliasID]; ok && old.MemoryID != id {
			return fail(errors.New("sync physical ID identity conflict"))
		}
		aliases[a.AliasID] = id
	}
	// Register chosen state before native SQL so triggers do not allocate or advance it twice.
	for _, i := range chosen {
		query, args := QArgs(`INSERT INTO memory_sync_incarnations(memory_id,owner_id,collection_name,logical_key,original_key_resolved,generation,state_revision,state) VALUES($1,$2,$3,$4,1,$5,$6,$7) ON CONFLICT(memory_id) DO UPDATE SET state_revision=$6,state=$7`, i.MemoryID, i.OwnerID, i.CollectionName, i.LogicalKey, i.Generation, i.StateRevision, i.State)
		if _, err = tx.ExecContext(ctx, query, args...); err != nil {
			return fail(err)
		}
		query, args = QArgs(`INSERT INTO memory_sync_heads(owner_id,collection_name,logical_key,generation) VALUES($1,$2,$3,$4) ON CONFLICT(owner_id,collection_name,logical_key) DO UPDATE SET generation=CASE WHEN memory_sync_heads.generation<excluded.generation THEN excluded.generation ELSE memory_sync_heads.generation END`, i.OwnerID, i.CollectionName, i.LogicalKey, i.Generation)
		if _, err = tx.ExecContext(ctx, query, args...); err != nil {
			return fail(err)
		}
	}
	for _, a := range b.Aliases {
		if _, err = tx.ExecContext(ctx, Q(`INSERT INTO memory_sync_aliases(alias_id,memory_id) VALUES($1,$2) ON CONFLICT(alias_id) DO NOTHING`), a.AliasID, canonical[a.MemoryID]); err != nil {
			return fail(err)
		}
	}
	resolve := func(m syncMemory, ref string) (string, error) {
		if ref == "" {
			return "", nil
		}
		id := canonical[ref]
		if id == "" {
			id = aliases[ref]
		}
		i, ok := chosen[id]
		if !ok {
			i, ok = local[id]
		}
		if !ok || i.OwnerID != m.OwnerID || i.CollectionName != m.CollectionName {
			return "", errors.New("sync lifecycle unresolved or cross-scope reference")
		}
		if id == canonical[m.ID] {
			return "", errors.New("sync lifecycle canonical self reference")
		}
		return id, nil
	}
	prepared := map[string]syncMemory{}
	for _, m := range b.Memories {
		source := m.ID
		id := canonical[source]
		i := chosen[id]
		m.UpdatedAt, err = normalizeSyncTimestamp(m.UpdatedAt, false)
		if err != nil {
			return fail(err)
		}
		m.CreatedAt, err = normalizeSyncTimestamp(m.CreatedAt, false)
		if err != nil {
			return fail(err)
		}
		m.ValidUntil, err = normalizeSyncTimestamp(m.ValidUntil, true)
		if err != nil {
			return fail(err)
		}
		if m.SupersededBy != "" && m.ValidUntil == "" {
			return fail(errors.New("sync retired memory validity required"))
		}
		m.SupersededBy, err = resolve(m, m.SupersededBy)
		if err != nil {
			return fail(err)
		}
		m.SupersedesMemoryID, err = resolve(m, m.SupersedesMemoryID)
		if err != nil {
			return fail(err)
		}
		if m.ConsolidatedFrom != "" {
			var refs []string
			if err = json.Unmarshal([]byte(m.ConsolidatedFrom), &refs); err != nil {
				return fail(err)
			}
			for n, ref := range refs {
				refs[n], err = resolve(m, ref)
				if err != nil {
					return fail(err)
				}
			}
			raw, _ := json.Marshal(refs)
			m.ConsolidatedFrom = string(raw)
		}
		if m.Tier == "" {
			m.Tier = "raw"
		}
		if m.Tier != "raw" && m.Tier != "semantic" && m.Tier != "consolidated" {
			return fail(errors.New("invalid sync memory tier"))
		}
		if m.Key != incoming[source].LogicalKey {
			if incoming[source].State != "retired" || m.Key != incoming[source].LogicalKey+"#superseded:"+source {
				return fail(errors.New("sync logical key binding conflict"))
			}
			m.Key = i.LogicalKey + "#superseded:" + id
		}
		m.ID = id
		prepared[id] = m
	}
	// Retire/delete earlier generations first, freeing the active logical key.
	sort.Slice(ordered, func(a, c int) bool {
		ra, rc := syncMemoryStateRank(chosen[canonical[ordered[a].MemoryID]].State), syncMemoryStateRank(chosen[canonical[ordered[c].MemoryID]].State)
		if ra != rc {
			return ra > rc
		}
		return ordered[a].Generation < ordered[c].Generation
	})
	for _, remote := range ordered {
		id := canonical[remote.MemoryID]
		i := chosen[id]
		old, exists := local[id]
		current, e := scanSyncGenerationMemory(tx.QueryRowContext(ctx, Q("SELECT "+syncGenerationMemoryColumns+" FROM memories WHERE id=$1"), id))
		hasRow := e == nil
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return fail(e)
		}
		stateChanged := !exists || old.State != i.State || old.StateRevision != i.StateRevision
		if i.State == "deleted" {
			if hasRow {
				if _, err = tx.ExecContext(ctx, Q(`DELETE FROM memories WHERE id=$1`), id); err != nil {
					return fail(err)
				}
			}
			var deletion syncMemoryDeletion
			for _, d := range b.Deletions {
				if d.ID == remote.MemoryID {
					deletion = d
					break
				}
			}
			if deletion.ID != "" {
				stamp, e := normalizeSyncTimestamp(deletion.DeletedAt, false)
				if e != nil {
					return fail(e)
				}
				key := deletion.Key
				if hasRow {
					key = current.Key
				}
				if _, err = tx.ExecContext(ctx, Q(`INSERT INTO memory_sync_deletions(memory_id,key,owner_id,collection_name,deleted_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(memory_id,owner_id,collection_name) DO NOTHING`), id, key, i.OwnerID, i.CollectionName, stamp); err != nil {
					return fail(err)
				}
			}
			if hasRow || stateChanged {
				if err = enqueueSyncGenerationIntent(ctx, cfg, tx, syncMemory{ID: id, OwnerID: i.OwnerID, CollectionName: i.CollectionName}, true); err != nil {
					return fail(err)
				}
			}
			if remote.State == "deleted" {
				if hasRow || stateChanged {
					counts["imported"]++
				} else {
					counts["skipped"]++
				}
			}
			if remote.State != "deleted" {
				counts["skipped"]++
			}
			continue
		}
		m := prepared[id]
		// A stale remote state only contributes its immutable aliases.
		if exists && (remote.State != i.State || remote.StateRevision != i.StateRevision) {
			counts["skipped"]++
			continue
		}
		if hasRow {
			m.CreatedAt = current.CreatedAt
			incomingTime, _ := time.Parse(time.RFC3339Nano, m.UpdatedAt)
			currentTime, _ := time.Parse(time.RFC3339Nano, current.UpdatedAt)
			if !stateChanged {
				losing := incomingTime.Before(currentTime)
				if incomingTime.Equal(currentTime) {
					wanted, e := syncMemoryGenerationContentRank(m, chosen, local)
					if e != nil {
						return fail(e)
					}
					existing, e := syncMemoryGenerationContentRank(current, chosen, local)
					if e != nil {
						return fail(e)
					}
					losing = wanted <= existing
				}
				if losing {
					counts["skipped"]++
					continue
				}
			}
		}
		if i.State == "retired" {
			k := i.identity()
			for tuple := range byTuple {
				if tuple.Owner == k.Owner && tuple.Collection == k.Collection && tuple.Key == k.Key && tuple.Generation > k.Generation {
					m.Key = i.LogicalKey + "#superseded:" + id
					break
				}
			}
		}
		if cfg.MemoryIndexOutbox == nil && cfg.EmbedEndpoint != "" && cfg.Collections != nil {
			return fail(errors.New("sync memory index outbox not configured"))
		}
		if hasRow {
			provenance := ""
			if current.Value != m.Value {
				provenance = ",source_task_id='',source_receipt_ids='[]',verification_status='unverified'"
			}
			query, args := QArgs(`UPDATE memories SET key=$2,value=$3,type=$4,room=$5,hall=$6,is_pinned=$7,pin_priority=$8,updated_at=$9,valid_until=$10,superseded_by=$11,supersedes_memory_id=$12,supersession_reason=$13,tier=$14,consolidated_from=$15,consolidation_run_id=$16`+provenance+` WHERE id=$1`, id, m.Key, m.Value, m.Type, m.Room, m.Hall, m.IsPinned, m.PinPriority, m.UpdatedAt, syncOptionalTime(m.ValidUntil), m.SupersededBy, m.SupersedesMemoryID, m.SupersessionReason, m.Tier, m.ConsolidatedFrom, m.ConsolidationRunID)
			if _, err = tx.ExecContext(ctx, query, args...); err != nil {
				return fail(err)
			}
		} else {
			query, args := QArgs(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at,valid_until,superseded_by,supersedes_memory_id,supersession_reason,tier,consolidated_from,consolidation_run_id,source_task_id,source_receipt_ids,verification_status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'','[]','unverified')`, id, m.Key, m.Value, m.Type, m.OwnerID, m.CollectionName, m.Room, m.Hall, m.IsPinned, m.PinPriority, m.CreatedAt, m.UpdatedAt, syncOptionalTime(m.ValidUntil), m.SupersededBy, m.SupersedesMemoryID, m.SupersessionReason, m.Tier, m.ConsolidatedFrom, m.ConsolidationRunID)
			if _, err = tx.ExecContext(ctx, query, args...); err != nil {
				return fail(err)
			}
		}
		if err = enqueueSyncGenerationIntent(ctx, cfg, tx, m, i.State == "retired"); err != nil {
			return fail(err)
		}
		counts["imported"]++
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return counts, nil
}
func enqueueSyncGenerationIntent(ctx context.Context, cfg APIConfig, tx *sql.Tx, m syncMemory, deleted bool) error {
	if cfg.MemoryIndexOutbox == nil {
		if cfg.EmbedEndpoint != "" && cfg.Collections != nil {
			return errors.New("sync memory index outbox not configured")
		}
		return nil
	}
	operation, digest, model := "upsert_vector", fmt.Sprintf("%x", sha256.Sum256([]byte(m.Key+"\x00"+m.Value))), cfg.EmbedModel
	if deleted {
		operation, digest, model = "delete_vector", "delete:"+m.ID, ""
	}
	job, err := cfg.MemoryIndexOutbox.EnqueueTx(ctx, tx, memoryindex.Job{MemoryID: m.ID, Operation: operation, Collection: m.CollectionName, OwnerID: m.OwnerID, Digest: digest, Model: model})
	if err != nil {
		return err
	}
	if job.MemoryID != m.ID || job.OwnerID != m.OwnerID || job.Collection != m.CollectionName || job.Operation != operation || job.Digest != digest || job.Model != model {
		return errors.New("sync memory outbox identity conflict")
	}
	return nil
}

// Rank logical reference identities, not receiver-local UUID spellings.
func syncMemoryGenerationContentRank(m syncMemory, chosen, local map[string]syncMemoryIncarnation) (string, error) {
	reference := func(id string) (string, error) {
		if id == "" {
			return "", nil
		}
		i, ok := chosen[id]
		if !ok {
			i, ok = local[id]
		}
		if !ok || i.OriginalKeyResolved != 1 || i.OwnerID != m.OwnerID || i.CollectionName != m.CollectionName {
			return "", errors.New("sync rank reference identity unavailable")
		}
		encoded, _ := json.Marshal(struct {
			Owner      string
			Collection string
			LogicalKey string
			Generation int64
		}{i.OwnerID, i.CollectionName, i.LogicalKey, i.Generation})
		return string(encoded), nil
	}
	var err error
	m.SupersededBy, err = reference(m.SupersededBy)
	if err != nil {
		return "", err
	}
	m.SupersedesMemoryID, err = reference(m.SupersedesMemoryID)
	if err != nil {
		return "", err
	}
	if m.ConsolidatedFrom != "" {
		var refs []string
		if err = json.Unmarshal([]byte(m.ConsolidatedFrom), &refs); err != nil {
			return "", err
		}
		for n, id := range refs {
			refs[n], err = reference(id)
			if err != nil {
				return "", err
			}
		}
		encoded, _ := json.Marshal(refs)
		m.ConsolidatedFrom = string(encoded)
	}
	return syncMemoryRank(m), nil
}
