//go:build darwin

package governor

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var rssCache struct {
	mu    sync.Mutex
	at    time.Time
	value uint64
}

// readOSRSS asks /bin/ps for the live resident size. The kern.proc sysctl
// does not expose current RSS on macOS (kinfo_proc.Eproc.Xrssize is always
// 0), and getrusage reports only the historical peak — one transient spike
// would read as permanent pressure. ps is the one accurate source without
// cgo; a short cache keeps the governor's per-tick checks to one exec.
func readOSRSS() uint64 {
	rssCache.mu.Lock()
	defer rssCache.mu.Unlock()
	if rssCache.value > 0 && time.Since(rssCache.at) < 250*time.Millisecond {
		return rssCache.value
	}
	out, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return rssCache.value // stale value beats the Sys over-approximation
	}
	kb, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || kb == 0 {
		return rssCache.value
	}
	rssCache.value = kb * 1024
	rssCache.at = time.Now()
	return rssCache.value
}
