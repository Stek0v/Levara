// chat_sources.go — P2: transcript sources as a first-class core entity.
//
// Configured local transcript roots (codex / claude-code / cursor) are
// scanned on an interval; new and changed files are parsed by the same
// pure pkg/chatimport adapters and written straight into the raw layer
// (chat_import_*) — no CLI, no HTTP, message-level idempotency by design.
// When a RAG dataset is configured, each new conversation also gets its
// rendered document via the server's own /add endpoint, and a rag-mode
// cognify is triggered once per scan. Stragglers are picked up by the P1
// boot-time resume.
//
// Privacy: sources are OFF unless explicitly enabled via
// LEVARA_CHAT_SOURCES (comma list). Everything auto-ingested lands in the
// raw layer only; the RAG dataset is opt-in via LEVARA_CHAT_SOURCES_DATASET.
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/chatimport"
)

const (
	distillDefaultBudget = 2
	distillMinMessages   = 6
)

const (
	chatSourcesDefaultInterval = 5 * time.Minute
	chatSourcesAddPace         = 700 * time.Millisecond
)

// ChatSourceState is the persisted per-source operational state —
// observability, not correctness (ingest is idempotent at the message
// level, so a lost watermark costs a re-scan, never a duplicate).
type ChatSourceState struct {
	Platform      string
	RootPath      string
	Enabled       bool
	LastScanAt    string
	LastError     string
	Scans         int
	FilesImported int
}

// chatSourcesSchema is dialect-neutral by the same convention as
// pkg/chatimport.SchemaStatements.
var chatSourcesSchema = []string{
	`CREATE TABLE IF NOT EXISTS chat_import_sources (
		platform TEXT PRIMARY KEY,
		root_path TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		last_scan_at TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		scans INTEGER NOT NULL DEFAULT 0,
		files_imported INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT ''
	)`,
}

func ensureChatSourcesSchema(ctx context.Context, db *sql.DB) error {
	for _, stmt := range chatSourcesSchema {
		if _, err := db.ExecContext(ctx, Q(stmt)); err != nil {
			return fmt.Errorf("chat sources schema: %w", err)
		}
	}
	return nil
}

// chatSourceDefinition binds a platform to a filesystem root.
type chatSourceDefinition struct {
	Platform chatimport.Platform
	Root     string
}

// chatSourcesFromEnv resolves the enabled source set. Empty or "0"
// disables the daemon entirely — auto-ingest is strictly opt-in.
func chatSourcesFromEnv() ([]chatSourceDefinition, bool) {
	raw := strings.TrimSpace(os.Getenv("LEVARA_CHAT_SOURCES"))
	if raw == "" {
		return nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}
	std := map[chatimport.Platform]string{
		chatimport.PlatformCodex:      filepath.Join(home, ".codex", "sessions"),
		chatimport.PlatformClaudeCode: filepath.Join(home, ".claude", "projects"),
		chatimport.PlatformCursor:     filepath.Join(home, "Library", "Application Support", "Cursor", "User"),
	}
	overrides := map[chatimport.Platform]string{
		chatimport.PlatformCodex:      os.Getenv("LEVARA_CHAT_SOURCE_CODEX_PATH"),
		chatimport.PlatformClaudeCode: os.Getenv("LEVARA_CHAT_SOURCE_CLAUDE_PATH"),
		chatimport.PlatformCursor:     os.Getenv("LEVARA_CHAT_SOURCE_CURSOR_PATH"),
	}
	var defs []chatSourceDefinition
	for _, name := range strings.Split(raw, ",") {
		platform := chatimport.Platform(strings.TrimSpace(strings.ToLower(name)))
		root, ok := std[platform]
		if !ok {
			continue
		}
		if o := overrides[platform]; o != "" {
			root = o
		}
		defs = append(defs, chatSourceDefinition{Platform: platform, Root: root})
	}
	return defs, len(defs) > 0
}

func chatSourcesInterval() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("LEVARA_CHAT_SOURCES_INTERVAL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d >= 30*time.Second {
			return d
		}
	}
	return chatSourcesDefaultInterval
}

