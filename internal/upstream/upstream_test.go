package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/store/sqlite"
)

func TestInRange(t *testing.T) {
	cases := []struct {
		version, expr string
		in, known     bool
	}{
		{"1.14.6", "<= 1.14.6", true, true},
		{"1.14.7", "<= 1.14.6", false, true},
		{"7.2.4", ">= 7.0.0, < 7.2.5", true, true},
		{"7.2.5", ">= 7.0.0, < 7.2.5", false, true},
		{"6.2.0", ">= 7.0.0, < 7.2.5", false, true},
		{"1.0.0", "= 1.0.0", true, true},
		{"1.0.0", "v1.0.0", true, true},
		{"1.0.0", "All", false, false},
		{"1.0.0", "", false, false},
		{"1.0.0", "< garbage", false, false},
		{"1.12.0-rc.1", "< 1.12.0", true, true},
	}
	for _, c := range cases {
		v, _ := catalog.ParseVersion(c.version, "")
		in, known := InRange(v, c.expr)
		if in != c.in || known != c.known {
			t.Errorf("InRange(%s, %q) = %v,%v want %v,%v", c.version, c.expr, in, known, c.in, c.known)
		}
	}
}

func TestRepoAdvisoryMatching(t *testing.T) {
	info := &Info{Advisories: []Advisory{
		{ID: "A", Severity: "high", Ranges: []AffectedRange{{Range: "<= 1.14.6", Patched: "1.14.7"}}},
		{ID: "B", Severity: "low", Ranges: []AffectedRange{{Range: ">= 2.0.0"}}},
		{ID: "C", Severity: "high", Ranges: []AffectedRange{{Range: "All", Patched: "TBD"}}},
	}}
	ms, err := (&Client{}).AdvisoriesFor(context.Background(), &catalog.Component{ID: "x"}, info, "1.13.1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, m := range ms {
		got[m.ID] = m.Certain
	}
	if c, ok := got["A"]; !ok || !c {
		t.Errorf("A must be a certain match: %v", got)
	}
	if _, ok := got["B"]; ok {
		t.Errorf("B must not match 1.13.1: %v", got)
	}
	if c, ok := got["C"]; !ok || c {
		t.Errorf(`C ("All") must be listed as uncertain, not affected: %v`, got)
	}
}

func TestFetcherCachesAndServesStale(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := time.Now()
	f := &Fetcher{HTTP: srv.Client(), Cache: db, TTL: time.Hour, now: func() time.Time { return clock }}
	ctx := context.Background()

	if r, err := f.get(ctx, "k", srv.URL); err != nil || string(r.Body) != `{"ok":true}` {
		t.Fatalf("first fetch: %v %v", r, err)
	}
	f.get(ctx, "k", srv.URL) // fresh: served from cache
	if calls.Load() != 1 {
		t.Errorf("fresh entry must not refetch, calls=%d", calls.Load())
	}
	clock = clock.Add(2 * time.Hour) // expired -> revalidate with ETag -> 304
	if r, err := f.get(ctx, "k", srv.URL); err != nil || r.Stale || string(r.Body) != `{"ok":true}` {
		t.Fatalf("revalidated: %+v %v", r, err)
	}
	clock = clock.Add(2 * time.Hour)
	fail.Store(true) // upstream down -> last good copy, marked stale
	if r, err := f.get(ctx, "k", srv.URL); err != nil || !r.Stale {
		t.Fatalf("want stale copy, got %+v %v", r, err)
	}
	if _, err := f.get(ctx, "other", srv.URL); err == nil {
		t.Fatal("no cache + upstream down must be an error")
	}
}

func TestEventsToRanges(t *testing.T) {
	got := eventsToRanges([]map[string]string{{"introduced": "0"}, {"fixed": "1.11.5"}})
	if len(got) != 1 || got[0].Range != "< 1.11.5" || got[0].Patched != "1.11.5" {
		t.Errorf("simple: %+v", got)
	}
	got = eventsToRanges([]map[string]string{{"introduced": "1.12.0-beta.0"}, {"fixed": "1.12.1"}, {"introduced": "1.13.0"}})
	if len(got) != 2 || got[0].Range != ">= 1.12.0-beta.0, < 1.12.1" || got[1].Range != ">= 1.13.0" || got[1].Patched != "" {
		t.Errorf("multi/open: %+v", got)
	}
	// IngressNightmare: fixed in 1.11.5 and 1.12.1 — 1.11.5 and 1.15.1 must not match, 1.11.3 must.
	info := &Info{Advisories: []Advisory{{ID: "CVE-2025-1974", Ranges: append(
		eventsToRanges([]map[string]string{{"introduced": "0"}, {"fixed": "1.11.5"}}),
		eventsToRanges([]map[string]string{{"introduced": "1.12.0-beta.0"}, {"fixed": "1.12.1"}})...)}}}
	for v, want := range map[string]bool{"1.11.3": true, "1.11.5": false, "1.12.0": true, "1.15.1": false} {
		ms, _ := (&Client{}).AdvisoriesFor(context.Background(), &catalog.Component{}, info, v)
		if (len(ms) == 1) != want {
			t.Errorf("%s: affected=%v want %v", v, len(ms) == 1, want)
		}
	}
}

