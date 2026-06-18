package http

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	graphContextOrderVSAFirst = "vsa_first"
	graphContextOrderSQLFirst = "sql_first"
	graphContextOrderSQLOnly  = "sql_only"
	graphContextOrderVSAOnly  = "vsa_only"

	graphContextProviderVSA   = "vsa"
	graphContextProviderSQL   = "sql"
	graphContextProviderNeo4j = "neo4j"
)

type graphContextItem struct {
	SourceName   string
	Predicate    string
	TargetName   string
	DatasetID    string
	DomainID     string
	CollectionID string
	DocumentID   string
	Provider     string
	Score        float64
	Text         string
	RouteBoost   float64
	HasRouteMeta bool
}

type graphContextPolicy struct {
	TotalLimit      int
	VSAReserve      int
	Order           string
	QueryText       string
	RouteCandidates []dcdRouteCandidate
}

type graphContextAssembly struct {
	Context                []string
	VSAContext             []string
	SQLContext             []string
	Neo4jContext           []string
	TargetNames            []string
	Order                  string
	TotalLimit             int
	VSAReserve             int
	DedupCount             int
	VSALatency             time.Duration
	GraphLatency           time.Duration
	GraphProvider          string
	RouteBoostEnabled      bool
	RouteBoostedCount      int
	RouteMetadataHitCount  int
	RouteMetadataMissCount int
}

func defaultGraphContextPolicy() graphContextPolicy {
	total := graphContextEnvInt("LEVARA_GRAPH_CONTEXT_LIMIT", 20)
	if total <= 0 {
		total = 20
	}
	reserve := graphContextEnvInt("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", 10)
	if reserve < 0 {
		reserve = 0
	}
	if reserve > total {
		reserve = total
	}
	order := strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_GRAPH_CONTEXT_ORDER")))
	switch order {
	case "", graphContextOrderVSAFirst:
		order = graphContextOrderVSAFirst
	case graphContextOrderSQLFirst, graphContextOrderSQLOnly, graphContextOrderVSAOnly:
	default:
		order = graphContextOrderVSAFirst
	}
	return graphContextPolicy{
		TotalLimit: total,
		VSAReserve: reserve,
		Order:      order,
	}
}

func graphContextEnvInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func assembleGraphContext(ctx context.Context, cfg APIConfig, entityNames []string, allowedDatasetIDs []string, policy graphContextPolicy) graphContextAssembly {
	entityNames = dedup(entityNames)
	if len(entityNames) == 0 || policy.TotalLimit <= 0 {
		return graphContextAssembly{
			Order:      policy.Order,
			TotalLimit: policy.TotalLimit,
			VSAReserve: policy.VSAReserve,
		}
	}
	if policy.Order == "" {
		policy.Order = graphContextOrderVSAFirst
	}
	if policy.VSAReserve > policy.TotalLimit {
		policy.VSAReserve = policy.TotalLimit
	}

	out := graphContextAssembly{
		Order:             policy.Order,
		TotalLimit:        policy.TotalLimit,
		VSAReserve:        policy.VSAReserve,
		RouteBoostEnabled: len(policy.RouteCandidates) > 0,
	}
	seen := map[string]struct{}{}
	add := func(items []graphContextItem, limit int) []string {
		if limit <= 0 {
			return nil
		}
		lines := make([]string, 0, limit)
		for _, item := range items {
			if len(out.Context) >= policy.TotalLimit || len(lines) >= limit {
				break
			}
			key := item.relationshipKey()
			if _, ok := seen[key]; ok {
				out.DedupCount++
				continue
			}
			seen[key] = struct{}{}
			line := item.format()
			out.Context = append(out.Context, line)
			lines = append(lines, line)
			switch item.Provider {
			case graphContextProviderVSA:
				out.VSAContext = append(out.VSAContext, line)
				if item.RouteBoost > 0 {
					out.RouteBoostedCount++
				}
				if item.HasRouteMeta {
					out.RouteMetadataHitCount++
				} else {
					out.RouteMetadataMissCount++
				}
			case graphContextProviderNeo4j:
				out.Neo4jContext = append(out.Neo4jContext, line)
				if item.TargetName != "" {
					out.TargetNames = append(out.TargetNames, item.TargetName)
				}
			default:
				out.SQLContext = append(out.SQLContext, line)
				if item.TargetName != "" {
					out.TargetNames = append(out.TargetNames, item.TargetName)
				}
			}
		}
		return lines
	}

	queryVSA := func(limit int) []graphContextItem {
		if limit <= 0 || cfg.DB == nil {
			return nil
		}
		start := time.Now()
		items := vsaGraphContextItems(ctx, cfg, entityNames, allowedDatasetIDs, limit, policy.QueryText, policy.RouteCandidates)
		out.VSALatency += time.Since(start)
		return items
	}
	queryGraph := func(limit int) []graphContextItem {
		if limit <= 0 {
			return nil
		}
		start := time.Now()
		items := graphContextItems(ctx, cfg, entityNames, allowedDatasetIDs)
		out.GraphLatency += time.Since(start)
		for _, item := range items {
			if item.Provider != "" {
				out.GraphProvider = item.Provider
				break
			}
		}
		return items
	}

	switch policy.Order {
	case graphContextOrderSQLOnly:
		add(queryGraph(policy.TotalLimit), policy.TotalLimit)
	case graphContextOrderVSAOnly:
		add(queryVSA(policy.TotalLimit), policy.TotalLimit)
	case graphContextOrderSQLFirst:
		add(queryGraph(policy.TotalLimit), policy.TotalLimit)
		add(queryVSA(policy.TotalLimit-len(out.Context)), policy.TotalLimit-len(out.Context))
	default:
		vsaLimit := policy.VSAReserve
		if vsaLimit <= 0 {
			vsaLimit = policy.TotalLimit
		}
		add(queryVSA(vsaLimit), vsaLimit)
		add(queryGraph(policy.TotalLimit-len(out.Context)), policy.TotalLimit-len(out.Context))
	}

	out.TargetNames = dedup(out.TargetNames)
	return out
}

