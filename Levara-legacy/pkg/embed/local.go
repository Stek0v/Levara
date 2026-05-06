// local.go — Local embedding interface for future ONNX/WASM runtime.
//
// Strategy: provide a LocalEmbedder interface that can be backed by:
//   1. HTTP client (current: embed-server, OpenAI-compatible) ✅
//   2. ONNX Runtime Go bindings (future: github.com/yalue/onnxruntime_go)
//   3. WASM runtime (future: wazero + ONNX→WASM compiled model)
//
// This file provides the interface and a fallback that uses the HTTP client.
// When ONNX support is added, it will implement the same interface.
package embed

import (
	"context"
	"fmt"
	"log"
)

// Embedder is the interface for text → vector embedding.
type Embedder interface {
	// Embed converts texts to vectors. Returns len(texts) vectors.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension returns the output vector dimension.
	Dimension() int
	// Close releases resources.
	Close() error
}

// HTTPEmbedder wraps the existing Client as an Embedder.
type HTTPEmbedder struct {
	client *Client
	dim    int
}

// NewHTTPEmbedder creates an Embedder backed by an HTTP embedding server.
func NewHTTPEmbedder(endpoint, model string, dim int) Embedder {
	return &HTTPEmbedder{
		client: NewClient(endpoint, model, 16, 3),
		dim:    dim,
	}
}

func (e *HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.client.EmbedTexts(ctx, texts)
}

func (e *HTTPEmbedder) Dimension() int { return e.dim }
func (e *HTTPEmbedder) Close() error   { return nil }

// AutoEmbedder tries local ONNX first, falls back to HTTP.
// This is the recommended way to create an embedder.
type AutoEmbedder struct {
	inner Embedder
	name  string
}

// NewAutoEmbedder creates the best available embedder.
// Priority: 1) ONNX local (if model path exists), 2) HTTP endpoint.
func NewAutoEmbedder(httpEndpoint, model string, dim int, onnxModelPath string) Embedder {
	// Future: check if ONNX model exists and ONNX Runtime is available
	// if onnxModelPath != "" {
	//     if onnx, err := NewONNXEmbedder(onnxModelPath, dim); err == nil {
	//         log.Printf("Using local ONNX embedder: %s (dim=%d)", onnxModelPath, dim)
	//         return &AutoEmbedder{inner: onnx, name: "onnx:" + onnxModelPath}
	//     }
	// }

	if httpEndpoint != "" {
		log.Printf("Using HTTP embedder: %s model=%s dim=%d", httpEndpoint, model, dim)
		return &AutoEmbedder{
			inner: NewHTTPEmbedder(httpEndpoint, model, dim),
			name:  fmt.Sprintf("http:%s/%s", httpEndpoint, model),
		}
	}

	log.Printf("WARNING: No embedder available (no HTTP endpoint, no ONNX model)")
	return &AutoEmbedder{inner: &noopEmbedder{dim: dim}, name: "noop"}
}

func (a *AutoEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return a.inner.Embed(ctx, texts)
}
func (a *AutoEmbedder) Dimension() int { return a.inner.Dimension() }
func (a *AutoEmbedder) Close() error   { return a.inner.Close() }
func (a *AutoEmbedder) String() string { return a.name }

// noopEmbedder returns zero vectors (used when no embedder is available).
type noopEmbedder struct{ dim int }

func (n *noopEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = make([]float32, n.dim)
	}
	return vecs, nil
}
func (n *noopEmbedder) Dimension() int { return n.dim }
func (n *noopEmbedder) Close() error   { return nil }

// ── Future ONNX Implementation Stub ──
//
// When ready to add ONNX support:
//   go get github.com/yalue/onnxruntime_go
//
// type ONNXEmbedder struct {
//     session *ort.AdvancedSession
//     dim     int
// }
//
// func NewONNXEmbedder(modelPath string, dim int) (*ONNXEmbedder, error) {
//     ort.SetSharedLibraryPath("/usr/lib/libonnxruntime.so")
//     err := ort.InitializeEnvironment()
//     session, err := ort.NewAdvancedSession(modelPath, ...)
//     return &ONNXEmbedder{session: session, dim: dim}, nil
// }
//
// func (o *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
//     // Tokenize texts → input_ids tensor
//     // Run ONNX session
//     // Extract output tensor → [][]float32
// }
