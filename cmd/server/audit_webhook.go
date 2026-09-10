package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stek0v/levara/pkg/audit"
)

type auditWebhookConfig struct {
	spool     audit.SpoolConfig
	webhook   audit.WebhookConfig
	admission time.Duration
}

func auditWebhookFromEnv(env func(string) string) (auditWebhookConfig, error) {
	cfg := auditWebhookConfig{admission: time.Second}
	cfg.webhook.URL = strings.TrimSpace(env("AUDIT_WEBHOOK_URL"))
	keys := []string{"AUDIT_WEBHOOK_TOKEN_FILE", "AUDIT_DESTINATION_ID", "AUDIT_QUEUE_MAX_EVENTS", "AUDIT_QUEUE_MAX_BYTES", "AUDIT_MAX_ATTEMPTS", "AUDIT_ADMISSION_TIMEOUT", "AUDIT_WEBHOOK_TIMEOUT"}
	if cfg.webhook.URL == "" {
		for _, key := range keys {
			if env(key) != "" {
				return cfg, fmt.Errorf("%s requires AUDIT_WEBHOOK_URL", key)
			}
		}
		return cfg, nil
	}
	cfg.spool.DestinationID = env("AUDIT_DESTINATION_ID")
	if cfg.spool.DestinationID == "" {
		hash := sha256.Sum256([]byte(cfg.webhook.URL))
		cfg.spool.DestinationID = hex.EncodeToString(hash[:16])
	}
	if path := env("AUDIT_WEBHOOK_TOKEN_FILE"); path != "" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 || info.Mode().Perm()&0077 != 0 {
			return cfg, errors.New("audit token file must be a private regular file up to 8192 bytes")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, errors.New("audit token file unavailable")
		}
		cfg.webhook.Token = strings.TrimSpace(string(raw))
		if cfg.webhook.Token == "" {
			return cfg, errors.New("audit token file is empty")
		}
	}
	for key, target := range map[string]*int{"AUDIT_QUEUE_MAX_EVENTS": &cfg.spool.MaxEvents, "AUDIT_MAX_ATTEMPTS": &cfg.spool.MaxAttempts} {
		if raw := env(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				return cfg, fmt.Errorf("invalid %s", key)
			}
			*target = n
		}
	}
	if raw := env("AUDIT_QUEUE_MAX_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			return cfg, errors.New("invalid AUDIT_QUEUE_MAX_BYTES")
		}
		cfg.spool.MaxBytes = n
	}
	for key, target := range map[string]*time.Duration{"AUDIT_ADMISSION_TIMEOUT": &cfg.admission, "AUDIT_WEBHOOK_TIMEOUT": &cfg.webhook.Timeout} {
		if raw := env(key); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return cfg, fmt.Errorf("invalid %s", key)
			}
			*target = d
		}
	}
	if cfg.admission > time.Minute {
		return cfg, errors.New("AUDIT_ADMISSION_TIMEOUT must be at most 1m")
	}
	// Dialect changes SQL placeholders only; validate bounds before any database exists.
	validationSpool := cfg.spool
	validationSpool.Dialect = "sqlite"
	if err := audit.ValidateWebhookConfiguration(validationSpool, cfg.webhook); err != nil {
		return cfg, err
	}
	return cfg, nil
}

type auditWebhookRuntime struct {
	spool *audit.SQLSpool
	sink  *audit.SpoolSink
	stop  func()
}

func startAuditWebhook(ctx context.Context, db *sql.DB, dialect string, cfg auditWebhookConfig, registry prometheus.Registerer, report func(error)) (*auditWebhookRuntime, error) {
	if cfg.webhook.URL == "" {
		return nil, nil
	}
	cfg.spool.Dialect = dialect
	spool, err := audit.NewSQLSpool(db, cfg.spool)
	if err != nil {
		return nil, err
	}
	worker, err := audit.NewWebhookWorker(spool, cfg.webhook)
	if err != nil {
		return nil, err
	}
	collector := &auditSpoolCollector{spool: spool}
	onError := func(err error) {
		collector.failures.Add(1)
		if report != nil {
			report(err)
		}
	}
	sink, err := audit.NewSpoolSink(spool, cfg.admission, onError)
	if err != nil {
		return nil, err
	}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = spool.EnsureSchema(setup)
	cancel()
	if err != nil {
		return nil, err
	}
	if err := registry.Register(collector); err != nil {
		return nil, err
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = worker.Run(workerCtx, onError) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancelWorker(); <-done; registry.Unregister(collector) }) }
	return &auditWebhookRuntime{spool: spool, sink: sink, stop: stop}, nil
}

var auditSpoolMetrics = []struct {
	name, help string
	counter    bool
}{
	{"up", "Whether the durable audit queue could be read.", false},
	{"pending", "Pending and in-flight audit events.", false},
	{"dead", "Audit events awaiting operator action.", false},
	{"pending_bytes", "Bytes in pending and in-flight audit events.", false},
	{"dead_bytes", "Bytes in dead-letter audit events.", false},
	{"oldest_pending_seconds", "Age of the oldest pending audit event.", false},
	{"oldest_dead_seconds", "Age of the oldest dead-letter audit event.", false},
	{"delivered_total", "Durably acknowledged audit events.", true},
	{"retried_total", "Audit delivery retries.", true},
	{"rejected_total", "Audit admissions rejected by the durable queue.", true},
	{"failures_total", "Audit admission or delivery failures observed by this process.", true},
}

type auditSpoolCollector struct {
	spool    *audit.SQLSpool
	failures atomic.Uint64
}

func auditSpoolDesc(i int) *prometheus.Desc {
	m := auditSpoolMetrics[i]
	return prometheus.NewDesc("levara_audit_spool_"+m.name, m.help, nil, nil)
}
func (c *auditSpoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for i := range auditSpoolMetrics {
		ch <- auditSpoolDesc(i)
	}
}
func (c *auditSpoolCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	s, err := c.spool.Stats(ctx)
	cancel()
	values := []float64{1, float64(s.Pending), float64(s.Dead), float64(s.PendingBytes), float64(s.DeadBytes), s.OldestPendingAge.Seconds(), s.OldestDeadAge.Seconds(), float64(s.Delivered), float64(s.Retried), float64(s.Rejected), float64(c.failures.Load())}
	if err != nil {
		values[0] = 0
	}
	for i, value := range values {
		if err != nil && i != 0 && i != len(values)-1 {
			continue
		} // Missing SQL state must not look like an empty queue.
		typ := prometheus.GaugeValue
		if auditSpoolMetrics[i].counter {
			typ = prometheus.CounterValue
		}
		ch <- prometheus.MustNewConstMetric(auditSpoolDesc(i), typ, value)
	}
}
