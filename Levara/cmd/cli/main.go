// levara CLI — command-line interface for the Levara REST API.
//
// Usage:
//
//	levara health [--details]
//	levara add <file|url|text> [--dataset=name]
//	levara cognify [--dataset=name] [--collection=name] [--wait]
//	levara search <query> [--type=CHUNKS] [--top-k=10]
//	levara datasets list
//	levara datasets create <name>
//	levara datasets delete <id>
//	levara cache stats
//
// Configuration:
//
//	LEVARA_URL   API base URL (default http://localhost:8080/api/v1)
//	LEVARA_TOKEN Auth token (or ~/.levara/token file)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ANSI color codes.
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
)

var (
	baseURL string
	token   string
)

func main() {
	baseURL = envOr("LEVARA_URL", "http://localhost:8080/api/v1")
	token = os.Getenv("LEVARA_TOKEN")

	// Parse global flags before the subcommand.
	args := os.Args[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch {
		case strings.HasPrefix(args[0], "--url="):
			baseURL = args[0][len("--url="):]
		case strings.HasPrefix(args[0], "--token="):
			token = args[0][len("--token="):]
		default:
			fatalf("unknown global flag: %s", args[0])
		}
		args = args[1:]
	}

	// Load token from file if not set via env/flag.
	if token == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if data, err := os.ReadFile(filepath.Join(home, ".levara", "token")); err == nil {
				token = strings.TrimSpace(string(data))
			}
		}
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	args = args[1:]

	switch cmd {
	case "health":
		cmdHealth(args)
	case "add":
		cmdAdd(args)
	case "cognify":
		cmdCognify(args)
	case "search":
		cmdSearch(args)
	case "datasets":
		cmdDatasets(args)
	case "cache":
		cmdCache(args)
	case "git":
		cmdGit(args)
	case "help", "--help", "-h":
		printUsage()
	default:
		fatalf("unknown command: %s\nRun 'levara help' for usage.", cmd)
	}
}

// ── health ──────────────────────────────────────────────────────────────────

func cmdHealth(args []string) {
	details := hasFlag(args, "--details")

	var endpoint string
	if details {
		// /health/details is at app root, not under /api/v1
		endpoint = strings.TrimSuffix(baseURL, "/api/v1") + "/health/details"
	} else {
		endpoint = baseURL + "/health"
	}

	body, status := doGet(endpoint)
	if status != 200 {
		fmt.Printf("%s%sERROR%s  server returned %d\n", colorBold, colorRed, colorReset, status)
		os.Exit(1)
	}

	if !details {
		var resp map[string]any
		json.Unmarshal(body, &resp)
		health, _ := resp["health"].(string)
		version, _ := resp["version"].(string)
		color := colorGreen
		if health != "healthy" {
			color = colorRed
		}
		fmt.Printf("%s%s%s%s  %s\n", colorBold, color, strings.ToUpper(health), colorReset, version)
		return
	}

	// Detailed: parse services map and print table.
	var resp struct {
		Services map[string]map[string]any `json:"services"`
	}
	json.Unmarshal(body, &resp)

	fmt.Printf("\n%s%-20s %-14s %s%s\n", colorBold, "SERVICE", "STATUS", "DETAILS", colorReset)
	fmt.Println(strings.Repeat("─", 60))

	for name, info := range resp.Services {
		st, _ := info["status"].(string)
		color := statusColor(st)
		detail := ""
		for k, v := range info {
			if k == "status" {
				continue
			}
			detail += fmt.Sprintf("%s=%v  ", k, v)
		}
		fmt.Printf("%-20s %s%-14s%s %s%s%s\n", name, color, st, colorReset, colorDim, detail, colorReset)
	}
	fmt.Println()
}

// ── add ─────────────────────────────────────────────────────────────────────