func graphContextItems(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []graphContextItem {
	if cfg.Neo4jCfg.Neo4jURL != "" {
		return graphContextItemsFromNeo4j(ctx, cfg, names, allowedDatasetIDs)
	}
	if cfg.DB != nil {
		return graphContextItemsFromPostgres(ctx, cfg, names, allowedDatasetIDs)
	}
	return nil
}

func (i graphContextItem) relationshipKey() string {
	return strings.ToLower(strings.TrimSpace(i.DatasetID)) + "\x00" +
		strings.ToLower(strings.TrimSpace(i.SourceName)) + "\x00" +
		strings.ToLower(strings.TrimSpace(i.Predicate)) + "\x00" +
		strings.ToLower(strings.TrimSpace(i.TargetName))
}

func (i graphContextItem) format() string {
	if i.Text != "" {
		return i.Text
	}
	if i.Provider == graphContextProviderVSA {
		return fmt.Sprintf("%s is related to %s via %s (VSA score %.3f)", i.SourceName, i.TargetName, i.Predicate, i.Score)
	}
	return fmt.Sprintf("%s is related to %s via %s", i.SourceName, i.TargetName, i.Predicate)
}

func graphContextDebugMetadata(a graphContextAssembly) map[string]any {
	return map[string]any{
		"graph_context_order":                     a.Order,
		"graph_context_total_limit":               a.TotalLimit,
		"graph_context_vsa_reserve":               a.VSAReserve,
		"graph_context_total_count":               len(a.Context),
		"graph_context_vsa_count":                 len(a.VSAContext),
		"graph_context_sql_count":                 len(a.SQLContext),
		"graph_context_neo4j_count":               len(a.Neo4jContext),
		"graph_context_provider":                  a.GraphProvider,
		"graph_context_dedup_count":               a.DedupCount,
		"graph_context_vsa_latency_ms":            a.VSALatency.Milliseconds(),
		"graph_context_sql_latency_ms":            a.GraphLatency.Milliseconds(),
		"graph_context_route_boost_enabled":       a.RouteBoostEnabled,
		"graph_context_route_boosted_count":       a.RouteBoostedCount,
		"graph_context_route_metadata_hit_count":  a.RouteMetadataHitCount,
		"graph_context_route_metadata_miss_count": a.RouteMetadataMissCount,
	}
}
