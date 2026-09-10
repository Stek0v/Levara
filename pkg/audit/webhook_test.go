package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func webhookFixture(t *testing.T, s *SQLSpool, url string) *WebhookWorker {
	t.Helper()
	w, err := NewWebhookWorker(s, WebhookConfig{URL: url, Token: "test-token", Timeout: 200 * time.Millisecond, BatchCount: 2, BatchBytes: 4096, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.client.CloseIdleConnections)
	return w
}

func TestWebhookSpoolLostACKAndStableRetry(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		var requests atomic.Int32
		var firstID atomic.Value
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Audit-Destination") != "siem" || strings.Contains(string(body), "test-token") {
				t.Errorf("credential envelope: %s", body)
			}
			var batch struct {
				Events []SpoolEnvelope `json:"events"`
			}
			if err := json.Unmarshal(body, &batch); err != nil || len(batch.Events) != 1 {
				t.Errorf("payload: %s %v", body, err)
				w.WriteHeader(400)
				return
			}
			if requests.Add(1) == 1 {
				firstID.Store(batch.Events[0].EventID)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			if batch.Events[0].EventID != firstID.Load() {
				t.Errorf("retry changed ID")
			}
			w.WriteHeader(204)
		}))
		defer server.Close()
		id, err := s.AdmitEntry(ctx, Entry{Tool: "search", Outcome: OutcomeOK})
		if err != nil {
			t.Fatal(err)
		}
		worker := webhookFixture(t, s, server.URL)
		if n, err := worker.DeliverOnce(ctx); n != 0 || !errors.Is(err, ErrWebhookDelivery) {
			t.Fatalf("lost ACK: %d %v", n, err)
		}
		time.Sleep(3 * time.Millisecond)
		if n, err := worker.DeliverOnce(ctx); n != 1 || err != nil {
			t.Fatalf("retry ACK: %d %v", n, err)
		}
		if id != firstID.Load() || requests.Load() != 2 {
			t.Fatalf("retry identity: %s %s %d", id, firstID.Load(), requests.Load())
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Pending != 0 || stats.Delivered != 1 || stats.Retried != 1 {
			t.Fatalf("delivery stats: %+v %v", stats, err)
		}
	})
}

func TestWebhookSpoolHTTPFailuresAndRedirectDenied(t *testing.T) {
	for _, status := range []int{503, 429, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
				s := spoolFixture(t, db, dialect, func(c *SpoolConfig) { c.MaxAttempts = 2 })
				ctx := context.Background()
				var redirected atomic.Int32
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
				defer target.Close()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", target.URL)
					w.Header().Set("Retry-After", "9999999999")
					w.WriteHeader(status)
					w.Write([]byte("private error body"))
				}))
				defer server.Close()
				worker := webhookFixture(t, s, server.URL)
				if _, err := s.AdmitEntry(ctx, Entry{Tool: "search"}); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if _, err := worker.DeliverOnce(ctx); !errors.Is(err, ErrWebhookDelivery) || strings.Contains(err.Error(), "private") {
						t.Fatalf("HTTP failure: %v", err)
					}
					time.Sleep(3 * time.Millisecond)
				}
				stats, err := s.Stats(ctx)
				if err != nil || stats.Dead != 1 || stats.Pending != 0 || stats.Retried != 1 || redirected.Load() != 0 {
					t.Fatalf("bounded failed delivery: %+v redirect=%d err=%v", stats, redirected.Load(), err)
				}
			})
		})
	}
}