// fileFingerprint is the in-memory watermark: a file is re-parsed only
// when its mtime or size changed. After a restart the map is empty, so
// the first scan re-parses everything — idempotent inserts make that a
// no-op at the data level.
type fileFingerprint struct {
	modTimeUnix int64
	size        int64
}

type chatSourcesDaemon struct {
	db           *sql.DB
	loopback     string // base URL for /add and /cognify, "" disables rag sink
	dataset      string // rag dataset name, "" disables rag sink
	seen         map[string]fileFingerprint
	mu           sync.Mutex
	interval     time.Duration
	client       *http.Client
	logEveryScan bool
}

// StartChatSourcesDaemon boots the opt-in transcript ingest daemon. It
// never blocks startup and exits quietly when no sources are configured.
func StartChatSourcesDaemon(ctx context.Context, db *sql.DB, loopbackBase string) {
	defs, ok := chatSourcesFromEnv()
	if !ok || db == nil {
		return
	}
	if err := ensureChatSourcesSchema(ctx, db); err != nil {
		log.Printf("[chat-sources] schema init failed, daemon disabled: %v", err)
		return
	}
	if err := chatimport.EnsureRagSchema(ctx, db, Q); err != nil {
		log.Printf("[chat-sources] rag schema init failed: %v", err)
	}
	if autoDistillEnabled() {
		if err := chatimport.EnsureDistillSchema(ctx, db, Q); err != nil {
			log.Printf("[chat-sources] distill schema init failed: %v", err)
		} else {
			log.Printf("[chat-sources] auto-distill enabled (budget=%d/tick, halls=%s)",
				distillBudget(), distillHallsLabel())
		}
	}
	d := &chatSourcesDaemon{
		db:       db,
		loopback: loopbackBase,
		dataset:  strings.TrimSpace(os.Getenv("LEVARA_CHAT_SOURCES_DATASET")),
		seen:     make(map[string]fileFingerprint),
		interval: chatSourcesInterval(),
		client:   &http.Client{Timeout: 60 * time.Second},
	}
	log.Printf("[chat-sources] daemon enabled: %s (interval=%s, rag=%q)",
		formatSources(defs), d.interval, datasetLabel(d.dataset))
	go d.loop(ctx, defs)
}

func datasetLabel(d string) string {
	if d == "" {
		return "off"
	}
	return d
}

func formatSources(defs []chatSourceDefinition) string {
	parts := make([]string, 0, len(defs))
	for _, def := range defs {
		parts = append(parts, fmt.Sprintf("%s=%s", def.Platform, def.Root))
	}
	return strings.Join(parts, ", ")
}

func (d *chatSourcesDaemon) loop(ctx context.Context, defs []chatSourceDefinition) {
	// First scan immediately at boot, then on the interval.
	for {
		d.scanOnce(ctx, defs)
		d.refreshStaleRenders(ctx)
		d.distillNewSessions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.interval):
		}
	}
}

// refreshStaleRenders is the P3 derivative-migration janitor: sessions
// rendered with an older RenderVersion get re-rendered from the raw layer
// on a budget, so renderer upgrades roll out incrementally instead of via
// overnight rebuild scripts.
func (d *chatSourcesDaemon) refreshStaleRenders(ctx context.Context) {
	if !d.ragEnabled() {
		return
	}
	budget := ragRebuildBudget()
	stale, err := chatimport.StaleRagSessions(ctx, d.db, Q, chatimport.RenderVersion, budget)
	if err != nil {
		log.Printf("[chat-sources] rag stale query failed: %v", err)
		return
	}
	if len(stale) == 0 {
		return
	}
	refreshed := 0
	for _, ref := range stale {
		conv, err := chatimport.LoadConversation(ctx, d.db, Q, ref.Platform, ref.SessionID)
		if err != nil {
			continue
		}
		// Best-effort removal of the stale item; the re-add below uses the
		// same deterministic name either way.
		d.deleteRagItemByName(ctx, ragItemName(ref))
		d.pushRagDocument(ctx, conv)
		if err := chatimport.RecordRagVersion(ctx, d.db, Q, ref.Platform, ref.SessionID, chatimport.RenderVersion, ""); err == nil {
			refreshed++
		}
	}
	if refreshed > 0 {
		log.Printf("[chat-sources] rag janitor: refreshed %d/%d stale renders (budget %d)", refreshed, len(stale), budget)
		d.triggerCognify(ctx)
	}
}

