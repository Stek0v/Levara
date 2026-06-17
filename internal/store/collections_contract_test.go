package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stek0v/levara/pkg/embcontract"
)

func TestCollectionInsertStampsEmbeddingContract(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewCollectionManager(4, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()

	contract := embcontract.Contract{Encoder: "encoder-v1", Tokenizer: "tok-v1", Pooling: "mean", Normalization: "l2", Dim: 4, Metric: "cosine"}
	cm.SetDefaultModel("encoder-v1")
	cm.SetDefaultEmbeddingContract(contract)
	if err := cm.CreateWithDim("docs", 4, "encoder-v1", "cosine"); err != nil {
		t.Fatalf("CreateWithDim: %v", err)
	}
	if err := cm.Insert("docs", "r1", []float32{1, 0, 0, 0}, map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	db, err := cm.Get("docs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, raw, ok := db.Get("r1")
	if !ok {
		t.Fatal("record not found")
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if got := embcontract.VersionFromMetadata(meta); got != contract.Fingerprint() {
		t.Fatalf("embedding_version=%q, want %q", got, contract.Fingerprint())
	}
}

func TestCollectionInsertRejectsMixedEmbeddingContractSameDim(t *testing.T) {
	dir := t.TempDir()
	cm, err := NewCollectionManager(4, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()

	v1 := embcontract.Contract{Encoder: "encoder-v1", Tokenizer: "tok-v1", Pooling: "mean", Normalization: "l2", Dim: 4, Metric: "cosine"}
	v2 := embcontract.Contract{Encoder: "encoder-v2", Tokenizer: "tok-v2", Pooling: "mean", Normalization: "l2", Dim: 4, Metric: "cosine"}
	cm.SetDefaultEmbeddingContract(v1)
	if err := cm.CreateWithDim("docs", 4, "encoder-v1", "cosine"); err != nil {
		t.Fatalf("CreateWithDim: %v", err)
	}

	meta := embcontract.StampMetadata(map[string]any{"text": "new space"}, v2)
	err = cm.Insert("docs", "r2", []float32{0, 1, 0, 0}, meta)
	if !errors.Is(err, ErrEmbeddingContractMismatch) {
		t.Fatalf("Insert error=%v, want ErrEmbeddingContractMismatch", err)
	}
}
