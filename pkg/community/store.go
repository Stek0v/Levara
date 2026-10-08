package community

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
)

// SummarizeConfig holds configuration for community summarization.
type SummarizeConfig struct {
	LLMProvider llm.Provider
	LLMModel    string
	EmbedClient *embed.Client
	Collections *store.CollectionManager
	DB          *sql.DB
	LLMCache    llmcache.LLMCacher
	Concurrency int // parallel LLM calls, default 3
	MinMembers  int // min members to summarize, default 3
	MaxContext  int // max entities per prompt, default 50
}

// BuildGraphFromSQL loads all active graph nodes and edges from SQL into a Graph.
// Used for full-graph community detection (not per-dataset).
func BuildGraphFromSQL(ctx context.Context, db *sql.DB) (*Graph, error) {
	if db == nil {
		return NewGraph(nil), nil
	}

	rows, err := db.QueryContext(ctx, "SELECT id FROM graph_nodes")
	if err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}
	var nodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan node: %w", err)
		}
		nodeIDs = append(nodeIDs, id)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return nil, fmt.Errorf("load nodes: %w", rowErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close nodes: %w", closeErr)
	}

	g := NewGraph(nodeIDs)
	// Parse exact instants in Go: SQLite TEXT comparisons are lexical, and
	// julianday loses subsecond precision. Both dialects use [from, until).
	now := time.Now().UTC().Round(time.Microsecond)
	edgeRows, err := db.QueryContext(ctx,
		"SELECT source_id,target_id,confidence,CAST(valid_from AS TEXT),CAST(valid_until AS TEXT) FROM graph_edges")
	if err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var src, dst string
		var conf float64
		var from, until sql.NullString
		if err := edgeRows.Scan(&src, &dst, &conf, &from, &until); err != nil {
			return nil, fmt.Errorf("scan edge: %w", err)
		}
		active := true
		for i, bound := range []sql.NullString{from, until} {
			if !bound.Valid {
				continue
			}
			stamp, err := communityTimestamp(bound.String)
			if err != nil {
				return nil, err
			}
			if i == 0 && now.Before(stamp) || i == 1 && !now.Before(stamp) {
				active = false
			}
		}
		if !active {
			continue
		}
		if conf <= 0 {
			conf = 1.0
		}
		g.AddEdge(src, dst, conf)
	}
	if err := edgeRows.Err(); err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}
	if err := edgeRows.Close(); err != nil {
		return nil, fmt.Errorf("close edges: %w", err)
	}
	return g, nil
}