// ragItemName is the deterministic document name for a session render.
func ragItemName(ref chatimport.RagRef) string {
	return fmt.Sprintf("chatimport-%s-%s.md", ref.Platform, ref.SessionID)
}

// ragRebuildBudget caps per-tick refreshes. Default 10 keeps the embedder
// background lane light; the full corpus rolls over in hours, not one burst.
func ragRebuildBudget() int {
	if n := envIntLocal(os.Getenv("LEVARA_RAG_REBUILD_BUDGET")); n > 0 {
		return n
	}
	return 10
}

func envIntLocal(raw string) int {
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1000 {
			return 1000
		}
	}
	return n
}

// scanOnce walks every source root, imports new/changed transcript files
// into the raw layer, pushes rag documents for genuinely new
// conversations and records per-source state for observability.
func (d *chatSourcesDaemon) scanOnce(ctx context.Context, defs []chatSourceDefinition) {
	for _, def := range defs {
		state := ChatSourceState{Platform: string(def.Platform), RootPath: def.Root, Enabled: true}
		files, err := d.changedFiles(def)
		if err != nil {
			state.LastError = err.Error()
			d.persistState(ctx, state)
			log.Printf("[chat-sources] %s scan failed: %v", def.Platform, err)
			continue
		}
		imported, ragDocs, scanErr := d.importFiles(ctx, def, files)
		state.FilesImported = imported
		state.Scans = 1
		if scanErr != nil {
			state.LastError = scanErr.Error()
			log.Printf("[chat-sources] %s imported %d files with error: %v", def.Platform, imported, scanErr)
		} else if imported > 0 || d.logEveryScan {
			log.Printf("[chat-sources] %s scan: %d changed files, %d new rag docs", def.Platform, len(files), ragDocs)
		}
		d.persistState(ctx, state)

		if ragDocs > 0 && d.ragEnabled() {
			d.triggerCognify(ctx)
		}
	}
}

func (d *chatSourcesDaemon) ragEnabled() bool {
	return d.loopback != "" && d.dataset != ""
}

// changedFiles returns files whose fingerprint differs from the cached
// one; cursor roots contribute their state.vscdb files, the transcript
// platforms their .jsonl files.
func (d *chatSourcesDaemon) changedFiles(def chatSourceDefinition) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	suffix := ".jsonl"
	if def.Platform == chatimport.PlatformCursor {
		suffix = "state.vscdb"
	}
	var changed []string
	err := filepath.Walk(def.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, next scan retries
		}
		if info.IsDir() || !strings.HasSuffix(filepath.Base(path), suffix) {
			return nil
		}
		if def.Platform == chatimport.PlatformCursor {
			// Only the global DB and per-workspace DBs; ignore -wal/-shm.
			if strings.Contains(path, "-wal") || strings.Contains(path, "-shm") {
				return nil
			}
		}
		fp := fileFingerprint{modTimeUnix: info.ModTime().Unix(), size: info.Size()}
		if prev, ok := d.seen[path]; ok && prev == fp {
			return nil
		}
		d.seen[path] = fp
		changed = append(changed, path)
		return nil
	})
	sort.Strings(changed)
	return changed, err
}

