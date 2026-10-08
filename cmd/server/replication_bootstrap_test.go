package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/cluster"
	"github.com/stek0v/levara/internal/store"
)

func TestHTTPReplicationUnsupportedJoinProcess(t *testing.T) {
	if mode := os.Getenv("LEVARA_TEST_HTTP_REPLICATION_JOIN_MODE"); mode != "" {
		cfg := vectorRuntimeConfig{Dim: 2, NumShards: 2, Standalone: true, JoinAddr: "127.0.0.1:1", DataDir: os.Getenv("LEVARA_TEST_HTTP_REPLICATION_JOIN_ROOT")}
		if mode == "raft" {
			cfg.NumShards = 1
			cfg.Standalone = false
		}
		initVectorRuntime(cfg)
		t.Fatal("unsupported join returned")
	}
	for _, mode := range []string{"multi", "raft"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "unopened")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHTTPReplicationUnsupportedJoinProcess$")
			cmd.Env = append(os.Environ(), "LEVARA_TEST_HTTP_REPLICATION_JOIN_MODE="+mode, "LEVARA_TEST_HTTP_REPLICATION_JOIN_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			failure, ok := err.(*exec.ExitError)
			if !ok || failure.ExitCode() != 1 || !strings.Contains(string(output), "requires standalone=true and shards=1") {
				t.Fatalf("unsupported join not rejected: %v %s", err, output)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("opened roots before validation: %v", err)
			}
		})
	}
}

func TestHTTPReplicationBootstrapScope(t *testing.T) {
	db, err := store.NewLevara(2, filepath.Join(t.TempDir(), "meta.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dn := &cluster.DirectNode{DB: db}
	if rs := initReplicationServer("p", "", []store.ShardHandler{dn}); rs == nil || dn.Repl != rs || rs.Role() != "primary" {
		t.Fatal("single DirectNode primary unavailable")
	}
	dn.Repl = nil
	if rs := initReplicationServer("p", "", []store.ShardHandler{dn, &cluster.DirectNode{DB: db}}); rs != nil || dn.Repl != nil {
		t.Fatal("partial multi-shard replication exposed")
	}
	if rs := initReplicationServer("p", "", []store.ShardHandler{&cluster.RaftNode{DB: db}}); rs != nil {
		t.Fatal("HTTP stream exposed on Raft store")
	}
	if rs := initReplicationServer("r", "127.0.0.1:1", []store.ShardHandler{dn}); rs == nil || rs.Role() != "replica" || rs.PrimaryAddr() != "127.0.0.1:1" {
		t.Fatal("single DirectNode replica unavailable")
	}
}

func TestReplicaProductionShutdownDrainsStartupAndApply(t *testing.T) {
	for _, phase := range []string{"startup", "live"} {
		t.Run(phase, func(t *testing.T) {
			db, err := store.NewLevara(2, filepath.Join(t.TempDir(), "meta.bin"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			entered := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				if phase == "live" {
					io.WriteString(w, `{"version":1,"kind":"snapshot","seq":0,"records":[]}`+"\n")
					io.WriteString(w, `{"op":1,"id":"live","seq":1,"vector":[1,0],"metadata":{"live":true}}`+"\n")
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer ts.Close()
			stop := startReplicaClient(strings.TrimPrefix(ts.URL, "http://"), "r", db, nil)
			defer stop()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("startup not admitted")
			}
			if phase == "live" {
				deadline := time.Now().Add(2 * time.Second)
				for {
					_, meta, ok := db.Get("live")
					if ok && string(meta) == `{"live":true}` {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("live apply not observed")
					}
					time.Sleep(time.Millisecond)
				}
			}
			stopped := make(chan struct{})
			go func() { stop(); close(stopped) }()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("shutdown did not drain startup/apply")
			}
			stop() // Repeated shutdown is safe.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
