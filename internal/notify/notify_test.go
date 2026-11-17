package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"owl/internal/catalog"
	"owl/internal/inventory"
	"owl/internal/mail"
	"owl/internal/policy"
	"owl/internal/report"
	"owl/internal/snapshot"
	"owl/internal/store"
	"owl/internal/store/sqlite"
)

// fakeMM is a Mattermost incoming webhook that records posts.
type fakeMM struct {
	mu    sync.Mutex
	posts []map[string]string
	fail  int // answer 500 this many times first
	code  int // with this status (default 500)
}

func (f *fakeMM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		code := f.code
		if code == 0 {
			code = 500
		}
		w.WriteHeader(code)
		io.WriteString(w, `{"message":"boom"}`)
		return
	}
	var m map[string]string
	_ = json.NewDecoder(r.Body).Decode(&m)
	f.posts = append(f.posts, m)
	io.WriteString(w, "ok")
}

func (f *fakeMM) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.posts {
		out = append(out, p["text"])
	}
	return out
}

func (f *fakeMM) reset() { f.mu.Lock(); f.posts = nil; f.mu.Unlock() }

type fakeMail struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (f *fakeMail) Send(_ context.Context, m mail.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

type env struct {
	t     *testing.T
	db    *sqlite.DB
	mm    *fakeMM
	n     *Notifier
	rep   *report.Report
	now   time.Time
	cat   *catalog.Catalog
	ready bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cat, err := catalog.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, db: db, mm: &fakeMM{}, cat: cat, ready: true,
		now: time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC), // a Monday
		rep: &report.Report{Stages: []string{"staging", "prelive", "live"}}}
	srv := httptest.NewServer(e.mm)
	t.Cleanup(srv.Close)
	pub, _ := url.Parse("https://owl.example.com")
	e.n = e.notifier(srv.URL)
	e.n.PublicURL = pub
	return e
}

func (e *env) notifier(hook string) *Notifier {
	n := &Notifier{Store: e.db, Catalog: e.cat, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Report: func(context.Context) (*report.Report, error) { return e.rep, nil },
		Ready:  func() bool { return e.ready }, Now: func() time.Time { return e.now }}
	if hook != "" {
		n.Webhook = &Webhook{URL: hook, Username: "Owl", Backoff: time.Millisecond}
	}
	return n
}

func (e *env) set(kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		if err := e.db.SetSetting(context.Background(), kv[i], kv[i+1]); err != nil {
			e.t.Fatal(err)
		}
	}
}

// cluster registers a cluster and sends it a snapshot with the given images (so it counts as reported).
func (e *env) cluster(name, tenant string, images ...string) {
	ctx := context.Background()
	c, err := e.db.GetCluster(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		_ = e.db.CreateTenant(ctx, tenant) // exists already: fine
		c, err = e.db.CreateCluster(ctx, name, store.StageLive, tenant)
	}
	if err != nil {
		e.t.Fatal(err)
	}
	snap := &snapshot.Snapshot{SchemaVersion: snapshot.SchemaVersion, Cluster: snapshot.Cluster{KubernetesVersion: "v1.31.2"}}
	for i, img := range images {
		snap.Workloads = append(snap.Workloads, snapshot.Workload{Kind: "Deployment", Namespace: "ns", Name: "w" + itoa(i),
			DesiredReplicas: 1, Containers: []snapshot.Container{{Name: "c", Image: img}}})
	}
	if _, err := e.db.Ingest(ctx, c.ID, snap); err != nil {
		e.t.Fatal(err)
	}
}