// importFiles parses and imports one platform's changed files. Cursor
// files expand to many conversations; the transcript platforms to one
// each. Returns (filesImported, ragDocs, firstError).
func (d *chatSourcesDaemon) importFiles(ctx context.Context, def chatSourceDefinition, files []string) (int, int, error) {
	opts := chatimport.DefaultParseOptions()
	imported, ragDocs := 0, 0
	var firstErr error
	runID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("chat-sources/"+string(def.Platform)+"/"+time.Now().UTC().Format("20060102T150405"))).String()
	runStarted := time.Now().UTC().Format(time.RFC3339)
	runOpen := false

	openRun := func() error {
		if runOpen {
			return nil
		}
		if err := chatimport.StartRun(ctx, d.db, Q, chatimport.RunInfo{
			ID: runID, Platform: def.Platform, SourcePath: def.Root, StartedAt: runStarted,
		}); err != nil {
			return err
		}
		runOpen = true
		return nil
	}

	ragPush := func(convs []chatimport.Conversation) {
		if !d.ragEnabled() {
			return
		}
		for i := range convs {
			if convs[i].Messages == nil {
				continue
			}
			// P3 version gate: an up-to-date render is never re-pushed —
			// grown conversations refresh only when their render version
			// is stale, not on every new message.
			if chatimport.RagVersion(ctx, d.db, Q, convs[i].Platform, convs[i].SessionID) >= chatimport.RenderVersion {
				continue
			}
			d.pushRagDocument(ctx, &convs[i])
			ragDocs++
		}
	}

	for _, path := range files {
		var conversations []chatimport.Conversation
		switch def.Platform {
		case chatimport.PlatformCodex:
			conv, _, err := parseCodexFile(path, opts)
			if err != nil {
				firstErr = recordErr(firstErr, path, err)
				continue
			}
			conversations = append(conversations, *conv)
		case chatimport.PlatformClaudeCode:
			conv, _, err := parseClaudeFile(path, opts)
			if err != nil {
				firstErr = recordErr(firstErr, path, err)
				continue
			}
			conversations = append(conversations, *conv)
		case chatimport.PlatformCursor:
			convs, _, err := chatimport.ParseCursorChats(path, opts)
			if err != nil {
				firstErr = recordErr(firstErr, path, err)
				continue
			}
			conversations = convs
		}
		insertedTotal := 0
		for i := range conversations {
			if err := openRun(); err != nil {
				firstErr = recordErr(firstErr, path, err)
				break
			}
			inserted, err := chatimport.InsertConversation(ctx, d.db, Q, runID, &conversations[i], &[]string{}, time.Now())
			if err != nil {
				firstErr = recordErr(firstErr, path, err)
				continue
			}
			insertedTotal += inserted
		}
		// Only conversations with fresh rows produce rag docs — the raw
		// layer is the idempotency oracle (CLI --force-rag is the repair
		// path for missing renders).
		if insertedTotal > 0 {
			ragPush(dedupeConversations(conversations))
		}
		imported++
	}
	if runOpen {
		_ = chatimport.FinishRun(ctx, d.db, Q, runID, "ok", 0, 0, 0, nil, time.Now().UTC().Format(time.RFC3339))
	}
	return imported, ragDocs, firstErr
}

func recordErr(first error, path string, err error) error {
	log.Printf("[chat-sources] skip %s: %v", filepath.Base(path), err)
	if first == nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return first
}

func dedupeConversations(convs []chatimport.Conversation) []chatimport.Conversation {
	seen := make(map[string]bool, len(convs))
	out := convs[:0]
	for _, c := range convs {
		if seen[c.SessionID] {
			continue
		}
		seen[c.SessionID] = true
		out = append(out, c)
	}
	return out
}

func parseCodexFile(path string, opts chatimport.ParseOptions) (*chatimport.Conversation, chatimport.ParseStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, chatimport.ParseStats{}, err
	}
	defer f.Close()
	return chatimport.ParseCodexRollout(f, opts)
}

func parseClaudeFile(path string, opts chatimport.ParseOptions) (*chatimport.Conversation, chatimport.ParseStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, chatimport.ParseStats{}, err
	}
	defer f.Close()
	return chatimport.ParseClaudeCodeTranscript(f, opts)
}

