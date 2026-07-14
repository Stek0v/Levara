package trajectory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/audit"
)

func TestAnalyzeBehaviorConsultRepeatAndErrors(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	recall := ev("1", ts(base, 0), "recall_memory")
	recall.TraceID = "a"
	recall.ResultCount = 1
	save1 := ev("2", ts(base, time.Second), "save_memory")
	save1.TraceID = "a"
	save1.Args = rawArgs(map[string]any{"key": "k", "room": "mcp", "hall": "decision"})
	save2 := ev("3", ts(base, 2*time.Second), "save_memory")
	save2.TraceID = "b"
	save2.Args = rawArgs(map[string]any{"key": "k", "room": "", "hall": ""})
	badHall := ev("4", ts(base, 3*time.Second), "save_memory")
	badHall.TraceID = "b"
	badHall.Outcome = string(audit.OutcomeClientError)
	badHall.ErrorMessage = "unknown hall"
	badHall.Args = rawArgs(map[string]any{"key": "x", "room": "mcp", "hall": "bad"})
	empty := ev("5", ts(base, 4*time.Second), "recall_memory")
	empty.TraceID = "b"
	empty.ZeroResult = true
	traces := Build([]Event{recall, save1, save2, badHall, empty}, true)
	got := AnalyzeBehavior(traces)
	if got.TotalTrajectories != 2 {
		t.Fatalf("trajectories=%d", got.TotalTrajectories)
	}
	if got.RecallBeforeSaveRate != 1.0/3.0 {
		t.Fatalf("recall-before-save=%f", got.RecallBeforeSaveRate)
	}
	if got.RepeatSaveRate != 1.0/3.0 {
		t.Fatalf("repeat-save=%f", got.RepeatSaveRate)
	}
	if got.SaveWithoutRoomOrHallCount != 1 {
		t.Fatalf("missing room/hall=%d", got.SaveWithoutRoomOrHallCount)
	}
	if got.UnknownHallErrorCount != 1 || got.ToolErrorsByTool["save_memory"] != 1 {
		t.Fatalf("errors=%+v unknown=%d", got.ToolErrorsByTool, got.UnknownHallErrorCount)
	}
	if got.EmptyRecallRate != 0.5 {
		t.Fatalf("empty recall=%f", got.EmptyRecallRate)
	}
}

func TestAnalyzeBehaviorUsesAuditFlagsWithoutArgs(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	recall := ev("1", ts(base, 0), "recall_memory")
	recall.TraceID = "a"
	recall.ResultCount = 1
	save := ev("2", ts(base, time.Second), "save_memory")
	save.TraceID = "a"
	save.BlindSave = true
	save.RepeatSave = true
	traces := Build([]Event{recall, save}, true)
	got := AnalyzeBehavior(traces)
	if got.RepeatSaveRate != 1 {
		t.Fatalf("repeat rate=%f want 1", got.RepeatSaveRate)
	}
	if len(got.ProblemTrajectories) != 1 || got.ProblemTrajectories[0].BlindSaves != 1 || got.ProblemTrajectories[0].RepeatSaves != 1 {
		t.Fatalf("problems=%+v", got.ProblemTrajectories)
	}
}

func TestAnalyzeBehaviorEmptySelectionReturnsStableZeroShape(t *testing.T) {
	got := AnalyzeBehavior(nil)
	if got.TotalTrajectories != 0 || got.RecallBeforeSaveRate != 0 || got.ToolErrorsByTool == nil {
		t.Fatalf("summary=%+v", got)
	}
	if got.ProblemTrajectories == nil || len(got.ProblemTrajectories) != 0 {
		t.Fatalf("problem shape=%+v", got.ProblemTrajectories)
	}
}

func TestAnalyzeBehaviorContextBytesPerTrajectory(t *testing.T) {
	base := time.Date(2026, 7, 13, 1, 0, 0, 0, time.UTC)
	a := ev("1", ts(base, 0), "wake_up")
	a.TraceID = "a"
	a.RequestBytes = 10
	a.ResponseBytes = 90
	b := ev("2", ts(base, time.Second), "save_memory")
	b.TraceID = "b"
	b.RequestBytes = 100
	b.ResponseBytes = 300
	got := AnalyzeBehavior(Build([]Event{a, b}, true))
	if got.ContextBytesPerTrajectory != 250 {
		t.Fatalf("context bytes/trajectory=%f want 250", got.ContextBytesPerTrajectory)
	}
	if got.MemoryOpsPerTrajectory != 1 {
		t.Fatalf("memory ops/trajectory=%f want 1", got.MemoryOpsPerTrajectory)
	}
}

func rawArgs(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return b
}
