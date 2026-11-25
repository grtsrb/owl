package scan

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"owl/internal/store"
)

func TestParseReport(t *testing.T) {
	b, err := os.ReadFile("testdata/redis-report.json") // a real Trivy 0.76.2 report, trimmed
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseReport(b)
	if err != nil {
		t.Fatal(err)
	}
	if res.OS != "debian 12.5" {
		t.Errorf("OS = %q", res.OS)
	}
	if len(res.Vulns) != 15 { // 8 distinct OS findings (2 exact duplicates dropped) + 7 in the Go binary
		t.Fatalf("got %d vulns", len(res.Vulns))
	}
	v := res.Vulns[0]
	if v.ID != "CVE-2026-33845" || v.Severity != "critical" || v.Class != "os" || v.Fixed == "" || v.Target != "" || !strings.HasPrefix(v.URL, "https://") {
		t.Errorf("first = %+v", v)
	}
	if res.Vulns[1].URL != "" {
		t.Errorf("an http:// URL must be dropped: %q", res.Vulns[1].URL)
	}
	var lang *Vuln
	for i := range res.Vulns {
		if res.Vulns[i].Class == "lang" {
			lang = &res.Vulns[i]
			break
		}
	}
	if lang == nil || lang.Target != "usr/local/bin/gosu" || lang.PkgType != "gobinary" {
		t.Errorf("lang = %+v", lang)
	}
	for _, v := range res.Vulns {
		if v.Severity == "" || v.Severity == "UNKNOWN" {
			t.Errorf("severity not normalized: %+v", v)
		}
	}
}

func TestCredentials(t *testing.T) {
	cs, err := ParseCredentials([]byte(`
- prefix: registry.gitlab.example.com/team-a/
  username: deploy-a
  password: pa
- prefix: https://REGISTRY.gitlab.example.com
  username: deploy-all
  password: pall
- prefix: registry.gitlab.example.com/team-a/sub
  username: deploy-sub
  password: psub
- prefix: index.docker.io/acme
  username: hub
  password: ph
`))
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{
		"registry.gitlab.example.com/team-a/app@sha256:1":     "deploy-a",
		"registry.gitlab.example.com/team-a/sub/app@sha256:1": "deploy-sub",
		"registry.gitlab.example.com/team-ab/app@sha256:1":    "deploy-all", // not team-a: path boundary
		"registry.gitlab.example.com/other/app:1.0":           "deploy-all",
		"registry.gitlab.example.com:5000/team-a/app":         "", // another registry (port)
		"docker.io/acme/tool@sha256:1":                        "hub",
		"docker.io/library/redis@sha256:1":                    "",
		"ghcr.io/team-a/app@sha256:1":                         "",
	} {
		got := ""
		if c := cs.For(ref); c != nil {
			got = c.Username
		}
		if got != want {
			t.Errorf("For(%s) = %q, want %q", ref, got, want)
		}
	}
	for _, bad := range []string{
		"- prefix: x\n  username: u\n",                                                                  // no password
		"- prefix: ''\n  username: u\n  password: p\n",                                                  // no prefix
		"- prefix: a.io\n  username: u\n  password: p\n  token: secret-value\n",                         // unknown field
		"- prefix: a.io/\n  username: u\n  password: p\n- prefix: a.io\n  username: v\n  password: q\n", // twice
		"prefix: a.io", // not a list
	} {
		_, err := ParseCredentials([]byte(bad))
		if err == nil {
			t.Errorf("accepted %q", bad)
		} else if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "password: p") {
			t.Errorf("error quotes the file: %v", err)
		}
	}
}

func TestTargetFromImageID(t *testing.T) {
	d := "sha256:" + strings.Repeat("ab", 32)
	for id, want := range map[string]string{
		"docker.io/library/redis@" + d:                    "docker.io/library/redis@" + d,
		"docker-pullable://registry.example.com/a/b@" + d: "registry.example.com/a/b@" + d,
		d:                                   "", // built or imported on the node
		"docker.io/library/redis@sha256:12": "", // not a digest
		"":                                  "",
	} {
		tg, ok := TargetFromImageID(id, "amd64")
		if got := tg.Ref; got != want || ok != (want != "") {
			t.Errorf("%q → %q %v, want %q", id, got, ok, want)
		}
		if ok && (tg.Digest != d || tg.Platform != "linux/amd64") {
			t.Errorf("%q → %+v", id, tg)
		}
	}
}

// fakeScanner: results per ref; block makes a scan wait until released (or cancelled).
type fakeScanner struct {
	mu      sync.Mutex
	results map[string]*Result
	errs    map[string]error
	block   map[string]chan struct{}
	scanned []string
	creds   []string
	updates int
}

