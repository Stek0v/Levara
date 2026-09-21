// status.go — `levara status` CLI: operational snapshot in the terminal.
//
// Renders the /api/v1/status endpoint with box-drawing characters.
// `--watch` refreshes every 5 seconds (Ctrl+C to stop).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type cliStatus struct {
	Server struct {
		Version    string `json:"version"`
		Standalone bool   `json:"standalone"`
	} `json:"server"`
	Memory struct {
		RSSBytes  uint64 `json:"rss_bytes"`
		HeapAlloc uint64 `json:"heap_alloc"`
		NumGC     uint32 `json:"num_gc"`
		Pressure  string `json:"pressure"`
	} `json:"memory"`
	Corpus []struct {
		Name         string `json:"name"`
		Records      int64  `json:"records"`
		Dim          int    `json:"dim"`
		IndexingTier string `json:"indexing_tier"`
	} `json:"corpus"`
	Jobs struct {
		SourcesDaemon []struct {
			Platform    string `json:"platform"`
			LastScanAgo string `json:"last_scan_ago"`
			Scans       int    `json:"scans"`
			LastError   string `json:"last_error,omitempty"`
		} `json:"sources_daemon"`
		CognifyPending int `json:"cognify_pending"`
		RagJanitor     struct {
			AtCurrent     int `json:"at_current"`
			TotalSessions int `json:"total_sessions"`
		} `json:"rag_janitor"`
		Distill struct {
			Done    int `json:"done"`
			Failed  int `json:"failed"`
			Pending int `json:"pending"`
		} `json:"distill"`
	} `json:"jobs"`
	Warnings []string `json:"warnings"`
}

func cmdStatus(args []string) {
	watch := hasFlag(args, "--watch")
	for {
		printStatus()
		if !watch {
			return
		}
		time.Sleep(5 * time.Second)
		fmt.Print("\033[2J\033[H") // clear screen
	}
}

func printStatus() {
	body, code := doGet(baseURL + "/status")
	if code != 200 {
		fmt.Printf("%sERROR%s  server returned %d\n", colorRed, colorReset, code)
		os.Exit(1)
	}
	var s cliStatus
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Printf("%sERROR%s  bad response: %v\n", colorRed, colorReset, err)
		os.Exit(1)
	}

	// Header
	fmt.Printf("%s╭─ Levara Status ─────────────────────────────╮%s\n", colorCyan, colorReset)

	// Server
	fmt.Printf("│ %-44s │\n", fmt.Sprintf("version: %s", s.Server.Version))

	// Memory
	rssGB := float64(s.Memory.RSSBytes) / 1e9
	heapGB := float64(s.Memory.HeapAlloc) / 1e9
	pressColor := colorGreen
	switch s.Memory.Pressure {
	case "elevated":
		pressColor = colorYellow
	case "critical":
		pressColor = colorRed
	}
	fmt.Printf("│ %-44s │\n", fmt.Sprintf("memory: %.1f GB RSS (%s%s%s) heap %.1f GB gc=%d",
		rssGB, pressColor, s.Memory.Pressure, colorReset, heapGB, s.Memory.NumGC))

	// Corpus
	if len(s.Corpus) > 0 {
		fmt.Printf("%s├─ Collections ──────────────────────────────┤%s\n", colorCyan, colorReset)
		for _, c := range s.Corpus {
			tierColor := colorGreen
			if c.IndexingTier != "full" {
				tierColor = colorYellow
			}
			fmt.Printf("│ %-44s │\n", fmt.Sprintf("  %s: %s records (%s%s%s)",
				c.Name, humanCount(c.Records), tierColor, c.IndexingTier, colorReset))
		}
	}

	// Jobs
	fmt.Printf("%s├─ Background Jobs ──────────────────────────┤%s\n", colorCyan, colorReset)
	if s.Jobs.CognifyPending > 0 {
		fmt.Printf("│ %-44s │\n", fmt.Sprintf("  cognify: %s%d pending%s",
			colorYellow, s.Jobs.CognifyPending, colorReset))
	} else {
		fmt.Printf("│ %-44s │\n", fmt.Sprintf("  cognify: %sidle%s", colorGreen, colorReset))
	}
	if s.Jobs.RagJanitor.TotalSessions > 0 {
		pct := 100 * s.Jobs.RagJanitor.AtCurrent / s.Jobs.RagJanitor.TotalSessions
		fmt.Printf("│ %-44s │\n", fmt.Sprintf("  rag v2: %s%d/%d (%d%%)%s",
			colorGreen, s.Jobs.RagJanitor.AtCurrent, s.Jobs.RagJanitor.TotalSessions, pct, colorReset))
	}
	if s.Jobs.Distill.Done+s.Jobs.Distill.Pending > 0 {
		fmt.Printf("│ %-44s │\n", fmt.Sprintf("  distill: %d done, %d failed, %d pending",
			s.Jobs.Distill.Done, s.Jobs.Distill.Failed, s.Jobs.Distill.Pending))
	}
	for _, src := range s.Jobs.SourcesDaemon {
		status := fmt.Sprintf("%s✓%s", colorGreen, colorReset)
		if src.LastError != "" {
			status = fmt.Sprintf("%s✗ %s%s", colorRed, src.LastError, colorReset)
		}
		fmt.Printf("│ %-44s │\n", fmt.Sprintf("  src %s: %s scan %s",
			src.Platform, status, src.LastScanAgo))
	}

	// Warnings
	if len(s.Warnings) > 0 {
		fmt.Printf("%s├─ Warnings ─────────────────────────────────┤%s\n", colorYellow, colorReset)
		for _, w := range s.Warnings {
			w = truncateFor(w, 42)
			fmt.Printf("│ %s⚠%s %-40s │\n", colorYellow, colorReset, w)
		}
	}

	fmt.Printf("%s╰─────────────────────────────────────────────╯%s\n", colorCyan, colorReset)
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// watchInterrupt installs a clean Ctrl+C handler for --watch mode.
func watchInterrupt() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Printf("\n%sstopped%s\n", colorDim, colorReset)
		os.Exit(0)
	}()
	// suppress default handler
	signal.Ignore(os.Interrupt)
}
