package main

import "testing"

func TestShouldBootstrapNeo4jSchema(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "empty defaults enabled", in: "", want: true},
		{name: "whitespace defaults enabled", in: "   ", want: true},
		{name: "false", in: "false", want: false},
		{name: "FALSE", in: "FALSE", want: false},
		{name: "zero", in: "0", want: false},
		{name: "no", in: "no", want: false},
		{name: "off", in: "off", want: false},
		{name: "true", in: "true", want: true},
		{name: "one", in: "1", want: true},
		{name: "unexpected treated enabled", in: "maybe", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldBootstrapNeo4jSchema(tc.in)
			if got != tc.want {
				t.Fatalf("shouldBootstrapNeo4jSchema(%q)=%v want %v", tc.in, got, tc.want)
			}
		})
	}
}
