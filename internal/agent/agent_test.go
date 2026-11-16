package agent

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"owl/internal/snapshot"
)

func newTestAgent(url string) *Agent {
	return &Agent{
		Collector: &Collector{Client: fake.NewClientset(), Version: "test"},
		Config:    Config{ServerURL: url, Token: "secret-token", Interval: time.Hour, Timeout: 30 * time.Second, AllowInsecureHTTP: true},
		HTTP:      NewHTTPClient(),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestPushRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != snapshot.IngestPath || r.Header.Get("Authorization") != "Bearer secret-token" ||
			r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var s snapshot.Snapshot
		if err := json.NewDecoder(zr).Decode(&s); err != nil || s.SchemaVersion != snapshot.SchemaVersion {
			t.Errorf("decode: %v %+v", err, s)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	if err := newTestAgent(srv.URL + "/").RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("want 2 calls (1 retry), got %d", calls.Load())
	}
}

func TestPushDoesNotRetryAuthErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := newTestAgent(srv.URL).RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("auth errors must not be retried, got %d calls", calls.Load())
	}
}

func TestPushDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	if err := newTestAgent(srv.URL).RunOnce(context.Background()); err == nil {
		t.Fatal("redirect must be treated as an error")
	}
	if leaked.Load() {
		t.Fatal("token was sent to the redirect target")
	}
}

func TestConfigRefusesPlainHTTPAnyCase(t *testing.T) {
	for _, u := range []string{"http://inv.example.com", "HTTP://inv.example.com", "HtTp://inv.example.com"} {
		c := Config{ServerURL: u, Token: "t", Timeout: time.Minute}
		if err := c.Validate(); err == nil {
			t.Errorf("%s: plain http must be refused", u)
		}
	}
	if err := (Config{ServerURL: "https://inv.example.com", Token: "t", Timeout: time.Minute}).Validate(); err != nil {
		t.Errorf("https refused: %v", err)
	}
}
