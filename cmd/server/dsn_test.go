package main

import (
	"strings"
	"testing"
)

func TestResolveRuntimeDSN(t *testing.T) {
	cases := []struct {
		name               string
		flag, dburl, pgdsn string
		want               string
		wantErr            bool
	}{
		{"empty", "", "", "", "", false},
		{"flag wins silently only when others empty", "pg://a", "", "", "pg://a", false},
		{"env fallback", "", "pg://b", "", "pg://b", false},
		{"preset fallback", "", "", "pg://c", "pg://c", false},
		{"flag+env agree", "pg://a", "pg://a", "", "pg://a", false},
		{"flag vs preset diverge", "pg://a", "", "pg://c", "", true},
		{"all agree", "pg://a", "pg://a", "pg://a", "pg://a", false},
		{"flag vs env diverge", "pg://a", "pg://b", "", "", true},
		{"env vs preset diverge", "", "pg://b", "pg://c", "", true},
		{"all diverge", "pg://a", "pg://b", "pg://c", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRuntimeDSN(tc.flag, tc.dburl, tc.pgdsn)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRuntimeDSNErrorMessageNamesSources(t *testing.T) {
	_, err := resolveRuntimeDSN("", "postgres://one", "postgres://two")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"DATABASE_URL", "POSTGRES_DSN", "differ"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}