func cmdAdd(args []string) {
	dataset := flagValue(args, "--dataset", "default")
	positional := positionalArgs(args)

	if len(positional) == 0 {
		fatalf("usage: levara add <file|url|text> [--dataset=name]")
	}
	input := positional[0]

	// Determine if input is a file path.
	if fi, err := os.Stat(input); err == nil && !fi.IsDir() {
		addFile(input, dataset)
		return
	}

	// URL or plain text — send as raw body.
	addText(input, dataset)
}

func addFile(path, dataset string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("read file: %v", err)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("datasetName", dataset)
	part, err := w.CreateFormFile("data", filepath.Base(path))
	if err != nil {
		fatalf("create form file: %v", err)
	}
	part.Write(data)
	w.Close()

	req, _ := http.NewRequest("POST", baseURL+"/add", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	applyAuth(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		fatalf("server error %d: %s", resp.StatusCode, body)
	}

	var result map[string]any
	json.Unmarshal(body, &result)
	items, _ := result["items"].(float64)
	dsName, _ := result["dataset_name"].(string)
	dsID, _ := result["dataset_id"].(string)
	fmt.Printf("%s%sOK%s  ingested %s → %d item(s) into dataset %q (%s)\n",
		colorBold, colorGreen, colorReset, filepath.Base(path), int(items), dsName, dsID[:8])
}

func addText(text, dataset string) {
	// Send as raw body with datasetName query param.
	endpoint := fmt.Sprintf("%s/add?datasetName=%s", baseURL, dataset)
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader(text))
	req.Header.Set("Content-Type", "text/plain")
	applyAuth(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		fatalf("server error %d: %s", resp.StatusCode, body)
	}

	var result map[string]any
	json.Unmarshal(body, &result)
	items, _ := result["items"].(float64)
	dsName, _ := result["dataset_name"].(string)
	dsID, _ := result["dataset_id"].(string)

	label := "text"
	if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
		label = "url"
	}
	fmt.Printf("%s%sOK%s  ingested %s → %d item(s) into dataset %q (%s)\n",
		colorBold, colorGreen, colorReset, label, int(items), dsName, dsID[:min(8, len(dsID))])
}

// ── cognify ─────────────────────────────────────────────────────────────────

func cmdCognify(args []string) {
	dataset := flagValue(args, "--dataset", "")
	collection := flagValue(args, "--collection", "")
	wait := hasFlag(args, "--wait")

	payload := map[string]any{}
	if dataset != "" {
		payload["datasets"] = []string{dataset}
	}
	if collection != "" {
		payload["collection"] = collection
	}

	body, status := doPost(baseURL+"/cognify", payload)
	if status >= 400 {
		fatalf("cognify failed (%d): %s", status, body)
	}

	var resp map[string]any
	json.Unmarshal(body, &resp)
	runID, _ := resp["pipeline_run_id"].(string)
	fmt.Printf("Pipeline started: %s\n", runID)

	if !wait {
		return
	}

	// Poll status until terminal.
	fmt.Print("Progress: ")
	lastStage := ""
	for {
		statusBody, sc := doGet(fmt.Sprintf("%s/cognify/%s/status", baseURL, runID))
		if sc != 200 {
			fmt.Printf("\n%sERROR%s  status check failed: %d\n", colorRed, colorReset, sc)
			os.Exit(1)
		}

		var run map[string]any
		json.Unmarshal(statusBody, &run)
		st, _ := run["status"].(string)
		stage, _ := run["stage"].(string)
		elapsed, _ := run["elapsed_ms"].(float64)

		if stage != lastStage {
			fmt.Printf("\n  %s%-20s%s", colorCyan, stage, colorReset)
			lastStage = stage
		} else {
			fmt.Print(".")
		}

		if st == "COMPLETED" {
			chunks, _ := run["chunks_created"].(float64)
			entities, _ := run["entities_extracted"].(float64)
			edges, _ := run["edges_extracted"].(float64)
			fmt.Printf("\n\n%s%sCOMPLETED%s in %.1fs — %d chunks, %d entities, %d edges\n",
				colorBold, colorGreen, colorReset, elapsed/1000.0, int(chunks), int(entities), int(edges))
			return
		}
		if st == "FAILED" {
			msg, _ := run["message"].(string)
			fmt.Printf("\n\n%s%sFAILED%s: %s\n", colorBold, colorRed, colorReset, msg)
			os.Exit(1)
		}

		time.Sleep(1 * time.Second)
	}
}