func TestWebhookSpoolTwoWorkersAndBatchBounds(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		var mu sync.Mutex
		seen := map[string]int{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Events []SpoolEnvelope `json:"events"`
			}
			if err := json.Unmarshal(body, &payload); err != nil || len(body) > 4096 || len(payload.Events) > 2 {
				t.Errorf("batch exceeded bound: count=%d bytes=%d err=%v", len(payload.Events), len(body), err)
			}
			mu.Lock()
			for _, e := range payload.Events {
				seen[e.EventID]++
			}
			mu.Unlock()
			w.WriteHeader(200)
		}))
		defer server.Close()
		for i := 0; i < 8; i++ {
			if err := s.Admit(ctx, spoolTestEnvelope()); err != nil {
				t.Fatal(err)
			}
		}
		other, err := NewSQLSpool(db, s.cfg)
		if err != nil {
			t.Fatal(err)
		}
		workers := []*WebhookWorker{webhookFixture(t, s, server.URL), webhookFixture(t, other, server.URL)}
		var wg sync.WaitGroup
		for _, worker := range workers {
			wg.Add(1)
			go func(w *WebhookWorker) {
				defer wg.Done()
				for {
					n, err := w.DeliverOnce(ctx)
					if err != nil {
						t.Errorf("delivery: %v", err)
						return
					}
					if n == 0 {
						return
					}
				}
			}(worker)
		}
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 8 {
			t.Fatalf("delivered IDs: %v", seen)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("live duplicate lease %s: %d", id, n)
			}
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Delivered != 8 || stats.Pending != 0 {
			t.Fatalf("two-worker stats: %+v %v", stats, err)
		}
	})
}

func TestWebhookSpoolSlowReceiverAndBoundedShutdown(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		started := make(chan struct{}, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}))
		defer server.Close()
		worker := webhookFixture(t, s, server.URL)
		worker.cfg.Timeout = 30 * time.Millisecond
		worker.client.Timeout = 30 * time.Millisecond
		if _, err := s.AdmitEntry(ctx, Entry{Tool: "search"}); err != nil {
			t.Fatal(err)
		}
		before := time.Now()
		if _, err := worker.DeliverOnce(ctx); !errors.Is(err, ErrWebhookDelivery) {
			t.Fatalf("slow delivery: %v", err)
		}
		if time.Since(before) > 500*time.Millisecond {
			t.Fatal("HTTP timeout was not bounded")
		}
		<-started
		time.Sleep(3 * time.Millisecond)
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- worker.Run(runCtx, nil) }()
		<-started
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatal("shutdown blocked on receiver")
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Pending != 1 || stats.Delivered != 0 {
			t.Fatalf("shutdown lost pending event: %+v %v", stats, err)
		}
	})
}

func TestWebhookSpoolHTTPDoesNotHoldSQLTransaction(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		entered, release := make(chan struct{}), make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) }))
		defer server.Close()
		worker := webhookFixture(t, s, server.URL)
		if err := s.Admit(ctx, spoolTestEnvelope()); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := worker.DeliverOnce(ctx); done <- err }()
		<-entered
		admitCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err := s.Admit(admitCtx, spoolTestEnvelope())
		cancel()
		close(release)
		if err != nil {
			t.Errorf("HTTP held quota lock: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		server.Close()
		if _, err := worker.DeliverOnce(ctx); !errors.Is(err, ErrWebhookDelivery) {
			t.Fatalf("offline receiver: %v", err)
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Pending != 1 || stats.Delivered != 1 {
			t.Fatalf("offline event lost: %+v %v", stats, err)
		}
	})
}

func TestWebhookSpoolConfigurationBoundary(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		for _, url := range []string{"http://example.invalid/hook", "https://user:password@example.invalid/hook", "https://example.invalid/hook?token=secret", "file:///private/log"} {
			if _, err := NewWebhookWorker(s, WebhookConfig{URL: url, Timeout: time.Millisecond}); err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") {
				t.Errorf("unsafe destination: %v", err)
			}
		}
		if _, err := NewWebhookWorker(s, WebhookConfig{URL: "https://example.invalid/hook", Timeout: s.cfg.LeaseDuration}); err == nil {
			t.Fatal("HTTP timeout can outlive lease")
		}
		if _, err := NewWebhookWorker(s, WebhookConfig{URL: "https://example.invalid/hook", Timeout: time.Millisecond, Token: "a\r\nb"}); err == nil {
			t.Fatal("header injection accepted")
		}
	})
}
