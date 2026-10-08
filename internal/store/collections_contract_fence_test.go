package store

import (
	"errors"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/embcontract"
)

func TestCollectionInsertContractFenceThroughNativeWrite(t *testing.T) {
	cm, err := NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	old := embcontract.FromEnv("old", 2, "cosine")
	newer := embcontract.FromEnv("new", 2, "cosine")
	cm.SetDefaultEmbeddingContract(old)
	if err := cm.CreateWithDim("docs", 2, "old", "cosine"); err != nil {
		t.Fatal(err)
	}
	db, err := cm.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	db.disk.mu.RLock()
	beforePosition := db.disk.pos
	db.disk.mu.RUnlock()
	db.mu.Lock()
	locked := true
	defer func() {
		if locked {
			db.mu.Unlock()
		}
	}()
	inserted := make(chan error, 1)
	go func() { inserted <- cm.Insert("docs", "record", []float32{1, 0}, embcontract.StampMetadata(nil, old)) }()
	deadline := time.Now().Add(2 * time.Second)
	// DiskStore advances before db.Insert takes db.mu. Observe this native
	// progress so a brief initial metadata precheck cannot satisfy the fence.
	for {
		db.disk.mu.RLock()
		position := db.disk.pos
		db.disk.mu.RUnlock()
		if position > beforePosition {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native insertion did not reach its blocked database write")
		}
		time.Sleep(time.Millisecond)
	}
	if cm.mu.TryLock() {
		cm.mu.Unlock()
		t.Fatal("native write does not retain collection admission lock")
	}
	updated := make(chan error, 1)
	go func() { updated <- cm.UpdateEmbeddingContract("docs", newer) }()
	select {
	case err := <-updated:
		t.Fatalf("contract changed during blocked native write: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	db.mu.Unlock()
	locked = false
	for _, result := range []<-chan error{inserted, updated} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("native insertion or contract update did not finish")
		}
	}
	if err := cm.Insert("docs", "stale", []float32{1, 0}, embcontract.StampMetadata(nil, old)); !errors.Is(err, ErrEmbeddingContractMismatch) {
		t.Fatalf("stale contract accepted: %v", err)
	}
	if cm.HasRecord("docs", "stale") {
		t.Fatal("stale insertion had effect")
	}
	if err := cm.Insert("docs", "fresh", []float32{0, 1}, embcontract.StampMetadata(nil, newer)); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionMetadataSnapshotAndUnknownStampedAdmission(t *testing.T) {
	cm, err := NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	contract := embcontract.FromEnv("known", 2, "cosine")
	cm.SetDefaultEmbeddingContract(contract)
	if err := cm.CreateWithDim("docs", 2, "known", "cosine"); err != nil {
		t.Fatal(err)
	}
	snapshot := cm.GetMeta("docs")
	snapshot.EmbeddingModel = "changed"
	snapshot.EmbeddingContract.Encoder = "changed"
	actual := cm.GetMeta("docs")
	if actual.EmbeddingModel != "known" || actual.EmbeddingContract.Fingerprint() != contract.Fingerprint() {
		t.Fatal("caller mutated native metadata through snapshot")
	}
	// Unknown legacy destination must not accept an explicitly stamped vector.
	cm.mu.Lock()
	cm.metas["docs"].EmbeddingVersion = ""
	cm.metas["docs"].EmbeddingContract = nil
	cm.mu.Unlock()
	err = cm.Insert("docs", "unknown", []float32{1, 0}, embcontract.StampMetadata(nil, contract))
	if !errors.Is(err, ErrEmbeddingContractMismatch) || cm.HasRecord("docs", "unknown") {
		t.Fatalf("unknown contract accepted stamped write: %v", err)
	}
}