// ── search ──────────────────────────────────────────────────────────────────

func cmdSearch(args []string) {
	queryType := flagValue(args, "--type", "CHUNKS")
	topK := flagValue(args, "--top-k", "10")
	positional := positionalArgs(args)

	if len(positional) == 0 {
		fatalf("usage: levara search <query> [--type=CHUNKS] [--top-k=10]")
	}
	query := strings.Join(positional, " ")

	payload := map[string]any{
		"query_text": query,
		"query_type": queryType,
		"top_k":      jsonNumber(topK),
	}

	body, status := doPost(baseURL+"/search/text", payload)
	if status >= 400 {
		fatalf("search failed (%d): %s", status, body)
	}

	// Results may be an array or an object with "chunks" key (RAG_COMPLETION).
	var results []json.RawMessage
	if err := json.Unmarshal(body, &results); err != nil {
		// Try object format.
		var obj map[string]json.RawMessage
		if err2 := json.Unmarshal(body, &obj); err2 == nil {
			if chunks, ok := obj["chunks"]; ok {
				json.Unmarshal(chunks, &results)
			}
			if answer, ok := obj["answer"]; ok {
				var ans string
				json.Unmarshal(answer, &ans)
				if ans != "" {
					fmt.Printf("\n%s%sAnswer:%s %s\n\n", colorBold, colorCyan, colorReset, ans)
				}
			}
		}
	}

	if len(results) == 0 {
		fmt.Println("No results.")
		return
	}

	// Pretty-print results table.
	fmt.Printf("\n%s%-4s %-8s %-16s %s%s\n", colorBold, "#", "SCORE", "COLLECTION", "METADATA", colorReset)
	fmt.Println(strings.Repeat("─", 80))

	for i, raw := range results {
		var r map[string]any
		json.Unmarshal(raw, &r)

		score := "—"
		if s, ok := r["score"].(float64); ok {
			score = fmt.Sprintf("%.4f", s)
		} else if s, ok := r["fused_score"].(float64); ok {
			score = fmt.Sprintf("%.4f", s)
		}

		coll, _ := r["collection"].(string)
		if coll == "" {
			coll = "—"
		}

		meta := extractMetaText(r)
		if len(meta) > 120 {
			meta = meta[:117] + "..."
		}

		fmt.Printf("%-4d %-8s %-16s %s\n", i+1, score, coll, meta)
	}
	fmt.Printf("\n%s%d result(s)%s\n\n", colorDim, len(results), colorReset)
}

// extractMetaText pulls readable text from a result's metadata.
func extractMetaText(r map[string]any) string {
	meta := r["metadata"]
	if meta == nil {
		id, _ := r["id"].(string)
		return id
	}

	// Try to parse metadata JSON.
	var m map[string]any
	switch v := meta.(type) {
	case string:
		json.Unmarshal([]byte(v), &m)
	case map[string]any:
		m = v
	default:
		raw, _ := json.Marshal(v)
		json.Unmarshal(raw, &m)
	}

	// Prefer text > chunk_text > description > name.
	for _, key := range []string{"text", "chunk_text", "description", "name", "content"} {
		if s, ok := m[key].(string); ok && s != "" {
			return s
		}
	}

	// Fallback: compact JSON.
	b, _ := json.Marshal(m)
	return string(b)
}

// ── datasets ────────────────────────────────────────────────────────────────

