package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/stek0v/levara/pkg/bm25"
)

type dcdRouteScope struct {
	OwnerID           string
	TeamID            string
	AllowedDatasetIDs []string
}

type dcdRoutePolicy struct {
	MaxCandidates       int
	MinConfidence       float64
	AllowGlobalFallback bool
}

type dcdRouteCandidate struct {
	DomainID     string  `json:"domain_id"`
	CollectionID string  `json:"collection_id"`
	DocumentID   string  `json:"document_id"`
	DatasetID    string  `json:"dataset_id"`
	Confidence   float64 `json:"confidence"`
	Source       string  `json:"source"`
	Reason       string  `json:"reason"`
}

type dcdRouteRow struct {
	DomainID              string
	DomainName            string
	DomainDescription     string
	DomainAliasesJSON     string
	CollectionID          string
	CollectionName        string
	CollectionDescription string
	CollectionAliasesJSON string
	DocumentID            string
	DocumentTitle         string
	DocumentDescription   string
	DocumentAliasesJSON   string
	DatasetID             string
}

func resolveDCDRouteCandidates(ctx context.Context, db *sql.DB, query string, scope dcdRouteScope, policy dcdRoutePolicy) ([]dcdRouteCandidate, error) {
	if db == nil {
		return nil, nil
	}
	if policy.MaxCandidates <= 0 {
		policy.MaxCandidates = 3
	}
	rows, err := loadDCDRouteRows(ctx, db, scope)
	if err != nil {
		return nil, err
	}
	candidates := scoreDCDRouteRows(query, rows)
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Confidence >= policy.MinConfidence {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) > policy.MaxCandidates {
		filtered = filtered[:policy.MaxCandidates]
	}
	return append([]dcdRouteCandidate(nil), filtered...), nil
}

