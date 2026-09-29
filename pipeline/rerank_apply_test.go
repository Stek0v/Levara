package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/rerank"
)

func TestApplyRerankToScoredRequiresValidIndex(t *testing.T) {
	in := []ScoredResult{
		{ID: "no-text", Score: .9},
		{ID: "a", Score: .8, Metadata: json.RawMessage(`{"text":"first document"}`)},
		{ID: "b", Score: .7, Metadata: json.RawMessage(`{"text":"second document"}`)},
	}
	for _, tc := range []struct {
		name    string
		indices []int
		ran     bool
	}{
		{name: "negative", indices: []int{-1}},
		{name: "past_text_candidates", indices: []int{2}},
		{name: "all_invalid", indices: []int{-1, 2, 99}},
		{name: "one_valid", indices: []int{1}, ran: true},
		{name: "partial_valid", indices: []int{-1, 1, 2}, ran: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				results := make([]rerank.Result, len(tc.indices))
				for i, index := range tc.indices {
					results[i] = rerank.Result{Index: index, Score: float64(len(tc.indices) - i)}
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"results": results}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			okBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
			errorBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("error"))
			ran, got := ApplyRerankToScored(context.Background(), ApplyRerankConfig{},
				rerank.NewClient(server.URL, "test", 0, 1000), "query", in, 2)
			want := in[:2]
			wantOK, wantError := 0.0, 1.0
			if tc.ran {
				want = []ScoredResult{in[2], in[0]}
				wantOK, wantError = 1, 0
			}
			if ran != tc.ran || !reflect.DeepEqual(got, want) {
				t.Errorf("reranked=%v, results=%v; want reranked=%v, results=%v", ran, got, tc.ran, want)
			}
			if delta := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok")) - okBefore; delta != wantOK {
				t.Errorf("ok counter delta=%v; want %v", delta, wantOK)
			}
			if delta := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("error")) - errorBefore; delta != wantError {
				t.Errorf("error counter delta=%v; want %v", delta, wantError)
			}
		})
	}
}
