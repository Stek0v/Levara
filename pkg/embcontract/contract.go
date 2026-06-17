package embcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	MetadataVersionKey  = "embedding_version"
	MetadataContractKey = "embedding_contract"
)

// Contract identifies an embedding vector space. Same dimension and metric are
// not enough: tokenizer, pooling and normalization changes can move vectors
// into an incompatible space.
type Contract struct {
	Encoder       string `json:"encoder"`
	Tokenizer     string `json:"tokenizer,omitempty"`
	Pooling       string `json:"pooling,omitempty"`
	Normalization string `json:"normalization,omitempty"`
	Dim           int    `json:"dim"`
	Metric        string `json:"metric"`
}

func FromEnv(model string, dim int, metric string) Contract {
	return Contract{
		Encoder:       firstNonEmpty(model, os.Getenv("EMBEDDING_ENCODER"), os.Getenv("EMBEDDING_MODEL")),
		Tokenizer:     firstNonEmpty(os.Getenv("EMBEDDING_TOKENIZER"), os.Getenv("EMBEDDING_TOKENIZER_NAME")),
		Pooling:       firstNonEmpty(os.Getenv("EMBEDDING_POOLING"), "mean"),
		Normalization: firstNonEmpty(os.Getenv("EMBEDDING_NORMALIZATION"), "l2"),
		Dim:           dim,
		Metric:        firstNonEmpty(metric, os.Getenv("EMBEDDING_METRIC"), "cosine"),
	}
}

func (c Contract) Normalized() Contract {
	c.Encoder = strings.TrimSpace(c.Encoder)
	c.Tokenizer = strings.TrimSpace(c.Tokenizer)
	c.Pooling = strings.ToLower(strings.TrimSpace(c.Pooling))
	c.Normalization = strings.ToLower(strings.TrimSpace(c.Normalization))
	c.Metric = strings.ToLower(strings.TrimSpace(c.Metric))
	if c.Pooling == "" {
		c.Pooling = "mean"
	}
	if c.Normalization == "" {
		c.Normalization = "l2"
	}
	if c.Metric == "" {
		c.Metric = "cosine"
	}
	return c
}

func (c Contract) Fingerprint() string {
	c = c.Normalized()
	payload := fmt.Sprintf("encoder=%s\ntokenizer=%s\npooling=%s\nnormalization=%s\ndim=%d\nmetric=%s\n",
		c.Encoder, c.Tokenizer, c.Pooling, c.Normalization, c.Dim, c.Metric)
	sum := sha256.Sum256([]byte(payload))
	return "emb:" + hex.EncodeToString(sum[:])[:24]
}

func (c Contract) Empty() bool {
	c = c.Normalized()
	return c.Encoder == ""
}

func StampMetadata(meta any, c Contract) any {
	version := c.Fingerprint()
	switch v := meta.(type) {
	case nil:
		return map[string]any{MetadataVersionKey: version, MetadataContractKey: c.Normalized()}
	case map[string]any:
		cp := copyMap(v)
		cp[MetadataVersionKey] = version
		cp[MetadataContractKey] = c.Normalized()
		return cp
	case map[string]string:
		cp := make(map[string]string, len(v)+1)
		for k, val := range v {
			cp[k] = val
		}
		cp[MetadataVersionKey] = version
		return cp
	case json.RawMessage:
		return stampJSONBytes([]byte(v), c)
	case []byte:
		return stampJSONBytes(v, c)
	case string:
		return stampJSONBytes([]byte(v), c)
	default:
		return meta
	}
}

func VersionFromMetadata(meta any) string {
	switch v := meta.(type) {
	case nil:
		return ""
	case map[string]any:
		return stringField(v, MetadataVersionKey)
	case map[string]string:
		return strings.TrimSpace(v[MetadataVersionKey])
	case json.RawMessage:
		return versionFromJSON([]byte(v))
	case []byte:
		return versionFromJSON(v)
	case string:
		return versionFromJSON([]byte(v))
	default:
		return ""
	}
}

func versionFromJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return stringField(m, MetadataVersionKey)
}

func stampJSONBytes(raw []byte, c Contract) any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	m[MetadataVersionKey] = c.Fingerprint()
	m[MetadataContractKey] = c.Normalized()
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return json.RawMessage(out)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func copyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
