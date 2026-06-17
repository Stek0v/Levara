package embcontract

import "testing"

func TestContractFingerprintChangesWhenTokenizerChanges(t *testing.T) {
	base := Contract{
		Encoder:       "model-a",
		Tokenizer:     "tok-a",
		Pooling:       "mean",
		Normalization: "l2",
		Dim:           256,
		Metric:        "cosine",
	}
	changed := base
	changed.Tokenizer = "tok-b"

	if base.Fingerprint() == changed.Fingerprint() {
		t.Fatal("fingerprint must change when tokenizer changes")
	}
}

func TestStampMetadataAddsVersionAndContract(t *testing.T) {
	contract := Contract{Encoder: "potion-code-16M", Dim: 256, Metric: "cosine"}
	meta, ok := StampMetadata(map[string]any{"text": "hello"}, contract).(map[string]any)
	if !ok {
		t.Fatalf("stamped metadata type = %T, want map[string]any", meta)
	}
	if got := VersionFromMetadata(meta); got != contract.Fingerprint() {
		t.Fatalf("embedding_version=%q, want %q", got, contract.Fingerprint())
	}
	if _, ok := meta[MetadataContractKey].(Contract); !ok {
		t.Fatalf("embedding_contract missing or wrong type: %T", meta[MetadataContractKey])
	}
}
