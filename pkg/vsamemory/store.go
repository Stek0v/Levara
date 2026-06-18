// Package vsamemory stores predicate-sharded VSA fact vectors over Levara's SQL graph.
package vsamemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/stek0v/levara/pkg/vsa"
)

type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

type Config struct {
	Dim       int
	ShardSize int
	Dialect   Dialect
}

type Store struct {
	db        *sql.DB
	dim       int
	shardSize int
	dialect   Dialect
}

type Candidate struct {
	TargetID     string  `json:"target_id"`
	TargetName   string  `json:"target_name,omitempty"`
	EdgeID       string  `json:"edge_id"`
	Predicate    string  `json:"predicate"`
	DatasetID    string  `json:"dataset_id"`
	DomainID     string  `json:"domain_id,omitempty"`
	CollectionID string  `json:"collection_id,omitempty"`
	DocumentID   string  `json:"document_id,omitempty"`
	ShardID      string  `json:"shard_id"`
	Similarity   float64 `json:"similarity"`
	RerankScore  float64 `json:"rerank_score,omitempty"`
	Confidence   float64 `json:"confidence,omitempty"`
}

type QueryOptions struct {
	QueryText string
	TopK      int
	Rerank    bool
}

type Stats struct {
	Available     bool     `json:"available"`
	Datasets      []string `json:"datasets"`
	Predicates    []string `json:"predicates"`
	ShardCount    int      `json:"shard_count"`
	MemberCount   int      `json:"member_count"`
	FactCount     int      `json:"fact_count"`
	MaxDim        int      `json:"max_dim"`
	LastUpdatedAt string   `json:"last_updated_at,omitempty"`
}

