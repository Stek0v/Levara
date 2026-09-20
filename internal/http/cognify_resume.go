// cognify_resume.go — P1: durable cognify.
//
// Pipeline runs live in an in-memory registry: a server restart (or the
// 30-minute run budget) leaves document_pipeline_statuses rows stuck in
// RUNNING/FAILED with nobody processing them. The resume loop re-triggers
// cognify for every dataset that still has unfinished documents — through
// the server's own HTTP endpoint, so the exact production path (auth,
// claims, publication) runs instead of a parallel copy. Resume runs in
// rag mode (skip_graph): graph extraction is an explicit choice, never a
// resume storm.
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	cognifyResumeMaxAttempts = 25
	cognifyResumeCooldown    = 5 * time.Second
	cognifyResumeHTTPTimeout = 35 * time.Minute // covers the run budget
)

type cognifyResumeGroup struct {
	DatasetID  string
	Collection string
	Pending    int
}

// UnfinishedCognifyGroups returns (dataset, collection) groups that still
// have documents in a non-terminal pipeline state. At boot every RUNNING
// row is orphaned (the registry is in-memory), and FAILED rows are
// retryable by design — a claim moves them back to RUNNING.
func UnfinishedCognifyGroups(ctx context.Context, db *sql.DB) ([]cognifyResumeGroup, error) {
	rows, err := db.QueryContext(ctx, Q(`
		SELECT s.dataset_id, s.collection_name, COUNT(*)
		FROM document_pipeline_statuses s
		WHERE s.pipeline_state IN ('RUNNING','FAILED')
			AND EXISTS (SELECT 1 FROM datasets ds WHERE ds.id = s.dataset_id)
			AND EXISTS (SELECT 1 FROM dataset_data dd WHERE dd.dataset_id = s.dataset_id AND dd.data_id = s.data_id)
		GROUP BY s.dataset_id, s.collection_name
		ORDER BY COUNT(*) DESC
	`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []cognifyResumeGroup
	for rows.Next() {
		var g cognifyResumeGroup
		if err := rows.Scan(&g.DatasetID, &g.Collection, &g.Pending); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// cognifyResumeEnabled: on by default; LEVARA_COGNIFY_RESUME=0 opts out.
func cognifyResumeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_COGNIFY_RESUME"))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// CognifyRunSummary reports what one cognify pass accomplished.
type CognifyRunSummary struct {
	Chunks  int           // chunks the run wrote before reaching a terminal state
	Elapsed time.Duration // wall time the run consumed
}

// CognifyStarter abstracts "run one cognify pass over the dataset and wait
// for the run to reach a terminal state" so the resume loop is testable.
type CognifyStarter interface {
	Start(ctx context.Context, datasetID, collection string) (CognifyRunSummary, error)
}

// ResumeUnfinishedCognify drains the unfinished-pipeline backlog. Never
// blocks the caller: the drain runs in its own goroutine.
func ResumeUnfinishedCognify(ctx context.Context, db *sql.DB, starter CognifyStarter) {
	if !cognifyResumeEnabled() || db == nil || starter == nil {
		return
	}
	go func() {
		for round := 1; ; round++ {
			groups, err := UnfinishedCognifyGroups(ctx, db)
			if err != nil {
				log.Printf("[cognify-resume] backlog query failed: %v", err)
				return
			}
			if len(groups) == 0 {
				log.Printf("[cognify-resume] backlog drained")
				return
			}
			progressed := false
			for _, g := range groups {
				for attempt := 1; attempt <= cognifyResumeMaxAttempts; attempt++ {
					log.Printf("[cognify-resume] round %d: dataset %s collection %s, %d pending (attempt %d)",
						round, g.DatasetID, g.Collection, g.Pending, attempt)
					summary, err := starter.Start(ctx, g.DatasetID, g.Collection)
					if err != nil {
						log.Printf("[cognify-resume] run failed: %v", err)
						time.Sleep(cognifyResumeCooldown)
						break
					}
					fresh, err := PendingCognifyCount(ctx, db, g.DatasetID, g.Collection)
					if err != nil {
						log.Printf("[cognify-resume] progress query failed: %v", err)
						break
					}
					if fresh == 0 {
						progressed = true
						break
					}
					if fresh < g.Pending {
						progressed = true
						g.Pending = fresh
						continue
					}
					// Pending unchanged, but the run may still have been
					// working: oversized documents consume the whole run
					// budget mid-document without completing it. Chunks
					// written are real progress — keep draining; only a
					// full-budget attempt with zero chunks is a stuck cause.
					if summary.Chunks > 0 {
						progressed = true
						continue
					}
					break
				}
			}
			if !progressed {
				log.Printf("[cognify-resume] backlog made no progress this round; stopping (re-trigger manually once the cause is fixed)")
				return
			}
		}
	}()
}

// PendingCognifyCount counts non-terminal pipeline rows for one dataset
// and collection.
func PendingCognifyCount(ctx context.Context, db *sql.DB, datasetID, collection string) (int, error) {
	rows, err := db.QueryContext(ctx, Q(`
		SELECT COUNT(*) FROM document_pipeline_statuses
		WHERE dataset_id = $1 AND collection_name = $2 AND pipeline_state IN ('RUNNING','FAILED')
	`), datasetID, collection)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}

// LoopbackCognifyStarter runs cognify through the server's own HTTP API.
// It is the production path verbatim; on require-auth deployments the
// self-call needs a token (LEVARA_COGNIFY_RESUME_TOKEN) or the resume
// logs in and stops safely.
type LoopbackCognifyStarter struct {
	BaseURL string // e.g. http://127.0.0.1:8081
	Token   string // optional bearer for auth-enabled deployments
	Client  *http.Client
}

func (s LoopbackCognifyStarter) Start(ctx context.Context, datasetID, collection string) (CognifyRunSummary, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: cognifyResumeHTTPTimeout}
	}
	payload, err := json.Marshal(map[string]any{
		"datasets":   []string{datasetID},
		"collection": collection,
		"skip_graph": true,
	})
	if err != nil {
		return CognifyRunSummary{}, err
	}
	var summary CognifyRunSummary
	runCtx, cancel := context.WithTimeout(ctx, cognifyResumeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(runCtx, http.MethodPost, s.BaseURL+"/api/v1/cognify", bytes.NewReader(payload))
	if err != nil {
		return summary, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return summary, err
	}
	var started struct {
		RunID string `json:"pipeline_run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&started)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || started.RunID == "" {
		return summary, fmt.Errorf("cognify start: HTTP %d", resp.StatusCode)
	}

	// Poll until terminal; the POST returns before the run finishes.
	for {
		select {
		case <-runCtx.Done():
			return summary, runCtx.Err()
		case <-time.After(5 * time.Second):
		}
		stReq, _ := http.NewRequestWithContext(runCtx, http.MethodGet, s.BaseURL+"/api/v1/cognify/"+started.RunID+"/status", nil)
		if s.Token != "" {
			stReq.Header.Set("Authorization", "Bearer "+s.Token)
		}
		stResp, err := client.Do(stReq)
		if err != nil {
			return summary, err
		}
		var run struct {
			Status  string `json:"status"`
			Chunks  int    `json:"chunks_created"`
			Elapsed int64  `json:"elapsed_ms"`
		}
		_ = json.NewDecoder(stResp.Body).Decode(&run)
		stResp.Body.Close()
		switch run.Status {
		case "COMPLETED", "FAILED":
			summary.Chunks = run.Chunks
			summary.Elapsed = time.Duration(run.Elapsed) * time.Millisecond
			return summary, nil
		}
	}
}
