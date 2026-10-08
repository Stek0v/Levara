package community

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/llm"
)

const maxProofBytes = 128 << 10
const maxProofSources = 256

// Source is immutable native document lineage used by a global community build.
// InputSHA256 binds each persisted entry to the full detection snapshot.
type Source struct {
	DatasetID       string `json:"dataset_id"`
	DocumentID      string `json:"document_id"`
	ContentRevision int64  `json:"content_revision"`
	Derived         bool   `json:"derived"`
	Collection      string `json:"collection,omitempty"`
	Generation      string `json:"generation,omitempty"`
	SourceRevision  int64  `json:"source_revision"`
	RawContentHash  string `json:"raw_content_hash"`
	InputSHA256     string `json:"input_sha256,omitempty"`
}

func canonicalDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func sourceShape(s Source, persisted bool) error {
	if s.DatasetID == "" || s.DocumentID == "" || strings.TrimSpace(s.DatasetID) != s.DatasetID || strings.TrimSpace(s.DocumentID) != s.DocumentID || s.ContentRevision < 0 || s.SourceRevision <= 0 || !canonicalDigest(s.RawContentHash) {
		return access.ErrDocumentInvalid
	}
	if s.Derived {
		if s.Collection == "" || s.Generation == "" || strings.TrimSpace(s.Collection) != s.Collection || strings.TrimSpace(s.Generation) != s.Generation {
			return access.ErrDocumentInvalid
		}
	} else if s.Collection != "" || s.Generation != "" {
		return access.ErrDocumentInvalid
	}
	if persisted && !canonicalDigest(s.InputSHA256) {
		return access.ErrDocumentInvalid
	}
	return nil
}
func parseSources(raw string, persisted bool) ([]Source, error) {
	if len(raw) > maxProofBytes {
		return nil, access.ErrDocumentInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var sources []Source
	if persisted {
		if err := decoder.Decode(&sources); err != nil {
			return nil, access.ErrDocumentInvalid
		}
	} else {
		// Native HTTP lineage uses searchDocumentSource's serializer, which emits
		// empty workspace fields. Accept that wire shape without admitting workspace proof.
		var native []struct {
			Source
			ProjectID           string `json:"project_id"`
			Branch              string `json:"branch"`
			ChunkID             string `json:"chunk_id"`
			Path                string `json:"path"`
			FileDigest          string `json:"file_digest"`
			VectorID            string `json:"vector_id,omitempty"`
			DatasetMetadataOnly bool   `json:"dataset_metadata_only,omitempty"`
		}
		if err := decoder.Decode(&native); err != nil || native == nil {
			return nil, access.ErrDocumentInvalid
		}
		sources = make([]Source, 0, len(native))
		for _, entry := range native {
			if entry.ProjectID != "" || entry.Branch != "" || entry.ChunkID != "" || entry.Path != "" || entry.FileDigest != "" || entry.DatasetMetadataOnly {
				return nil, access.ErrDocumentInvalid
			}
			sources = append(sources, entry.Source)
		}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || sources == nil || len(sources) > maxProofSources || persisted && len(sources) == 0 {
		return nil, access.ErrDocumentInvalid
	}
	digest := ""
	for _, s := range sources {
		if err := sourceShape(s, persisted); err != nil {
			return nil, err
		}
		if persisted {
			if digest != "" && digest != s.InputSHA256 {
				return nil, access.ErrDocumentInvalid
			}
			digest = s.InputSHA256
		}
	}
	return sources, nil
}

// ParseSources rejects unsupported provenance and empty/unverified community proof.
func ParseSources(raw string) ([]Source, error) { return parseSources(raw, true) }

// CheckSources checks native document liveness; caller authorization is separate.
// The supplied policy may use a caller-owned fence transaction.
func CheckSources(ctx context.Context, policy access.SQLPolicy, sources []Source) error {
	if len(sources) == 0 || len(sources) > maxProofSources {
		return access.ErrDocumentInvalid
	}
	seen := map[Source]bool{}
	path := map[Source]bool{}
	var check func(Source, int) error
	check = func(s Source, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sourceShape(s, false); err != nil {
			return err
		}
		s.InputSHA256 = ""
		if depth >= 16 || path[s] {
			return access.ErrDocumentInvalid
		}
		if seen[s] {
			return nil
		}
		if len(seen) >= maxProofSources {
			return access.ErrDocumentInvalid
		}
		path[s] = true
		defer delete(path, s)
		ref := access.DocumentRef{DatasetID: s.DatasetID, DataID: s.DocumentID}
		version, hash, err := policy.SourceVersion(ctx, ref)
		if err != nil {
			return err
		}
		if version != s.SourceRevision || hash != s.RawContentHash {
			return access.ErrDocumentVersionConflict
		}
		resource, err := policy.GetDocumentResource(ctx, ref)
		if err != nil && !errors.Is(err, access.ErrDocumentNotFound) {
			return err
		}
		if err == nil && (resource.Tombstoned || resource.ContentRevision != s.ContentRevision) || errors.Is(err, access.ErrDocumentNotFound) && s.ContentRevision != 0 {
			return access.ErrDocumentVersionConflict
		}
		if s.Derived {
			lineage, present, err := policy.DocumentIndexLineage(ctx, ref, s.ContentRevision, s.Collection, s.Generation)
			if err != nil {
				return err
			}
			if !present || lineage.SourceRevision != s.SourceRevision || lineage.RawContentHash != s.RawContentHash {
				return access.ErrDocumentVersionConflict
			}
			dependencies, err := parseSources(lineage.SourcesJSON, false)
			if err != nil {
				return err
			}
			for _, dependency := range dependencies {
				if err := check(dependency, depth+1); err != nil {
					return err
				}
			}
		}
		if len(seen) >= maxProofSources {
			return access.ErrDocumentInvalid
		}
		seen[s] = true
		return nil
	}
	for _, s := range sources {
		if err := check(s, 0); err != nil {
			return err
		}
	}
	return nil
}

type snapshotNode struct {
	ID, Name, Type, Description, Dataset string
	Properties                           json.RawMessage
}
type snapshotEdge struct {
	ID, Source, Target, Relation, Dataset string
	Confidence                            float64
	From, Until                           sql.NullString
	Properties                            json.RawMessage
}
type communitySnapshot struct {
	Nodes []snapshotNode
	Edges []snapshotEdge
}

func canonicalSnapshotProperties(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return nil, access.ErrDocumentInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, access.ErrDocumentInvalid
	}
	return json.Marshal(fields)
}

func readCommunitySnapshot(ctx context.Context, tx *sql.Tx) (communitySnapshot, *Graph, error) {
	var snapshot communitySnapshot
	rows, err := tx.QueryContext(ctx, "SELECT id,COALESCE(name,''),COALESCE(type,''),COALESCE(description,''),COALESCE(dataset_id,''),COALESCE(CAST(properties AS TEXT),'{}') FROM graph_nodes ORDER BY id")
	if err != nil {
		return snapshot, nil, err
	}
	var ids []string
	for rows.Next() {
		var n snapshotNode
		var properties string
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Dataset, &properties); err != nil {
			_ = rows.Close()
			return snapshot, nil, err
		}
		n.Properties, err = canonicalSnapshotProperties(json.RawMessage(properties))
		if err != nil {
			_ = rows.Close()
			return snapshot, nil, err
		}
		snapshot.Nodes = append(snapshot.Nodes, n)
		ids = append(ids, n.ID)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil {
		return snapshot, nil, rowErr
	}
	if closeErr != nil {
		return snapshot, nil, closeErr
	}
	g := NewGraph(ids)
	rows, err = tx.QueryContext(ctx, "SELECT id,source_id,target_id,relationship_name,COALESCE(dataset_id,''),confidence,CAST(valid_from AS TEXT),CAST(valid_until AS TEXT),COALESCE(CAST(properties AS TEXT),'{}') FROM graph_edges ORDER BY id")
	if err != nil {
		return snapshot, nil, err
	}
	now := time.Now().UTC().Round(time.Microsecond)
	for rows.Next() {
		var e snapshotEdge
		var properties string
		if err := rows.Scan(&e.ID, &e.Source, &e.Target, &e.Relation, &e.Dataset, &e.Confidence, &e.From, &e.Until, &properties); err != nil {
			_ = rows.Close()
			return snapshot, nil, err
		}
		active := true
		for i, bound := range []sql.NullString{e.From, e.Until} {
			if !bound.Valid {
				continue
			}
			stamp, err := communityTimestamp(bound.String)
			if err != nil {
				_ = rows.Close()
				return snapshot, nil, err
			}
			if i == 0 && now.Before(stamp) || i == 1 && !now.Before(stamp) {
				active = false
			}
		}
		if !active {
			continue
		}
		if _, ok := g.idxOf[e.Source]; !ok {
			_ = rows.Close()
			return snapshot, nil, access.ErrDocumentInvalid
		}
		if _, ok := g.idxOf[e.Target]; !ok {
			_ = rows.Close()
			return snapshot, nil, access.ErrDocumentInvalid
		}
		e.Properties, err = canonicalSnapshotProperties(json.RawMessage(properties))
		if err != nil {
			_ = rows.Close()
			return snapshot, nil, err
		}
		snapshot.Edges = append(snapshot.Edges, e)
		weight := e.Confidence
		if weight <= 0 {
			weight = 1
		}
		g.AddEdge(e.Source, e.Target, weight)
	}
	rowErr = rows.Err()
	closeErr = rows.Close()
	if rowErr != nil {
		return snapshot, nil, rowErr
	}
	if closeErr != nil {
		return snapshot, nil, closeErr
	}
	return snapshot, g, nil
}

func captureCommunitySources(ctx context.Context, policy access.SQLPolicy, snapshot communitySnapshot) ([]Source, bool, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, false, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	verified := true
	unique := map[Source]bool{}
	var add func(Source, int) error
	add = func(s Source, depth int) error {
		if depth >= 16 {
			return access.ErrDocumentInvalid
		}
		if err := sourceShape(s, false); err != nil {
			return err
		}
		s.InputSHA256 = digest
		if unique[s] {
			return nil
		}
		if len(unique) >= maxProofSources {
			return access.ErrDocumentInvalid
		}
		unique[s] = true
		if s.Derived {
			lineage, present, err := policy.DocumentIndexLineage(ctx, access.DocumentRef{DatasetID: s.DatasetID, DataID: s.DocumentID}, s.ContentRevision, s.Collection, s.Generation)
			if err != nil {
				return err
			}
			if !present {
				return access.ErrDocumentVersionConflict
			}
			deps, err := parseSources(lineage.SourcesJSON, false)
			if err != nil {
				return err
			}
			for _, d := range deps {
				if err := add(d, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	assertion := func(dataset string, properties json.RawMessage) error {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(properties, &fields); err != nil {
			return access.ErrDocumentInvalid
		}
		for _, key := range []string{"project_id", "branch", "chunk_id", "path", "file_digest", "dataset_metadata_only"} {
			if _, present := fields[key]; present {
				return access.ErrDocumentInvalid
			}
		}
		var s Source
		if err := json.Unmarshal(properties, &s); err != nil {
			return access.ErrDocumentInvalid
		}
		s.DatasetID = dataset
		s.Derived = true
		s.InputSHA256 = ""
		if s.DocumentID == "" {
			if dataset != "" {
				registered, err := policy.HasRegisteredDocuments(ctx, dataset)
				if err != nil {
					return err
				}
				if registered {
					return access.ErrDocumentInvalid
				}
			}
			verified = false
			return nil
		}
		if dataset == "" || s.Collection == "" || s.Generation == "" {
			return access.ErrDocumentInvalid
		}
		lineage, present, err := policy.DocumentIndexLineage(ctx, access.DocumentRef{DatasetID: dataset, DataID: s.DocumentID}, s.ContentRevision, s.Collection, s.Generation)
		if err != nil {
			return err
		}
		if !present {
			return access.ErrDocumentVersionConflict
		}
		s.SourceRevision = lineage.SourceRevision
		s.RawContentHash = lineage.RawContentHash
		return add(s, 0)
	}
	for _, n := range snapshot.Nodes {
		if err := assertion(n.Dataset, n.Properties); err != nil {
			return nil, false, err
		}
	}
	for _, e := range snapshot.Edges {
		if err := assertion(e.Dataset, e.Properties); err != nil {
			return nil, false, err
		}
	}
	sources := make([]Source, 0, len(unique))
	for s := range unique {
		sources = append(sources, s)
	}
	sort.Slice(sources, func(i, j int) bool {
		a, _ := json.Marshal(sources[i])
		b, _ := json.Marshal(sources[j])
		return bytes.Compare(a, b) < 0
	})
	if len(sources) > 0 {
		if err := CheckSources(ctx, policy, sources); err != nil {
			return nil, false, err
		}
	}
	if len(sources) == 0 {
		verified = false
	}
	encoded, err := json.Marshal(sources)
	if err != nil {
		return nil, false, err
	}
	if len(encoded) > maxProofBytes {
		return nil, false, access.ErrDocumentInvalid
	}
	return sources, verified, nil
}

// RebuildPublished is trusted local global maintenance. It never invents an
// authenticated source owner; request consumers still authorize all sources.
func RebuildPublished(ctx context.Context, cfg Config, summaries SummarizeConfig, sqlite bool) (*Dendrogram, error) {
	if summaries.DB == nil {
		return nil, access.ErrDocumentInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	policy := access.SQLPolicy{DB: summaries.DB}
	tx, locked, release, err := policy.BeginTransferFenceTx(ctx, sqlite)
	if err != nil {
		return nil, err
	}
	defer release()
	if !sqlite {
		if _, err := tx.ExecContext(ctx, "LOCK TABLE graph_nodes,graph_edges IN SHARE MODE"); err != nil {
			return nil, err
		}
	}
	snapshot, g, err := readCommunitySnapshot(ctx, tx)
	if err != nil {
		return nil, err
	}
	sources, verified, err := captureCommunitySources(ctx, locked, snapshot)
	if err != nil {
		return nil, err
	}
	// ponytail: full recompute preserves one exact input snapshot. Reuse incremental
	// detection only after a caller-owned snapshot path is justified by measurements.
	dendro := Louvain(g, cfg)
	texts, err := summarizeSnapshot(ctx, snapshot, g, dendro, summaries)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(sources) > 0 {
		if err := CheckSources(ctx, locked, sources); err != nil {
			return nil, err
		}
	}
	generation := uuid.NewString()
	proof, err := json.Marshal(sources)
	if err != nil {
		return nil, err
	}
	flag := 0
	if verified {
		flag = 1
	}
	for _, table := range []string{"community_members", "graph_communities"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return nil, err
		}
	}
	for _, level := range dendro.Levels {
		for _, c := range level {
			members, err := json.Marshal(c.Members)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO graph_communities(id,level,parent_id,member_node_ids,member_count,internal_weight,modularity,resolution,summary,generation,sources_json,lineage_verified)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, c.ID, c.Level, c.ParentID, string(members), c.MemberCount, c.InternalWeight, 0.0, dendro.Resolution, texts[c.ID], generation, string(proof), flag); err != nil {
				return nil, err
			}
			for _, id := range c.Members {
				if _, err := tx.ExecContext(ctx, "INSERT INTO community_members(community_id,node_id,level) VALUES($1,$2,$3) ON CONFLICT(community_id,node_id) DO NOTHING", c.ID, id, c.Level); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	release()
	if summaries.EmbedClient != nil && summaries.Collections != nil && len(texts) > 0 {
		if err := embedPublished(ctx, summaries, dendro, texts, generation); err != nil {
			return &dendro, err
		}
	}
	return &dendro, nil
}

func summarizeSnapshot(ctx context.Context, snapshot communitySnapshot, g *Graph, dendro Dendrogram, cfg SummarizeConfig) (map[string]string, error) {
	out := map[string]string{}
	if cfg.LLMProvider == nil || strings.TrimSpace(cfg.LLMModel) == "" {
		return out, nil
	}
	if cfg.MinMembers <= 0 {
		cfg.MinMembers = 3
	}
	if cfg.MaxContext <= 0 {
		cfg.MaxContext = 50
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 3
	}
	nodes := map[string]snapshotNode{}
	for _, n := range snapshot.Nodes {
		nodes[n.ID] = n
	}
	for _, level := range dendro.Levels {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error
		sem := make(chan struct{}, cfg.Concurrency)
		for _, comm := range level {
			if comm.MemberCount < cfg.MinMembers {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			acquired := false
			select {
			case sem <- struct{}{}:
				acquired = true
			case <-ctx.Done():
			}
			if !acquired {
				break
			}
			if ctx.Err() != nil {
				<-sem
				break
			}
			comm := comm
			var prompt string
			if comm.Level == 0 {
				members := append([]string(nil), comm.Members...)
				sort.SliceStable(members, func(i, j int) bool { return g.degree[g.idxOf[members[i]]] > g.degree[g.idxOf[members[j]]] })
				if len(members) > cfg.MaxContext {
					members = members[:cfg.MaxContext]
				}
				included := map[string]bool{}
				var lines []string
				for _, id := range members {
					n := nodes[id]
					included[id] = true
					lines = append(lines, fmt.Sprintf("- %s (%s): %s", n.Name, n.Type, n.Description))
				}
				for _, e := range snapshot.Edges {
					if included[e.Source] && included[e.Target] {
						lines = append(lines, fmt.Sprintf("- %s → %s → %s", nodes[e.Source].Name, e.Relation, nodes[e.Target].Name))
					}
				}
				prompt = "Summarize these knowledge graph entities and relationships in 2-4 factual sentences:\n" + strings.Join(lines, "\n")
			} else {
				var children []string
				mu.Lock()
				for _, child := range dendro.Levels[comm.Level-1] {
					if child.ParentID == comm.ID && out[child.ID] != "" {
						children = append(children, out[child.ID])
					}
				}
				mu.Unlock()
				if len(children) == 0 {
					<-sem
					continue
				}
				prompt = "Summarize these child community summaries in 2-4 factual sentences:\n" + strings.Join(children, "\n")
			}
			wg.Add(1)
			go func(id, prompt string) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := ctx.Err(); err != nil {
					return
				}
				response, err := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{Model: cfg.LLMModel, Messages: []llm.Message{{Role: "user", Content: prompt}}, Temperature: 0.3, MaxTokens: 300})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				if err := ctx.Err(); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				if response == nil {
					if firstErr == nil {
						firstErr = errors.New("empty community model response")
					}
					return
				}
				if text := strings.TrimSpace(response.Content); text != "" {
					out[id] = text
				}
			}(comm.ID, prompt)
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}
	return out, nil
}

func embedPublished(ctx context.Context, cfg SummarizeConfig, dendro Dendrogram, texts map[string]string, generation string) error {
	const collection = "_community_summaries"
	var ids, values []string
	var levels, counts []int
	for _, level := range dendro.Levels {
		for _, c := range level {
			if texts[c.ID] != "" {
				ids = append(ids, c.ID)
				values = append(values, texts[c.ID])
				levels = append(levels, c.Level)
				counts = append(counts, c.MemberCount)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	vectors, err := cfg.EmbedClient.EmbedTexts(ctx, values)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(vectors) != len(ids) {
		return errors.New("community embedding count mismatch")
	}
	if !cfg.Collections.Has(collection) {
		if err := cfg.Collections.Create(collection); err != nil {
			return err
		}
	}
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]any{"community_id": id, "generation": generation, "text": values[i], "level": levels[i], "member_count": counts[i]})
		if err != nil {
			return err
		}
		if err := cfg.Collections.Insert(collection, id, vectors[i], string(metadata)); err != nil {
			return err
		}
	}
	return nil
}