func NewStore(db *sql.DB, cfg Config) *Store {
	dim := cfg.Dim
	if dim <= 0 {
		dim = vsa.DefaultDim
	}
	shardSize := cfg.ShardSize
	if shardSize <= 0 {
		shardSize = 12
	}
	dialect := cfg.Dialect
	if dialect == "" {
		dialect = DialectPostgres
	}
	return &Store{db: db, dim: dim, shardSize: shardSize, dialect: dialect}
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	for _, stmt := range s.schemaStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RebuildFromGraph(ctx context.Context, datasetID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM vsa_fact_members WHERE dataset_id = $1`), datasetID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM vsa_fact_shards WHERE dataset_id = $1`), datasetID); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, s.q(`
		SELECT id, source_id, relationship_name, target_id, COALESCE(dataset_id, '')
		FROM graph_edges
		WHERE ($1 = '' OR dataset_id = $2)
		  AND relationship_name <> ''
		  AND source_id <> ''
		  AND target_id <> ''
		  AND (valid_until IS NULL OR valid_until = '')
		ORDER BY relationship_name, source_id, target_id, id`), datasetID, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()

	type edgeFact struct {
		id        string
		sourceID  string
		predicate string
		targetID  string
		datasetID string
	}
	byPredicate := map[string][]edgeFact{}
	for rows.Next() {
		var f edgeFact
		if err := rows.Scan(&f.id, &f.sourceID, &f.predicate, &f.targetID, &f.datasetID); err != nil {
			return err
		}
		byPredicate[f.predicate] = append(byPredicate[f.predicate], f)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	predicates := make([]string, 0, len(byPredicate))
	for predicate := range byPredicate {
		predicates = append(predicates, predicate)
	}
	sort.Strings(predicates)

	now := time.Now().UTC()
	for _, predicate := range predicates {
		facts := byPredicate[predicate]
		for shardIndex, start := 0, 0; start < len(facts); shardIndex, start = shardIndex+1, start+s.shardSize {
			end := start + s.shardSize
			if end > len(facts) {
				end = len(facts)
			}
			shardID := shardKey(datasetID, predicate, shardIndex)
			var counts vsa.Counts
			for _, fact := range facts[start:end] {
				encoded, err := vsa.EncodeFact(fact.sourceID, fact.predicate, fact.targetID, s.dim)
				if err != nil {
					return err
				}
				counts, err = vsa.Add(counts, encoded)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, s.q(`
					INSERT INTO vsa_fact_members (shard_id, edge_id, source_id, target_id, predicate, dataset_id)
					VALUES ($1, $2, $3, $4, $5, $6)`),
					shardID, fact.id, fact.sourceID, fact.targetID, fact.predicate, datasetID); err != nil {
					return err
				}
			}
			vecJSON, _ := json.Marshal(counts)
			if _, err := tx.ExecContext(ctx, s.q(`
				INSERT INTO vsa_fact_shards (id, dataset_id, predicate, shard_index, dim, fact_count, vector_json, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
				shardID, datasetID, predicate, shardIndex, s.dim, end-start, string(vecJSON), now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) QueryObject(ctx context.Context, datasetID, subjectID, predicate string, topK int) ([]Candidate, error) {
	return s.QueryObjectWithOptions(ctx, datasetID, subjectID, predicate, QueryOptions{TopK: topK})
}

func (s *Store) QueryObjectWithOptions(ctx context.Context, datasetID, subjectID, predicate string, opts QueryOptions) ([]Candidate, error) {
	if s == nil || s.db == nil || subjectID == "" || predicate == "" {
		return nil, nil
	}
	topK := opts.TopK
	if topK <= 0 {
		topK = 10
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, dim, vector_json
		FROM vsa_fact_shards
		WHERE dataset_id = $1 AND predicate = $2
		ORDER BY shard_index`), datasetID, predicate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scoreByTarget := map[string]Candidate{}
	for rows.Next() {
		var shardID, raw string
		var dim int
		if err := rows.Scan(&shardID, &dim, &raw); err != nil {
			return nil, err
		}
		if dim <= 0 {
			dim = s.dim
		}
		key, err := vsa.QueryKey(subjectID, predicate, dim)
		if err != nil {
			return nil, err
		}
		var counts vsa.Counts
		if err := json.Unmarshal([]byte(raw), &counts); err != nil {
			return nil, err
		}
		estimate, err := vsa.BindCounts(key, counts)
		if err != nil {
			return nil, err
		}
		members, err := s.membersForShard(ctx, shardID, subjectID)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			score, err := vsa.CountSimilarity(estimate, vsa.Symbol("entity:"+m.TargetID, dim))
			if err != nil {
				return nil, err
			}
			c := Candidate{
				TargetID:     m.TargetID,
				TargetName:   m.TargetName,
				EdgeID:       m.EdgeID,
				Predicate:    predicate,
				DatasetID:    datasetID,
				DomainID:     m.DomainID,
				CollectionID: m.CollectionID,
				DocumentID:   m.DocumentID,
				ShardID:      shardID,
				Similarity:   score,
				Confidence:   m.Confidence,
			}
			c.RerankScore = c.Similarity
			if opts.Rerank {
				c.RerankScore = rerankScoreCandidate(c, opts.QueryText)
			}
			if prev, ok := scoreByTarget[c.TargetID]; !ok || candidateScore(c, opts.Rerank) > candidateScore(prev, opts.Rerank) {
				scoreByTarget[c.TargetID] = c
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Candidate, 0, len(scoreByTarget))
	for _, c := range scoreByTarget {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].Similarity, out[j].Similarity
		if opts.Rerank {
			left, right = out[i].RerankScore, out[j].RerankScore
		}
		if left == right {
			if out[i].Similarity != out[j].Similarity {
				return out[i].Similarity > out[j].Similarity
			}
			return out[i].TargetID < out[j].TargetID
		}
		return left > right
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func candidateScore(c Candidate, rerank bool) float64 {
	if rerank {
		return c.RerankScore
	}
	return c.Similarity
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	if s == nil || s.db == nil {
		return out, nil
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return out, err
	}
	out.Available = true

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vsa_fact_shards`).Scan(&out.ShardCount); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vsa_fact_members`).Scan(&out.MemberCount); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(fact_count), 0) FROM vsa_fact_shards`).Scan(&out.FactCount); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(dim), 0) FROM vsa_fact_shards`).Scan(&out.MaxDim); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(updated_at), '') FROM vsa_fact_shards`).Scan(&out.LastUpdatedAt); err != nil {
		return out, err
	}

	datasets, err := s.DatasetIDs(ctx)
	if err != nil {
		return out, err
	}
	out.Datasets = datasets

	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT predicate FROM vsa_fact_shards ORDER BY predicate`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var predicate string
		if err := rows.Scan(&predicate); err != nil {
			return out, err
		}
		out.Predicates = append(out.Predicates, predicate)
	}
	return out, rows.Err()
}

