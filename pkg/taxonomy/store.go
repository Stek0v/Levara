package taxonomy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"sort"
	"strings"
	"time"
)

type ImportRequest struct {
	Seed           string `json:"seed"`
	SourceName     string `json:"source_name,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}
type RemoveRequest struct {
	Domain     string `json:"domain"`
	Collection string `json:"collection,omitempty"`
	Document   string `json:"document,omitempty"`
	Force      bool   `json:"force,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
}
type ImportReport struct {
	RunID       string   `json:"run_id"`
	Domains     int      `json:"domains"`
	Collections int      `json:"collections"`
	Documents   int      `json:"documents"`
	Created     int      `json:"created"`
	Updated     int      `json:"updated"`
	Warnings    []string `json:"warnings"`
}
type RemoveReport struct {
	RunID   string `json:"run_id"`
	Removed int    `json:"removed"`
}

func scope(actor access.Actor, dataset string) ([]any, error) {
	if actor.UserID == "" || strings.TrimSpace(dataset) == "" {
		return nil, access.ErrDocumentForbidden
	}
	return []any{actor.UserID, actor.TenantID, dataset}, nil
}
func authorize(ctx context.Context, tx *sql.Tx, p access.SQLPolicy, actor access.Actor, dataset, action string) error {
	if tx == nil {
		return ErrInvalid
	}
	if _, err := scope(actor, dataset); err != nil {
		return err
	}
	filter, extra := access.TenantOwnerFilterSQL(actor.TenantID, 2, false)
	args := append([]any{dataset}, extra...)
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM datasets WHERE id=$1"+filter, args...).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return access.ErrDocumentForbidden
		}
		return err
	}
	d, err := p.AuthorizeDataset(ctx, actor, dataset, action)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return access.ErrDocumentForbidden
	}
	return nil
}
func aliases(raw string) ([]string, error) {
	var a []string
	err := json.Unmarshal([]byte(raw), &a)
	if err != nil {
		return nil, ErrConflict
	}
	if a == nil {
		a = []string{}
	}
	return a, nil
}