// crit sets the report to these Critical (component, cluster) pairs.
func (e *env) crit(pairs ...string) {
	e.rep = &report.Report{Stages: []string{"staging", "prelive", "live"}}
	byComp := map[string]*report.ComponentReport{}
	var order []string
	for _, p := range pairs {
		comp, cl, _ := strings.Cut(p, "@")
		c := byComp[comp]
		if c == nil {
			c = &report.ComponentReport{ID: comp, Name: strings.ToUpper(comp[:1]) + comp[1:], Level: report.LevelCritical,
				Action: report.Action{Kind: report.ActionUpgrade, Text: "Upgrade to 2.0.0", Why: "1 critical advisory"}}
			byComp[comp] = c
			order = append(order, comp)
		}
		c.Instances = append(c.Instances, report.ScoredInstance{
			Instance: inventory.Instance{Cluster: cl, Stage: "live", Tenant: "IT", Version: "1.0.0", Status: "identified"},
			Score:    report.Score{Level: report.LevelCritical, Reasons: []report.Reason{{Level: report.LevelCritical, Text: "1 critical advisory"}}},
			Action:   report.Action{Kind: report.ActionUpgrade, Text: "Upgrade to 2.0.0", Why: "1 critical advisory"},
		})
	}
	for _, id := range order {
		e.rep.Components = append(e.rep.Components, *byComp[id])
	}
}

func (e *env) check() {
	e.t.Helper()
	if err := e.n.Check(context.Background(), false); err != nil {
		e.t.Fatal(err)
	}
}

func TestFirstCheckIsQuietThenAlertsOnlyNew(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on", KeyUnrecognized, "on", KeyNewCluster, "on")
	e.cluster("it-aws-prod", "IT", "redis:7.2.4", "example.org/team/thing:1.0")
	e.crit("redis@it-aws-prod")
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("first check posted: %q", got)
	}

	// Two new pairs in one check: one message. The existing pair isn't repeated.
	e.crit("redis@it-aws-prod", "coredns@it-aws-prod", "grafana@it-aws-prod")
	e.check()
	got := e.mm.texts()
	if len(got) != 1 || !strings.Contains(got[0], "2 components became Critical") ||
		!strings.Contains(got[0], "**Coredns** on **it-aws-prod**") || strings.Contains(got[0], "Redis") ||
		!strings.Contains(got[0], "(https://owl.example.com/components/coredns)") {
		t.Fatalf("alert: %q", got)
	}

	// A new Notifier (server restart) on the same database doesn't alert again.
	e.mm.reset()
	e.n = e.notifier(e.n.Webhook.URL)
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("re-alerted after restart: %q", got)
	}

	// Leaving Critical: no message, but recorded for the digest; Critical again later = alert again.
	e.crit("redis@it-aws-prod", "grafana@it-aws-prod")
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("resolved posted: %q", got)
	}
	res, _ := e.db.ListResolved(context.Background(), time.Time{})
	if len(res) != 1 || res[0].Component != "coredns" {
		t.Fatalf("resolved: %+v", res)
	}
	e.crit("redis@it-aws-prod", "grafana@it-aws-prod", "coredns@it-aws-prod")
	e.check()
	if got := e.mm.texts(); len(got) != 1 || !strings.Contains(got[0], "Coredns became Critical") {
		t.Fatalf("critical again: %q", got)
	}
}

func TestSwitchedOffStillTracks(t *testing.T) {
	e := newEnv(t)
	e.cluster("it-hetzner", "IT", "redis:7.2.4")
	e.check() // initial
	e.crit("redis@it-hetzner")
	e.cluster("it-hetzner", "IT", "redis:7.2.4", "example.org/new/thing:1")
	e.check() // all switches off: nothing posted, state recorded
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("posted while off: %q", got)
	}
	e.set(KeyCritical, "on", KeyUnrecognized, "on")
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("switching on flooded: %q", got)
	}
}

