package main

import (
	"testing"
	"time"
)

func TestMemoryIndexWorkerCount(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		want        int
	}{
		{name: "default", want: 2},
		{name: "override", value: "1", want: 1},
		{name: "invalid falls back", value: "invalid", want: 2},
		{name: "non-positive falls back", value: "0", want: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LEVARA_MEMORY_INDEX_WORKERS", tt.value)
			if got := memoryIndexWorkerCount(); got != tt.want {
				t.Fatalf("memoryIndexWorkerCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

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