func cmdDatasets(args []string) {
	if len(args) == 0 {
		args = []string{"list"}
	}

	sub := args[0]
	args = args[1:]

	switch sub {
	case "list":
		datasetsListCmd()
	case "create":
		if len(args) == 0 {
			fatalf("usage: levara datasets create <name>")
		}
		datasetsCreateCmd(args[0])
	case "delete":
		if len(args) == 0 {
			fatalf("usage: levara datasets delete <id>")
		}
		datasetsDeleteCmd(args[0])
	default:
		fatalf("unknown datasets subcommand: %s\nUsage: levara datasets [list|create <name>|delete <id>]", sub)
	}
}

func datasetsListCmd() {
	body, status := doGet(baseURL + "/datasets")
	if status >= 400 {
		fatalf("datasets list failed (%d): %s", status, body)
	}

	var datasets []map[string]any
	json.Unmarshal(body, &datasets)

	if len(datasets) == 0 {
		fmt.Println("No datasets.")
		return
	}

	fmt.Printf("\n%s%-38s %-24s %-24s%s\n", colorBold, "ID", "NAME", "CREATED", colorReset)
	fmt.Println(strings.Repeat("─", 88))

	for _, d := range datasets {
		id, _ := d["id"].(string)
		name, _ := d["name"].(string)
		created, _ := d["created_at"].(string)
		fmt.Printf("%-38s %-24s %-24s\n", id, name, created)
	}
	fmt.Printf("\n%s%d dataset(s)%s\n\n", colorDim, len(datasets), colorReset)
}

func datasetsCreateCmd(name string) {
	payload := map[string]any{"name": name}
	body, status := doPost(baseURL+"/datasets", payload)
	if status >= 400 {
		fatalf("create failed (%d): %s", status, body)
	}

	var d map[string]any
	json.Unmarshal(body, &d)
	id, _ := d["id"].(string)
	fmt.Printf("%s%sOK%s  created dataset %q (%s)\n", colorBold, colorGreen, colorReset, name, id)
}

func datasetsDeleteCmd(id string) {
	req, _ := http.NewRequest("DELETE", baseURL+"/datasets/"+id, nil)
	applyAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fatalf("delete failed (%d): %s", resp.StatusCode, body)
	}

	fmt.Printf("%s%sOK%s  deleted dataset %s\n", colorBold, colorGreen, colorReset, id)
}

// ── cache ───────────────────────────────────────────────────────────────────

func cmdCache(args []string) {
	if len(args) == 0 || args[0] != "stats" {
		fatalf("usage: levara cache stats")
	}

	body, status := doGet(baseURL + "/cache/stats")
	if status >= 400 {
		fatalf("cache stats failed (%d): %s", status, body)
	}

	var stats map[string]any
	json.Unmarshal(body, &stats)

	fmt.Printf("\n%sLLM Cache Stats%s\n", colorBold, colorReset)
	fmt.Println(strings.Repeat("─", 40))
	for k, v := range stats {
		fmt.Printf("  %-20s %v\n", k, v)
	}
	fmt.Println()
}

// ── git ─────────────────────────────────────────────────────────────────────

func cmdGit(args []string) {
	if len(args) == 0 {
		fatalf("usage: levara git [analyze|search] ...")
	}

	sub := args[0]
	args = args[1:]

	switch sub {
	case "analyze":
		cmdGitAnalyze(args)
	case "search":
		cmdGitSearch(args)
	default:
		fatalf("unknown git subcommand: %s\nUsage: levara git [analyze|search]", sub)
	}
}