func TestNewClusterOneMessage(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on", KeyUnrecognized, "on", KeyNewCluster, "on")
	e.cluster("it-hetzner", "IT", "example.org/shared/tool:1")
	e.check() // initial

	e.cluster("ds-prod", "DS", "example.org/shared/tool:1", "example.org/brand/new:2")
	e.crit("redis@ds-prod")
	e.check()
	got := e.mm.texts()
	if len(got) != 1 {
		t.Fatalf("want one combined message, got %d: %q", len(got), got)
	}
	msg := got[0]
	for _, want := range []string{"New cluster connected: ds-prod", "tenant **DS**", "**Critical**", "**Redis**",
		"`example.org/brand/new` — **new to Owl**", "`example.org/shared/tool` — also on it-hetzner, not decided yet",
		"(https://owl.example.com/clusters/ds-prod?show=unrecognized)"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}

	// With the new-cluster message off, the usual alerts cover a new cluster.
	e.mm.reset()
	e.set(KeyNewCluster, "")
	e.cluster("ds-test", "DS", "example.org/other/new:1")
	e.crit("redis@ds-prod", "redis@ds-test")
	e.check()
	got = e.mm.texts()
	if len(got) != 2 || !strings.Contains(got[0], "Redis** on **ds-test") || !strings.Contains(got[1], "`example.org/other/new` on") || !strings.Contains(got[1], "(https://owl.example.com/settings/catalog?new=example.org%2Fother%2Fnew)") {
		t.Fatalf("alerts: %q", got)
	}
}

func TestDeletedClusterIsNotResolved(t *testing.T) {
	e := newEnv(t)
	e.cluster("it-hetzner", "IT", "redis:7.2.4")
	e.crit("redis@it-hetzner")
	e.check()
	if err := e.db.DeleteCluster(context.Background(), "it-hetzner"); err != nil {
		t.Fatal(err)
	}
	e.crit()
	e.check()
	st, _ := e.db.NotifyState(context.Background())
	res, _ := e.db.ListResolved(context.Background(), time.Time{})
	if len(st.Critical) != 0 || len(res) != 0 || len(st.Clusters) != 0 {
		t.Fatalf("state %+v resolved %+v", st, res)
	}
}

func TestWaitsForUpstreamAndQuietRestore(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT", "redis:7.2.4")
	e.check()
	e.ready = false
	e.crit("redis@it-hetzner")
	if err := e.n.Check(context.Background(), true); err != nil { // e.g. a restore before upstream loaded
		t.Fatal(err)
	}
	st, _ := e.db.NotifyState(context.Background())
	if len(st.Critical) != 0 {
		t.Fatal("checked before upstream data loaded")
	}
	e.ready = true
	e.check() // the pending quiet check
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("restore alerted: %q", got)
	}
	st, _ = e.db.NotifyState(context.Background())
	if len(st.Critical) != 1 {
		t.Fatalf("not recorded: %+v", st.Critical)
	}
}

func TestNoWebhook(t *testing.T) {
	e := newEnv(t)
	e.n.Webhook = nil
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT")
	e.check()
	e.crit("redis@it-hetzner")
	e.check()
	if err := e.n.SendTest(context.Background()); err == nil {
		t.Fatal("test message without webhook")
	}
	if st, _ := e.db.NotifyState(context.Background()); len(st.Critical) != 1 {
		t.Fatal("state not tracked without webhook")
	}
}

func TestFailedAlertIsRecordedAndDropped(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT")
	e.check()
	e.mm.fail = 3 // every retry fails
	e.crit("redis@it-hetzner")
	e.check()
	st, err := e.n.Status(context.Background())
	if err != nil || !strings.Contains(st.LastError, "critical alert: Mattermost answered 500") {
		t.Fatalf("status %+v %v", st, err)
	}
	e.check() // dropped: not retried by the next check
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("retried a dropped alert: %q", got)
	}
}

