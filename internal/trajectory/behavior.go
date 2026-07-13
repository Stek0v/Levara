package trajectory

import (
	"encoding/json"
	"strings"

	"github.com/stek0v/levara/pkg/audit"
)

type BehaviorSummary struct {
	TotalTrajectories          int              `json:"total_trajectories"`
	TotalEvents                int              `json:"total_events"`
	MemoryOps                  int              `json:"memory_ops"`
	RecallBeforeSaveRate       float64          `json:"recall_before_save_rate"`
	RepeatSaveRate             float64          `json:"repeat_save_rate"`
	ZeroResultRate             float64          `json:"zero_result_rate"`
	EmptyRecallRate            float64          `json:"empty_recall_rate"`
	MemoryOpsPerTrajectory     float64          `json:"memory_ops_per_trajectory"`
	ContextBytesPerTrajectory  float64          `json:"context_bytes_per_trajectory"`
	SaveWithoutRoomOrHallCount int              `json:"save_without_room_or_hall_count"`
	UnknownHallErrorCount      int              `json:"unknown_hall_error_count"`
	ToolErrorsByTool           map[string]int   `json:"tool_errors_by_tool"`
	ProblemTrajectories        []ProblemSummary `json:"problem_trajectories"`
}

type ProblemSummary struct {
	ID              string `json:"id"`
	Collection      string `json:"collection,omitempty"`
	ClientName      string `json:"client_name,omitempty"`
	RepeatSaves     int    `json:"repeat_saves"`
	BlindSaves      int    `json:"blind_saves"`
	ZeroResults     int    `json:"zero_results"`
	Errors          int    `json:"errors"`
	ContextBytes    int    `json:"context_bytes"`
	MemoryOperation int    `json:"memory_ops"`
}

func AnalyzeBehavior(traces []Trajectory) BehaviorSummary {
	out := BehaviorSummary{ToolErrorsByTool: map[string]int{}}
	if len(traces) == 0 {
		out.ProblemTrajectories = []ProblemSummary{}
		return out
	}
	out.TotalTrajectories = len(traces)
	var saveCount, saveAfterConsult, repeatSave, searchOrRecallCount, emptyRecall int
	seenSaveKeys := map[string]bool{}
	for _, tr := range traces {
		out.TotalEvents += tr.EventCount
		out.MemoryOps += tr.Counters.SearchCount + tr.Counters.RecallCount + tr.Counters.SaveCount
		out.ContextBytesPerTrajectory += float64(tr.Counters.RequestBytes + tr.Counters.ResponseBytes)
		problem := ProblemSummary{ID: tr.ID, Collection: tr.Collection, ClientName: tr.ClientName, ZeroResults: tr.Counters.ZeroResultCount, Errors: tr.Counters.ErrorCount, ContextBytes: tr.Counters.RequestBytes + tr.Counters.ResponseBytes, MemoryOperation: tr.Counters.SearchCount + tr.Counters.RecallCount + tr.Counters.SaveCount}
		consulted := false
		for _, event := range tr.Events {
			if event.Outcome != "" && event.Outcome != string(audit.OutcomeOK) {
				out.ToolErrorsByTool[event.Tool]++
				if event.Tool == "save_memory" && strings.Contains(strings.ToLower(event.ErrorMessage), "hall") {
					out.UnknownHallErrorCount++
				}
			}
			if isConsultTool(event.Tool) {
				searchOrRecallCount++
				if event.ZeroResult || event.ResultCount == 0 {
					emptyRecall++
				}
				consulted = true
			}
			if event.Tool != "save_memory" {
				continue
			}
			saveCount++
			args := eventArgs(event)
			if strings.TrimSpace(argString(args, "room")) == "" || strings.TrimSpace(argString(args, "hall")) == "" {
				out.SaveWithoutRoomOrHallCount++
			}
			if consulted {
				saveAfterConsult++
			} else {
				problem.BlindSaves++
			}
			if event.BlindSave && consulted {
				problem.BlindSaves++
			}
			key := argString(args, "key")
			if event.RepeatSave {
				repeatSave++
				problem.RepeatSaves++
			} else if key != "" {
				repeatKey := tr.Collection + "\x00" + key
				if seenSaveKeys[repeatKey] {
					repeatSave++
					problem.RepeatSaves++
				}
				seenSaveKeys[repeatKey] = true
			}
		}
		if problem.RepeatSaves > 0 || problem.BlindSaves > 0 || problem.ZeroResults > 0 || problem.Errors > 0 {
			out.ProblemTrajectories = append(out.ProblemTrajectories, problem)
		}
	}
	if saveCount > 0 {
		out.RecallBeforeSaveRate = float64(saveAfterConsult) / float64(saveCount)
		out.RepeatSaveRate = float64(repeatSave) / float64(saveCount)
	}
	if out.TotalEvents > 0 {
		var zero int
		for _, tr := range traces {
			zero += tr.Counters.ZeroResultCount
		}
		out.ZeroResultRate = float64(zero) / float64(out.TotalEvents)
	}
	if searchOrRecallCount > 0 {
		out.EmptyRecallRate = float64(emptyRecall) / float64(searchOrRecallCount)
	}
	out.MemoryOpsPerTrajectory = float64(out.MemoryOps) / float64(len(traces))
	out.ContextBytesPerTrajectory = out.ContextBytesPerTrajectory / float64(len(traces))
	return out
}

func isConsultTool(tool string) bool {
	switch tool {
	case "recall_memory", "list_memories", "wake_up", "search", "workspace_search", "cross_search":
		return true
	default:
		return false
	}
}

func eventArgs(event Event) map[string]any {
	if len(event.Args) == 0 {
		return nil
	}
	var args map[string]any
	_ = json.Unmarshal(event.Args, &args)
	return args
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}