func communityTimestamp(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999", "2006-01-02"} {
		if stamp, err := time.Parse(layout, raw); err == nil {
			micros := math.RoundToEven(float64(stamp.Nanosecond()) / 1000)
			return stamp.Truncate(time.Second).Add(time.Duration(micros) * time.Microsecond), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid community edge timestamp %q", raw)
}

// ReplaceCommunities deletes all existing communities and writes new ones.
// Also populates the community_members join table.
func ReplaceCommunities(ctx context.Context, db *sql.DB, dendro Dendrogram) error {
	if db == nil {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Clear old data
	if _, err := tx.ExecContext(ctx, "DELETE FROM community_members"); err != nil {
		return fmt.Errorf("delete members: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM graph_communities"); err != nil {
		return fmt.Errorf("delete communities: %w", err)
	}

	// Insert communities
	for _, level := range dendro.Levels {
		for _, c := range level {
			membersJSON, _ := json.Marshal(c.Members)
			_, err := tx.ExecContext(ctx,
				`INSERT INTO graph_communities (id, level, parent_id, member_node_ids, member_count, internal_weight, modularity, resolution, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
				c.ID, c.Level, c.ParentID, string(membersJSON), c.MemberCount, c.InternalWeight,
				0.0, dendro.Resolution)
			if err != nil {
				return fmt.Errorf("insert community %s: %w", c.ID, err)
			}

			// Populate join table
			for _, nodeID := range c.Members {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO community_members (community_id, node_id, level) VALUES ($1, $2, $3) ON CONFLICT (community_id,node_id) DO NOTHING`,
					c.ID, nodeID, c.Level); err != nil {
					return fmt.Errorf("insert member %s/%s: %w", c.ID, nodeID, err)
				}
			}
		}
	}

	return tx.Commit()
}

// SummarizeHierarchy generates LLM summaries for communities at all levels.
// Level 0: summaries from entity names + descriptions + edges.
// Level N: summaries from child community summaries.
// Embeds summaries into _community_summaries vector collection.
func SummarizeHierarchy(ctx context.Context, dendro Dendrogram, g *Graph, cfg SummarizeConfig) error {
	if cfg.LLMProvider == nil {
		log.Printf("[community] LLM not configured — skipping summarization")
		return nil
	}
	if strings.TrimSpace(cfg.LLMModel) == "" {
		log.Printf("[community] LLM model not configured — skipping summarization")
		return nil
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	if cfg.MinMembers <= 0 {
		cfg.MinMembers = 3
	}
	if cfg.MaxContext <= 0 {
		cfg.MaxContext = 50
	}

	// Process levels bottom-up: level 0 first, then 1, etc.
	summaryByID := make(map[string]string) // community ID → summary text

	for _, level := range dendro.Levels {
		sem := make(chan struct{}, cfg.Concurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		var firstErr error

		for _, comm := range level {
			if comm.MemberCount < cfg.MinMembers {
				continue
			}

			wg.Add(1)
			comm := comm
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				var summary string
				var err error
				if comm.Level == 0 {
					// Level 0: summarize from entity names + graph edges
					summary, err = summarizeFromEntities(ctx, cfg, comm, g)
				} else {
					// Level N: summarize from child summaries
					var childSummaries []string
					mu.Lock()
					for _, child := range dendro.Levels[comm.Level-1] {
						if child.ParentID == comm.ID {
							if s, ok := summaryByID[child.ID]; ok && s != "" {
								childSummaries = append(childSummaries, s)
							}
						}
					}
					mu.Unlock()
					if len(childSummaries) > 0 {
						summary = summarizeFromChildren(ctx, cfg, comm, childSummaries)
					}
				}

				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				if summary != "" {
					mu.Lock()
					summaryByID[comm.ID] = summary
					mu.Unlock()

					// Write summary to DB
					if cfg.DB != nil {
						_, err := cfg.DB.ExecContext(ctx,
							"UPDATE graph_communities SET summary = $1, generation = '', sources_json = '[]', lineage_verified = 0, updated_at = CURRENT_TIMESTAMP WHERE id = $2",
							summary, comm.ID)
						if err != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = fmt.Errorf("write community summary: %w", err)
							}
							mu.Unlock()
						}
					}
				}
			}()
		}
		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
	}

	// Embed all summaries into _community_summaries collection
	if cfg.EmbedClient != nil && cfg.Collections != nil {
		embedSummaries(ctx, cfg, summaryByID, dendro)
	}

	return nil
}

func summarizeFromEntities(ctx context.Context, cfg SummarizeConfig, comm Community, g *Graph) (string, error) {
	// Load entity names from graph
	members := comm.Members
	if len(members) > cfg.MaxContext {
		// Take top-N by degree
		type nodeDeg struct {
			id  string
			deg float64
		}
		var degs []nodeDeg
		for _, m := range members {
			if idx, ok := g.idxOf[m]; ok {
				degs = append(degs, nodeDeg{m, g.degree[idx]})
			}
		}
		// Sort by degree desc
		for i := 0; i < len(degs); i++ {
			for j := i + 1; j < len(degs); j++ {
				if degs[j].deg > degs[i].deg {
					degs[i], degs[j] = degs[j], degs[i]
				}
			}
		}
		members = make([]string, cfg.MaxContext)
		for i := 0; i < cfg.MaxContext && i < len(degs); i++ {
			members[i] = degs[i].id
		}
	}

	// Load entity details from DB
	var entityLines []string
	if cfg.DB != nil {
		for _, id := range members {
			var name, typ, desc string
			err := cfg.DB.QueryRowContext(ctx,
				"SELECT name, type, description FROM graph_nodes WHERE id = $1", id).
				Scan(&name, &typ, &desc)
			if err != nil && err != sql.ErrNoRows {
				return "", fmt.Errorf("load summary entity: %w", err)
			}
			if err == nil && name != "" {
				line := fmt.Sprintf("- %s (%s)", name, typ)
				if desc != "" {
					line += ": " + desc
				}
				entityLines = append(entityLines, line)
			}
		}
	}

	// Load inter-member edges
	var edgeLines []string
	memberSet := make(map[string]bool)
	for _, m := range members {
		memberSet[m] = true
	}
	if cfg.DB != nil && len(members) > 0 {
		// Build IN clause
		placeholders := make([]string, len(members))
		args := make([]any, len(members))
		for i, m := range members {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			args[i] = m
		}
		inClause := strings.Join(placeholders, ",")

		rows, err := cfg.DB.QueryContext(ctx, fmt.Sprintf(
			`SELECT gn.name, ge.relationship_name, gn2.name
			 FROM graph_edges ge
			 JOIN graph_nodes gn ON ge.source_id = gn.id
			 JOIN graph_nodes gn2 ON ge.target_id = gn2.id
			 WHERE ge.source_id IN (%s) AND ge.target_id IN (%s)
			 LIMIT 30`, inClause, inClause),
			args...)
		if err != nil {
			return "", fmt.Errorf("load summary edges: %w", err)
		}
		for rows.Next() {
			var src, rel, tgt string
			if err := rows.Scan(&src, &rel, &tgt); err != nil {
				_ = rows.Close()
				return "", fmt.Errorf("scan summary edge: %w", err)
			}
			edgeLines = append(edgeLines, fmt.Sprintf("- %s → %s → %s", src, rel, tgt))
		}
		rowErr := rows.Err()
		closeErr := rows.Close()
		if rowErr != nil {
			return "", fmt.Errorf("load summary edges: %w", rowErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close summary edges: %w", closeErr)
		}
	}

	if len(entityLines) == 0 {
		return "", nil
	}

	prompt := "You are summarizing a cluster of related entities from a knowledge graph.\n\n"
	prompt += "Entities in this group:\n" + strings.Join(entityLines, "\n") + "\n"
	if len(edgeLines) > 0 {
		prompt += "\nRelationships:\n" + strings.Join(edgeLines, "\n") + "\n"
	}
	if len(comm.Members) > cfg.MaxContext {
		prompt += fmt.Sprintf("\n(Showing top %d of %d entities by connectivity)\n", cfg.MaxContext, len(comm.Members))
	}
	prompt += "\nWrite a 2-4 sentence summary describing what this group represents, the key relationships, and the main topics. Be factual and concise."

	resp, err := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       cfg.LLMModel,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		log.Printf("[community] LLM summarize error: %v", err)
		return "", nil
	}
	return strings.TrimSpace(resp.Content), nil
}

func summarizeFromChildren(ctx context.Context, cfg SummarizeConfig, comm Community, childSummaries []string) string {
	prompt := "You are creating a higher-level summary from multiple topic clusters.\n\n"
	for i, s := range childSummaries {
		prompt += fmt.Sprintf("Cluster %d:\n%s\n\n", i+1, s)
	}
	prompt += "Write a 2-4 sentence overview that captures the broader theme connecting these clusters. Focus on overarching topics, not details."

	resp, err := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       cfg.LLMModel,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: 0.3,
		MaxTokens:   300,
	})
	if err != nil {
		log.Printf("[community] LLM summarize level %d error: %v", comm.Level, err)
		return ""
	}
	return strings.TrimSpace(resp.Content)
}