// List materializes and closes every result before any nested policy query.
func List(ctx context.Context, tx *sql.Tx, ownerID, tenantID, datasetID string) (Catalog, error) {
	out := Catalog{Domains: []Domain{}}
	if tx == nil || ownerID == "" || datasetID == "" {
		return out, ErrInvalid
	}
	args := []any{ownerID, tenantID, datasetID}
	domainIndex := map[string]int{}
	collectionIndex := map[string][2]int{}
	rows, err := tx.QueryContext(ctx, "SELECT id,name,description,CAST(aliases_json AS TEXT) FROM knowledge_domains WHERE owner_id=$1 AND team_id=$2 AND dataset_id=$3 ORDER BY name,id", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d Domain
		var raw string
		if err = rows.Scan(&d.ID, &d.Name, &d.Description, &raw); err != nil {
			break
		}
		d.Aliases, err = aliases(raw)
		if err != nil {
			break
		}
		d.Collections = []Collection{}
		domainIndex[d.ID] = len(out.Domains)
		out.Domains = append(out.Domains, d)
	}
	if rowErr := rows.Err(); err == nil {
		err = rowErr
	}
	closeErr := rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,domain_id,name,description,CAST(aliases_json AS TEXT) FROM knowledge_collections WHERE owner_id=$1 AND team_id=$2 AND dataset_id=$3 ORDER BY name,id", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var c Collection
		var parent, raw string
		if err = rows.Scan(&c.ID, &parent, &c.Name, &c.Description, &raw); err != nil {
			break
		}
		c.Aliases, err = aliases(raw)
		if err != nil {
			break
		}
		di, ok := domainIndex[parent]
		if !ok {
			err = ErrConflict
			break
		}
		c.Documents = []Document{}
		collectionIndex[c.ID] = [2]int{di, len(out.Domains[di].Collections)}
		out.Domains[di].Collections = append(out.Domains[di].Collections, c)
	}
	if rowErr := rows.Err(); err == nil {
		err = rowErr
	}
	closeErr = rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,domain_id,collection_id,title,description,CAST(aliases_json AS TEXT),source_document_id FROM knowledge_documents WHERE owner_id=$1 AND team_id=$2 AND dataset_id=$3 ORDER BY title,id", args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var d Document
		var domain, parent, raw string
		if err = rows.Scan(&d.ID, &domain, &parent, &d.Name, &d.Description, &raw, &d.SourceDocumentID); err != nil {
			break
		}
		d.Aliases, err = aliases(raw)
		if err != nil {
			break
		}
		idx, ok := collectionIndex[parent]
		if !ok || out.Domains[idx[0]].ID != domain {
			err = ErrConflict
			break
		}
		c := &out.Domains[idx[0]].Collections[idx[1]]
		c.Documents = append(c.Documents, d)
	}
	if rowErr := rows.Err(); err == nil {
		err = rowErr
	}
	closeErr = rows.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	return out, nil
}
func validateCatalog(c Catalog) error {
	unique := func(nodes []Node) error {
		seen := map[string]bool{}
		for _, n := range nodes {
			key := Key(n.Name)
			if key == "" || seen[key] {
				return ErrConflict
			}
			seen[key] = true
		}
		return nil
	}
	domains := []Node{}
	for _, d := range c.Domains {
		domains = append(domains, d.Node)
		collections := []Node{}
		for _, col := range d.Collections {
			collections = append(collections, col.Node)
			docs := []Node{}
			for _, doc := range col.Documents {
				docs = append(docs, doc.Node)
			}
			if err := unique(docs); err != nil {
				return err
			}
		}
		if err := unique(collections); err != nil {
			return err
		}
	}
	return unique(domains)
}
func merge(in, old Node) Node {
	if old.ID == "" {
		return in
	}
	in.ID = old.ID
	if !in.descriptionSet {
		in.Description = old.Description
	}
	if !in.aliasesSet {
		in.Aliases = old.Aliases
	}
	return in
}
func replay(ctx context.Context, tx *sql.Tx, actor access.Actor, dataset, requestID, action, hash string, target any) (bool, error) {
	var oldAction, oldHash, report string
	err := tx.QueryRowContext(ctx, "SELECT action,request_sha256,report_json FROM knowledge_taxonomy_runs WHERE owner_id=$1 AND team_id=$2 AND dataset_id=$3 AND request_id=$4", actor.UserID, actor.TenantID, dataset, requestID).Scan(&oldAction, &oldHash, &report)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if action != oldAction || hash != oldHash {
		return false, ErrConflict
	}
	if err := json.Unmarshal([]byte(report), target); err != nil {
		return false, err
	}
	return true, nil
}
func requestHash(value any) string {
	b, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func journal(ctx context.Context, tx *sql.Tx, actor access.Actor, dataset, requestID, action, hash, runID, source, revision string, report any) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO knowledge_taxonomy_runs(id,owner_id,team_id,dataset_id,request_id,action,request_sha256,source_name,source_revision,report_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)", runID, actor.UserID, actor.TenantID, dataset, requestID, action, hash, source, revision, string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}
func saveNode(ctx context.Context, tx *sql.Tx, actor access.Actor, dataset, parent, domain, source string, node Node, kind string) (bool, error) {
	created := node.ID == ""
	if created {
		node.ID = uuid.NewString()
	}
	raw, err := json.Marshal(node.Aliases)
	if err != nil {
		return false, err
	}
	var query string
	var args []any
	switch kind {
	case "domain":
		query = "INSERT INTO knowledge_domains(id,owner_id,team_id,dataset_id,name,description,aliases_json) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,aliases_json=EXCLUDED.aliases_json,updated_at=CURRENT_TIMESTAMP WHERE knowledge_domains.owner_id=EXCLUDED.owner_id AND knowledge_domains.team_id=EXCLUDED.team_id AND knowledge_domains.dataset_id=EXCLUDED.dataset_id"
		args = []any{node.ID, actor.UserID, actor.TenantID, dataset, node.Name, node.Description, string(raw)}
	case "collection":
		query = "INSERT INTO knowledge_collections(id,owner_id,team_id,dataset_id,domain_id,name,description,aliases_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,aliases_json=EXCLUDED.aliases_json,updated_at=CURRENT_TIMESTAMP WHERE knowledge_collections.owner_id=EXCLUDED.owner_id AND knowledge_collections.team_id=EXCLUDED.team_id AND knowledge_collections.dataset_id=EXCLUDED.dataset_id"
		args = []any{node.ID, actor.UserID, actor.TenantID, dataset, parent, node.Name, node.Description, string(raw)}
	case "document":
		query = "INSERT INTO knowledge_documents(id,owner_id,team_id,dataset_id,domain_id,collection_id,title,description,aliases_json,source_document_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET title=EXCLUDED.title,description=EXCLUDED.description,aliases_json=EXCLUDED.aliases_json,source_document_id=EXCLUDED.source_document_id,updated_at=CURRENT_TIMESTAMP WHERE knowledge_documents.owner_id=EXCLUDED.owner_id AND knowledge_documents.team_id=EXCLUDED.team_id AND knowledge_documents.dataset_id=EXCLUDED.dataset_id"
		args = []any{node.ID, actor.UserID, actor.TenantID, dataset, domain, parent, node.Name, node.Description, string(raw), source}
	default:
		return false, ErrInvalid
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return created, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return created, err
	}
	if n != 1 {
		return created, ErrConflict
	}
	return created, nil
}

// ponytail: the existing global metadata fence serializes bounded namespace scans.
// Add canonical-key indexes or narrower catalog locks only after measured contention.
// Import requires the caller-owned metadata write transaction and matching policy.
func Import(ctx context.Context, tx *sql.Tx, p access.SQLPolicy, actor access.Actor, dataset string, req ImportRequest) (ImportReport, error) {
	report := ImportReport{Warnings: []string{}}
	parsed, err := Parse(req.Seed)
	if err != nil {
		return report, err
	}
	if len(req.RequestID) > 256 || len(req.SourceName) > 1024 || len(req.SourceRevision) > 1024 || !validText(req.RequestID) || !validText(req.SourceName) || !validText(req.SourceRevision) {
		return report, ErrInvalid
	}
	if err := authorize(ctx, tx, p, actor, dataset, access.ActionWrite); err != nil {
		return report, err
	}
	for _, d := range parsed.Domains {
		for _, c := range d.Collections {
			for _, doc := range c.Documents {
				var count int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM dataset_data dd JOIN data d ON d.id=dd.data_id WHERE dd.dataset_id=$1 AND dd.data_id=$2", dataset, doc.SourceDocumentID).Scan(&count); err != nil {
					return report, err
				}
				if count == 0 {
					return report, access.ErrDocumentForbidden
				}
				decision, err := p.AuthorizeDocument(ctx, actor, access.DocumentRef{DatasetID: dataset, DataID: doc.SourceDocumentID}, access.ActionRead)
				if err != nil {
					return report, err
				}
				if !decision.Allowed {
					return report, access.ErrDocumentForbidden
				}
			}
		}
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}
	hash := requestHash(req)
	if found, err := replay(ctx, tx, actor, dataset, requestID, "import", hash, &report); err != nil || found {
		return report, err
	}
	old, err := List(ctx, tx, actor.UserID, actor.TenantID, dataset)
	if err != nil {
		return report, err
	}
	if err := validateCatalog(old); err != nil {
		return report, err
	}
	record := func(created bool) {
		if created {
			report.Created++
		} else {
			report.Updated++
		}
	}
	for _, d := range parsed.Domains {
		var prior Domain
		for _, candidate := range old.Domains {
			if Key(candidate.Name) == Key(d.Name) {
				prior = candidate
				break
			}
		}
		d.Node = merge(d.Node, prior.Node)
		if d.ID == "" {
			d.ID = uuid.NewString()
			report.Created++
		} else {
			report.Updated++
		}
		if _, err := saveNode(ctx, tx, actor, dataset, "", "", "", d.Node, "domain"); err != nil {
			return report, err
		}
		report.Domains++
		for _, c := range d.Collections {
			var pc Collection
			for _, candidate := range prior.Collections {
				if Key(candidate.Name) == Key(c.Name) {
					pc = candidate
					break
				}
			}
			c.Node = merge(c.Node, pc.Node)
			created := c.ID == ""
			if created {
				c.ID = uuid.NewString()
			}
			record(created)
			if _, err := saveNode(ctx, tx, actor, dataset, d.ID, "", "", c.Node, "collection"); err != nil {
				return report, err
			}
			report.Collections++
			for _, doc := range c.Documents {
				var pd Document
				for _, candidate := range pc.Documents {
					if Key(candidate.Name) == Key(doc.Name) {
						pd = candidate
						break
					}
				}
				doc.Node = merge(doc.Node, pd.Node)
				created := doc.ID == ""
				if created {
					doc.ID = uuid.NewString()
				}
				record(created)
				if _, err := saveNode(ctx, tx, actor, dataset, c.ID, d.ID, doc.SourceDocumentID, doc.Node, "document"); err != nil {
					return report, err
				}
				report.Documents++
			}
		}
	}
	catalog, err := List(ctx, tx, actor.UserID, actor.TenantID, dataset)
	if err != nil {
		return report, err
	}
	owners := map[string]string{}
	warnings := map[string]bool{}
	for _, d := range catalog.Domains {
		for _, alias := range d.Aliases {
			key := Key(alias)
			if other, ok := owners[key]; ok && other != d.ID {
				warnings["domain alias collision: "+key] = true
			}
			owners[key] = d.ID
		}
	}
	for warning := range warnings {
		report.Warnings = append(report.Warnings, warning)
	}
	sort.Strings(report.Warnings)
	report.RunID = uuid.NewString()
	if err := journal(ctx, tx, actor, dataset, requestID, "import", hash, report.RunID, req.SourceName, req.SourceRevision, report); err != nil {
		return report, err
	}
	return report, nil
}
func Remove(ctx context.Context, tx *sql.Tx, p access.SQLPolicy, actor access.Actor, dataset string, req RemoveRequest) (RemoveReport, error) {
	report := RemoveReport{}
	if Key(req.Domain) == "" || req.Document != "" && Key(req.Collection) == "" || len(req.RequestID) > 256 || !validText(req.Domain) || !validText(req.Collection) || !validText(req.Document) || !validText(req.RequestID) || req.Collection != "" && Key(req.Collection) == "" || req.Document != "" && Key(req.Document) == "" {
		return report, ErrInvalid
	}
	if err := authorize(ctx, tx, p, actor, dataset, access.ActionWrite); err != nil {
		return report, err
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}
	hash := requestHash(req)
	if found, err := replay(ctx, tx, actor, dataset, requestID, "remove", hash, &report); err != nil || found {
		return report, err
	}
	catalog, err := List(ctx, tx, actor.UserID, actor.TenantID, dataset)
	if err != nil {
		return report, err
	}
	if err := validateCatalog(catalog); err != nil {
		return report, err
	}
	type removal struct{ table, id string }
	var remove []removal
	for _, d := range catalog.Domains {
		if Key(d.Name) != Key(req.Domain) {
			continue
		}
		if req.Collection == "" && len(d.Collections) > 0 && !req.Force {
			return report, ErrConflict
		}
		for _, c := range d.Collections {
			if req.Collection != "" && Key(c.Name) != Key(req.Collection) {
				continue
			}
			if req.Document == "" && req.Collection != "" && len(c.Documents) > 0 && !req.Force {
				return report, ErrConflict
			}
			for _, doc := range c.Documents {
				if req.Document == "" || Key(doc.Name) == Key(req.Document) {
					remove = append(remove, removal{"knowledge_documents", doc.ID})
				}
			}
			if req.Document == "" {
				remove = append(remove, removal{"knowledge_collections", c.ID})
			}
		}
		if req.Collection == "" {
			remove = append(remove, removal{"knowledge_domains", d.ID})
		}
	}
	for _, r := range remove {
		result, err := tx.ExecContext(ctx, "DELETE FROM "+r.table+" WHERE id=$1 AND owner_id=$2 AND team_id=$3 AND dataset_id=$4", r.id, actor.UserID, actor.TenantID, dataset)
		if err != nil {
			return report, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return report, err
		}
		report.Removed += int(n)
	}
	report.RunID = uuid.NewString()
	if err := journal(ctx, tx, actor, dataset, requestID, "remove", hash, report.RunID, "", "", report); err != nil {
		return report, err
	}
	return report, nil
}
