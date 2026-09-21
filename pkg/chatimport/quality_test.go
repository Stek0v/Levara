package chatimport

import (
	"strings"
	"testing"
)

func qualityAllMessages(conv *Conversation) []Message {
	var out []Message
	for _, m := range conv.Messages {
		if m.Kind == KindSystem || m.Content == "" {
			continue
		}
		out = append(out, m)
	}
	return out
}

func TestQuality1EveryMessageInSegment(t *testing.T) {
	conv := &Conversation{
		Platform:  PlatformCodex,
		SessionID: "q1",
		Title:     "All types",
		Model:     "test",
		Messages: []Message{
			{ExternalID: "m1", Ordinal: 0, Role: "user", Kind: KindText, Content: "user question"},
			{ExternalID: "m2", Ordinal: 1, Role: "assistant", Kind: KindReasoning, Content: "thinking"},
			{ExternalID: "m3", Ordinal: 2, Role: "assistant", Kind: KindText, Content: "the answer"},
			{ExternalID: "m4", Ordinal: 3, Role: "assistant", Kind: KindToolCall, Content: "exec ls", Metadata: map[string]string{"tool_name": "exec"}},
			{ExternalID: "m5", Ordinal: 4, Role: "tool", Kind: KindToolResult, Content: "file1.txt"},
		},
	}

	full := RenderConversationMarkdown(conv)
	for _, m := range qualityAllMessages(conv) {
		if !strings.Contains(full, m.Content) {
			t.Errorf("Q1: message %s (kind=%s) not in render", m.ExternalID, m.Kind)
		}
	}

	segs := RenderConversationSegments(conv, 200)
	if len(segs) < 2 {
		t.Fatalf("expected multi-segment, got %d", len(segs))
	}
	combined := ""
	for _, s := range segs {
		combined += s.Content + "\n"
	}
	for _, m := range qualityAllMessages(conv) {
		if !strings.Contains(combined, m.Content) {
			t.Errorf("Q1: message %s lost in segmentation", m.ExternalID)
		}
	}
}

func TestQuality2AllSessionTypesSearchable(t *testing.T) {
	cases := []struct {
		name string
		conv *Conversation
	}{
		{"small", &Conversation{Platform: PlatformCodex, SessionID: "s1", Messages: []Message{
			{Role: "user", Kind: KindText, Content: "short q"},
			{Role: "assistant", Kind: KindText, Content: "short a"},
		}}},
		{"tools", &Conversation{Platform: PlatformCodex, SessionID: "s2", Messages: []Message{
			{Role: "user", Kind: KindText, Content: "run tests"},
			{Role: "assistant", Kind: KindToolCall, Content: "exec npm test"},
			{Role: "tool", Kind: KindToolResult, Content: "all pass"},
			{Role: "assistant", Kind: KindText, Content: "done"},
		}}},
		{"reasoning", &Conversation{Platform: PlatformCodex, SessionID: "s3", Messages: []Message{
			{Role: "user", Kind: KindText, Content: "why"},
			{Role: "assistant", Kind: KindReasoning, Content: "because"},
			{Role: "assistant", Kind: KindText, Content: "therefore"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := RenderConversationMarkdown(tc.conv)
			if len(full) < 30 {
				t.Errorf("Q2: %s too short: %d bytes", tc.name, len(full))
			}
			for _, m := range qualityAllMessages(tc.conv) {
				if !strings.Contains(full, m.Content) {
					t.Errorf("Q2: %s: kind=%s not searchable", tc.name, m.Kind)
				}
			}
		})
	}
}

func TestQuality3SegmentsIndependentlySearchable(t *testing.T) {
	conv := &Conversation{
		Platform:  PlatformCodex,
		SessionID: "q3-session",
		Title:     "Searchable",
		Model:     "m",
		Messages: []Message{
			{Role: "user", Kind: KindText, Content: strings.Repeat("alpha ", 50)},
			{Role: "assistant", Kind: KindText, Content: strings.Repeat("beta ", 50)},
			{Role: "user", Kind: KindText, Content: strings.Repeat("gamma ", 50)},
			{Role: "assistant", Kind: KindText, Content: strings.Repeat("delta ", 50)},
		},
	}
	segs := RenderConversationSegments(conv, 300)
	if len(segs) < 2 {
		t.Fatalf("expected multi-segment, got %d", len(segs))
	}
	for i, s := range segs {
		if !strings.Contains(s.Content, conv.SessionID) {
			t.Errorf("Q3: segment %d missing session_id", i+1)
		}
		if !strings.Contains(s.Content, conv.Title) {
			t.Errorf("Q3: segment %d missing title", i+1)
		}
		if len(s.Content) < 50 {
			t.Errorf("Q3: segment %d too short", i+1)
		}
	}
}

func TestQuality4DistillProvenance(t *testing.T) {
	provenance := "[source: codex session abc123, Test Title]"
	if !strings.Contains(provenance, "codex") {
		t.Error("Q4: provenance missing platform")
	}
	if !strings.Contains(provenance, "abc123") {
		t.Error("Q4: provenance missing session ID")
	}
}

func TestQuality5IncrementalCorrectness(t *testing.T) {
	base := &Conversation{
		Platform: PlatformCodex, SessionID: "q5", Title: "Growing",
		Messages: []Message{
			{ExternalID: "m1", Role: "user", Kind: KindText, Content: "original q"},
			{ExternalID: "m2", Role: "assistant", Kind: KindText, Content: "original a"},
		},
	}
	v1 := RenderConversationMarkdown(base)

	grown := *base
	grown.Messages = append(grown.Messages,
		Message{ExternalID: "m3", Role: "user", Kind: KindText, Content: "follow-up q"},
		Message{ExternalID: "m4", Role: "assistant", Kind: KindText, Content: "follow-up a"},
	)
	v2 := RenderConversationMarkdown(&grown)

	for _, m := range base.Messages {
		if !strings.Contains(v2, m.Content) {
			t.Errorf("Q5: old message %s lost after growth", m.ExternalID)
		}
	}
	if !strings.Contains(v2, "follow-up q") {
		t.Error("Q5: new message not in re-render")
	}
	if len(v2) <= len(v1) {
		t.Errorf("Q5: v2 (%d) not longer than v1 (%d)", len(v2), len(v1))
	}
}