func TestDigestSchedule(t *testing.T) {
	e := newEnv(t)
	fm := &fakeMail{}
	e.n.Mail = fm
	ctx := context.Background()
	e.cluster("it-hetzner", "IT")
	u, _ := e.db.CreateUser(ctx, "a@example.com", store.RoleViewer)
	_ = e.db.SetPassword(ctx, u.ID, "x", "")
	_ = e.db.SetDigestEmail(ctx, u.ID, true)
	v, _ := e.db.CreateUser(ctx, "b@example.com", store.RoleViewer) // not activated: no mail
	_ = e.db.SetDigestEmail(ctx, v.ID, true)

	e.set(KeyDigest, "on", KeyDigestTZ, "UTC")
	// Switched on Monday 09:30, after the 09:00 slot: not sent at once.
	e.n.digestIfDue(ctx) // first run after a restore-like switch-on records enabledAt
	_ = e.n.DigestEnabled(ctx)
	e.n.digestIfDue(ctx)
	if len(e.mm.texts()) != 0 {
		t.Fatal("sent the slot from before it was switched on")
	}
	// Next Monday 09:00: sent once, by Mattermost and email to the active opted-in user.
	e.now = time.Date(2026, 11, 23, 9, 0, 30, 0, time.UTC)
	e.n.digestIfDue(ctx)
	e.n.digestIfDue(ctx)
	if got := e.mm.texts(); len(got) != 1 || !strings.Contains(got[0], "Owl weekly digest") {
		t.Fatalf("digest posts: %q", got)
	}
	if len(fm.sent) != 1 || fm.sent[0].To != "a@example.com" || fm.sent[0].HTML == "" || !strings.Contains(fm.sent[0].Body, "Owl weekly digest") {
		t.Fatalf("mail: %+v", fm.sent)
	}
	// Server down until Tuesday 08:00 of the week after: < 24 h late = catch up once; Wednesday = skipped.
	e.mm.reset()
	e.now = time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	e.n.digestIfDue(ctx)
	if len(e.mm.texts()) != 1 {
		t.Fatal("missed digest not caught up")
	}
	e.mm.reset()
	e.now = time.Date(2026, 12, 9, 10, 0, 0, 0, time.UTC) // Wednesday: Monday's slot is > 24 h old
	e.n.digestIfDue(ctx)
	if len(e.mm.texts()) != 0 {
		t.Fatal("sent a digest more than 24 h late")
	}
	// Mattermost down: retried hourly within the window, email not sent twice.
	e.now = time.Date(2026, 12, 14, 9, 1, 0, 0, time.UTC)
	e.mm.fail = 3
	e.n.digestIfDue(ctx)
	e.now = e.now.Add(30 * time.Minute)
	e.n.digestIfDue(ctx) // within the hour: no attempt
	e.now = e.now.Add(31 * time.Minute)
	e.n.digestIfDue(ctx)
	if got := e.mm.texts(); len(got) != 1 {
		t.Fatalf("retry: %d posts", len(got))
	}
	if len(fm.sent) != 3 { // 23 Nov, 1 Dec (catch-up), 14 Dec (once, despite the Mattermost retry)
		t.Fatalf("emails: %d", len(fm.sent))
	}
}

func TestDigestContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.cluster("it-hetzner", "IT", "redis:7.2.4", "coredns/coredns:1.11.3")
	e.cluster("ds-prod", "DS", "redis:7.2.4")
	e.check()
	e.crit("redis@it-hetzner")
	e.check()
	e.crit()
	e.check() // redis left Critical on it-hetzner
	e.now = time.Now().UTC()
	e.cluster("it-hetzner", "IT", "redis:7.2.5", "coredns/coredns:1.11.1") // redis up, coredns rolled back
	e.rep.Components = []report.ComponentReport{{ID: "redis", Name: "Redis", Level: report.LevelHigh,
		Action: report.Action{Text: "Update to 7.2.6", Why: "fixes 9 high advisories (another tenant's)"},
		Target: &policy.Target{Version: "7.2.6"},
		Instances: []report.ScoredInstance{{Instance: inventory.Instance{Cluster: "it-hetzner", Stage: "live", Version: "7.2.5"},
			Score:  report.Score{Level: report.LevelHigh},
			Action: report.Action{Kind: report.ActionPatch, Text: "Patch to 7.2.6", Why: "fixes 1 high advisory"}}}}}
	d, err := e.n.BuildDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	md := d.Markdown()
	for _, want := range []string{"##### Tenant DS", "##### Tenant IT", "**Redis** — High · **Update to 7.2.6** — fixes 1 high advisory",
		"**Redis** `7.2.4` → `7.2.5` on it-hetzner — now High", "⚠ Rolled back this week", "**CoreDNS** `1.11.3` → `1.11.1` on it-hetzner",
		"**No longer Critical**", "**Redis** on it-hetzner — now High"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "another tenant") {
		t.Errorf("a tenant section shows reasons from another tenant:\n%s", md)
	}
	if strings.Count(md, "Rolled back") != 1 {
		t.Errorf("rollback listed in the wrong tenant:\n%s", md)
	}
	html := d.HTML()
	if strings.Contains(html, "<script") || strings.Contains(html, "http://") || !strings.Contains(html, "Tenant IT") {
		t.Errorf("html: %s", html)
	}
}

