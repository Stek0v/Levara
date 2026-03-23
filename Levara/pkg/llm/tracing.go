package llm

import (
	"context"

	"github.com/stek0v/levara/pkg/observe"
)

// LangfuseAdapter wraps observe.LangfuseTracer to satisfy the llm.Tracer interface.
type LangfuseAdapter struct {
	lt *observe.LangfuseTracer
}

// NewLangfuseAdapter creates a Tracer from a LangfuseTracer.
func NewLangfuseAdapter(lt *observe.LangfuseTracer) Tracer {
	return &LangfuseAdapter{lt: lt}
}

func (a *LangfuseAdapter) Enabled() bool {
	return a.lt.Enabled()
}

func (a *LangfuseAdapter) TraceGeneration(ctx context.Context, td TracerData) error {
	return a.lt.TraceGeneration(ctx, observe.TraceData{
		TraceID:   td.TraceID,
		Name:      td.Name,
		Model:     td.Model,
		Input:     td.Input,
		Output:    td.Output,
		LatencyMs: td.LatencyMs,
		TokensIn:  td.TokensIn,
		TokensOut: td.TokensOut,
		Status:    td.Status,
		Metadata:  td.Metadata,
	})
}
