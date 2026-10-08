package memoryhall

import (
	"reflect"
	"testing"
)

func TestHallVocabularyAndDefensiveCopy(t *testing.T) {
	want := []string{"fact", "event", "decision", "preference", "advice", "discovery"}
	got := ValidHalls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("halls = %v, want %v", got, want)
	}
	got[0] = "unknown"
	if !reflect.DeepEqual(ValidHalls(), want) {
		t.Fatal("caller changed shared vocabulary")
	}
	for _, h := range want {
		if !IsValidHall(h) {
			t.Errorf("valid hall %q rejected", h)
		}
	}
	for _, h := range []string{"", "unknown", "semantic", "FACT", " fact", "fact "} {
		if IsValidHall(h) {
			t.Errorf("invalid hall %q accepted", h)
		}
	}
}
