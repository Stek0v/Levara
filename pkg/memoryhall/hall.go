// Package memoryhall owns the public vocabulary used to classify memories.
package memoryhall

var hallVocab = []string{"fact", "event", "decision", "preference", "advice", "discovery"}

// ValidHalls returns a defensive copy in the stable public order.
func ValidHalls() []string { return append([]string(nil), hallVocab...) }

// IsValidHall reports exact membership. Empty or unknown legacy halls are invalid.
func IsValidHall(h string) bool {
	for _, v := range hallVocab {
		if v == h {
			return true
		}
	}
	return false
}
