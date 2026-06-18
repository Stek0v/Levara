//go:build !race

package http

func raceDetectorEnabled() bool {
	return false
}
