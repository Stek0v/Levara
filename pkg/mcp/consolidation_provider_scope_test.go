package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/llm"
)

func consolidationBase(d *memoryCommitEvidenceDeps) *fakeDeps {
	if base, ok := d.Deps.(*postgresMemoryDeps); ok {
		return base.fakeDeps
	}
	return d.Deps.(*fakeDeps)
}

type consolidationScopeSummarizer struct{ prompts []string }

func (*consolidationScopeSummarizer) Name() string { return "scope-test" }
func (p *consolidationScopeSummarizer) ChatCompletion(_ context.Context, r llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.prompts = append(p.prompts, r.Messages[0].Content)
	return &llm.CompletionResponse{Content: "consolidated summary"}, nil
}

func TestConsolidationMaintenanceNamespaceIsolation(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			if err := NewConsolidationRunner(d, 0).RunOnce(ctx); err == nil {
				t.Fatal("authenticated maintenance invented background authority")
			}
			d.actor.TrustedLocal = true
			for _, row := range []struct{ id, owner, tag string }{{"f1", "owner-b", "foreign"}, {"f2", "owner-b", "foreign"}, {"s1", "", "shared"}, {"s2", "", "shared"}} {
				if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at) VALUES($1,$2,$3,'user',$4,'levara','memory','fact',$5,$6)`), row.id, row.id, row.tag+" plum", row.owner, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			base := consolidationBase(d)
			base.embedAvailable = true
			base.embedFn = func(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
			base.searchFn = func(string, []float32, int) ([]SearchResult, error) {
				var rows []SearchResult
				for _, id := range []string{"a", "b", "c", "d", "f1", "f2", "s1", "s2"} {
					rows = append(rows, SearchResult{ID: id, Score: 0.92})
				}
				return rows, nil
			}
			p := &consolidationScopeSummarizer{}
			base.llmProvider = p
			if err := NewConsolidationRunner(d, 0).RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if len(p.prompts) != 3 {
				t.Fatalf("owner namespace provider groups=%d want3", len(p.prompts))
			}
			for _, prompt := range p.prompts {
				count := 0
				for _, tag := range []string{"private", "foreign", "shared"} {
					if strings.Contains(prompt, tag) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("mixed provider source: %s", prompt)
				}
			}
		})
	}
}

func TestConsolidationProviderRevocationAndExpiry(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, kind := range []string{"revoked", "expired-during-call"} {
				t.Run(kind, func(t *testing.T) {
					d, ctx := consolidationLifecycleFixture(t, pg)
					base := consolidationBase(d)
					base.embedAvailable = true
					s := &sqlStore{deps: d, collection: "levara"}
					if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
						t.Fatal(err)
					}
					calls := 0
					if kind == "revoked" {
						if _, err := d.DB().Exec(`UPDATE users SET is_active=FALSE WHERE id='owner-a'`); err != nil {
							t.Fatal(err)
						}
					} else {
						d.actor.Credential.ExpiresAt = time.Now().Unix() + 2
					}
					base.embedFn = func(context.Context, string) ([]float32, error) {
						calls++
						if kind == "expired-during-call" {
							for time.Now().Unix() < d.actor.Credential.ExpiresAt {
								time.Sleep(10 * time.Millisecond)
							}
						}
						return []float32{1, 0}, nil
					}
					n := &collectionNeighbors{deps: d, collection: "_memories_levara", store: s}
					recs := []consolidate.MemoryRecord{{ID: "a", Key: "a", Value: "private a"}, {ID: "b", Key: "b", Value: "private b"}}
					if _, err := n.Edges(ctx, recs, consolidate.DefaultConfig()); err == nil {
						t.Fatal("provider authority failure masked")
					}
					want := 0
					if kind == "expired-during-call" {
						want = 1
					}
					if calls != want {
						t.Fatalf("provider calls=%d want%d", calls, want)
					}
				})
			}
		})
	}
}
