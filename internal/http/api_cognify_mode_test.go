package http

import "testing"

func TestCognifySkipGraphFromMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		skipGraph bool
		want      bool
	}{
		{name: "explicit skip graph", skipGraph: true, want: true},
		{name: "rag mode", mode: "rag", want: true},
		{name: "rag mode case insensitive", mode: "RAG", want: true},
		{name: "graph default", mode: "", want: false},
		{name: "other mode", mode: "graph", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cognifySkipGraphFromMode(tt.mode, tt.skipGraph); got != tt.want {
				t.Fatalf("cognifySkipGraphFromMode(%q, %v) = %v, want %v", tt.mode, tt.skipGraph, got, tt.want)
			}
		})
	}
}