// pushRagDocument renders one conversation and uploads it through the
// server's own multipart /add with a deterministic filename — the janitor
// resolves and replaces items by that name. Paced: bulk first-scans must
// not trip the user rate bucket. Records the render version on success.
func (d *chatSourcesDaemon) pushRagDocument(ctx context.Context, conv *chatimport.Conversation) {
	name := ragItemName(chatimport.RagRef{Platform: conv.Platform, SessionID: conv.SessionID})
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("data", name)
	fw.Write([]byte(chatimport.RenderConversationMarkdown(conv)))
	w.WriteField("dataset_name", d.dataset)
	w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.loopback+"/api/v1/add", &buf)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("[chat-sources] rag push failed: %v", err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		// One cooldown per burst, not per doc: the scan resumes next tick.
		log.Printf("[chat-sources] rag push rate-limited; remaining docs deferred to next scan")
		time.Sleep(5 * time.Second)
		return
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = chatimport.RecordRagVersion(ctx, d.db, Q, conv.Platform, conv.SessionID, chatimport.RenderVersion, "")
	}
	time.Sleep(chatSourcesAddPace)
}

// deleteRagItemByName resolves the dataset item with the given name and
// deletes it (best effort — a miss is a no-op). Returns true when deleted.
func (d *chatSourcesDaemon) deleteRagItemByName(ctx context.Context, name string) bool {
	datasetID, err := d.resolveDatasetID(ctx)
	if err != nil || datasetID == "" {
		return false
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.loopback+"/api/v1/datasets/"+datasetID+"/data", nil)
	resp, err := d.client.Do(req)
	if err != nil {
		return false
	}
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&items)
	resp.Body.Close()
	for _, item := range items {
		if item.Name != name {
			continue
		}
		del, _ := http.NewRequestWithContext(ctx, http.MethodDelete, d.loopback+"/api/v1/datasets/"+datasetID+"/data/"+item.ID, nil)
		dresp, err := d.client.Do(del)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, dresp.Body)
		dresp.Body.Close()
		return dresp.StatusCode >= 200 && dresp.StatusCode < 300
	}
	return false
}

