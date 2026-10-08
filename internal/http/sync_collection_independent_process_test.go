//go:build darwin || linux

package http

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embcontract"
)

type syncCollectionProcessConfig struct {
	Root, Model string
	Dim         int
}
type syncCollectionProcessSnapshot struct {
	IDs      []string
	Vectors  [][]float32
	Metadata []json.RawMessage
	Contract string
	InUse    int
}

// This is real loopback HTTP in separate receiver processes. Sync authority is
// instance-wide active-admin authority, not a per-user or per-tenant job scope.
func TestSyncCollectionIndependentProcessChild(t *testing.T) {
	raw := os.Getenv("LEVARA_SYNC_COLLECTION_PROCESS")
	if raw == "" {
		return
	}
	log.SetOutput(os.Stderr)
	var conf syncCollectionProcessConfig
	if err := json.Unmarshal([]byte(raw), &conf); err != nil {
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	db, err := sql.Open("sqlite3", filepath.Join(conf.Root, "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO principals(id,type) VALUES('root','user'),('ordinary','user')",
		"INSERT INTO users(id,email,hashed_password,is_active,is_superuser) VALUES('root','root@test.invalid','locked',TRUE,TRUE),('ordinary','ordinary@test.invalid','locked',TRUE,FALSE)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(conf.Root, "vectors")
	cm, err := store.NewCollectionManager(conf.Dim, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cm.Close() }()
	cm.SetDefaultModel(conf.Model)
	expected := embcontract.FromEnv(conf.Model, conf.Dim, "cosine")
	cm.SetDefaultEmbeddingContract(expected)
	// Every node starts with a native, correctly stamped record. Imports must
	// upsert it, not multiply physical records or inherit the peer vector space.
	vector := make([]float32, conf.Dim)
	vector[conf.Dim-1] = 1
	if err := cm.CreateWithDim("docs", conf.Dim, conf.Model, "cosine"); err != nil {
		t.Fatal(err)
	}
	seedVector := make([]float32, conf.Dim)
	seedVector[0] = 1
	if err := cm.Insert("docs", "record", seedVector, map[string]any{"text": "business document", "business": "retained"}); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != conf.Model || len(req.Input) == 0 {
			http.Error(w, "wrong local encoder request", 400)
			return
		}
		data := make([]map[string]any, len(req.Input))
		for i := range data {
			data[i] = map[string]any{"index": i, "embedding": vector}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer provider.Close()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	defer app.Shutdown()
	api := app.Group("/api/v1", func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() }, JWTMiddleware(syncTestSecret, true), APIKeyPermissionMiddleware())
	RegisterSyncAPI(api, APIConfig{DB: db, RequireAuth: true, Collections: cm, EmbedModel: conf.Model, EmbedEndpoint: provider.URL})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan error, 1)
	go func() { serving <- app.Listener(listener) }()
	lifetime := time.AfterFunc(45*time.Second, func() { os.Exit(124) })
	defer lifetime.Stop()
	writer := json.NewEncoder(os.Stdout)
	if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "http://" + listener.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command taskProcessAuthorityCommand
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			t.Fatal(err)
		}
		if command.Op == "stop" {
			if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "stopped"}); err != nil {
				t.Fatal(err)
			}
			return
		}
		if command.Op != "reopen" {
			t.Fatal("unexpected collection child command")
		}
		// The parent polls every job to terminal before this barrier. HTTP closes
		// before reopening native files; no concurrent manager over one root.
		if err := app.Shutdown(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-serving:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("HTTP listener did not stop")
		}
		if err := cm.Close(); err != nil {
			t.Fatal(err)
		}
		cm, err = store.NewCollectionManager(conf.Dim, root)
		if err != nil {
			t.Fatal(err)
		}
		ids, vectors, metadata, err := cm.AllRecords("docs")
		if err != nil {
			t.Fatal(err)
		}
		snap := syncCollectionProcessSnapshot{IDs: ids, Vectors: vectors, Contract: cm.GetMeta("docs").EmbeddingVersion, InUse: db.Stats().InUse}
		for _, m := range metadata {
			snap.Metadata = append(snap.Metadata, json.RawMessage(m))
		}
		encoded, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Encode(taskProcessAuthorityReply{Digest: string(encoded)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func syncCollectionStartProcess(t *testing.T, ctx context.Context, conf syncCollectionProcessConfig) (*taskAuthorityProcess, string) {
	t.Helper()
	raw, err := json.Marshal(conf)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSyncCollectionIndependentProcessChild$")
	cmd.Env = append(os.Environ(), "LEVARA_SYNC_COLLECTION_PROCESS="+string(raw))
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	p := &taskAuthorityProcess{cmd: cmd, input: input, replies: make(chan taskProcessAuthorityReply, 8), exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var reply taskProcessAuthorityReply
			if err := json.Unmarshal(line, &reply); err != nil {
				reply.Error = err.Error()
			}
			select {
			case p.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { <-drained; p.waitErr = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() { p.kill(t) })
	ready := p.receive(t, ctx)
	if ready.Error != "" || ready.Checkpoint == "" {
		t.Fatalf("collection child readiness: %+v", ready)
	}
	return p, ready.Checkpoint
}

func TestSyncCollectionIndependentProcessImportStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	configs := []syncCollectionProcessConfig{{Root: t.TempDir(), Model: "node-a", Dim: 2}, {Root: t.TempDir(), Model: "node-b", Dim: 3}}
	processes := make([]*taskAuthorityProcess, 2)
	urls := make([]string, 2)
	for i, conf := range configs {
		processes[i], urls[i] = syncCollectionStartProcess(t, ctx, conf)
	}
	if processes[0].cmd.Process.Pid == processes[1].cmd.Process.Pid {
		t.Fatal("nodes share an OS process")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(node int, user, method, path string, body []byte, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, urls[node]+"/api/v1"+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", syncTestSecret))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", err, closeErr)
		}
		if resp.StatusCode != want {
			t.Fatalf("node%d %s %s status%d want%d: %s", node, method, path, resp.StatusCode, want, raw)
		}
		return raw
	}
	start := func(node int, body []byte) string {
		t.Helper()
		var response struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(request(node, "root", "POST", "/sync/import/collection", body, 200), &response); err != nil {
			t.Fatal(err)
		}
		if response.RunID == "" {
			t.Fatal("missing run identity")
		}
		return response.RunID
	}
	terminal := func(node int, run string) syncCollectionImportStatus {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var status syncCollectionImportStatus
			if err := json.Unmarshal(request(node, "root", "GET", "/sync/import/collection/"+run+"/status", nil, 200), &status); err != nil {
				t.Fatal(err)
			}
			if status.Status != "RUNNING" {
				return status
			}
			if time.Now().After(deadline) {
				t.Fatal("import never terminated")
			}
			time.Sleep(time.Millisecond)
		}
	}
	exports := make([][]byte, 2)
	for i := range exports {
		exports[i] = request(i, "root", "GET", "/sync/export/collection/docs", nil, 200)
	}
	runs := make([]string, 2)
	for i := range runs {
		runs[i] = start(i, exports[1-i])
		status := terminal(i, runs[i])
		if status.Status != "COMPLETED" || status.Total != 1 || status.Processed != 1 || status.Failed != 0 || status.Skipped != 0 {
			t.Fatalf("native import node%d: %+v", i, status)
		}
	}
	if runs[0] == runs[1] {
		t.Fatal("independent runs coincidentally collided; cannot prove foreign status isolation")
	}
	for i := range runs {
		request(i, "root", "GET", "/sync/import/collection/"+runs[1-i]+"/status", nil, 404)
		request(i, "ordinary", "GET", "/sync/import/collection/"+runs[i]+"/status", nil, 403)
	}
	// Processed counts successful units, while repeated IDs keep one physical row.
	var duplicate syncCollectionExport
	if err := json.Unmarshal(exports[0], &duplicate); err != nil {
		t.Fatal(err)
	}
	last := duplicate.Records[0]
	var lastMetadata map[string]any
	if err := json.Unmarshal(last.Metadata, &lastMetadata); err != nil {
		t.Fatal(err)
	}
	// Keep the real native source embedding stamp; only business payload changes.
	last.Text = "last sequential document"
	lastMetadata["text"] = last.Text
	lastMetadata["business"] = "last sequential payload"
	lastRaw, marshalErr := json.Marshal(lastMetadata)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	last.Metadata = lastRaw
	duplicate.Records = append(duplicate.Records, last)
	raw, err := json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	replay := terminal(1, start(1, raw))
	if replay.Status != "COMPLETED" || replay.Total != 2 || replay.Processed != 2 || replay.Failed != 0 {
		t.Fatalf("duplicate unit accounting: %+v", replay)
	}
	// Object-only business metadata must fail visibly without modifying native rows.
	duplicate.Records = []syncCollectionRecord{{ID: "invalid", Text: "business document", Metadata: json.RawMessage("[]")}}
	raw, err = json.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	failed := terminal(1, start(1, raw))
	if failed.Status != "FAILED" || failed.Processed != 0 || failed.Failed != 1 {
		t.Fatalf("terminal failure accounting: %+v", failed)
	}
	for i, p := range processes {
		reply := p.call(t, ctx, taskProcessAuthorityCommand{Op: "reopen"})
		var snapshot syncCollectionProcessSnapshot
		if err := json.Unmarshal([]byte(reply.Digest), &snapshot); err != nil {
			t.Fatal(err)
		}
		wantVector := make([]float32, configs[i].Dim)
		wantVector[configs[i].Dim-1] = 1
		wantContract := embcontract.FromEnv(configs[i].Model, configs[i].Dim, "cosine")
		if !reflect.DeepEqual(snapshot.IDs, []string{"record"}) || !reflect.DeepEqual(snapshot.Vectors, [][]float32{wantVector}) || snapshot.Contract != wantContract.Fingerprint() || snapshot.InUse != 0 {
			t.Fatalf("node%d native reopened snapshot: %+v", i, snapshot)
		}
		if len(snapshot.Metadata) != 1 {
			t.Fatal("native metadata cardinality")
		}
		var metadata map[string]any
		if err := json.Unmarshal(snapshot.Metadata[0], &metadata); err != nil {
			t.Fatal(err)
		}
		wantBusiness, wantText := "retained", "business document"
		if i == 1 {
			wantBusiness, wantText = "last sequential payload", "last sequential document"
		}
		if metadata["business"] != wantBusiness || metadata["text"] != wantText || embcontract.VersionFromMetadata(metadata) != wantContract.Fingerprint() {
			t.Fatalf("node%d foreign or lost metadata: %v", i, metadata)
		}
		contractJSON, err := json.Marshal(metadata[embcontract.MetadataContractKey])
		if err != nil {
			t.Fatal(err)
		}
		var actual embcontract.Contract
		if err := json.Unmarshal(contractJSON, &actual); err != nil || actual.Fingerprint() != wantContract.Fingerprint() {
			t.Fatalf("node%d full fresh contract invalid: %+v err=%v", i, actual, err)
		}
		stopped := p.call(t, ctx, taskProcessAuthorityCommand{Op: "stop"})
		if stopped.Checkpoint != "stopped" {
			t.Fatalf("graceful close reply: %+v", stopped)
		}
		select {
		case <-p.exited:
			if p.waitErr != nil {
				t.Fatal(p.waitErr)
			}
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("child close: %w", ctx.Err()))
		}
	}
}
