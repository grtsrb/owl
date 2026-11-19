package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/store"
	"owl/internal/store/sqlite"
)

// Known CVEs, checked end to end: real upstream responses (testdata/cves, trimmed to these advisories)
// go through the same parsing and range matching as production. Each row was verified by hand against
// the vendor advisory. Re-record with:
//
//	set -a; . ./.env; set +a; OWL_RECORD_CVES=1 go test ./internal/upstream -run TestRecordCVEFixtures
type cveCase struct {
	component, version, cve string
	affected                bool
	severity                string // when affected
	fix                     string // RelevantFix when affected; "" = no fix listed (NoFix)
}

var cveCases = []cveCase{
	// IngressNightmare: admission controller RCE, fixed in 1.11.5 / 1.12.1.
	{"ingress-nginx", "1.11.3", "CVE-2025-1974", true, "critical", "1.11.5"},
	{"ingress-nginx", "1.12.0", "CVE-2025-1974", true, "critical", "1.12.1"},
	{"ingress-nginx", "1.11.5", "CVE-2025-1974", false, "", ""},
	{"ingress-nginx", "1.12.1", "CVE-2025-1974", false, "", ""},
	// RediShell: Lua use-after-free, fixed per line (6.2.20, 7.2.11, 7.4.6, 8.0.4, 8.2.2). GitHub repo advisories.
	{"redis", "7.2.4", "CVE-2025-49844", true, "critical", "7.2.11"},
	{"redis", "7.4.2", "CVE-2025-49844", true, "critical", "7.4.6"},
	{"redis", "7.2.11", "CVE-2025-49844", false, "", ""},
	{"redis", "8.2.2", "CVE-2025-49844", false, "", ""},
	// CoreDNS DoQ memory exhaustion, fixed in 1.12.2.
	{"coredns", "1.12.1", "CVE-2025-47950", true, "high", "1.12.2"},
	{"coredns", "1.12.2", "CVE-2025-47950", false, "", ""},
	// Kubernetes gitRepo volume command execution, fixed in 1.28.12 / 1.29.7 / 1.30.3.
	{"kubernetes", "1.30.2", "CVE-2024-10220", true, "high", "1.30.3"},
	{"kubernetes", "1.29.6", "CVE-2024-10220", true, "high", "1.29.7"},
	{"kubernetes", "1.30.3", "CVE-2024-10220", false, "", ""},
	// The other gitRepo CVE: never fixed (the volume type is deprecated) — affected up to 1.32.3, no fix.
	{"kubernetes", "1.30.3", "CVE-2025-1767", true, "moderate", ""},
	{"kubernetes", "1.33.0", "CVE-2025-1767", false, "", ""},
	// Grafana SQL expressions command injection; vendor fix is a +security build.
	{"grafana", "11.2.0", "CVE-2024-9264", true, "critical", "11.2.2+security-01"},
	{"grafana", "10.4.2", "CVE-2024-9264", false, "", ""},
}

const cveDir = "testdata/cves"

func cveComponents() []string {
	var ids []string
	for _, c := range cveCases {
		if !slices.Contains(ids, c.component) {
			ids = append(ids, c.component)
		}
	}
	return ids
}

