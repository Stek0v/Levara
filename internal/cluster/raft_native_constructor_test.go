package cluster

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stek0v/levara/internal/store"
)

// Bound native transport/Bolt handles to a child lifetime: NewRaftNode does
// not expose all of them for explicit cleanup in the parent test process.
func TestRaftNativeConstructorProcess(t *testing.T) {
	const childFlag = "LEVARA_T23_RAFT_CONSTRUCTOR_CHILD"
	const rootFlag = "LEVARA_T23_RAFT_CONSTRUCTOR_ROOT"
	if os.Getenv(childFlag) == "1" {
		root := os.Getenv(rootFlag)
		if root == "" {
			t.Fatal("child private root missing")
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err = listener.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := store.NewLevara(2, filepath.Join(root, "shard_0", "meta.bin"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		node, err := NewRaftNode(0, "native-constructor", root, port, db, WithBindAddr("127.0.0.1"))
		if err != nil {
			t.Fatalf("production native Raft constructor: %v", err)
		}
		defer func() {
			if err := node.Raft.Shutdown().Error(); err != nil {
				t.Errorf("Raft shutdown: %v", err)
			}
		}()
		for _, name := range []string{"logs.dat", "stable.dat"} {
			if _, err := os.Stat(filepath.Join(root, "shard_0", "raft", name)); err != nil {
				t.Fatalf("native Bolt %s: %v", name, err)
			}
		}
		configuration := raft.Configuration{Servers: []raft.Server{{
			ID: "native-constructor-shard-0", Address: raft.ServerAddress(fmt.Sprintf("127.0.0.1:%d", port)), Suffrage: raft.Voter,
		}}}
		if err := node.Raft.BootstrapCluster(configuration).Error(); err != nil {
			t.Fatalf("native bootstrap: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for node.Raft.State() != raft.Leader && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if node.Raft.State() != raft.Leader {
			t.Fatal("native leader election timed out")
		}
		if err := node.Insert("native-record", []float32{1, 0}, map[string]string{"text": "native constructor control"}); err != nil {
			t.Fatalf("native Raft insert: %v", err)
		}
		vector, metadata, present := db.Get("native-record")
		if !present || len(vector) != 2 || vector[0] != 1 || vector[1] != 0 || metadata == nil {
			t.Fatalf("native applied record: present=%v vector=%v metadata=%v", present, vector, metadata)
		}
		fmt.Println("T23_NATIVE_RAFT_CONSTRUCTOR_OK")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRaftNativeConstructorProcess$", "-test.count=1")
	command.Env = append(os.Environ(), childFlag+"=1", rootFlag+"="+t.TempDir())
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("native constructor child deadline: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("native constructor child exit: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "T23_NATIVE_RAFT_CONSTRUCTOR_OK") {
		t.Fatalf("native constructor child lacks success control\n%s", output)
	}
}
