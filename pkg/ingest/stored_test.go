package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/storage"
)

type storedReceiver struct {
	storage.Storage
	objects map[string]string
	fail    bool
}

func (s *storedReceiver) Save(ctx context.Context, key string, reader io.Reader) error {
	if s.fail {
		return errors.New("backend unavailable")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objects[key] = string(data)
	return nil
}
func TestIngestStoredNeverStagesRemotePlaintext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	receiver := &storedReceiver{objects: map[string]string{}}
	data := "Приватный текст с Unicode и binary \\ bytes"
	items := []Item{{Text: data, Filename: "документ.txt", OwnerID: "alice"}}
	results, err := IngestStored(context.Background(), items, dir, receiver)
	if err != nil || len(results) != 1 {
		t.Fatalf("ingest %+v %v", results, err)
	}
	key := strings.TrimPrefix(results[0].FilePath, "storage://")
	if receiver.objects[key] != data || len(receiver.objects) != 1 {
		t.Fatal("backend did not receive exact bytes")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("remote ingest created local plaintext directory: %v", err)
	}
	receiver.fail = true
	if _, err := IngestStored(context.Background(), items, dir, receiver); err == nil {
		t.Fatal("failed backend silently accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("backend failure fell back to local storage: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := IngestStored(ctx, items, dir, receiver); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}
func TestIngestStoredRejectsUntrustedIDPaths(t *testing.T) {
	for _, id := range []string{"../escape", "dir/name", "dir\\name", ".", "..", "bad\x00id"} {
		t.Run(id, func(t *testing.T) {
			receiver := &storedReceiver{objects: map[string]string{}}
			if _, err := IngestStored(context.Background(), []Item{{Text: "valid first item"}, {ID: id, Text: "text"}}, t.TempDir(), receiver); err == nil {
				t.Fatal("unsafe object ID accepted")
			}
			if len(receiver.objects) != 0 {
				t.Fatal("validation followed a write")
			}
		})
	}
}

func TestIngestStoredTagsRoundtrip(t *testing.T) {
	tags := []string{"кириллица", "line\nbreak", `slash\quote"`, "%_"}
	results, err := IngestStored(context.Background(), []Item{{Text: "text", Tags: tags}}, t.TempDir(), &storedReceiver{objects: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	if err := json.Unmarshal([]byte(results[0].Tags), &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(tags) {
		t.Fatal(actual)
	}
	for i := range tags {
		if actual[i] != tags[i] {
			t.Fatalf("tag mismatch %q %q", actual[i], tags[i])
		}
	}
}
