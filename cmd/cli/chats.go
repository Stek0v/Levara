package main

// chats.go — `levara chats` subcommand: import local agent transcripts
// (codex first) into the server's chat_import store and inspect the result.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/chatimport"
)

func cmdChats(args []string) {
	if len(args) == 0 {
		fatalf("usage: levara chats <import|runs|session> ...")
	}
	switch args[0] {
	case "import":
		cmdChatsImport(args[1:])
	case "runs":
		cmdChatsRuns(args[1:])
	case "session":
		cmdChatsSession(args[1:])
	default:
		fatalf("unknown chats command: %s (import|runs|session)", args[0])
	}
}

func chatsFlag(args []string, name string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, name+"=") {
			return a[len(name)+1:], true
		}
	}
	return "", false
}

// cmdChatsImport parses transcripts locally and POSTs each conversation to
// /chats/import. Run ids are derived from the file sha256, so re-running the
// import over the same files is a no-op on the server.
func cmdChatsImport(args []string) {
	platform, _ := chatsFlag(args, "--platform")
	path, _ := chatsFlag(args, "--path")
	if platform != "codex" {
		fatalf("--platform=codex is the only supported source (claude-code lands in E2c)")
	}
	if path == "" {
		fatalf("--path=<file-or-dir> required")
	}
	noReasoning := hasFlag(args, "--no-reasoning")
	dryRun := hasFlag(args, "--dry-run")

	files, err := collectTranscriptFiles(path)
	if err != nil {
		fatalf("%v", err)
	}
	if len(files) == 0 {
		fatalf("no .jsonl transcripts under %s", path)
	}
	sort.Strings(files)

	opts := chatimport.DefaultParseOptions()
	opts.IncludeReasoning = !noReasoning

	totalInserted, totalMessages, totalSkipped := 0, 0, 0
	failures := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("%sFAIL%s   %s %v\n", colorRed, colorReset, filepath.Base(f), err)
			failures++
			continue
		}
		conv, stats, err := chatimport.ParseCodexRollout(bytes.NewReader(raw), opts)
		if err != nil {
			fmt.Printf("%sFAIL%s   %s %v\n", colorRed, colorReset, filepath.Base(f), err)
			failures++
			continue
		}
		totalMessages += stats.Messages
		totalSkipped += stats.Skipped

		if dryRun {
			fmt.Printf("%sDRY%s    %s  session=%s messages=%d skipped=%d\n",
				colorCyan, colorReset, filepath.Base(f), shortID(conv.SessionID), stats.Messages, stats.Skipped)
			continue
		}

		sum := sha256.Sum256(raw)
		payload := map[string]any{
			"run_id":        uuid.NewSHA1(uuid.NameSpaceURL, []byte("codex/"+hex.EncodeToString(sum[:]))).String(),
			"source_path":   f,
			"source_sha256": hex.EncodeToString(sum[:]),
			"skipped":       stats.Skipped,
			"finish":        true,
			"conversation":  conv,
		}
		body, status := postJSON(baseURL+"/chats/import", payload)
		if status != http.StatusCreated {
			fmt.Printf("%sFAIL%s   %s HTTP %d %s\n", colorRed, colorReset, filepath.Base(f), status, truncateFor(string(body), 120))
			failures++
			continue
		}
		var resp struct {
			Inserted    int      `json:"inserted"`
			WarnedCount int      `json:"warned_count"`
			Warnings    []string `json:"warnings"`
		}
		_ = json.Unmarshal(body, &resp)
		totalInserted += resp.Inserted
		marker := colorGreen + "OK  " + colorReset
		if resp.Inserted == 0 {
			marker = colorDim + "SKIP" + colorReset // already imported
		}
		fmt.Printf("%s%s session=%s inserted=%d msgs=%d skipped=%d warnings=%d\n",
			marker, filepath.Base(f), shortID(conv.SessionID), resp.Inserted, stats.Messages, stats.Skipped, resp.WarnedCount)
		for _, w := range resp.Warnings {
			fmt.Printf("        %s⚠%s %s\n", colorYellow, colorReset, w)
		}
	}
	fmt.Printf("\nfiles=%d inserted=%d messages=%d skipped=%d failures=%d\n",
		len(files), totalInserted, totalMessages, totalSkipped, failures)
	if failures > 0 {
		os.Exit(1)
	}
}

func cmdChatsRuns(args []string) {
	endpoint := baseURL + "/chats/import/runs"
	if p, ok := chatsFlag(args, "--platform"); ok {
		endpoint += "?platform=" + p
	}
	body, status := doGet(endpoint)
	if status != 200 {
		fatalf("HTTP %d: %s", status, truncateFor(string(body), 200))
	}
	var resp struct {
		Runs []struct {
			ID            string `json:"id"`
			Platform      string `json:"platform"`
			Status        string `json:"status"`
			ImportedCount int    `json:"imported_count"`
			SkippedCount  int    `json:"skipped_count"`
			WarnedCount   int    `json:"warned_count"`
			StartedAt     string `json:"started_at"`
			SourcePath    string `json:"source_path"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fatalf("bad response: %v", err)
	}
	if len(resp.Runs) == 0 {
		fmt.Println("no import runs")
		return
	}
	for _, r := range resp.Runs {
		fmt.Printf("%s %-6s %-7s inserted=%-5d skipped=%-4d warned=%-3d %s\n  %s\n",
			shortID(r.ID), r.Platform, r.Status, r.ImportedCount, r.SkippedCount, r.WarnedCount, r.StartedAt, r.SourcePath)
	}
}

func cmdChatsSession(args []string) {
	if len(args) < 2 {
		fatalf("usage: levara chats session <platform> <session-id> [--kind=...] [--full]")
	}
	platform, sessionID := args[0], args[1]
	endpoint := fmt.Sprintf("%s/chats/import/sessions/%s/%s", baseURL, platform, sessionID)
	body, status := doGet(endpoint)
	if status != 200 {
		fatalf("HTTP %d: %s", status, truncateFor(string(body), 200))
	}
	var resp struct {
		Messages []struct {
			Ordinal   int    `json:"ordinal"`
			Role      string `json:"role"`
			Kind      string `json:"kind"`
			Model     string `json:"model"`
			Content   string `json:"content"`
			CreatedAt string `json:"created_at"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fatalf("bad response: %v", err)
	}
	kindFilter, _ := chatsFlag(args, "--kind")
	full := hasFlag(args, "--full")
	for _, m := range resp.Messages {
		if kindFilter != "" && m.Kind != kindFilter {
			continue
		}
		content := m.Content
		if !full {
			content = truncateFor(strings.ReplaceAll(content, "\n", " ⏎ "), 160)
		}
		fmt.Printf("[%3d] %-9s %-10s %s\n      %s\n", m.Ordinal, m.Role, m.Kind, m.CreatedAt, content)
	}
	fmt.Printf("\n%d messages\n", len(resp.Messages))
}

func collectTranscriptFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.Walk(path, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// postJSON is doPost for typed payloads (the shared helper only takes maps).
func postJSON(endpoint string, payload any) ([]byte, int) {
	data, err := json.Marshal(payload)
	if err != nil {
		fatalf("marshal payload: %v", err)
	}
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("connection failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}

func truncateFor(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
