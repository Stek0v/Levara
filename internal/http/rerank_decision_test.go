package http

import "testing"

func TestRerankResponseDecision(t *testing.T) {
	tru, fal := true, false
	cases := []struct {
		flag     *bool
		reranked bool
		want     string
	}{{&tru, true, "forced"}, {&tru, false, "forced"}, {&fal, false, "disabled"}, {nil, true, "adaptive_run"}, {nil, false, "adaptive_skip"}}
	for _, tc := range cases {
		got, _ := rerankResponseDecision(tc.flag, tc.reranked)
		if got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
}
