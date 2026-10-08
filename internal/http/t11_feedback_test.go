package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/router"
)

func t11FeedbackApp(f *documentHTTPFixture, base context.Context) (*fiber.App, *router.AdaptiveWeights) {
	cfg := f.cfg
	cfg.AdaptiveWeights = router.NewAdaptiveWeights(nil, 0.1)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	if base != nil {
		app.Use(func(c *fiber.Ctx) error { c.SetUserContext(base); return c.Next() })
	}
	RegisterFeedbackAPI(app, cfg)
	return app, cfg.AdaptiveWeights
}

func t11FeedbackRequest(t *testing.T, app *fiber.App, method, path, body string) (int, any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp.StatusCode, parsed
}

func t11FeedbackTable(f *documentHTTPFixture) {
	f.exec("ALTER TABLE search_feedback RENAME TO feedback_original")
	f.exec(`CREATE TABLE search_feedback(id TEXT PRIMARY KEY,query TEXT CHECK(query<>'reject'),result_id TEXT DEFAULT '',collection TEXT DEFAULT '',search_type TEXT DEFAULT '',rating INTEGER,comment TEXT DEFAULT '',user_id TEXT DEFAULT '',created_at TEXT DEFAULT '2026-10-06')`)
}

func TestT11FeedbackRESTPersistenceAndReads(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		t11FeedbackTable(f)
		app, weights := t11FeedbackApp(f, nil)
		before := weights.AllWeights()
		status, body := t11FeedbackRequest(t, app, "POST", "/feedback", `{"query":"reject","search_type":"semantic","rating":5}`)
		if status != 503 {
			t.Errorf("rejected INSERT reported %d %v", status, body)
		}
		if !reflect.DeepEqual(before, weights.AllWeights()) {
			t.Error("failed INSERT changed adaptive weights")
		}
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM search_feedback").Scan(&n); err != nil || n != 0 {
			t.Fatalf("rejected row persisted %d %v", n, err)
		}
		for _, rating := range []string{"1", "2"} {
			status, body = t11FeedbackRequest(t, app, "POST", "/feedback", `{"query":"valid","collection":"chosen","search_type":"semantic","rating":`+rating+`}`)
			if status != 201 || body.(map[string]any)["saved"] != true {
				t.Fatalf("valid INSERT %d %v", status, body)
			}
		}
		if weights.GetWeight("semantic") == 1 {
			t.Error("successful INSERT did not update adaptive weights")
		}
		f.exec("INSERT INTO search_feedback(id,query,collection,rating) VALUES('control','elsewhere','other',5)")
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback/stats?collection=chosen", "")
		stats := body.(map[string]any)
		if status != 200 || stats["total"] != float64(2) || stats["avg_rating"] != 1.5 || stats["worst_query"] != "valid" {
			t.Errorf("fractional average/filter %d %v", status, body)
		}
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback/stats?collection=empty", "")
		if status != 200 || body.(map[string]any)["total"] != float64(0) || body.(map[string]any)["worst_query"] != "" {
			t.Errorf("empty stats %d %v", status, body)
		}
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback?collection=chosen", "")
		if status != 200 || len(body.([]any)) != 2 {
			t.Fatalf("list/filter %d %v", status, body)
		}
		// First accumulate valid rows; a later NULL must fail the whole response.
		f.exec("INSERT INTO search_feedback(id,query,collection,rating,comment,created_at) VALUES('bad-scan','valid','chosen',1,NULL,'2026-01-01')")
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback?collection=chosen", "")
		if status != 503 {
			t.Errorf("partial list/scan failure reported %d %v", status, body)
		}
		// COUNT/AVG still work: failure in the second statistics query is observable.
		f.exec("ALTER TABLE search_feedback RENAME COLUMN query TO missing_query")
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback/stats", "")
		if status != 503 {
			t.Errorf("second stats query failure %d %v", status, body)
		}
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback", "")
		if status != 503 {
			t.Errorf("list query failure %d %v", status, body)
		}
		f.exec("DROP TABLE search_feedback")
		status, body = t11FeedbackRequest(t, app, "GET", "/feedback/stats", "")
		if status != 503 {
			t.Errorf("aggregate query failure %d %v", status, body)
		}
	})
}

func TestT11FeedbackRESTCancellationAndPoolWait(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		t11FeedbackTable(f)
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		app, weights := t11FeedbackApp(f, ctx)
		for _, request := range []struct{ method, path, body string }{{"POST", "/feedback", `{"query":"valid","search_type":"semantic","rating":5}`}, {"GET", "/feedback/stats", ""}, {"GET", "/feedback", ""}} {
			status, body := t11FeedbackRequest(t, app, request.method, request.path, request.body)
			if status != 503 {
				t.Errorf("canceled %s %s reported %d %v", request.method, request.path, status, body)
			}
		}
		if len(weights.AllWeights()) != 0 {
			t.Error("canceled INSERT trained adaptive")
		}
		held, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		waitBefore := f.db.Stats().WaitCount
		waitCtx, waitCancel := context.WithCancel(context.Background())
		defer waitCancel()
		waiting, _ := t11FeedbackApp(f, waitCtx)
		result := make(chan int, 1)
		go func() {
			req := httptest.NewRequest("POST", "/feedback", strings.NewReader(`{"query":"valid","rating":5}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := waiting.Test(req, -1)
			if err != nil {
				result <- 0
				return
			}
			defer resp.Body.Close()
			result <- resp.StatusCode
		}()
		deadline := time.Now().Add(2 * time.Second)
		for f.db.Stats().WaitCount == waitBefore && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if f.db.Stats().WaitCount == waitBefore {
			held.Close()
			t.Fatal("request did not reach pool wait")
		}
		waitCancel()
		select {
		case status := <-result:
			if status != 503 {
				t.Errorf("pool cancellation status %d", status)
			}
		case <-time.After(time.Second):
			held.Close()
			<-result
			t.Error("SQL wait ignored caller cancellation")
		}
		held.Close()
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM search_feedback").Scan(&n); err != nil || n != 0 {
			t.Errorf("canceled requests persisted %d rows %v", n, err)
		}
	})
}
