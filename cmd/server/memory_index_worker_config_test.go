package main

import (
	"testing"
	"time"
)

func TestMemoryIndexWorkerInterval(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "default", want: 250 * time.Millisecond},
		{name: "duration override", value: "1s", want: time.Second},
		{name: "integer seconds", value: "2", want: 2 * time.Second},
		{name: "invalid falls back", value: "not-a-duration", want: 250 * time.Millisecond},
		{name: "non-positive falls back", value: "0", want: 250 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LEVARA_MEMORY_INDEX_WORKER_INTERVAL", tt.value)
			if got := memoryIndexWorkerInterval(); got != tt.want {
				t.Fatalf("memoryIndexWorkerInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}
