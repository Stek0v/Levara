package mcp

import "testing"

func TestValidHallsDefensiveCopy(t *testing.T) {
	halls := ValidHalls()
	original := halls[0]
	defer func() { halls[0] = original }()
	halls[0] = "unknown"
	if ValidHalls()[0] != "fact" || !IsValidHall("fact") || IsValidHall("unknown") {
		t.Fatal("caller mutation changed the shared hall vocabulary")
	}
}
