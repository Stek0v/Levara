package http

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	dcdRouteModeOff     = "off"
	dcdRouteObserveMode = "observe"
	dcdRouteBoostMode   = "boost"
	dcdRouteFilterMode  = "filter"
)

func maybeAttachDCDRouteObserve(ctx context.Context, c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest, ownerID string) {
	if !dcdRouteObserveEnabled() || c == nil || cfg.DB == nil {
		return
	}
	teamID, _ := c.Locals("team_id").(string)
	requestedMode := dcdRouteRequestedMode()
	mode := dcdRouteMode()
	start := time.Now()
	debug := fiber.Map{
		"enabled":        true,
		"requested_mode": requestedMode,
		"mode":           mode,
		"boost_active":   mode == dcdRouteBoostMode,
		"filter_active":  false,
	}
	candidates, err := resolveDCDRouteCandidates(ctx, cfg.DB, req.QueryText, dcdRouteScope{
		OwnerID:           ownerID,
		TeamID:            teamID,
		AllowedDatasetIDs: req.AllowedDatasetIDs,
	}, dcdRoutePolicy{
		MaxCandidates:       dcdRouteMaxCandidates(),
		MinConfidence:       dcdRouteMinConfidence(),
		AllowGlobalFallback: true,
	})
	debug["latency_us"] = time.Since(start).Microseconds()
	if err != nil {
		debug["error"] = err.Error()
		c.Locals("dcd_route_debug", debug)
		return
	}
	debug["candidate_count"] = len(candidates)
	debug["candidates"] = candidates
	c.Locals("dcd_route_candidates", candidates)
	if len(candidates) > 0 {
		debug["top_confidence"] = candidates[0].Confidence
	}
	c.Locals("dcd_route_debug", debug)
}

func dcdRouteObserveEnabled() bool {
	switch dcdRouteRequestedMode() {
	case dcdRouteObserveMode, dcdRouteBoostMode, dcdRouteFilterMode:
		return true
	default:
		return false
	}
}

func dcdRouteRequestedMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_DCD_ROUTER"))) {
	case dcdRouteObserveMode, dcdRouteBoostMode, dcdRouteFilterMode:
		return strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_DCD_ROUTER")))
	case "on", "true", "enabled":
		return dcdRouteObserveMode
	default:
		return dcdRouteModeOff
	}
}

func dcdRouteMode() string {
	mode := dcdRouteRequestedMode()
	if mode == dcdRouteFilterMode {
		return dcdRouteBoostMode
	}
	return mode
}

func dcdRouteBoostEnabled() bool {
	return dcdRouteMode() == dcdRouteBoostMode
}

func dcdRouteCandidatesFromCtx(c *fiber.Ctx) []dcdRouteCandidate {
	if c == nil {
		return nil
	}
	candidates, _ := c.Locals("dcd_route_candidates").([]dcdRouteCandidate)
	if !dcdRouteBoostEnabled() {
		return nil
	}
	return candidates
}

func dcdRouteMaxCandidates() int {
	raw := os.Getenv("LEVARA_DCD_ROUTE_MAX_CANDIDATES")
	if raw == "" {
		return 3
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 3
	}
	return value
}

func dcdRouteMinConfidence() float64 {
	raw := os.Getenv("LEVARA_DCD_ROUTE_MIN_CONFIDENCE")
	if raw == "" {
		return 0.05
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 {
		return 0.05
	}
	if value > 1 {
		return 1
	}
	return value
}
