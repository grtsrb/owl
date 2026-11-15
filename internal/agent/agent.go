package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"service-inventory/internal/snapshot"
)

type Config struct {
	ServerURL string
	Token     string
	Interval  time.Duration
	// Timeout bounds one collect+push cycle.
	Timeout time.Duration
	// AllowInsecureHTTP permits http:// server URLs (local development only).
	AllowInsecureHTTP bool
}

// Validate checks the config. Plain http:// is refused unless AllowInsecureHTTP is set, so a
// token is never sent in cleartext by accident.
func (c Config) Validate() error {
	if c.ServerURL == "" || c.Token == "" {
		return errors.New("server URL (INV_SERVER_URL) and token (INV_TOKEN) are required")
	}
	if strings.HasPrefix(c.ServerURL, "http://") && !c.AllowInsecureHTTP {
		return errors.New("refusing to send the token over plain http; use https or set INV_ALLOW_INSECURE_HTTP=true (local dev only)")
	}
	return nil
}

type Agent struct {
	Collector *Collector
	Config    Config
	HTTP      *http.Client
	Log       *slog.Logger
}

// Run collects and pushes every Interval until ctx is cancelled. Failures are logged and retried
// on the next tick; a short jitter spreads agents so they don't all hit the server at once.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Config.Validate(); err != nil {
		return err
	}
	jitter := time.Duration(rand.Int64N(int64(30 * time.Second)))
	a.Log.Info("agent started", "server", a.Config.ServerURL, "interval", a.Config.Interval, "firstRunIn", jitter.Round(time.Second))
	timer := time.NewTimer(jitter)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := a.RunOnce(ctx); err != nil {
			a.Log.Error("cycle failed", "err", err)
		}
		timer.Reset(a.Config.Interval)
	}
}

func (a *Agent) RunOnce(ctx context.Context) error {
	if err := a.Config.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, a.Config.Timeout)
	defer cancel()
	start := time.Now()
	snap, err := a.Collector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	if err := a.push(ctx, snap); err != nil {
		return err
	}
	a.Log.Info("snapshot pushed", "workloads", len(snap.Workloads), "nodes", len(snap.Cluster.Nodes),
		"took", time.Since(start).Round(time.Millisecond))
	return nil
}

// push sends the snapshot, retrying transient failures (network errors, 5xx, 429) with backoff.
func (a *Agent) push(ctx context.Context, snap *snapshot.Snapshot) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(snap); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	body := buf.Bytes()
	url := strings.TrimRight(a.Config.ServerURL, "/") + snapshot.IngestPath

	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		retry, err := a.pushOnce(ctx, url, body)
		if err == nil {
			return nil
		}
		if !retry || attempt == 4 {
			return fmt.Errorf("push (attempt %d): %w", attempt, err)
		}
		a.Log.Warn("push failed, retrying", "attempt", attempt, "in", backoff, "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

func (a *Agent) pushOnce(ctx context.Context, url string, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+a.Config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("User-Agent", "service-inventory-agent/"+a.Collector.Version)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	default: // 401/403/400: retrying won't help — likely a bad/revoked token or schema mismatch
		return false, fmt.Errorf("server rejected snapshot: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
}
