package http

import (
	"context"
	"log"
	"sync"

	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/decisions"
)

// decisionsGate adapts the FRIDA-Decisions sidecar client to the
// consolidate.FactGate seam. Threshold selection is left at the engine's
// calibrated default (consolidate.DefaultGateThreshold).
type decisionsGate struct{ client *decisions.Client }

func (g decisionsGate) Supersedes(ctx context.Context, oldRec, newRec consolidate.MemoryRecord) (float64, error) {
	return g.client.Supersedes(ctx, oldRec.Value, newRec.Value)
}

// decisionsClients caches one client per endpoint so concurrent handlers and
// repeated consolidation runs reuse the HTTP transport. Keyed by endpoint to
// keep tests with different APIConfig values isolated.
var decisionsClients sync.Map // endpoint string -> *decisions.Client

// ConsolidationGate implements mcp.DecisionDeps. An empty
// APIConfig.DecisionsEndpoint disables the gate (nil, 0).
func (h *mcpHandler) ConsolidationGate() (consolidate.FactGate, float64) {
	if h.cfg.DecisionsEndpoint == "" {
		return nil, 0
	}
	v, loaded := decisionsClients.LoadOrStore(h.cfg.DecisionsEndpoint,
		decisions.New(h.cfg.DecisionsEndpoint, h.cfg.DecisionsTimeoutMs))
	c, _ := v.(*decisions.Client)
	if c == nil || !c.Enabled() {
		return nil, 0
	}
	if !loaded {
		log.Printf("[decisions] fact gate enabled via %s (CPU-only sidecar, see benchmark/frida_gate)", h.cfg.DecisionsEndpoint)
	}
	return decisionsGate{client: c}, 0
}