func (s *Store) DatasetIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT dataset_id FROM vsa_fact_shards ORDER BY dataset_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) Predicates(ctx context.Context, datasetID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT DISTINCT predicate
		FROM vsa_fact_shards
		WHERE dataset_id = $1
		ORDER BY predicate`), datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var predicate string
		if err := rows.Scan(&predicate); err != nil {
			return nil, err
		}
		out = append(out, predicate)
	}
	return out, rows.Err()
}

type member struct {
	EdgeID       string
	TargetID     string
	TargetName   string
	Confidence   float64
	DomainID     string
	CollectionID string
	DocumentID   string
}

func (s *Store) membersForShard(ctx context.Context, shardID, subjectID string) ([]member, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT m.edge_id, m.target_id, COALESCE(n.name, ''), COALESCE(e.confidence, 1.0),
		       COALESCE(NULLIF(e.domain_id, ''), NULLIF(n.domain_id, ''), ''),
		       COALESCE(NULLIF(e.collection_id, ''), NULLIF(n.collection_id, ''), ''),
		       COALESCE(NULLIF(e.document_id, ''), NULLIF(n.document_id, ''), '')
		FROM vsa_fact_members m
		LEFT JOIN graph_nodes n ON n.id = m.target_id
		LEFT JOIN graph_edges e ON e.id = m.edge_id
		WHERE m.shard_id = $1 AND m.source_id = $2`), shardID, subjectID)
	if err != nil {
		return s.membersForShardWithoutRoute(ctx, shardID, subjectID)
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.EdgeID, &m.TargetID, &m.TargetName, &m.Confidence, &m.DomainID, &m.CollectionID, &m.DocumentID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) membersForShardWithoutRoute(ctx context.Context, shardID, subjectID string) ([]member, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT m.edge_id, m.target_id, COALESCE(n.name, ''), COALESCE(e.confidence, 1.0)
		FROM vsa_fact_members m
		LEFT JOIN graph_nodes n ON n.id = m.target_id
		LEFT JOIN graph_edges e ON e.id = m.edge_id
		WHERE m.shard_id = $1 AND m.source_id = $2`), shardID, subjectID)
	if err != nil {
		return s.membersForShardLegacy(ctx, shardID, subjectID)
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.EdgeID, &m.TargetID, &m.TargetName, &m.Confidence); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) membersForShardLegacy(ctx context.Context, shardID, subjectID string) ([]member, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT edge_id, target_id
		FROM vsa_fact_members
		WHERE shard_id = $1 AND source_id = $2`), shardID, subjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.EdgeID, &m.TargetID); err != nil {
			return nil, err
		}
		m.Confidence = 1.0
		out = append(out, m)
	}
	return out, rows.Err()
}

func rerankScoreCandidate(c Candidate, queryText string) float64 {
	score := c.Similarity
	qTokens := queryTokens(queryText)
	if len(qTokens) == 0 {
		return score + confidenceBonus(c.Confidence)
	}
	targetTokens := queryTokens(c.TargetName + " " + c.TargetID)
	var overlap int
	for token := range targetTokens {
		if _, ok := qTokens[token]; ok {
			overlap++
		}
	}
	if overlap > 0 {
		score += math.Min(0.25, 0.12*float64(overlap))
	}
	score += confidenceBonus(c.Confidence)
	return score
}

