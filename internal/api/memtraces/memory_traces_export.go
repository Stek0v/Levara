// memory_traces_export.go — curated memory-trace export (NDJSON) for
// fine-tuning corpora.
//
// Extracted verbatim from internal/http/memory_traces_export.go (Phase-2
// route-group slice, code-modernization brief §3). The admin gate, the
// audit scoping and the window parsing stay in internal/http and are
// threaded through Config as function values.
package memtraces

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/trajectory"
	"github.com/stek0v/levara/pkg/audit"
)

// Config carries the export plane's dependencies: the audit read model and
// the three transport-side helpers the handler used to call in package http.
type Config struct {
	MCPAuditReadModel *audit.ReadModel

	RequireSuperuser func(*fiber.Ctx) error
	ScopedAuditFilter func(*fiber.Ctx, audit.EventFilter) (audit.EventFilter, error)
	WindowHours       func(*fiber.Ctx) int
}

type curatedMemoryTrace struct {
	TrajectoryID          string              `json:"trajectory_id"`
	Collection            string              `json:"collection,omitempty"`
	ClientName            string              `json:"client_name,omitempty"`
	StartedAt             string              `json:"started_at"`
	EndedAt               string              `json:"ended_at"`
	MemoryActions         []string            `json:"memory_actions"`
	ReasonLabel           string              `json:"reason_label"`
	OutcomeMetrics        trajectory.Counters `json:"outcome_metrics"`
	SourceReviewFindingID string              `json:"source_review_finding_id,omitempty"`
}

// Handler returns the GET /memory-traces/export endpoint.
func Handler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := cfg.RequireSuperuser(c); err != nil {
			return err
		}
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		quality := strings.TrimSpace(c.Query("quality", "good"))
		if quality != "good" {
			return c.Status(400).JSON(fiber.Map{"error": "unsupported quality"})
		}
		hours := cfg.WindowHours(c)
		filter, err := cfg.ScopedAuditFilter(c, audit.EventFilter{
			Since:      time.Now().Add(-time.Duration(hours) * time.Hour),
			Collection: c.Query("collection"),
			Limit:      20000,
		})
		if err != nil {
			return err
		}
		rows, err := cfg.MCPAuditReadModel.EventsForTrajectories(c.UserContext(), filter)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory trace export query failed"})
		}
		c.Set("Content-Type", "application/x-ndjson")
		for _, tr := range trajectory.Build(rows, true) {
			if !isGoodMemoryTrace(tr) {
				continue
			}
			raw, _ := json.Marshal(curatedMemoryTrace{
				TrajectoryID:   tr.ID,
				Collection:     tr.Collection,
				ClientName:     tr.ClientName,
				StartedAt:      tr.StartedAt,
				EndedAt:        tr.EndedAt,
				MemoryActions:  memoryActions(tr),
				ReasonLabel:    "good_memory_behavior",
				OutcomeMetrics: tr.Counters,
			})
			if _, err := c.Write(append(raw, '\n')); err != nil {
				return err
			}
		}
		return nil
	}
}

func isGoodMemoryTrace(tr trajectory.Trajectory) bool {
	if tr.Counters.ErrorCount > 0 || tr.Counters.SaveCount == 0 {
		return false
	}
	consulted := false
	nonZeroRetrieval := false
	for _, event := range tr.Events {
		if event.RepeatSave {
			return false
		}
		if isConsultToolForExport(event.Tool) {
			consulted = true
			if !event.ZeroResult && event.ResultCount > 0 {
				nonZeroRetrieval = true
			}
		}
		if event.Tool == "save_memory" && !consulted {
			return false
		}
	}
	return consulted && nonZeroRetrieval
}

func isConsultToolForExport(tool string) bool {
	switch tool {
	case "recall_memory", "list_memories", "wake_up", "search", "workspace_search", "cross_search":
		return true
	default:
		return false
	}
}

func memoryActions(tr trajectory.Trajectory) []string {
	out := []string{}
	for _, event := range tr.Events {
		switch event.Tool {
		case "recall_memory", "list_memories", "wake_up", "search", "workspace_search", "cross_search", "save_memory":
			out = append(out, event.Tool)
		}
	}
	return out
}