func embedSummaries(ctx context.Context, cfg SummarizeConfig, summaryByID map[string]string, dendro Dendrogram) {
	collName := "_community_summaries"
	if !cfg.Collections.Has(collName) {
		if err := cfg.Collections.Create(collName); err != nil {
			log.Printf("[community] create collection %q: %v", collName, err)
			return
		}
	}

	// Delete old summaries
	for _, level := range dendro.Levels {
		for _, c := range level {
			cfg.Collections.Delete(collName, c.ID)
		}
	}

	// Collect texts to embed
	var ids []string
	var texts []string
	var metas []string
	for id, summary := range summaryByID {
		if summary == "" {
			continue
		}
		ids = append(ids, id)
		texts = append(texts, summary)

		// Find community for metadata
		var level, memberCount int
		for _, lvl := range dendro.Levels {
			for _, c := range lvl {
				if c.ID == id {
					level = c.Level
					memberCount = c.MemberCount
					break
				}
			}
		}
		meta := fmt.Sprintf(`{"community_id":"%s","level":%d,"member_count":%d,"text":%s}`,
			id, level, memberCount, mustJSONStr(summary))
		metas = append(metas, meta)
	}

	if len(texts) == 0 {
		return
	}

	vecs, err := cfg.EmbedClient.EmbedTexts(ctx, texts)
	if err != nil {
		log.Printf("[community] embed summaries: %v", err)
		return
	}

	inserted := 0
	for i, vec := range vecs {
		if i < len(ids) {
			if err := cfg.Collections.Insert(collName, ids[i], vec, metas[i]); err != nil {
				log.Printf("[community] insert summary %s: %v", ids[i], err)
			} else {
				inserted++
			}
		}
	}
	log.Printf("[community] embedded %d/%d community summaries into %q", inserted, len(texts), collName)
}

// mustJSONStr safely marshals a string to JSON.
func mustJSONStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// LookupCommunities finds which communities a set of node IDs belong to.
// Returns community IDs at the specified level.
func LookupCommunities(ctx context.Context, db *sql.DB, nodeIDs []string, level int) ([]string, error) {
	if db == nil || len(nodeIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(nodeIDs))
	args := make([]any, len(nodeIDs)+1)
	for i, id := range nodeIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	args[len(nodeIDs)] = level

	query := fmt.Sprintf(
		"SELECT DISTINCT community_id FROM community_members WHERE node_id IN (%s) AND level = $%d",
		strings.Join(placeholders, ","), len(nodeIDs)+1)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup communities: %w", err)
	}
	defer rows.Close()

	var commIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan community lookup: %w", err)
		}
		commIDs = append(commIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lookup communities: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close community lookup: %w", err)
	}
	return commIDs, nil
}
