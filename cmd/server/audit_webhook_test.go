package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stek0v/levara/pkg/audit"
)

func TestAuditWebhookRuntimeDeliveryReplayAndMetrics(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			store := newBrowserAuthStore(t, dialect)
			received := make(chan audit.SpoolEnvelope, 10)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Events []audit.SpoolEnvelope `json:"events"`
				}
				if r.Header.Get("Authorization") != "Bearer private-test-token" {
					t.Error("missing configured token")
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				for _, event := range body.Events {
					received <- event
				}
				w.WriteHeader(503)
			}))
			defer endpoint.Close()
			cfg := auditWebhookConfig{spool: audit.SpoolConfig{DestinationID: "test", MaxAttempts: 1}, webhook: audit.WebhookConfig{URL: endpoint.URL, Token: "private-test-token", PollInterval: time.Millisecond}, admission: time.Second}
			registry := prometheus.NewRegistry()
			run, err := startAuditWebhook(context.Background(), store.DB, dialect, cfg, registry, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer run.stop()
			run.sink.LogEvent(audit.Event{Type: "write", Source: "workspace.rest", Outcome: "success", ActorID: "forged", Metadata: map[string]any{"text": "secret-document", "actor_id": "forged"}, VerifiedScope: audit.VerifiedScope{ActorID: "alice", TenantID: "tenant-a", Verified: true}})
			var delivered audit.SpoolEnvelope
			select {
			case delivered = <-received:
			case <-time.After(3 * time.Second):
				t.Fatal("no webhook delivery")
			}
			if delivered.ActorID != "alice" || delivered.TenantID != "tenant-a" || !delivered.ScopeVerified {
				t.Fatalf("scope=%+v", delivered)
			}
			raw, _ := json.Marshal(delivered)
			if strings.Contains(string(raw), "secret-document") || strings.Contains(string(raw), "forged") {
				t.Fatalf("payload=%s", raw)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				stats, err := run.spool.Stats(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if stats.Dead == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("failed event did not reach DLQ")
				}
				time.Sleep(time.Millisecond)
			}
			metrics, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, m := range metrics {
				if m.GetName() == "levara_audit_spool_dead" {
					found = m.Metric[0].GetGauge().GetValue() == 1
				}
			}
			if !found {
				t.Fatal("DLQ metric absent")
			}
			run.stop()
			rows, err := run.spool.DeadLetters(context.Background(), "", 100)
			if err != nil || len(rows) != 1 || rows[0].EventID != delivered.EventID {
				t.Fatalf("DLQ=%+v %v", rows, err)
			}
			for _, want := range []int64{1, 0} {
				n, err := run.spool.ResolveDeadLetters(context.Background(), []string{delivered.EventID}, false)
				if err != nil || n != want {
					t.Fatalf("replay n=%d want=%d %v", n, want, err)
				}
			}
			// Restart sends the same event ID; the receiver owns deduplication.
			restarted, err := startAuditWebhook(context.Background(), store.DB, dialect, cfg, registry, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.stop()
			select {
			case again := <-received:
				if again.EventID != delivered.EventID {
					t.Fatal("replay changed event ID")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no restart replay")
			}
		})
	}
}

func TestAuditWebhookConfigurationFailsClosed(t *testing.T) {
	for _, env := range []map[string]string{
		{"AUDIT_WEBHOOK_TOKEN_FILE": "/absent"},
		{"AUDIT_WEBHOOK_URL": "https://siem.example.test/events", "AUDIT_MAX_ATTEMPTS": "zero"},
		{"AUDIT_WEBHOOK_URL": "https://siem.example.test/events", "AUDIT_WEBHOOK_TIMEOUT": "-1s"},
	} {
		if _, err := auditWebhookFromEnv(func(key string) string { return env[key] }); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
