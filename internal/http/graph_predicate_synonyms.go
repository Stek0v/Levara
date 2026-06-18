package http

import (
	"context"
	"database/sql"
	"log"
	"sort"
	"strings"
)

const (
	predicateSynonymSourceGenerated = "generated"
	predicateSynonymSourceManual    = "manual"
	predicateSynonymSourceFeedback  = "feedback"

	predicateSynonymWeightExact     = 100
	predicateSynonymWeightManual    = 80
	predicateSynonymWeightGenerated = 50
	predicateSynonymWeightFeedback  = 30
	predicateSynonymWeightSubstring = 20
)

var manualGraphPredicateSynonyms = map[string][]string{
	"OWNED_BY":   {"owner", "owned", "owns", "maintainer", "maintains", "team"},
	"DEPENDS_ON": {"depends", "dependency", "requires", "require", "needs"},
	"CALLS":      {"calls", "call", "invokes", "invoke", "requests", "request"},
	"VALIDATES":  {"validates", "validate", "checks", "check", "verifies", "verify", "ensures", "ensure"},
	"SECURED_BY": {"protects", "protect", "guards", "guard", "secures", "secure", "security"},
	"EMITS":      {"emits", "emit", "publishes", "publish", "sends", "send", "produces", "produce", "topic"},
}

type graphPredicateSynonym struct {
	Predicate string
	Synonym   string
	Source    string
	Weight    int
}

type graphPredicateSynonymExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ensureGraphPredicateSynonymSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, Q(`CREATE TABLE IF NOT EXISTS graph_predicate_synonyms (
		dataset_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		synonym TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'generated',
		weight REAL NOT NULL DEFAULT 50,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (dataset_id, predicate, synonym, source)
	)`))
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, Q(`CREATE INDEX IF NOT EXISTS idx_graph_predicate_synonyms_lookup ON graph_predicate_synonyms(dataset_id, synonym)`))
	return err
}

