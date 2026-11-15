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

	"service-inventory/internal/snapshot"
)

func newTestAgent(url string) *Agent {
	return &Agent{
		Collector: &Collector{Client: fake.NewClientset(), Version: "test"},
		Config:    Config{ServerURL: url, Token: "secret-token", Interval: time.Hour, Timeout: 30 * time.Second, AllowInsecureHTTP: true},
		HTTP:      &http.Client{Timeout: 5 * time.Second},
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