func (f *fakeScanner) Scan(ctx context.Context, t Target, cred *Credential) (*Result, error) {
	f.mu.Lock()
	f.scanned = append(f.scanned, t.Ref)
	if cred != nil {
		f.creds = append(f.creds, cred.Username)
	}
	ch := f.block[t.Ref]
	res, err := f.results[t.Ref], f.errs[t.Ref]
	f.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if res == nil {
		res = &Result{Vulns: []Vuln{}}
	}
	return res, nil
}

func (f *fakeScanner) UpdateDB(context.Context) error {
	f.mu.Lock()
	f.updates++
	f.mu.Unlock()
	return nil
}
func (f *fakeScanner) DBInfo(context.Context) (DBInfo, error) {
	return DBInfo{Version: "0.76.2", UpdatedAt: time.Now(), Downloaded: time.Now()}, nil
}

type memStore struct {
	mu       sync.Mutex
	scans    map[string]store.ImageScan
	settings map[string]string
}

func (m *memStore) ListImageScans(context.Context) ([]store.ImageScan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.ImageScan{}
	for _, s := range m.scans {
		out = append(out, s)
	}
	return out, nil
}
func (m *memStore) PutImageScan(_ context.Context, s store.ImageScan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scans[s.Digest] = s
	return nil
}
func (m *memStore) DeleteImageScansExcept(_ context.Context, keep []string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := map[string]bool{}
	for _, d := range keep {
		k[d] = true
	}
	n := int64(0)
	for d := range m.scans {
		if !k[d] {
			delete(m.scans, d)
			n++
		}
	}
	return n, nil
}
func (m *memStore) GetSettings(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.settings {
		out[k] = v
	}
	return out, nil
}
func (m *memStore) SetSetting(_ context.Context, k, v string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings[k] = v
	return nil
}

func target(n string) Target {
	d := "sha256:" + strings.Repeat(n, 64)
	return Target{Digest: d, Ref: "registry.example.com/app-" + n + "@" + d, Repository: "registry.example.com/app-" + n}
}

type harness struct {
	m       *Manager
	sc      *fakeScanner
	st      *memStore
	mu      sync.Mutex
	targets []Target
	cancel  context.CancelFunc
}

func newHarness(t *testing.T, targets ...Target) *harness {
	h := &harness{sc: &fakeScanner{results: map[string]*Result{}, errs: map[string]error{}, block: map[string]chan struct{}{}},
		st: &memStore{scans: map[string]store.ImageScan{}, settings: map[string]string{
			// a daily rescan just ran: tests start from new images only
			settingLastDaily: time.Now().UTC().Format(time.RFC3339)}}, targets: targets}
	h.m = &Manager{Scanner: h.sc, Store: h.st, Timeout: time.Minute, Targets: func(context.Context) ([]Target, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]Target{}, h.targets...), nil
	}}
	t.Cleanup(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
	return h
}