func TestOpenRangesWithPatchLists(t *testing.T) {
	info := &Info{Advisories: []Advisory{
		{ID: "per-line", Ranges: []AffectedRange{{Range: "All", Patched: "6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2"}}},
		{ID: "tbd", Ranges: []AffectedRange{{Range: ">= 7.0.0", Patched: "TBD"}}},
		{ID: "one-line", Ranges: []AffectedRange{{Range: ">= 8.2.0", Patched: "8.2.3"}}},
		{ID: "bounded", Ranges: []AffectedRange{{Range: "<= 8.0", Patched: ""}}},
	}}
	check := func(version string, want map[string]string) {
		t.Helper()
		ms, _ := (&Client{}).AdvisoriesFor(context.Background(), &catalog.Component{}, info, version)
		got := map[string]string{}
		for _, m := range ms {
			got[m.ID] = map[bool]string{true: "affected", false: "review"}[m.Certain]
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("%s %s: got %q want %q (all %v)", version, id, got[id], w, got)
			}
		}
	}
	check("7.2.4", map[string]string{"per-line": "affected", "tbd": "review", "one-line": "", "bounded": "affected"})
	check("7.2.11", map[string]string{"per-line": "", "tbd": "review"})
	check("7.0.15", map[string]string{"per-line": "affected"}) // 7.0 line unpatched, newer lines fixed
	check("8.10.3", map[string]string{"per-line": "", "one-line": "", "bounded": ""})
}

func TestDistroBuildsAndExactRanges(t *testing.T) {
	info := &Info{Advisories: []Advisory{
		{ID: "build-fix", Ranges: []AffectedRange{{Range: "< 1.30.4+rke2r2", Patched: "1.30.4+rke2r2"}}},
		{ID: "upstream-fix", Ranges: []AffectedRange{{Range: "<= 1.30.4", Patched: "1.30.5"}}},
		{ID: "exact", Ranges: []AffectedRange{{Range: "= 1.2.3"}}},
	}}
	affected := func(version string) map[string]bool {
		ms, _ := (&Client{}).AdvisoriesFor(context.Background(), &catalog.Component{}, info, version)
		got := map[string]bool{}
		for _, m := range ms {
			got[m.ID] = m.Certain
		}
		return got
	}
	if got := affected("1.30.4+rke2r1"); !got["build-fix"] || !got["upstream-fix"] {
		t.Errorf("rke2r1 must be affected by both: %v", got)
	}
	if got := affected("1.30.4+rke2r2"); got["build-fix"] || !got["upstream-fix"] {
		t.Errorf("rke2r2 fixes the build advisory but is still 1.30.4 upstream: %v", got)
	}
	if got := affected("1.2.3"); !got["exact"] {
		t.Errorf("exact range with no patch must be a certain match: %v", got)
	}
}

func TestRelevantFix(t *testing.T) {
	fixes := []string{"6.2.20", "7.2.11", "7.4.6", "8.0.4", "8.2.2"}
	for v, want := range map[string]string{"7.4.1": "7.4.6", "7.2.4": "7.2.11", "7.0.15": "7.2.11", "8.1.0": "8.2.2", "7.4.8": "8.0.4"} {
		pv, _ := catalog.ParseVersion(v, "")
		if got := RelevantFix(pv, fixes); got != want {
			t.Errorf("%s: got %q want %q", v, got, want)
		}
	}
	pv, _ := catalog.ParseVersion("17.0", "")
	if got := RelevantFix(pv, []string{"16.6", "17.2"}); got != "17.2" { // major-named lines (Postgres)
		t.Errorf("postgres: %q", got)
	}
}
