package mcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/memoryindex"
	"github.com/stek0v/levara/pkg/sqlcompat"
)

// ConsolidationRunsDDL is shared by both database startup paths and standalone tools.
const ConsolidationRunsDDL = `CREATE TABLE IF NOT EXISTS consolidation_runs (
 id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, collection_name TEXT NOT NULL,
 payload_json TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('applied','reverted')),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`

type consolidationRow map[string]any

type consolidationJournalRow struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Before     map[string]any `json:"before,omitempty"`
	After      string         `json:"after"`
	BeforeHash string         `json:"before_hash,omitempty"`
}

// sqlStore captures complete persisted rows; its journal contains hashes and
// retirement fields only, never copies of private memory text.
type sqlStore struct {
	deps             Deps
	collection       string
	shared           bool
	maintenanceOwner *string // server-owned trusted-local sweep namespace
	captured         map[string]consolidationRow
}

func (s *sqlStore) begin(ctx context.Context) (*sql.Tx, access.MetadataActor, access.SQLPolicy, error) {
	if s.deps == nil || s.deps.DB() == nil {
		return nil, access.MetadataActor{}, access.SQLPolicy{}, errors.New("database not configured")
	}
	tx, actor, policy, err := beginMemoryDelete(ctx, s.deps)
	if err == nil {
		err = memoryCommitRecheck(ctx, policy, actor)
	}
	if err != nil && tx != nil {
		_ = tx.Rollback()
	}
	return tx, actor, policy, err
}

func (s *sqlStore) owner(ctx context.Context, actor access.MetadataActor, policy access.SQLPolicy) (string, error) {
	if s.maintenanceOwner != nil {
		if !actor.TrustedLocal {
			return "", access.ErrDocumentForbidden
		}
		return *s.maintenanceOwner, nil
	}
	if s.shared {
		if !memoryCommitCanMutateShared(ctx, policy, actor) {
			return "", access.ErrDocumentForbidden
		}
		return "", nil
	}
	return actor.UserID, nil
}