func (h *harness) start(t *testing.T) {
	if err := h.m.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go h.m.Run(ctx)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManagerScansNewImagesAndKeepsResults(t *testing.T) {
	a, b := target("a"), target("b")
	h := newHarness(t, a, b)
	h.sc.results[a.Ref] = &Result{OS: "alpine 3.20", Vulns: []Vuln{{ID: "CVE-1", Severity: "high", Fixed: "1.1"}}}
	h.start(t)
	waitFor(t, "both scanned", func() bool { return len(h.m.Results()) == 2 })
	if r := h.m.Results()[a.Digest]; r.Result == nil || r.Result.OS != "alpine 3.20" || r.ScannedAt == nil {
		t.Fatalf("a = %+v", r)
	}
	if len(h.st.scans) != 2 {
		t.Errorf("stored %d", len(h.st.scans))
	}

	// a's rescan fails: the old result stays, with the error
	h.sc.mu.Lock()
	h.sc.errs[a.Ref] = ErrAuth
	h.sc.mu.Unlock()
	if q, err := h.m.Rescan(context.Background(), a.Digest); err != nil || !q {
		t.Fatalf("rescan: %v %v", q, err)
	}
	waitFor(t, "failed rescan", func() bool { r := h.m.Results()[a.Digest]; return r.Error != "" })
	r := h.m.Results()[a.Digest]
	if r.Result == nil || r.Result.OS != "alpine 3.20" || !strings.Contains(r.Error, "auth failed for anonymous: registry.example.com") {
		t.Errorf("after failure: %+v", r)
	}
	st := h.m.Status()
	if len(st.AuthFailures) != 1 || st.Recent[0].Outcome != OutcomeFailed || st.Recent[0].Reason != ReasonManual {
		t.Errorf("status = %+v", st)
	}

	// b stops running: its result goes, in memory and in the store
	h.mu.Lock()
	h.targets = []Target{a}
	h.mu.Unlock()
	h.m.Kick()
	waitFor(t, "b pruned", func() bool { _, ok := h.m.Results()[b.Digest]; return !ok })
	waitFor(t, "b deleted", func() bool { h.st.mu.Lock(); defer h.st.mu.Unlock(); _, ok := h.st.scans[b.Digest]; return !ok })

	// a rescan of an image that doesn't run is refused
	if _, err := h.m.Rescan(context.Background(), b.Digest); !errors.Is(err, ErrNotRunning) {
		t.Errorf("rescan of gone image: %v", err)
	}
}

func TestManagerRescanQueueAndCancel(t *testing.T) {
	a, b, c := target("a"), target("b"), target("c")
	h := newHarness(t, a, b, c)
	release := make(chan struct{})
	h.sc.block[a.Ref] = release
	h.start(t)
	waitFor(t, "a running", func() bool { st := h.m.Status(); return st.Current != nil && st.Current.Digest == a.Digest })
	if st := h.m.Status(); st.QueueLen != 2 || st.Queue[0].Digest != b.Digest {
		t.Fatalf("queue = %+v", st.Queue)
	}
	// manual rescan of c goes first; a second click (queued) and a click on the running one do nothing
	if q, _ := h.m.Rescan(context.Background(), c.Digest); q {
		t.Error("c is already queued: no second entry")
	}
	if q, _ := h.m.Rescan(context.Background(), a.Digest); q {
		t.Error("a is running: no entry")
	}
	if st := h.m.Status(); st.QueueLen != 2 {
		t.Fatalf("queue = %+v", st.Queue)
	}
	// drop b from the queue, cancel a
	if _, err := h.m.Cancel(b.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Cancel(a.Digest); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "c scanned", func() bool { _, ok := h.m.Results()[c.Digest]; return ok })
	st := h.m.Status()
	outcomes := map[string]string{}
	for _, f := range st.Recent {
		outcomes[f.Digest] = f.Outcome
	}
	if outcomes[a.Digest] != OutcomeCancelled || outcomes[b.Digest] != OutcomeDropped || outcomes[c.Digest] != OutcomeOK {
		t.Errorf("outcomes = %v", outcomes)
	}
	// stopped scans are remembered, so a sync doesn't queue them again (the daily rescan does)
	if r := h.m.Results()[a.Digest]; r == nil || r.Result != nil || r.Error != "cancelled by an admin" {
		t.Errorf("cancelled: %+v", r)
	}
	if r := h.m.Results()[b.Digest]; r == nil || r.Error != "removed from the queue by an admin" {
		t.Errorf("dropped: %+v", r)
	}
	h.m.Kick()
	time.Sleep(50 * time.Millisecond)
	if st := h.m.Status(); st.QueueLen != 0 || st.Current != nil {
		t.Errorf("a sync queued a stopped image again: %+v %+v", st.Queue, st.Current)
	}
	if _, err := h.m.Cancel(b.Digest); !errors.Is(err, ErrNotQueued) {
		t.Errorf("cancel again: %v", err)
	}
	// the manual rescan put a new job at the front
	h.sc.block[b.Ref] = nil
	if q, _ := h.m.Rescan(context.Background(), b.Digest); !q {
		t.Error("b should be queued now")
	}
	waitFor(t, "b scanned", func() bool { _, ok := h.m.Results()[b.Digest]; return ok })
}

func TestManagerCredentialsAndTimeout(t *testing.T) {
	a := target("a")
	h := newHarness(t, a)
	h.m.Creds = Credentials{{Prefix: "registry.example.com", Username: "deploy", Password: "p"}}
	h.m.Timeout = 20 * time.Millisecond
	h.sc.block[a.Ref] = make(chan struct{}) // never released
	h.start(t)
	waitFor(t, "timeout", func() bool { st := h.m.Status(); return len(st.Recent) > 0 })
	st := h.m.Status()
	if st.Recent[0].Outcome != OutcomeTimeout {
		t.Errorf("recent = %+v", st.Recent)
	}
	if len(h.sc.creds) != 1 || h.sc.creds[0] != "deploy" {
		t.Errorf("creds = %v", h.sc.creds)
	}
	if r := h.m.Results()[a.Digest]; r == nil || r.Error == "" {
		t.Errorf("a timeout is recorded as a failure: %+v", r)
	}
	if len(st.Prefixes) != 1 || st.Prefixes[0] != "registry.example.com" {
		t.Errorf("prefixes = %v", st.Prefixes)
	}
}

func TestManagerDailyRescan(t *testing.T) {
	a, b := target("a"), target("b")
	h := newHarness(t, a, b)
	h.st.scans[a.Digest] = store.ImageScan{Digest: a.Digest, Ref: a.Ref, Result: []byte(`{"vulns":[]}`), AttemptAt: time.Now()}
	h.st.scans[b.Digest] = store.ImageScan{Digest: b.Digest, Ref: b.Ref, Error: "x", AttemptAt: time.Now()}
	delete(h.st.settings, settingLastDaily) // never ran: due at once
	h.start(t)
	waitFor(t, "daily done", func() bool { return h.m.Status().LastDaily != nil })
	h.sc.mu.Lock()
	n := len(h.sc.scanned)
	h.sc.mu.Unlock()
	if n != 2 {
		t.Errorf("scanned %d, want both (daily)", n)
	}
	for _, f := range h.m.Status().Recent {
		if f.Reason != ReasonDaily {
			t.Errorf("reason = %s", f.Reason)
		}
	}
	waitFor(t, "saved", func() bool { s, _ := h.st.GetSettings(context.Background()); return s[settingLastDaily] != "" })
}

// The last queued jobs of a daily rescan stop running: the rescan is finished, not due again at once.
func TestManagerDailyEndsWhenImagesGo(t *testing.T) {
	a, b := target("a"), target("b")
	h := newHarness(t, a)
	h.m.init()
	old := time.Now().Add(-48 * time.Hour)
	h.m.lastDaily = old
	h.m.daily = map[string]bool{b.Digest: true}
	h.m.queue = []Job{{Target: b, Reason: ReasonDaily}}
	h.m.results[a.Digest] = &Stored{Digest: a.Digest}
	h.m.sync(context.Background()) // b no longer runs
	if h.m.Status().QueueLen != 0 || !h.m.lastDaily.After(old) || h.m.dailyDue() {
		t.Errorf("daily not finished: queue %d, lastDaily %v, due %v", h.m.Status().QueueLen, h.m.lastDaily, h.m.dailyDue())
	}
}

func TestClassify(t *testing.T) {
	other := errors.New("exit status 1")
	for stderr, want := range map[string]error{
		"FATAL ... GET https://reg/v2/x/manifests/sha256:ab401cd: UNAUTHORIZED: authentication required": ErrAuth,
		"unexpected status code 401 Unauthorized (HEAD responses have no body)":                          ErrAuth,
		"DENIED: requested access to the resource is denied":                                             ErrAuth,
		"MANIFEST_UNKNOWN: manifest unknown":                                                             ErrNotFound,
		"2026-11-25T13:07:24.401Z FATAL open /data/trivy/fanal/fanal.db: permission denied":              nil,
		"Get https://reg/v2/x@sha256:4011401: dial tcp: i/o timeout":                                     nil,
	} {
		err := classify(stderr, other)
		if want == nil {
			if errors.Is(err, ErrAuth) || errors.Is(err, ErrNotFound) {
				t.Errorf("%q → %v, want a plain error", stderr, err)
			}
		} else if !errors.Is(err, want) {
			t.Errorf("%q → %v, want %v", stderr, err, want)
		}
	}
}

func TestRegistryAllowed(t *testing.T) {
	for host, want := range map[string]bool{
		"docker.io": true, "registry.gitlab.example.com": true, "ghcr.io": true, "registry.local:5000": true,
		"169.254.169.254": false, "169.254.169.254:80": false, "10.0.0.5:5000": false, "127.0.0.1": false,
		"[::1]:5000": false, "[fd00::1]": false, "localhost:5000": false, "localhost": false, "a.localhost": false,
		"metadata.google.internal": false, "instance-data": false,
		"10.1.2.3:5000": true, // configured below
	} {
		if ok, why := RegistryAllowed(host, []string{"10.1.2.3:5000"}); ok != want {
			t.Errorf("%s: %v (%s), want %v", host, ok, why, want)
		}
	}
	if ok, _ := RegistryAllowed("10.1.2.3:6000", []string{"10.1.2.3:5000"}); ok {
		t.Error("another port of a configured IP")
	}
	if ok, _ := RegistryAllowed("10.1.2.3:6000", []string{"10.1.2.3"}); !ok {
		t.Error("a configured host without a port allows every port")
	}
	if tg, _ := TargetFromImageID("docker.io/library/redis@sha256:"+strings.Repeat("a", 64), "x; rm -rf /"); tg.Platform != "" {
		t.Errorf("unknown arch passed on: %q", tg.Platform)
	}
}