func confidenceBonus(confidence float64) float64 {
	if confidence <= 0 {
		return 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return 0.03 * confidence
}

func queryTokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		token := b.String()
		b.Reset()
		if len(token) < 2 {
			return
		}
		for _, variant := range []string{
			token,
			strings.TrimSuffix(token, "s"),
			strings.TrimSuffix(token, "ed"),
			strings.TrimSuffix(token, "ing"),
		} {
			if len(variant) >= 2 {
				out[variant] = struct{}{}
			}
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	expandQueryTokens(out)
	return out
}

func expandQueryTokens(tokens map[string]struct{}) {
	expansions := map[string][]string{
		"validate":  {"validator"},
		"validates": {"validator"},
		"verify":    {"verifier", "validator"},
		"verifies":  {"verifier", "validator"},
		"check":     {"checker", "validator"},
		"checks":    {"checker", "validator"},
		"maintain":  {"owner", "team"},
		"maintains": {"owner", "team"},
		"owner":     {"owned"},
		"require":   {"dependency"},
		"requires":  {"dependency"},
		"need":      {"dependency"},
		"needs":     {"dependency"},
		"protect":   {"guard", "security"},
		"protects":  {"guard", "security"},
		"secure":    {"guard", "security"},
		"publish":   {"topic"},
		"publishes": {"topic"},
		"emit":      {"topic"},
		"emits":     {"topic"},
	}
	for token := range tokens {
		for _, expansion := range expansions[token] {
			tokens[expansion] = struct{}{}
		}
	}
}

func (s *Store) q(query string) string {
	if s.dialect != DialectSQLite {
		return query
	}
	out := query
	for i := 16; i >= 1; i-- {
		out = strings.ReplaceAll(out, fmt.Sprintf("$%d", i), "?")
	}
	return out
}

func (s *Store) schemaStatements() []string {
	if s.dialect == DialectSQLite {
		return []string{
			`CREATE TABLE IF NOT EXISTS vsa_fact_shards (
				id TEXT PRIMARY KEY,
				dataset_id TEXT NOT NULL DEFAULT '',
				predicate TEXT NOT NULL DEFAULT '',
				shard_index INTEGER NOT NULL DEFAULT 0,
				dim INTEGER NOT NULL DEFAULT 1024,
				fact_count INTEGER NOT NULL DEFAULT 0,
				vector_json TEXT NOT NULL DEFAULT '[]',
				updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_vsa_shards_predicate ON vsa_fact_shards(dataset_id, predicate)`,
			`CREATE TABLE IF NOT EXISTS vsa_fact_members (
				shard_id TEXT NOT NULL,
				edge_id TEXT NOT NULL,
				source_id TEXT NOT NULL DEFAULT '',
				target_id TEXT NOT NULL DEFAULT '',
				predicate TEXT NOT NULL DEFAULT '',
				dataset_id TEXT NOT NULL DEFAULT '',
				PRIMARY KEY (shard_id, edge_id)
			)`,
			`CREATE INDEX IF NOT EXISTS idx_vsa_members_lookup ON vsa_fact_members(dataset_id, predicate, source_id)`,
		}
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS vsa_fact_shards (
			id TEXT PRIMARY KEY,
			dataset_id TEXT NOT NULL DEFAULT '',
			predicate TEXT NOT NULL DEFAULT '',
			shard_index INTEGER NOT NULL DEFAULT 0,
			dim INTEGER NOT NULL DEFAULT 1024,
			fact_count INTEGER NOT NULL DEFAULT 0,
			vector_json JSONB NOT NULL DEFAULT '[]',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vsa_shards_predicate ON vsa_fact_shards(dataset_id, predicate)`,
		`CREATE TABLE IF NOT EXISTS vsa_fact_members (
			shard_id TEXT NOT NULL,
			edge_id TEXT NOT NULL,
			source_id TEXT NOT NULL DEFAULT '',
			target_id TEXT NOT NULL DEFAULT '',
			predicate TEXT NOT NULL DEFAULT '',
			dataset_id TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (shard_id, edge_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vsa_members_lookup ON vsa_fact_members(dataset_id, predicate, source_id)`,
	}
}

func shardKey(datasetID, predicate string, shardIndex int) string {
	return datasetID + ":" + predicate + ":" + fmt.Sprint(shardIndex)
}
