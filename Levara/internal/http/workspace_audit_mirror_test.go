package http

import (
	"testing"

	"github.com/stek0v/levara/pkg/audit"
)

// TestMirrorWorkspaceAuditEventForwardsToSink pins the field mapping from the
// workspace-domain event onto the generic audit.Event handed to the enterprise
// export sink. The helper takes only the narrow AuditConfig group (from_cod.md
// C3), never the full APIConfig.
func TestMirrorWorkspaceAuditEventForwardsToSink(t *testing.T) {
	var got []audit.Event
	ac := AuditConfig{
		WorkspaceAuditSink: audit.EventSinkFunc(func(e audit.Event) {
			got = append(got, e)
		}),
	}

	mirrorWorkspaceAuditEvent(ac, workspaceAuditEvent{
		At:        "2026-06-06T10:00:00Z",
		Source:    "write",
		Operation: "commit",
		ProjectID: "proj-1",
		Branch:    "main",
		UserID:    "user-7",
		Access:    "write",
		Result:    "ok",
		Status:    200,
		Error:     "",
	})

	if len(got) != 1 {
		t.Fatalf("expected exactly 1 forwarded event, got %d", len(got))
	}
	e := got[0]
	if e.TS != "2026-06-06T10:00:00Z" {
		t.Errorf("TS: got %q", e.TS)
	}
	if e.Source != "workspace.write" {
		t.Errorf("Source: got %q want %q", e.Source, "workspace.write")
	}
	if e.Type != "commit" {
		t.Errorf("Type: got %q want %q", e.Type, "commit")
	}
	if e.Subject != "proj-1" {
		t.Errorf("Subject: got %q want %q", e.Subject, "proj-1")
	}
	if e.ActorID != "user-7" {
		t.Errorf("ActorID: got %q want %q", e.ActorID, "user-7")
	}
	if e.Outcome != "ok" {
		t.Errorf("Outcome: got %q want %q", e.Outcome, "ok")
	}
	if e.Metadata["branch"] != "main" {
		t.Errorf("Metadata[branch]: got %v", e.Metadata["branch"])
	}
	if e.Metadata["access"] != "write" {
		t.Errorf("Metadata[access]: got %v", e.Metadata["access"])
	}
	if e.Metadata["status"] != 200 {
		t.Errorf("Metadata[status]: got %v", e.Metadata["status"])
	}
	if _, ok := e.Metadata["error"]; !ok {
		t.Errorf("Metadata[error] key missing")
	}
}

// TestMirrorWorkspaceAuditEventNilSinkNoop confirms the common Personal/Solo Pro
// path — no enterprise sink configured — is a safe no-op rather than a panic.
func TestMirrorWorkspaceAuditEventNilSinkNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil sink must be a no-op, got panic: %v", r)
		}
	}()
	mirrorWorkspaceAuditEvent(AuditConfig{WorkspaceAuditSink: nil}, workspaceAuditEvent{
		Source:    "read",
		Operation: "context",
		ProjectID: "proj-2",
		Result:    "ok",
	})
}