func cmdGitAnalyze(args []string) {
	repo := flagValue(args, "--repo", ".")
	since := flagValue(args, "--since", "")
	limit := flagValue(args, "--limit", "100")

	payload := map[string]any{
		"name":      "analyze_commits",
		"arguments": map[string]any{
			"repo_path": repo,
			"since":     since,
			"limit":     jsonNumber(limit),
		},
	}

	// Call MCP tools/call endpoint
	mcpURL := strings.TrimSuffix(baseURL, "/api/v1") + "/mcp"
	rpcPayload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  payload,
	}

	data, _ := json.Marshal(rpcPayload)
	req, _ := http.NewRequest("POST", mcpURL, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("connection failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(body, &result)

	if errObj, ok := result["error"]; ok {
		fatalf("MCP error: %v", errObj)
	}

	// Extract text from result.content[0].text
	if res, ok := result["result"].(map[string]any); ok {
		if content, ok := res["content"].([]any); ok && len(content) > 0 {
			if item, ok := content[0].(map[string]any); ok {
				text, _ := item["text"].(string)
				fmt.Println(text)
				return
			}
		}
	}

	fmt.Printf("%s\n", body)
}

func cmdGitSearch(args []string) {
	positional := positionalArgs(args)
	if len(positional) == 0 {
		fatalf("usage: levara git search <query>")
	}
	query := strings.Join(positional, " ")

	payload := map[string]any{
		"name":      "git_search",
		"arguments": map[string]any{
			"query": query,
		},
	}

	mcpURL := strings.TrimSuffix(baseURL, "/api/v1") + "/mcp"
	rpcPayload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  payload,
	}

	data, _ := json.Marshal(rpcPayload)
	req, _ := http.NewRequest("POST", mcpURL, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	applyAuth(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("connection failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result map[string]any
	json.Unmarshal(body, &result)

	if errObj, ok := result["error"]; ok {
		fatalf("MCP error: %v", errObj)
	}

	if res, ok := result["result"].(map[string]any); ok {
		if content, ok := res["content"].([]any); ok && len(content) > 0 {
			if item, ok := content[0].(map[string]any); ok {
				text, _ := item["text"].(string)
				fmt.Println(text)
				return
			}
		}
	}

	fmt.Printf("%s\n", body)
}

// ── HTTP helpers ────────────────────────────────────────────────────────────

func doGet(url string) ([]byte, int) {
	req, _ := http.NewRequest("GET", url, nil)
	applyAuth(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("connection failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

func doPost(url string, payload map[string]any) ([]byte, int) {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
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

func applyAuth(req *http.Request) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// ── flag/arg helpers ────────────────────────────────────────────────────────

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// hasFlag checks if a boolean flag like "--wait" or "--details" is present.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// flagValue extracts a --key=value flag, returning def if not found.
func flagValue(args []string, prefix, def string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix+"=") {
			return a[len(prefix)+1:]
		}
	}
	return def
}

// positionalArgs returns args that are not flags (don't start with --).
func positionalArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			out = append(out, a)
		}
	}
	return out
}

// jsonNumber parses a string to a JSON number (int).
func jsonNumber(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n <= 0 {
		n = 10
	}
	return n
}

func statusColor(s string) string {
	switch s {
	case "connected", "ready", "listening", "healthy":
		return colorGreen
	case "unreachable", "error":
		return colorRed
	default:
		return colorYellow
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, colorRed+"error: "+colorReset+format+"\n", args...)
	os.Exit(1)
}

func printUsage() {
	fmt.Print(`Usage: levara <command> [flags]

Commands:
  health   [--details]                       Server health check
  add      <file|url|text> [--dataset=name]  Ingest data
  cognify  [--dataset=name] [--collection=name] [--wait]  Run cognify pipeline
  search   <query> [--type=CHUNKS] [--top-k=10]           Semantic search
  datasets [list|create <name>|delete <id>]  Manage datasets
  cache    stats                             LLM cache statistics
  git      analyze [--repo=.] [--since=...] [--limit=100]  Analyze git commits
  git      search <query>                    Search analyzed commits

Global flags:
  --url=<url>      API base URL (default: $LEVARA_URL or http://localhost:8080/api/v1)
  --token=<token>  Auth token (default: $LEVARA_TOKEN or ~/.levara/token)

Search types:
  CHUNKS, GRAPH_COMPLETION, RAG_COMPLETION, SUMMARIES,
  CHUNKS_LEXICAL, HYBRID, TEMPORAL, NATURAL_LANGUAGE, CYPHER
`)
}