func TestEscaping(t *testing.T) {
	d := Doc{Title: "x", Intro: []Line{Ln(T("@channel *bold* [x](javascript:alert(1)) <b> #h")), Ln(C("a`b@all"))}}
	md := d.Markdown()
	if strings.Contains(md, "@channel") || strings.Contains(md, "@all") || strings.Contains(md, "*bold*") ||
		strings.Contains(md, "[x](") || strings.Contains(md, "<b>") {
		t.Errorf("not escaped: %q", md)
	}
	if h := d.HTML(); strings.Contains(h, "<b>") && !strings.Contains(h, "&lt;b&gt;") {
		t.Errorf("html not escaped: %q", h)
	}
	long := strings.Repeat("- line\n", 4000)
	if f := Fit(long, "…cut"); len(f) > maxPost || !strings.HasSuffix(f, "…cut") {
		t.Errorf("Fit: %d", len(f))
	}
}

func TestWebhook(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://mm.example.com/hooks/abc": true, "http://127.0.0.1:8065/hooks/abc": true, "http://localhost/hooks/a": true, "http://owl-mattermost:8065/hooks/a": true,
		"http://mm.example.com/hooks/abc": false, "ftp://x/y": false, "https://user:pw@mm/x": false, "not a url": false,
	} {
		if err := ValidateWebhookURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
	mm := &fakeMM{fail: 2}
	srv := httptest.NewServer(mm)
	defer srv.Close()
	w := &Webhook{URL: srv.URL + "/hooks/secretkey", Username: "Owl", Backoff: time.Millisecond}
	if err := w.Post(context.Background(), "hi"); err != nil || len(mm.posts) != 1 || mm.posts[0]["username"] != "Owl" {
		t.Fatalf("retry on 500: %v %+v", err, mm.posts)
	}
	mm.fail, mm.code = 5, 400
	if err := w.Post(context.Background(), "hi"); err == nil || mm.fail != 4 {
		t.Fatalf("400 retried (left %d) %v", mm.fail, err)
	}
	dead := &Webhook{URL: "http://127.0.0.1:1/hooks/secretkey", Backoff: time.Millisecond}
	if err := dead.Post(context.Background(), "hi"); err == nil || strings.Contains(err.Error(), "secretkey") {
		t.Fatalf("error leaks the URL: %v", err)
	}
}

func TestSlots(t *testing.T) {
	s := Defaults() // Monday 09:00 Europe/Belgrade
	loc, _ := time.LoadLocation("Europe/Belgrade")
	for now, want := range map[time.Time]time.Time{
		time.Date(2026, 11, 16, 9, 0, 0, 0, loc):  time.Date(2026, 11, 16, 9, 0, 0, 0, loc),
		time.Date(2026, 11, 16, 8, 59, 0, 0, loc): time.Date(2026, 11, 9, 9, 0, 0, 0, loc),
		time.Date(2026, 11, 19, 12, 0, 0, 0, loc): time.Date(2026, 11, 16, 9, 0, 0, 0, loc),
		time.Date(2026, 3, 31, 12, 0, 0, 0, loc):  time.Date(2026, 3, 30, 9, 0, 0, 0, loc), // after DST starts (29 Mar)
	} {
		if got := s.LastSlot(now.UTC()); !got.Equal(want) {
			t.Errorf("LastSlot(%v) = %v, want %v", now, got, want)
		}
	}
	if got := s.NextSlot(time.Date(2026, 3, 24, 12, 0, 0, 0, loc)); !got.Equal(time.Date(2026, 3, 30, 9, 0, 0, 0, loc)) {
		t.Errorf("NextSlot across DST = %v", got)
	}
	bad := Settings{DigestDay: "funday", DigestTime: "9:00", DigestTZ: "UTC"}
	if bad.Normalize() == nil {
		t.Error("accepted funday")
	}
	ok := Settings{DigestDay: "Friday", DigestTime: "7:05", DigestTZ: "UTC"}
	if err := ok.Normalize(); err != nil || ok.DigestDay != "friday" || ok.DigestTime != "07:05" {
		t.Errorf("normalize: %+v %v", ok, err)
	}
	for _, tz := range []string{"", "Local", "../../etc/passwd", "Mars/Olympus"} {
		s := Settings{DigestDay: "monday", DigestTime: "09:00", DigestTZ: tz}
		if s.Normalize() == nil {
			t.Errorf("accepted time zone %q", tz)
		}
	}
}

