// Package notify posts Owl's notifications (milestone 10): a weekly digest and alerts to a Mattermost
// incoming webhook, and the digest by email to users who opted in. It never spends on Claude: only
// summaries already in the database are used.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ValidateWebhookURL accepts https URLs, and http only to a loopback host or the dev stack's Mattermost
// container (local testing). The URL is a secret (its path is the webhook key), so errors never repeat it.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("not a valid URL (expected https://mattermost.example.com/hooks/<key>)")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" || host == "owl-mattermost" { // owl-mattermost: make mattermost-up
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return errors.New("http:// is only allowed to a loopback host or the dev stack's owl-mattermost container; use https://")
	}
	return errors.New("the URL must start with https://")
}

// Webhook posts to a Mattermost incoming webhook.
type Webhook struct {
	URL      string
	HTTP     *http.Client
	Username string        // "Owl"; Mattermost ignores it unless overriding usernames is enabled
	Backoff  time.Duration // first retry delay (default 2s); tests set it small
}

// maxPost is below Mattermost's post limit (16383 characters by default).
const maxPost = 15000

// Post sends text, retrying network errors, 429 and 5xx a few times. 4xx (bad URL, disabled webhook) is
// not retried. Errors never contain the URL.
func (w *Webhook) Post(ctx context.Context, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text, "username": w.Username})
	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	delay := w.Backoff
	if delay == 0 {
		delay = 2 * time.Second
	}
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 4
		}
		retry, err := w.post(ctx, client, body)
		if err == nil {
			return nil
		}
		last = err
		if !retry {
			break
		}
	}
	return last
}

func (w *Webhook) post(ctx context.Context, client *http.Client, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return false, errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // *url.Error repeats the URL, which holds the webhook key
		}
		return true, fmt.Errorf("Mattermost not reachable: %v", err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode/100 == 2 {
		return false, nil
	}
	text := strings.TrimSpace(string(msg))
	if strings.Contains(text, w.URL) || strings.Contains(text, "/hooks/") {
		text = ""
	}
	err = fmt.Errorf("Mattermost answered %d %s", resp.StatusCode, text)
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, err
}
