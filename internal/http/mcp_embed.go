package http

import (
	"context"
	"fmt"
)

func (h *mcpHandler) Embed(ctx context.Context, text string) ([]float32, error) {
	if h.cfg.EmbedClient == nil {
		return nil, fmt.Errorf("embed client not configured")
	}
	vecs, err := h.cfg.EmbedClient.EmbedTexts(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("empty embedding result")
	}
	return vecs[0], nil
}

func (h *mcpHandler) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if h.cfg.EmbedClient == nil {
		return nil, fmt.Errorf("embed client not configured")
	}
	return h.cfg.EmbedClient.EmbedTexts(ctx, texts)
}

func (h *mcpHandler) EmbedAvailable() bool {
	return h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil
}

func (h *mcpHandler) EmbedEndpoint() string { return h.cfg.EmbedEndpoint }

func (h *mcpHandler) EmbedModel() string { return h.cfg.EmbedModel }
