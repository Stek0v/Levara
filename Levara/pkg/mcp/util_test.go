package mcp

import (
	"strings"
	"testing"
)

func TestRandomHex_Length(t *testing.T) {
	for _, n := range []int{1, 4, 8, 16, 32, 63, 64} {
		got := RandomHex(n)
		if len(got) != n {
			t.Errorf("RandomHex(%d) = %q (len %d), want length %d", n, got, len(got), n)
		}
		// Hex characters only
		for _, c := range got {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("RandomHex(%d) = %q contains non-hex char %q", n, got, c)
				break
			}
		}
	}
}

func TestRandomHex_Unguessable(t *testing.T) {
	// 100 calls should produce 100 distinct values for any reasonable n.
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		seen[RandomHex(16)] = struct{}{}
	}
	if len(seen) != 100 {
		t.Errorf("got %d unique values out of 100 — RNG too weak?", len(seen))
	}
}

func TestDiaryOwner(t *testing.T) {
	cases := map[string]string{
		"reviewer":     "agent:reviewer",
		"  reviewer  ": "agent:reviewer", // trims whitespace
		"":             "agent:",
		"explore":      "agent:explore",
	}
	for input, want := range cases {
		if got := DiaryOwner(input); got != want {
			t.Errorf("DiaryOwner(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDiaryOwnerPrefix_StableConst(t *testing.T) {
	// External consumers (search filters, dashboards) parse owner_id by
	// looking for the "agent:" prefix. Changing it would break them silently.
	if !strings.HasPrefix(DiaryOwner("x"), DiaryOwnerPrefix) {
		t.Errorf("DiaryOwner output doesn't start with DiaryOwnerPrefix")
	}
	if DiaryOwnerPrefix != "agent:" {
		t.Errorf("DiaryOwnerPrefix changed to %q — external consumers may break", DiaryOwnerPrefix)
	}
}
