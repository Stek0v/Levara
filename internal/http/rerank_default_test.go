package http

import "testing"

// Phase 2: rerank flipped from opt-in to default-on. Lock the tri-state
// contract so a future refactor can't accidentally regress to the
// opt-in semantics.
func TestRerankWanted(t *testing.T) {
	tru := true
	fal := false
	cases := []struct {
		name     string
		flag     *bool
		endpoint string
		want     bool
	}{
		{"nil flag, endpoint set -> default on", nil, "http://r:9100/rerank", true},
		{"nil flag, no endpoint -> off", nil, "", false},
		{"explicit true, endpoint set -> on", &tru, "http://r:9100/rerank", true},
		{"explicit true, no endpoint -> off (cannot rerank)", &tru, "", false},
		{"explicit false, endpoint set -> off (opt-out wins)", &fal, "http://r:9100/rerank", false},
		{"explicit false, no endpoint -> off", &fal, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rerankWanted(tc.flag, tc.endpoint); got != tc.want {
				t.Fatalf("rerankWanted(%v, %q) = %v, want %v", tc.flag, tc.endpoint, got, tc.want)
			}
		})
	}
}
