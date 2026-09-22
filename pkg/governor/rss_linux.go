//go:build linux

package governor

import (
	"os"
	"strconv"
	"strings"
)

// readOSRSS reads /proc/self/statm: field 2 is the resident page count.
func readOSRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