// A refresh with upstream errors can lower levels: those pairs aren't resolved (and so aren't announced
// again as "newly Critical" once a complete refresh comes back).
func TestIncompleteUpstreamDoesNotResolve(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT")
	e.crit("redis@it-hetzner")
	e.check()
	broken := true
	e.n.Incomplete = func(id string) bool { return broken && id == "redis" }
	e.crit()
	e.check()
	if res, _ := e.db.ListResolved(context.Background(), time.Time{}); len(res) != 0 {
		t.Fatalf("resolved on incomplete data: %+v", res)
	}
	broken = false
	e.crit("redis@it-hetzner")
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("re-announced after recovery: %q", got)
	}
}

// Checks wait while a restore runs, and the restored state is recorded without posting.
func TestRestoreHoldsChecks(t *testing.T) {
	e := newEnv(t)
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT")
	e.check()
	started, done := make(chan struct{}), make(chan error)
	go func() {
		done <- e.n.WhileQuiet(context.Background(), func() error {
			close(started)
			time.Sleep(50 * time.Millisecond) // a kicked check must not run in here
			e.crit("redis@it-hetzner")        // the backup had a Critical pair
			return nil
		})
	}()
	<-started
	go e.n.Check(context.Background(), false)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.check()
	if got := e.mm.texts(); len(got) != 0 {
		t.Fatalf("restore posted: %q", got)
	}
}

// A slow Mattermost doesn't hold the lock: a restore (or the next check) isn't blocked by a post.
func TestSlowWebhookDoesNotBlock(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)
	e.n.Webhook = &Webhook{URL: slow.URL, Backoff: time.Millisecond}
	e.set(KeyCritical, "on")
	e.cluster("it-hetzner", "IT")
	e.check()
	e.crit("redis@it-hetzner")
	go e.n.Check(context.Background(), false) // stuck posting
	time.Sleep(50 * time.Millisecond)
	ok := make(chan error, 1)
	go func() { ok <- e.n.WhileQuiet(context.Background(), func() error { return nil }) }()
	select {
	case err := <-ok:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a restore waited for a Mattermost post")
	}
}

func TestNoLongerCriticalSkipsCriticalAgain(t *testing.T) {
	e := newEnv(t)
	e.cluster("it-hetzner", "IT")
	e.crit("redis@it-hetzner")
	e.check()
	e.crit()
	e.check() // resolved
	e.crit("redis@it-hetzner")
	e.check() // Critical again
	d, err := e.n.BuildDigest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md := d.Markdown(); strings.Contains(md, "No longer Critical") {
		t.Errorf("lists a pair that is Critical again:\n%s", md)
	}
}

func TestCriticalTitleCountsComponentsAndClusters(t *testing.T) {
	n := &Notifier{}
	one := n.criticalDoc([]*critical{{pair: pair{"lpp", "a"}, Name: "LPP"}, {pair: pair{"lpp", "b"}, Name: "LPP"}}).Title
	two := n.criticalDoc([]*critical{{pair: pair{"lpp", "a"}, Name: "LPP"}, {pair: pair{"redis", "a"}, Name: "Redis"}}).Title
	if !strings.Contains(one, "LPP became Critical on 2 clusters") || !strings.Contains(two, "2 components became Critical") || strings.Contains(two, "clusters") {
		t.Errorf("%q / %q", one, two)
	}
}
