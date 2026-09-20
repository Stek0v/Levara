package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrWebhookDelivery = errors.New("audit webhook batch was not acknowledged")

type WebhookConfig struct {
	URL, Token             string // fixed trusted configuration, never queued or logged
	BatchCount, BatchBytes int
	Timeout, PollInterval  time.Duration
}

type WebhookWorker struct {
	spool  *SQLSpool
	cfg    WebhookConfig
	client *http.Client
}

func NewWebhookWorker(spool *SQLSpool, cfg WebhookConfig) (*WebhookWorker, error) {
	if spool == nil {
		return nil, errors.New("audit spool required")
	}
	var err error
	cfg, err = cfg.normalized(spool.cfg)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// A dedicated client never follows redirects with credentials or payload.
	client := &http.Client{Transport: transport, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &WebhookWorker{spool: spool, cfg: cfg, client: client}, nil
}

// ValidateWebhookConfiguration performs the same checks as startup without I/O.
func ValidateWebhookConfiguration(spool SpoolConfig, cfg WebhookConfig) error {
	spool, err := spool.normalized()
	if err != nil {
		return err
	}
	_, err = cfg.normalized(spool)
	return err
}

func (cfg WebhookConfig) normalized(spool SpoolConfig) (WebhookConfig, error) {
	if cfg.BatchCount == 0 {
		cfg.BatchCount = 100
	}
	if cfg.BatchBytes == 0 {
		cfg.BatchBytes = 256 << 10
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Second
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname()))) || strings.ContainsAny(cfg.Token, "\r\n") || len(cfg.Token) > 8192 || cfg.BatchCount < 1 || cfg.BatchCount > 1000 || cfg.BatchBytes < spool.MaxEventBytes+len(`{"events":[]}`) || cfg.BatchBytes > 4<<20 || cfg.Timeout <= 0 || cfg.Timeout >= spool.LeaseDuration || cfg.PollInterval <= 0 || cfg.PollInterval > time.Minute {
		return cfg, errors.New("invalid audit webhook configuration")
	}
	return cfg, nil
}

// isLoopbackHost reports whether host is exactly "localhost" or a loopback IP
// literal. Truth table: "localhost" -> true; any parseable loopback IP -> true;
// every other value (including non-IP hostnames and non-loopback IPs) -> false.
func isLoopbackHost(host string) bool {
	return host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

// DeliverOnce leases a bounded batch, releases SQL locks, then sends it. Any
// 2xx acknowledges the complete batch; receivers must durably accept all rows
// before acknowledging and deduplicate event_id when a response is lost.
func (w *WebhookWorker) DeliverOnce(ctx context.Context) (int, error) {
	batch, err := w.spool.claim(ctx, w.cfg.BatchCount, w.cfg.BatchBytes)
	if err != nil || len(batch) == 0 {
		return 0, err
	}
	events := make([]json.RawMessage, 0, len(batch))
	for _, c := range batch {
		events = append(events, json.RawMessage(c.payload))
	}
	body, err := json.Marshal(struct {
		Events []json.RawMessage `json:"events"`
	}{events})
	if err != nil {
		return 0, err
	}
	deadline := min(time.Now().Add(w.cfg.Timeout).UnixMilli(), batch[0].until-1)
	requestCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(deadline))
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return 0, ErrWebhookDelivery
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Audit-Destination", w.spool.cfg.DestinationID)
	if w.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
	}
	res, deliveryErr := w.client.Do(req)
	status := 0
	success := false
	if res != nil {
		status = res.StatusCode
		success = deliveryErr == nil && status >= 200 && status < 300
		res.Body.Close()
	}
	// Parent cancellation intentionally leaves an outstanding lease for
	// restart/reclaim. Never create an unbounded shutdown flush transaction.
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	settleCtx, settleCancel := context.WithTimeout(ctx, w.cfg.Timeout)
	defer settleCancel()
	if err := w.spool.settle(settleCtx, batch, status, success); err != nil {
		return 0, err
	}
	if !success {
		return 0, ErrWebhookDelivery
	}
	return len(batch), nil
}

// Run owns no unbounded queue or shutdown drain. Cancellation interrupts HTTP
// and SQL; remaining durable rows are reclaimed after lease expiry on restart.
func (w *WebhookWorker) Run(ctx context.Context, onError func(error)) error {
	defer w.client.CloseIdleConnections()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := w.DeliverOnce(ctx)
		if err != nil && ctx.Err() == nil && onError != nil {
			onError(ErrWebhookDelivery)
		}
		if n > 0 {
			continue
		}
		timer := time.NewTimer(w.cfg.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
