package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"owl/internal/catalog"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
	"owl/internal/upstream"
)

// offline answers every upstream request with 404 and remembers the paths, so tests see what was fetched.
type offline struct {
	mu    sync.Mutex
	paths []string
}

func (o *offline) RoundTrip(r *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.paths = append(o.paths, r.URL.Host+r.URL.Path)
	o.mu.Unlock()
	return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: r}, nil
}

func (o *offline) asked(fragment string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range o.paths {
		if strings.Contains(p, fragment) {
			return true
		}
	}
	return false
}

// Only components running in a cluster (and custom ones) are refreshed; a component that starts running
// is fetched by FetchNew without waiting for the next refresh, and one that stops leaves the map.
func TestRefreshOnlyRunning(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cat, _ := catalog.Builtin()
	rt := &offline{}
	app := &Server{Store: db, Catalog: cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Upstream: &upstream.Client{F: &upstream.Fetcher{HTTP: &http.Client{Transport: rt}, Cache: db, TTL: 1}}}
	c, _ := db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	push := func(images ...string) {
		snap := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion}
		for _, img := range images {
			snap.Workloads = append(snap.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "ns", Name: img,
				Containers: []snapshot.Container{{Name: "c", Image: img}}})
		}
		if _, err := db.Ingest(ctx, c.ID, snap); err != nil {
			t.Fatal(err)
		}
	}
	loaded := func() []string {
		app.mu.RLock()
		defer app.mu.RUnlock()
		var ids []string
		for id := range app.upstream {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return ids
	}

	app.FetchNew(ctx) // before the first refresh: nothing (the refresh covers it)
	if app.upstream != nil {
		t.Fatal("FetchNew must not load anything before the first refresh")
	}
	push("nginx:1.27.4", "redis:latest") // unpinned counts as running
	app.RefreshUpstream(ctx)
	if got := loaded(); !slices.Equal(got, []string{"nginx", "redis"}) {
		t.Errorf("after refresh: %v", got)
	}
	if rt.asked("grafana") || rt.asked("coredns") {
		t.Error("components not running anywhere must not be fetched")
	}

	push("nginx:1.27.4", "redis:latest", "grafana/grafana:11.1.0")
	app.FetchNew(ctx)
	if got := loaded(); !slices.Equal(got, []string{"grafana", "nginx", "redis"}) {
		t.Errorf("after FetchNew: %v", got)
	}
	if !rt.asked("grafana/grafana") {
		t.Error("FetchNew must fetch the component that started running")
	}

	push("grafana/grafana:11.1.0")
	app.RefreshUpstream(ctx)
	if got := loaded(); !slices.Equal(got, []string{"grafana"}) {
		t.Errorf("components that stopped running must leave the map: %v", got)
	}
}