// triggerCognify starts a rag-mode cognify over the configured dataset
// without waiting — the P1 boot resume is the durable backstop.
func (d *chatSourcesDaemon) triggerCognify(ctx context.Context) {
	datasetID, err := d.resolveDatasetID(ctx)
	if err != nil || datasetID == "" {
		log.Printf("[chat-sources] cognify trigger skipped: dataset %q unresolved (%v)", d.dataset, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"datasets":   []string{datasetID},
		"collection": chatImportsCollectionName,
		"skip_graph": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.loopback+"/api/v1/cognify", strings.NewReader(string(payload)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("[chat-sources] cognify trigger failed: %v", err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

const chatImportsCollectionName = "chat-imports"

func (d *chatSourcesDaemon) resolveDatasetID(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.loopback+"/api/v1/datasets", nil)
	if err != nil {
		return "", err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var datasets []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&datasets); err != nil {
		return "", err
	}
	for _, ds := range datasets {
		if ds.Name == d.dataset {
			return ds.ID, nil
		}
	}
	return "", nil
}

func (d *chatSourcesDaemon) persistState(ctx context.Context, state ChatSourceState) {
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 0
	if state.Enabled {
		enabled = 1
	}
	_, err := d.db.ExecContext(ctx, Q(`
		INSERT INTO chat_import_sources (platform, root_path, enabled, last_scan_at, last_error, scans, files_imported, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT(platform) DO UPDATE SET
			root_path = $9, enabled = $10, last_scan_at = $11, last_error = $12,
			scans = chat_import_sources.scans + 1, files_imported = chat_import_sources.files_imported + $13, updated_at = $14
	`), state.Platform, state.RootPath, enabled, now, state.LastError, state.Scans, state.FilesImported, now,
		state.RootPath, enabled, now, state.LastError, state.FilesImported, now)
	if err != nil {
		log.Printf("[chat-sources] state persist failed: %v", err)
	}
}

// autoDistillEnabled: LEVARA_AUTO_DISTILL=1 (opt-in; LLM calls cost money).
func autoDistillEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_AUTO_DISTILL"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func distillBudget() int {
	if n := envIntLocal(strings.TrimSpace(os.Getenv("LEVARA_DISTILL_BUDGET"))); n > 0 && n <= 10 {
		return n
	}
	return distillDefaultBudget
}

func distillHalls() []string {
	raw := strings.TrimSpace(os.Getenv("LEVARA_DISTILL_HALLS"))
	if raw == "" {
		return []string{"decision"}
	}
	var halls []string
	for _, h := range strings.Split(raw, ",") {
		h = strings.TrimSpace(strings.ToLower(h))
		switch h {
		case "decision", "discovery", "advice", "fact", "event":
			halls = append(halls, h)
		}
	}
	if len(halls) == 0 {
		return []string{"decision"}
	}
	return halls
}

func distillHallsLabel() string {
	return strings.Join(distillHalls(), ",")
}

// distillNewSessions is the P5 janitor: sessions with >= distillMinMessages
// messages and >= 2 user turns that have no recorded distillation get
// processed via the server's own MCP chat_distill — the exact production
// path (LLM, provenance, vector sidecar) — on a budget.
func (d *chatSourcesDaemon) distillNewSessions(ctx context.Context) {
	if !autoDistillEnabled() || d.loopback == "" {
		return
	}
	for _, hall := range distillHalls() {
		candidates, err := chatimport.DistillCandidates(ctx, d.db, Q, hall, distillMinMessages, distillBudget())
		if err != nil {
			log.Printf("[chat-sources] distill candidates failed: %v", err)
			continue
		}
		for _, c := range candidates {
			log.Printf("[chat-sources] distilling %s %s (hall=%s, %d msgs, %q)",
				c.Platform, shortIDLocal(c.SessionID), hall, c.Messages, truncateLocal(c.Title, 50))
			keys, err := d.callChatDistill(ctx, c.Platform, c.SessionID, hall)
			status := "ok"
			msg := ""
			if err != nil {
				status = "failed"
				msg = err.Error()
			}
			if rerr := chatimport.RecordDistillOutcome(ctx, d.db, Q, c.Platform, c.SessionID, hall, status, keys, msg); rerr != nil {
				log.Printf("[chat-sources] distill record failed: %v", rerr)
			}
			if err != nil {
				log.Printf("[chat-sources] distill %s failed: %v", shortIDLocal(c.SessionID), err)
			} else {
				log.Printf("[chat-sources] distilled %s → %d memories (hall=%s)", shortIDLocal(c.SessionID), len(keys), hall)
			}
		}
	}
}

// callChatDistill invokes the MCP tool through the server's own /mcp
// endpoint and returns the saved memory keys.
func (d *chatSourcesDaemon) callChatDistill(ctx context.Context, platform chatimport.Platform, sessionID, hall string) ([]string, error) {
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "chat_distill",
			"arguments": map[string]any{
				"platform":     string(platform),
				"session_id":   sessionID,
				"hall":         hall,
				"max_memories": 4,
			},
		},
	})
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, d.loopback+"/mcp", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	distillClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := distillClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rpc struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		return nil, err
	}
	if len(rpc.Result.Content) == 0 {
		return nil, fmt.Errorf("empty mcp response")
	}
	var parsed struct {
		Saved      int `json:"saved"`
		Candidates []struct {
			Key string `json:"key"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(rpc.Result.Content[0].Text), &parsed); err != nil {
		return nil, fmt.Errorf("bad distill response: %.100s", rpc.Result.Content[0].Text)
	}
	keys := make([]string, 0, len(parsed.Candidates))
	for _, c := range parsed.Candidates {
		if c.Key != "" {
			keys = append(keys, c.Key)
		}
	}
	return keys, nil
}

func shortIDLocal(id string) string {
	if len(id) > 13 {
		return id[:13] + "…"
	}
	return id
}

func truncateLocal(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