// Preserve raw timestamp text for exact SQLite restoration; canonicalization is
// confined to hashes, where PostgreSQL drivers return time.Time instead.
func scanConsolidationRows(rows *sql.Rows) ([]consolidationRow, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []consolidationRow
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(consolidationRow, len(cols))
		for i, col := range cols {
			v := values[i]
			switch v := v.(type) {
			case nil:
				r[col] = nil
			case []byte:
				r[col] = string(v)
			case time.Time:
				r[col] = v.UTC().Format(time.RFC3339Nano)
			case bool:
				if v {
					r[col] = "1"
				} else {
					r[col] = "0"
				}
			default:
				r[col] = fmt.Sprint(v)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func consolidationTime(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, errors.New("missing consolidation timestamp")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid consolidation timestamp")
}

// Lifecycle changes need a new sync revision even when apply and revert occur
// within one clock tick. Microseconds match PostgreSQL timestamp precision.
func consolidationRevision(r consolidationRow) string {
	now := time.Now().UTC().Truncate(time.Microsecond)
	if prior, err := consolidationTime(r["updated_at"]); err == nil && !now.After(prior) {
		now = prior.Truncate(time.Microsecond).Add(time.Microsecond)
	}
	return now.Format(time.RFC3339Nano)
}

func consolidationHash(r consolidationRow) string {
	canonical := make(consolidationRow, len(r))
	for k, v := range r {
		if k == "created_at" || k == "updated_at" || k == "valid_until" {
			if t, err := consolidationTime(v); err == nil {
				v = t.UTC().Format(time.RFC3339Nano)
			}
		}
		canonical[k] = v
	}
	raw, _ := json.Marshal(canonical) // scan values are only strings or nil
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func rowString(r consolidationRow, key string) string { s, _ := r[key].(string); return s }

func loadConsolidationRow(ctx context.Context, tx *sql.Tx, deps Deps, id string) (consolidationRow, error) {
	rows, err := tx.QueryContext(ctx, deps.Q(`SELECT * FROM memories WHERE id=$1`), id)
	if err != nil {
		return nil, err
	}
	all, err := scanConsolidationRows(rows)
	if err != nil {
		return nil, err
	}
	if len(all) != 1 {
		return nil, errors.New("consolidation memory missing or changed")
	}
	return all[0], nil
}

func (s *sqlStore) Candidates(ctx context.Context, collection, room, hall string) ([]consolidate.MemoryRecord, error) {
	if collection != s.collection {
		return nil, errors.New("consolidation collection mismatch")
	}
	tx, actor, policy, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	owner, err := s.owner(ctx, actor, policy)
	if err != nil {
		return nil, err
	}
	conds := []string{"collection_name=$1", "owner_id=$2", "superseded_by=''", sqlcompat.BoolFalse("is_pinned"), "tier='raw'"}
	args := []any{collection, owner}
	for _, f := range []struct{ key, value string }{{"room", room}, {"hall", hall}} {
		if f.value != "" {
			conds = append(conds, fmt.Sprintf("%s=$%d", f.key, len(args)+1))
			args = append(args, f.value)
		}
	}
	rows, err := tx.QueryContext(ctx, s.deps.Q(`SELECT * FROM memories WHERE `+strings.Join(conds, " AND ")+` ORDER BY id`), args...)
	if err != nil {
		return nil, err
	}
	all, err := scanConsolidationRows(rows)
	if err != nil {
		return nil, err
	}
	s.captured = make(map[string]consolidationRow, len(all))
	var out []consolidate.MemoryRecord
	for _, r := range all {
		for _, key := range []string{"id", "key", "value", "type", "owner_id", "collection_name", "room", "hall", "created_at"} {
			if _, ok := r[key].(string); !ok {
				return nil, fmt.Errorf("consolidation memory missing %s", key)
			}
		}
		created, err := consolidationTime(r["created_at"])
		if err != nil {
			return nil, err
		}
		id := rowString(r, "id")
		s.captured[id] = r
		out = append(out, consolidate.MemoryRecord{ID: id, Key: rowString(r, "key"), Value: rowString(r, "value"), Type: rowString(r, "type"), OwnerID: owner, Collection: collection, Room: rowString(r, "room"), Hall: rowString(r, "hall"), CreatedAt: created})
	}
	return out, memoryCommitRecheck(ctx, policy, actor)
}

func (s *sqlStore) verifyCaptured(ctx context.Context, tx *sql.Tx, owner string, ids []string) error {
	for _, id := range ids {
		before, ok := s.captured[id]
		if !ok {
			return errors.New("consolidation plan contains an uncaptured memory")
		}
		r, err := loadConsolidationRow(ctx, tx, s.deps, id)
		if err != nil {
			return err
		}
		if rowString(r, "owner_id") != owner || rowString(r, "collection_name") != s.collection || rowString(r, "superseded_by") != "" || rowString(r, "tier") != "raw" || rowString(r, "is_pinned") != "0" || consolidationHash(r) != consolidationHash(before) {
			return errors.New("consolidation memory changed since planning")
		}
	}
	return nil
}

// providerFence also revalidates captured bytes before sending them. The caller
// keeps the SQL/credential fence until the provider actually returns.
func consolidationProviderReady(ctx context.Context, deps Deps) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	actor := deps.MetadataActor(ctx)
	if !actor.TrustedLocal && (actor.Credential.Kind == "jwt" || actor.Credential.Kind == "external") && actor.Credential.ExpiresAt <= time.Now().Unix() {
		return access.ErrRevokedCredential
	}
	return nil
}

func (s *sqlStore) providerFence(ctx context.Context) (func(), error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("consolidation provider fence requires deadline")
	}
	actor := s.deps.MetadataActor(ctx)
	policy := access.SQLPolicy{DB: s.deps.DB(), Q: s.deps.Q}
	var tx *sql.Tx
	var release func()
	var err error
	var stop func() bool
	if actor.TrustedLocal {
		conn, connErr := s.deps.DB().Conn(ctx)
		if connErr != nil {
			return nil, connErr
		}
		txCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stop = context.AfterFunc(ctx, cancel)
		tx, err = conn.BeginTx(txCtx, nil)
		if err != nil {
			stop()
			cancel()
			_ = conn.Close()
			return nil, err
		}
		release = func() { stop(); _ = tx.Rollback(); cancel(); _ = conn.Close() }
		policy = policy.WithReadTransaction(tx)
	} else {
		tx, policy, release, err = policy.BeginTransferFenceTx(ctx, memoryCommitSQLite(s.deps))
		if err != nil {
			return nil, err
		}
	}
	fail := func(err error) (func(), error) { release(); return nil, err }
	if memoryCommitSQLite(s.deps) {
		_, err = tx.ExecContext(ctx, "UPDATE memories SET id=id WHERE 1=0")
	} else {
		_, err = tx.ExecContext(ctx, "LOCK TABLE memories IN SHARE MODE")
	}
	if err != nil {
		return fail(err)
	}
	if err = memoryCommitRecheck(ctx, policy, actor); err != nil {
		return fail(err)
	}
	owner, err := s.owner(ctx, actor, policy)
	if err != nil {
		return fail(err)
	}
	ids := make([]string, 0, len(s.captured))
	for id := range s.captured {
		ids = append(ids, id)
	}
	if err = s.verifyCaptured(ctx, tx, owner, ids); err != nil {
		return fail(err)
	}
	if stop != nil && !stop() {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		return fail(errors.New("consolidation transfer cancelled before handoff"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return release, nil
}

func (s *sqlStore) outbox() (*memoryindex.Store, error) {
	if p, ok := s.deps.(interface{ MemoryIndexOutbox() *memoryindex.Store }); ok && p.MemoryIndexOutbox() != nil {
		return p.MemoryIndexOutbox(), nil
	}
	if s.deps.HasCollections() {
		return nil, errors.New("memory index outbox not configured")
	}
	return nil, nil
}

func (s *sqlStore) enqueue(ctx context.Context, tx *sql.Tx, r consolidationRow, operation, runID string) error {
	outbox, err := s.outbox()
	if err != nil || outbox == nil {
		return err
	}
	id := rowString(r, "id")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(rowString(r, "key")+"\x00"+rowString(r, "value"))))
	if operation == "delete_vector" {
		digest = "delete:" + runID + ":" + id
	}
	_, err = outbox.EnqueueTx(ctx, tx, memoryindex.Job{MemoryID: id, Operation: operation, Collection: rowString(r, "collection_name"), OwnerID: rowString(r, "owner_id"), Digest: digest, Model: s.deps.EmbedModel()})
	return err
}

func (s *sqlStore) Apply(ctx context.Context, runID string, actions []consolidate.Action) error {
	if s.deps == nil || s.deps.DB() == nil {
		return errors.New("database not configured")
	}
	if runID == "" || len(actions) == 0 {
		return errors.New("consolidation run and actions required")
	}
	if _, err := s.deps.DB().ExecContext(ctx, ConsolidationRunsDDL); err != nil {
		return err
	}
	if _, err := s.outbox(); err != nil {
		return err
	}
	tx, actor, policy, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner, err := s.owner(ctx, actor, policy)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var ids []string
	for _, a := range actions {
		if a.Kind != consolidate.ActionMerge && a.Kind != consolidate.ActionAbstract {
			return errors.New("invalid consolidation action")
		}
		if len(a.SourceIDs) == 0 || a.Kind == consolidate.ActionAbstract && (len(a.SourceIDs) < 2 || a.NewValue == "") {
			return errors.New("invalid consolidation sources")
		}
		group := append([]string{}, a.SourceIDs...)
		if a.Kind == consolidate.ActionMerge {
			if a.SurvivorID == "" {
				return errors.New("missing consolidation survivor")
			}
			group = append(group, a.SurvivorID)
		}
		first, ok := s.captured[group[0]]
		if !ok {
			return errors.New("uncaptured consolidation source")
		}
		for _, id := range group {
			if seen[id] {
				return errors.New("duplicate or overlapping consolidation memory")
			}
			seen[id] = true
			ids = append(ids, id)
			r, ok := s.captured[id]
			if !ok {
				return errors.New("uncaptured consolidation memory")
			}
			for _, key := range []string{"owner_id", "collection_name", "type", "room", "hall"} {
				if r[key] != first[key] {
					return errors.New("mixed consolidation classification")
				}
			}
		}
		if a.Kind == consolidate.ActionAbstract && (!IsValidHall(rowString(first, "hall")) || a.Room != rowString(first, "room") || a.Hall != rowString(first, "hall")) {
			return errors.New("invalid abstract classification")
		}
	}
	if err := s.verifyCaptured(ctx, tx, owner, ids); err != nil {
		return err
	}
	var journal []consolidationJournalRow
	for _, a := range actions {
		first := s.captured[a.SourceIDs[0]]
		target := a.SurvivorID
		if a.Kind == consolidate.ActionAbstract {
			target = uuid.NewString()
			from, _ := json.Marshal(a.SourceIDs)
			_, err = tx.ExecContext(ctx, s.deps.Q(`INSERT INTO memories
 (id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,superseded_by,consolidated_from,consolidation_run_id,tier,created_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,FALSE,0,'',$9,$10,'semantic',$11,$12)`), target, "consolidated:"+target, a.NewValue, rowString(first, "type"), owner, s.collection, rowString(first, "room"), rowString(first, "hall"), string(from), runID, nowTS(), nowTS())
			if err != nil {
				return err
			}
			r, err := loadConsolidationRow(ctx, tx, s.deps, target)
			if err != nil {
				return err
			}
			journal = append(journal, consolidationJournalRow{ID: target, Role: "generated", After: consolidationHash(r)})
			if err := s.enqueue(ctx, tx, r, "upsert_vector", runID); err != nil {
				return err
			}
		} else {
			journal = append(journal, consolidationJournalRow{ID: target, Role: "survivor", After: consolidationHash(s.captured[target])})
		}
		for _, id := range a.SourceIDs {
			before := s.captured[id]
			revision := consolidationRevision(before)
			retiredAt := time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
			result, err := tx.ExecContext(ctx, s.deps.Q(`UPDATE memories SET superseded_by=$1,valid_until=$2,consolidation_run_id=$3,updated_at=$4 WHERE id=$5`), target, retiredAt, runID, revision, id)
			if err != nil {
				return err
			}
			if rowsAffected(result) != 1 {
				return errors.New("consolidation memory changed")
			}
			r, err := loadConsolidationRow(ctx, tx, s.deps, id)
			if err != nil {
				return err
			}
			journal = append(journal, consolidationJournalRow{ID: id, Role: "source", BeforeHash: consolidationHash(before), Before: map[string]any{"superseded_by": before["superseded_by"], "valid_until": before["valid_until"], "consolidation_run_id": before["consolidation_run_id"], "updated_at": before["updated_at"]}, After: consolidationHash(r)})
			if err := s.enqueue(ctx, tx, r, "delete_vector", runID); err != nil {
				return err
			}
		}
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.deps.Q(`INSERT INTO consolidation_runs(id,owner_id,collection_name,payload_json,status,created_at,updated_at) VALUES($1,$2,$3,$4,'applied',$5,$6)`), runID, owner, s.collection, string(raw), nowTS(), nowTS()); err != nil {
		return err
	}
	if err := memoryCommitRecheck(ctx, policy, actor); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlStore) Revert(ctx context.Context, runID string) error {
	if s.deps == nil || s.deps.DB() == nil {
		return errors.New("database not configured")
	}
	if _, err := s.deps.DB().ExecContext(ctx, ConsolidationRunsDDL); err != nil {
		return err
	}
	if _, err := s.outbox(); err != nil {
		return err
	}
	tx, actor, policy, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner, err := s.owner(ctx, actor, policy)
	if err != nil {
		return err
	}
	var collection, raw, status string
	if err := tx.QueryRowContext(ctx, s.deps.Q(`SELECT collection_name,payload_json,status FROM consolidation_runs WHERE id=$1 AND owner_id=$2`), runID, owner).Scan(&collection, &raw, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("consolidation run unavailable or lacks safe revert journal")
		}
		return err
	}
	if status == "reverted" {
		return memoryCommitRecheck(ctx, policy, actor)
	}
	if status != "applied" {
		return errors.New("invalid consolidation run status")
	}
	var journal []consolidationJournalRow
	if err := json.Unmarshal([]byte(raw), &journal); err != nil || len(journal) == 0 {
		return errors.New("invalid consolidation revert journal")
	}
	seen := map[string]bool{}
	current := map[string]consolidationRow{}
	for _, j := range journal {
		if j.ID == "" || seen[j.ID] || len(j.After) != 64 {
			return errors.New("invalid consolidation revert journal")
		}
		seen[j.ID] = true
		switch j.Role {
		case "source":
			if (len(j.Before) != 3 && len(j.Before) != 4) || len(j.BeforeHash) != 64 {
				return errors.New("invalid consolidation retirement journal")
			}
			keys := []string{"superseded_by", "valid_until", "consolidation_run_id"}
			if len(j.Before) == 4 {
				keys = append(keys, "updated_at")
			}
			for _, key := range keys {
				v, ok := j.Before[key]
				if !ok {
					return errors.New("invalid consolidation retirement journal")
				}
				if _, ok := v.(string); !ok && (key != "valid_until" || v != nil) {
					return errors.New("invalid consolidation retirement journal")
				}
			}
		case "generated", "survivor":
			if len(j.Before) != 0 {
				return errors.New("invalid consolidation revert journal")
			}
		default:
			return errors.New("invalid consolidation revert role")
		}
		r, err := loadConsolidationRow(ctx, tx, s.deps, j.ID)
		if err != nil {
			return err
		}
		if rowString(r, "owner_id") != owner || rowString(r, "collection_name") != collection || consolidationHash(r) != j.After {
			return errors.New("consolidation row changed; revert refused")
		}
		if j.Role == "source" {
			restored := make(consolidationRow, len(r))
			for key, value := range r {
				restored[key] = value
			}
			for key, value := range j.Before {
				restored[key] = value
			}
			if consolidationHash(restored) != j.BeforeHash {
				return errors.New("corrupt consolidation retirement journal")
			}
		}
		current[j.ID] = r
	}
	roles := map[string]string{}
	for _, j := range journal {
		roles[j.ID] = j.Role
	}
	targetSources := map[string][]string{}
	affected := 0
	for _, j := range journal {
		r := current[j.ID]
		switch j.Role {
		case "source":
			target := rowString(r, "superseded_by")
			if rowString(r, "consolidation_run_id") != runID || target == "" || (roles[target] != "survivor" && roles[target] != "generated") || r["valid_until"] == nil {
				return errors.New("invalid consolidation source journal")
			}
			targetSources[target] = append(targetSources[target], j.ID)
			affected++
		case "generated":
			if rowString(r, "tier") != "semantic" || rowString(r, "consolidation_run_id") != runID || rowString(r, "superseded_by") != "" {
				return errors.New("invalid generated consolidation journal")
			}
			affected++
		case "survivor":
			if rowString(r, "tier") != "raw" || rowString(r, "superseded_by") != "" || rowString(r, "consolidation_run_id") == runID {
				return errors.New("invalid consolidation survivor journal")
			}
		}
	}
	for _, j := range journal {
		if j.Role == "source" {
			continue
		}
		if len(targetSources[j.ID]) == 0 {
			return errors.New("incomplete consolidation target journal")
		}
		if j.Role == "generated" {
			var lineage []string
			if err := json.Unmarshal([]byte(rowString(current[j.ID], "consolidated_from")), &lineage); err != nil || len(lineage) != len(targetSources[j.ID]) {
				return errors.New("invalid consolidation lineage journal")
			}
			lineageSet := map[string]bool{}
			for _, id := range lineage {
				if lineageSet[id] || roles[id] != "source" || rowString(current[id], "superseded_by") != j.ID {
					return errors.New("invalid consolidation lineage journal")
				}
				lineageSet[id] = true
			}
		}
	}
	rows, err := tx.QueryContext(ctx, s.deps.Q("SELECT id FROM memories WHERE consolidation_run_id=$1"), runID)
	if err != nil {
		return err
	}
	found := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if roles[id] != "source" && roles[id] != "generated" {
			_ = rows.Close()
			return errors.New("incomplete consolidation revert journal")
		}
		found++
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if found != affected || affected == 0 {
		return errors.New("incomplete consolidation revert journal")
	}
	for _, j := range journal {
		var result sql.Result
		switch j.Role {
		case "source":
			result, err = tx.ExecContext(ctx, s.deps.Q(`UPDATE memories SET superseded_by=$1,valid_until=$2,consolidation_run_id=$3,updated_at=$4 WHERE id=$5`), j.Before["superseded_by"], j.Before["valid_until"], j.Before["consolidation_run_id"], consolidationRevision(current[j.ID]), j.ID)
		case "generated":
			result, err = tx.ExecContext(ctx, s.deps.Q(`DELETE FROM memories WHERE id=$1`), j.ID)
		case "survivor":
			continue
		}
		if err != nil {
			return err
		}
		if rowsAffected(result) != 1 {
			return errors.New("consolidation row changed")
		}
		op := "upsert_vector"
		if j.Role == "generated" {
			op = "delete_vector"
		}
		if err := s.enqueue(ctx, tx, current[j.ID], op, "revert:"+runID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, s.deps.Q(`UPDATE consolidation_runs SET status='reverted',updated_at=$1 WHERE id=$2 AND status='applied'`), nowTS(), runID)
	if err != nil {
		return err
	}
	if rowsAffected(result) != 1 {
		return errors.New("consolidation run changed")
	}
	if err := memoryCommitRecheck(ctx, policy, actor); err != nil {
		return err
	}
	return tx.Commit()
}