func cveCatalog(t *testing.T) *catalog.Catalog {
	cat, err := catalog.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// noNetwork fails every request: the replay test must be served from the recorded cache only.
type noNetwork struct{}

func (noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled in tests: " + r.URL.Host)
}

func TestKnownCVEs(t *testing.T) {
	ctx := context.Background()
	cat := cveCatalog(t)
	for _, id := range cveComponents() {
		comp := cat.ByID(id)
		db := openCache(t)
		raw, err := os.ReadFile(filepath.Join(cveDir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatal(err)
		}
		for k, body := range entries {
			if err := db.PutCache(ctx, store.CacheEntry{Key: k, FetchedAt: time.Now(), Body: body}); err != nil {
				t.Fatal(err)
			}
		}
		c := &Client{F: &Fetcher{HTTP: &http.Client{Transport: noNetwork{}}, Cache: db, TTL: time.Hour, now: time.Now}}
		info := &Info{ComponentID: id}
		c.advisories(ctx, comp, info, func(what string, err error) { t.Errorf("%s: %s: %v", id, what, err) })
		if info.Stale {
			t.Errorf("%s: served stale: a request was attempted (fixture incomplete?)", id)
		}

		for _, tc := range cveCases {
			if tc.component != id {
				continue
			}
			var got *Match
			for _, m := range AdvisoriesFor(info, tc.version) {
				if m.ID == tc.cve || slices.Contains(m.Aliases, tc.cve) {
					got = &m
					break
				}
			}
			name := tc.component + " " + tc.version + " " + tc.cve
			switch {
			case !tc.affected && got != nil:
				t.Errorf("%s: want not affected, got %+v", name, *got)
			case !tc.affected:
			case got == nil:
				t.Errorf("%s: want affected, not matched", name)
			case !got.Certain:
				t.Errorf("%s: want certain, got needs review", name)
			case got.Severity != tc.severity:
				t.Errorf("%s: severity %q, want %q", name, got.Severity, tc.severity)
			case tc.fix == "" && !got.NoFix:
				t.Errorf("%s: want no fix listed, got fix %q", name, got.RelevantFix)
			case tc.fix != "" && got.RelevantFix != tc.fix:
				t.Errorf("%s: fix %q, want %q (fixedIn %v, ranges %+v)", name, got.RelevantFix, tc.fix, got.FixedIn, got.Ranges)
			}
		}
	}
}

func openCache(t *testing.T) *sqlite.DB {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestRecordCVEFixtures fetches the live advisory data once and keeps only the advisories in cveCases.
func TestRecordCVEFixtures(t *testing.T) {
	if os.Getenv("OWL_RECORD_CVES") == "" {
		t.Skip("set OWL_RECORD_CVES=1 to re-record (network)")
	}
	ctx := context.Background()
	cat := cveCatalog(t)
	if err := os.MkdirAll(cveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range cveComponents() {
		comp := cat.ByID(id)
		var cves []string
		for _, tc := range cveCases {
			if tc.component == id && !slices.Contains(cves, tc.cve) {
				cves = append(cves, tc.cve)
			}
		}
		db := openCache(t)
		c := &Client{F: &Fetcher{HTTP: &http.Client{Timeout: 60 * time.Second}, Cache: db, TTL: time.Hour,
			GitHubToken: os.Getenv("GITHUB_TOKEN"), UserAgent: "owl-tests", now: time.Now}}
		info := &Info{ComponentID: id}
		c.advisories(ctx, comp, info, func(what string, err error) { t.Fatalf("%s: %s: %v", id, what, err) })

		wanted := func(ids ...string) bool {
			for _, x := range ids {
				if slices.Contains(cves, x) {
					return true
				}
			}
			return false
		}
		out := map[string]json.RawMessage{}
		keep := func(key string, filter func(json.RawMessage) bool) {
			e, err := db.GetCache(ctx, key)
			if err != nil {
				return
			}
			var all []json.RawMessage
			if err := json.Unmarshal(e.Body, &all); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			kept := []json.RawMessage{}
			for _, v := range all {
				if filter(v) {
					kept = append(kept, v)
				}
			}
			b, _ := json.Marshal(kept)
			out[key] = b
		}
		if comp.OSV != nil {
			keep("osv-all:"+comp.OSV.Ecosystem+":"+comp.OSV.Name, func(v json.RawMessage) bool {
				var x struct {
					ID      string   `json:"id"`
					Aliases []string `json:"aliases"`
				}
				_ = json.Unmarshal(v, &x)
				return wanted(append(x.Aliases, x.ID)...)
			})
			for _, a := range info.Advisories {
				if !wanted(append(a.Aliases, a.ID)...) {
					continue
				}
				for _, al := range a.Aliases { // severities looked up per GHSA alias
					if e, err := db.GetCache(ctx, "osv-vuln:"+al); err == nil {
						out["osv-vuln:"+al] = e.Body
					}
				}
			}
		} else {
			keep("gh-advisories:"+comp.GitHub, func(v json.RawMessage) bool {
				var x struct {
					CVE string `json:"cve_id"`
				}
				_ = json.Unmarshal(v, &x)
				return wanted(x.CVE)
			})
		}
		b, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(filepath.Join(cveDir, id+".json"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %d bytes, keys %v", id, len(b), strings.Join(keysOf(out), ", "))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