func refreshPredicateSynonyms(ctx context.Context, db *sql.DB, datasetID string) error {
	if db == nil {
		return nil
	}
	if err := vsaStoreForDB(db, 0, 0).EnsureSchema(ctx); err != nil {
		return err
	}
	if err := ensureGraphPredicateSynonymSchema(ctx, db); err != nil {
		return err
	}
	predicates, err := graphPredicatesForDataset(ctx, db, datasetID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, Q(`DELETE FROM graph_predicate_synonyms WHERE dataset_id = $1 AND source = $2`), datasetID, predicateSynonymSourceGenerated); err != nil {
		return err
	}
	sqlite := looksLikeSQLite(db)
	for _, predicate := range predicates {
		for _, synonym := range generatedPredicateSynonyms(predicate) {
			if err := upsertGraphPredicateSynonymWithDialect(ctx, tx, sqlite, datasetID, predicate, synonym, predicateSynonymSourceGenerated, predicateSynonymWeightGenerated); err != nil {
				return err
			}
		}
		for _, synonym := range manualSynonymsForPredicate(predicate) {
			if err := upsertGraphPredicateSynonymWithDialect(ctx, tx, sqlite, datasetID, predicate, synonym, predicateSynonymSourceManual, predicateSynonymWeightManual); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func graphPredicatesForDataset(ctx context.Context, db *sql.DB, datasetID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, Q(`
		SELECT DISTINCT relationship_name
		FROM graph_edges
		WHERE ($1 = '' OR dataset_id = $2)
		  AND relationship_name <> ''
		  AND (valid_until IS NULL OR valid_until = '')
		UNION
		SELECT DISTINCT predicate
		FROM vsa_fact_shards
		WHERE ($3 = '' OR dataset_id = $4)
		  AND predicate <> ''
		ORDER BY 1`), datasetID, datasetID, datasetID, datasetID)
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

func generatedPredicateSynonyms(predicate string) []string {
	tokens := graphContextTokens(predicate)
	out := make([]string, 0, len(tokens))
	for token := range tokens {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

func upsertGraphPredicateSynonym(ctx context.Context, db *sql.DB, datasetID, predicate, synonym, source string, weight int) error {
	return upsertGraphPredicateSynonymWithDialect(ctx, db, looksLikeSQLite(db), datasetID, predicate, synonym, source, weight)
}

func upsertGraphPredicateSynonymWithDialect(ctx context.Context, execer graphPredicateSynonymExecer, sqlite bool, datasetID, predicate, synonym, source string, weight int) error {
	synonym = strings.TrimSpace(strings.ToLower(synonym))
	if predicate == "" || synonym == "" || source == "" {
		return nil
	}
	if sqlite {
		_, err := execer.ExecContext(ctx, Q(`
			INSERT INTO graph_predicate_synonyms (dataset_id, predicate, synonym, source, weight, updated_at)
			VALUES ($1, $2, $3, $4, $5, CURRENT_TIMESTAMP)
			ON CONFLICT(dataset_id, predicate, synonym, source)
			DO UPDATE SET weight = excluded.weight, updated_at = CURRENT_TIMESTAMP`), datasetID, predicate, synonym, source, weight)
		return err
	}
	_, err := execer.ExecContext(ctx, Q(`
		INSERT INTO graph_predicate_synonyms (dataset_id, predicate, synonym, source, weight, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT(dataset_id, predicate, synonym, source)
		DO UPDATE SET weight = EXCLUDED.weight, updated_at = NOW()`), datasetID, predicate, synonym, source, weight)
	return err
}

func loadPredicateSynonyms(ctx context.Context, db *sql.DB, datasetIDs []string, predicates []string) map[string][]graphPredicateSynonym {
	out := map[string][]graphPredicateSynonym{}
	for _, predicate := range predicates {
		for _, synonym := range generatedPredicateSynonyms(predicate) {
			out[predicate] = append(out[predicate], graphPredicateSynonym{
				Predicate: predicate,
				Synonym:   synonym,
				Source:    predicateSynonymSourceGenerated,
				Weight:    predicateSynonymWeightGenerated,
			})
		}
		for _, synonym := range manualSynonymsForPredicate(predicate) {
			out[predicate] = append(out[predicate], graphPredicateSynonym{
				Predicate: predicate,
				Synonym:   strings.ToLower(synonym),
				Source:    predicateSynonymSourceManual,
				Weight:    predicateSynonymWeightManual,
			})
		}
	}
	if db == nil || len(predicates) == 0 {
		return out
	}
	if err := vsaStoreForDB(db, 0, 0).EnsureSchema(ctx); err != nil {
		log.Printf("[vsa] ensure VSA schema before predicate synonyms: %v", err)
		return out
	}
	if err := ensureGraphPredicateSynonymSchema(ctx, db); err != nil {
		log.Printf("[vsa] ensure predicate synonym schema: %v", err)
		return out
	}
	for _, datasetID := range datasetIDs {
		rows, err := db.QueryContext(ctx, Q(`
			SELECT predicate, synonym, source, weight
			FROM graph_predicate_synonyms
			WHERE dataset_id = $1`), datasetID)
		if err != nil {
			log.Printf("[vsa] load predicate synonyms dataset=%q: %v", datasetID, err)
			continue
		}
		for rows.Next() {
			var s graphPredicateSynonym
			var weight float64
			if err := rows.Scan(&s.Predicate, &s.Synonym, &s.Source, &weight); err != nil {
				log.Printf("[vsa] scan predicate synonym dataset=%q: %v", datasetID, err)
				continue
			}
			s.Synonym = strings.ToLower(strings.TrimSpace(s.Synonym))
			s.Weight = int(weight)
			if s.Predicate != "" && s.Synonym != "" {
				out[s.Predicate] = append(out[s.Predicate], s)
			}
		}
		if err := rows.Err(); err != nil {
			log.Printf("[vsa] read predicate synonyms dataset=%q: %v", datasetID, err)
		}
		rows.Close()
	}
	return out
}

func rankVSAPredicatesForQuery(predicates []string, queryText string, synonyms map[string][]graphPredicateSynonym) []string {
	if len(predicates) < 2 || strings.TrimSpace(queryText) == "" {
		return predicates
	}
	queryTokens := graphContextTokens(queryText)
	if len(queryTokens) == 0 {
		return predicates
	}
	type rankedPredicate struct {
		value string
		score int
		index int
	}
	ranked := make([]rankedPredicate, 0, len(predicates))
	for i, predicate := range predicates {
		predicateSynonyms := append([]graphPredicateSynonym{}, synonyms[predicate]...)
		predicateSynonyms = append(predicateSynonyms, fallbackPredicateSynonyms(predicate)...)
		ranked = append(ranked, rankedPredicate{
			value: predicate,
			score: predicateQueryScore(predicate, queryTokens, predicateSynonyms),
			index: i,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].index < ranked[j].index
	})
	out := make([]string, 0, len(predicates))
	for _, p := range ranked {
		out = append(out, p.value)
	}
	return out
}

func fallbackPredicateSynonyms(predicate string) []graphPredicateSynonym {
	out := make([]graphPredicateSynonym, 0)
	for _, synonym := range generatedPredicateSynonyms(predicate) {
		out = append(out, graphPredicateSynonym{
			Predicate: predicate,
			Synonym:   synonym,
			Source:    predicateSynonymSourceGenerated,
			Weight:    predicateSynonymWeightGenerated,
		})
	}
	for _, synonym := range manualSynonymsForPredicate(predicate) {
		out = append(out, graphPredicateSynonym{
			Predicate: predicate,
			Synonym:   strings.ToLower(synonym),
			Source:    predicateSynonymSourceManual,
			Weight:    predicateSynonymWeightManual,
		})
	}
	return out
}

func manualSynonymsForPredicate(predicate string) []string {
	if synonyms := manualGraphPredicateSynonyms[predicate]; len(synonyms) > 0 {
		return synonyms
	}
	best := ""
	for base := range manualGraphPredicateSynonyms {
		if strings.HasPrefix(predicate, base+"_") && len(base) > len(best) {
			best = base
		}
	}
	return manualGraphPredicateSynonyms[best]
}

func predicateQueryScore(predicate string, queryTokens map[string]struct{}, synonyms []graphPredicateSynonym) int {
	score := 0
	for token := range graphContextTokens(predicate) {
		score += predicateTokenScore(token, queryTokens, predicateSynonymWeightExact)
	}
	for _, synonym := range synonyms {
		weight := synonym.Weight
		if weight <= 0 {
			weight = predicateSynonymWeightGenerated
		}
		score += predicateTokenScore(synonym.Synonym, queryTokens, weight)
	}
	return score
}

func predicateTokenScore(token string, queryTokens map[string]struct{}, exactWeight int) int {
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return 0
	}
	score := 0
	if _, ok := queryTokens[token]; ok {
		score += exactWeight
	}
	for queryToken := range queryTokens {
		if len(token) >= 4 && len(queryToken) >= 4 && (strings.Contains(token, queryToken) || strings.Contains(queryToken, token)) {
			score += predicateSynonymWeightSubstring
		}
	}
	return score
}