func loadDCDRouteRows(ctx context.Context, db *sql.DB, scope dcdRouteScope) ([]dcdRouteRow, error) {
	query := `SELECT
		d.id, d.name, COALESCE(d.description, ''), COALESCE(d.aliases_json, '[]'),
		COALESCE(c.id, ''), COALESCE(c.name, ''), COALESCE(c.description, ''), COALESCE(c.aliases_json, '[]'),
		COALESCE(doc.id, ''), COALESCE(doc.title, ''), COALESCE(doc.description, ''), COALESCE(doc.aliases_json, '[]'),
		COALESCE(d.dataset_id, '')
	FROM knowledge_domains d
	LEFT JOIN knowledge_collections c
		ON c.domain_id = d.id
		AND c.owner_id = d.owner_id
		AND c.team_id = d.team_id
		AND c.dataset_id = d.dataset_id
	LEFT JOIN knowledge_documents doc
		ON doc.collection_id = c.id
		AND doc.domain_id = d.id
		AND doc.owner_id = d.owner_id
		AND doc.team_id = d.team_id
		AND doc.dataset_id = d.dataset_id`
	var where []string
	var args []any
	if scope.OwnerID != "" {
		args = append(args, scope.OwnerID)
		where = append(where, "d.owner_id = $"+itoaDCDRoute(len(args)))
	}
	if scope.TeamID != "" {
		args = append(args, scope.TeamID)
		where = append(where, "d.team_id = $"+itoaDCDRoute(len(args)))
	}
	if len(scope.AllowedDatasetIDs) > 0 {
		start := len(args) + 1
		where = append(where, "d.dataset_id "+InPlaceholders(len(scope.AllowedDatasetIDs), start))
		for _, id := range scope.AllowedDatasetIDs {
			args = append(args, id)
		}
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY d.name, c.name, doc.title"

	rows, err := db.QueryContext(ctx, Q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dcdRouteRow
	for rows.Next() {
		var row dcdRouteRow
		if err := rows.Scan(
			&row.DomainID, &row.DomainName, &row.DomainDescription, &row.DomainAliasesJSON,
			&row.CollectionID, &row.CollectionName, &row.CollectionDescription, &row.CollectionAliasesJSON,
			&row.DocumentID, &row.DocumentTitle, &row.DocumentDescription, &row.DocumentAliasesJSON,
			&row.DatasetID,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func scoreDCDRouteRows(query string, rows []dcdRouteRow) []dcdRouteCandidate {
	queryNorm := normalizeDCDRouteText(query)
	queryTokens := tokenSetDCDRoute(queryNorm)
	bm25Scores := scoreDCDRouteRowsBM25(query, rows)
	type scored struct {
		candidate dcdRouteCandidate
		score     float64
	}
	var scoredRows []scored
	for _, row := range rows {
		score, reasons := scoreDCDRouteRow(queryNorm, queryTokens, row)
		if bm25Score := bm25Scores[keyDCDRouteRow(row)]; bm25Score > 0 {
			score += bm25Score
			reasons = append(reasons, "bm25")
		}
		if score <= 0 {
			continue
		}
		confidence := score / 10
		if confidence > 1 {
			confidence = 1
		}
		scoredRows = append(scoredRows, scored{
			candidate: dcdRouteCandidate{
				DomainID:     row.DomainID,
				CollectionID: row.CollectionID,
				DocumentID:   row.DocumentID,
				DatasetID:    row.DatasetID,
				Confidence:   confidence,
				Source:       "deterministic",
				Reason:       strings.Join(reasons, ","),
			},
			score: score,
		})
	}
	sort.SliceStable(scoredRows, func(i, j int) bool {
		if scoredRows[i].score == scoredRows[j].score {
			return scoredRows[i].candidate.DomainID < scoredRows[j].candidate.DomainID
		}
		return scoredRows[i].score > scoredRows[j].score
	})
	out := make([]dcdRouteCandidate, 0, len(scoredRows))
	for _, row := range scoredRows {
		out = append(out, row.candidate)
	}
	return out
}

func scoreDCDRouteRowsBM25(query string, rows []dcdRouteRow) map[string]float64 {
	idx := bm25.NewIndex()
	for _, row := range rows {
		idx.Add(keyDCDRouteRow(row), textDCDRouteRow(row), "")
	}
	results := idx.Search(query, len(rows))
	out := make(map[string]float64, len(results))
	for _, result := range results {
		out[result.ID] = result.Score
	}
	return out
}

func scoreDCDRouteRow(queryNorm string, queryTokens map[string]struct{}, row dcdRouteRow) (float64, []string) {
	var score float64
	var reasons []string
	addText := func(label, value string, exactBoost, tokenBoost float64) {
		valueNorm := normalizeDCDRouteText(value)
		if valueNorm == "" {
			return
		}
		if strings.Contains(queryNorm, valueNorm) {
			score += exactBoost
			reasons = append(reasons, label+"_exact")
		}
		for token := range tokenSetDCDRoute(valueNorm) {
			if _, ok := queryTokens[token]; ok {
				score += tokenBoost
			}
		}
	}
	addAliases := func(label, raw string, exactBoost, tokenBoost float64) {
		for _, alias := range parseDCDRouteAliases(raw) {
			addText(label+"_alias", alias, exactBoost, tokenBoost)
		}
	}
	addText("domain", row.DomainName, 4, 0.5)
	addText("domain_description", row.DomainDescription, 1, 0.2)
	addAliases("domain", row.DomainAliasesJSON, 4, 0.5)
	addText("collection", row.CollectionName, 3, 0.4)
	addText("collection_description", row.CollectionDescription, 1, 0.2)
	addAliases("collection", row.CollectionAliasesJSON, 3, 0.4)
	addText("document", row.DocumentTitle, 5, 0.6)
	addText("document_description", row.DocumentDescription, 1, 0.2)
	addAliases("document", row.DocumentAliasesJSON, 5, 0.6)
	return score, reasons
}

func parseDCDRouteAliases(raw string) []string {
	var aliases []string
	if err := json.Unmarshal([]byte(raw), &aliases); err == nil {
		return aliases
	}
	return nil
}

func textDCDRouteRow(row dcdRouteRow) string {
	parts := []string{
		row.DomainName,
		row.DomainDescription,
		strings.Join(parseDCDRouteAliases(row.DomainAliasesJSON), " "),
		row.CollectionName,
		row.CollectionDescription,
		strings.Join(parseDCDRouteAliases(row.CollectionAliasesJSON), " "),
		row.DocumentTitle,
		row.DocumentDescription,
		strings.Join(parseDCDRouteAliases(row.DocumentAliasesJSON), " "),
	}
	return strings.Join(parts, " ")
}

func keyDCDRouteRow(row dcdRouteRow) string {
	return row.DomainID + "\x00" + row.CollectionID + "\x00" + row.DocumentID
}

func normalizeDCDRouteText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer("_", " ", "-", " ", ".", " ", ",", " ", ":", " ", ";", " ", "/", " ")
	return strings.Join(strings.Fields(replacer.Replace(value)), " ")
}

func tokenSetDCDRoute(value string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range strings.Fields(value) {
		if len(token) < 2 {
			continue
		}
		out[token] = struct{}{}
	}
	return out
}

func itoaDCDRoute(value int) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}
