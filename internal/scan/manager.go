package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"owl/internal/store"
)

// Scanner is Trivy (a fake in tests).
type Scanner interface {
	Scan(ctx context.Context, t Target, cred *Credential) (*Result, error)
	UpdateDB(ctx context.Context) error
	DBInfo(ctx context.Context) (DBInfo, error)
}

// Store is the part of store.Store the manager uses.
type Store interface {
	ListImageScans(ctx context.Context) ([]store.ImageScan, error)
	PutImageScan(ctx context.Context, s store.ImageScan) error
	DeleteImageScansExcept(ctx context.Context, keep []string) (int64, error)
	GetSettings(ctx context.Context) (map[string]string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Target is a running image digest that can be pulled from a registry.
type Target struct {
	Digest     string `json:"digest"` // sha256:…
	Ref        string `json:"ref"`    // repository@sha256:…
	Repository string `json:"repository"`
	// Platform is scanned for a multi-arch image ("linux/amd64"): the nodes' architecture, not the
	// server's (a t4g server is arm64; its clusters may not be). "" = Trivy's default.
	Platform string `json:"platform,omitempty"`
}

// TargetFromImageID turns a kubelet image ID ("docker.io/library/redis@sha256:…", possibly with a
// docker-pullable:// prefix) into a target. A bare "sha256:…" (an image built or imported on the node,
// not pulled from a registry) can't be scanned: ok=false.
func TargetFromImageID(imageID, arch string) (Target, bool) {
	id := strings.TrimPrefix(strings.TrimPrefix(imageID, "docker-pullable://"), "docker://")
	repo, digest, ok := strings.Cut(id, "@")
	if !ok || repo == "" || !ValidDigest(digest) {
		return Target{}, false
	}
	t := Target{Digest: digest, Ref: repo + "@" + digest, Repository: repo}
	if slices.Contains(knownArch, arch) {
		t.Platform = "linux/" + arch
	}
	return t, true
}

// RegistryOf is the registry host of a target (docker.io for Docker Hub's short names).
func (t Target) Registry() string { return registryOf(t.Ref) }

// ValidDigest: sha256:<64 lower-case hex>.
func ValidDigest(d string) bool {
	h, ok := strings.CutPrefix(d, "sha256:")
	if !ok || len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Stored is what Owl knows about one digest: the last good result and the last failure (if the last
// attempt failed). Values are never changed once published — an update replaces the pointer.
type Stored struct {
	Digest    string     `json:"digest"`
	Ref       string     `json:"ref"`
	ScannedAt *time.Time `json:"scannedAt,omitempty"` // last good scan
	Result    *Result    `json:"-"`
	Error     string     `json:"error,omitempty"` // the last attempt failed
	AttemptAt time.Time  `json:"attemptAt"`
}

// Reasons a digest is queued.
const (
	ReasonNew    = "new"
	ReasonDaily  = "daily"
	ReasonManual = "manual"
)

// Outcomes of a scan.
const (
	OutcomeOK        = "ok"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
	OutcomeTimeout   = "timeout"
	OutcomeDropped   = "removed from queue"
)

type Job struct {
	Target
	Reason   string    `json:"reason"`
	QueuedAt time.Time `json:"queuedAt"`
}

type Running struct {
	Job
	StartedAt  time.Time `json:"startedAt"`
	TimeoutSec int64     `json:"timeoutSeconds"`
}

type Finished struct {
	Job
	StartedAt time.Time `json:"startedAt"`
	TookMS    int64     `json:"tookMs"`
	Outcome   string    `json:"outcome"`
	Error     string    `json:"error,omitempty"`
}

type AuthFailure struct {
	Prefix string    `json:"prefix"` // the credential's prefix, or "anonymous: <registry>"
	Ref    string    `json:"ref"`
	At     time.Time `json:"at"`
}

type DBStatus struct {
	DBInfo
	Error    string `json:"error,omitempty"`
	Updating bool   `json:"updating"`
}

// Status is the scanner panel.
type Status struct {
	Current      *Running      `json:"current"`
	Queue        []Job         `json:"queue"` // the first 20
	QueueLen     int           `json:"queueLength"`
	Recent       []Finished    `json:"recent"` // newest first
	DB           DBStatus      `json:"db"`
	LastDaily    *time.Time    `json:"lastDaily"` // last finished daily rescan
	AuthFailures []AuthFailure `json:"authFailures"`
	Prefixes     []string      `json:"credentialPrefixes"` // never the credentials themselves
	Expiring     []Expiring    `json:"credentialsExpiring"`
}

const (
	settingLastDaily = "scanLastDaily"
	recentMax        = 20
	queueShown       = 20
)

// Manager owns the queue and the worker: one scan at a time.
type Manager struct {
	Scanner Scanner
	Store   Store
	Creds   Credentials // the initial list; then SetCredentials / Credentials (guarded by mu)
	// Targets lists every running digest that can be scanned (from the clusters' current state).
	Targets func(ctx context.Context) ([]Target, error)
	Log     *slog.Logger
	Timeout time.Duration // per scan; default 10 minutes
	Now     func() time.Time

	mu        sync.Mutex
	results   map[string]*Stored
	queue     []Job
	cur       *Running
	curCancel context.CancelFunc
	cancelled bool // the running scan was cancelled by an admin
	recent    []Finished
	db        DBStatus
	dbReady   bool
	lastDaily time.Time
	daily     map[string]bool // digests of the running daily rescan not done yet
	authFail  map[string]AuthFailure
	credsSet  bool // SetCredentials ran (the first call, at start, retries nothing)

	kick chan struct{} // → sync (new digests, gone digests)
	wake chan struct{} // → worker
	once sync.Once
}

func (m *Manager) init() {
	m.once.Do(func() {
		m.kick = make(chan struct{}, 1)
		m.wake = make(chan struct{}, 1)
		m.results = map[string]*Stored{}
		m.daily = map[string]bool{}
		m.authFail = map[string]AuthFailure{}
		if m.Timeout == 0 {
			m.Timeout = 10 * time.Minute
		}
		if m.Now == nil {
			m.Now = time.Now
		}
		if m.Log == nil {
			m.Log = slog.New(slog.DiscardHandler)
		}
	})
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Kick asks for a sync: digests that started running get queued, gone ones lose their results.
func (m *Manager) Kick() {
	m.init()
	signal(m.kick)
}

// Load reads stored results (before Run, so the API has them at once).
func (m *Manager) Load(ctx context.Context) error {
	m.init()
	rows, err := m.Store.ListImageScans(ctx)
	if err != nil {
		return err
	}
	sets, err := m.Store.GetSettings(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		st := &Stored{Digest: r.Digest, Ref: r.Ref, ScannedAt: r.ScannedAt, Error: r.Error, AttemptAt: r.AttemptAt}
		if len(r.Result) > 0 {
			var res Result
			if err := json.Unmarshal(r.Result, &res); err != nil {
				m.Log.Warn("scan result unreadable, will rescan", "digest", r.Digest, "err", err)
				continue
			}
			st.Result = &res
		}
		m.results[r.Digest] = st
	}
	if t, err := time.Parse(time.RFC3339, sets[settingLastDaily]); err == nil {
		m.lastDaily = t
	}
	return nil
}

// Run updates the DB, keeps the queue in sync with what runs and scans, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.init()
	go m.worker(ctx)
	m.ensureDB(ctx)
	m.sync(ctx)
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if m.dailyDue() {
			m.runDaily(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
			m.sync(ctx)
		case <-tick.C:
			if !m.isReady() {
				m.ensureDB(ctx)
			}
		}
	}
}

func (m *Manager) isReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dbReady
}

// ensureDB downloads the DB when there's none or it's older than a day (e.g. after a restart).
func (m *Manager) ensureDB(ctx context.Context) {
	info, err := m.Scanner.DBInfo(ctx)
	if err == nil && m.Now().Sub(info.Downloaded) < 24*time.Hour {
		m.setDB(info, nil)
		return
	}
	m.updateDB(ctx)
}

func (m *Manager) updateDB(ctx context.Context) {
	m.mu.Lock()
	m.db.Updating = true
	m.mu.Unlock()
	err := m.Scanner.UpdateDB(ctx)
	info, ierr := m.Scanner.DBInfo(ctx)
	if err == nil {
		err = ierr
	}
	m.setDB(info, err)
}

func (m *Manager) setDB(info DBInfo, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.db.Updating = false
	if !info.UpdatedAt.IsZero() {
		m.db.DBInfo = info
		m.dbReady = true // an older DB still scans; the error says it's stale
	}
	m.db.Error = ""
	if err != nil {
		m.db.Error = err.Error()
		m.Log.Warn("trivy DB update failed", "err", err)
	}
	if m.dbReady {
		signal(m.wake)
	}
}

func (m *Manager) dailyDue() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.daily) == 0 && m.Now().Sub(m.lastDaily) >= 24*time.Hour
}

// runDaily updates the DB, then queues every running digest (except those already queued or running).
func (m *Manager) runDaily(ctx context.Context) {
	m.mu.Lock()
	fresh := m.dbReady && m.Now().Sub(m.db.Downloaded) < time.Hour // just downloaded at startup
	m.mu.Unlock()
	if !fresh {
		m.updateDB(ctx)
	}
	targets, err := m.Targets(ctx)
	if err != nil {
		m.Log.Warn("scan: listing running images failed", "err", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range targets {
		if m.queuedOrRunning(t.Digest) {
			continue
		}
		m.queue = append(m.queue, Job{Target: t, Reason: ReasonDaily, QueuedAt: m.Now()})
		m.daily[t.Digest] = true
		n++
	}
	if n == 0 {
		m.finishDaily()
	}
	m.Log.Info("daily rescan queued", "images", n)
	signal(m.wake)
}

// finishDaily records the end of a daily rescan (mu held).
func (m *Manager) finishDaily() {
	m.lastDaily = m.Now()
	ts := m.lastDaily.UTC().Format(time.RFC3339)
	go func() {
		if err := m.Store.SetSetting(context.Background(), settingLastDaily, ts); err != nil {
			m.Log.Warn("scan: saving last daily rescan failed", "err", err)
		}
	}()
}

// sync queues digests never attempted and forgets digests that no longer run anywhere.
func (m *Manager) sync(ctx context.Context) {
	targets, err := m.Targets(ctx)
	if err != nil {
		m.Log.Warn("scan: listing running images failed", "err", err)
		return
	}
	running := map[string]bool{}
	keep := []string{}
	for _, t := range targets {
		if !running[t.Digest] {
			running[t.Digest] = true
			keep = append(keep, t.Digest)
		}
	}
	m.mu.Lock()
	gone := 0
	for d := range m.results {
		if !running[d] {
			delete(m.results, d)
			gone++
		}
	}
	m.queue = slices.DeleteFunc(m.queue, func(j Job) bool {
		if !running[j.Digest] {
			m.doneDaily(j.Digest) // the last ones of a daily rescan gone: it's finished, not due again
			return true
		}
		return false
	})
	added := 0
	for _, t := range targets {
		if _, known := m.results[t.Digest]; known || m.queuedOrRunning(t.Digest) {
			continue
		}
		m.queue = append(m.queue, Job{Target: t, Reason: ReasonNew, QueuedAt: m.Now()})
		added++
	}
	m.mu.Unlock()
	if gone > 0 {
		if _, err := m.Store.DeleteImageScansExcept(ctx, keep); err != nil {
			m.Log.Warn("scan: pruning results failed", "err", err)
		}
	}
	if added > 0 {
		m.Log.Info("images queued for scanning", "new", added)
		signal(m.wake)
	}
}

// queuedOrRunning (mu held).
func (m *Manager) queuedOrRunning(digest string) bool {
	if m.cur != nil && m.cur.Digest == digest {
		return true
	}
	return slices.ContainsFunc(m.queue, func(j Job) bool { return j.Digest == digest })
}

var (
	ErrNotRunning = errors.New("this image isn't running in any cluster")
	ErrNotQueued  = errors.New("this image isn't being scanned or queued")
)

// Rescan puts a running digest at the front of the queue. Already queued or running = nothing happens
// (queued=false).
func (m *Manager) Rescan(ctx context.Context, digest string) (queued bool, err error) {
	m.init()
	targets, err := m.Targets(ctx)
	if err != nil {
		return false, err
	}
	i := slices.IndexFunc(targets, func(t Target) bool { return t.Digest == digest })
	if i < 0 {
		return false, ErrNotRunning
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queuedOrRunning(digest) {
		return false, nil
	}
	m.queue = slices.Insert(m.queue, 0, Job{Target: targets[i], Reason: ReasonManual, QueuedAt: m.Now()})
	signal(m.wake)
	return true, nil
}

// Cancel stops the running scan of digest, or removes it from the queue.
func (m *Manager) Cancel(digest string) (Job, error) {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil && m.cur.Digest == digest {
		m.cancelled = true
		m.curCancel()
		return m.cur.Job, nil
	}
	i := slices.IndexFunc(m.queue, func(j Job) bool { return j.Digest == digest })
	if i < 0 {
		return Job{}, ErrNotQueued
	}
	j := m.queue[i]
	m.queue = slices.Delete(m.queue, i, i+1)
	m.addRecent(Finished{Job: j, StartedAt: m.Now(), Outcome: OutcomeDropped})
	m.doneDaily(j.Digest)
	row := m.recordStop(j, "removed from the queue by an admin")
	go m.save(context.Background(), row)
	return j, nil
}

// recordStop remembers that an admin stopped this digest's scan (mu held), so a sync doesn't queue it
// again as new; the daily rescan does. An earlier good result is kept.
func (m *Manager) recordStop(j Job, why string) store.ImageScan {
	st := &Stored{Digest: j.Digest, Ref: j.Ref, Error: why, AttemptAt: m.Now()}
	if prev := m.results[j.Digest]; prev != nil {
		st.ScannedAt, st.Result = prev.ScannedAt, prev.Result
	}
	m.results[j.Digest] = st
	return rowOf(st)
}

func rowOf(st *Stored) store.ImageScan {
	row := store.ImageScan{Digest: st.Digest, Ref: st.Ref, ScannedAt: st.ScannedAt, Error: st.Error, AttemptAt: st.AttemptAt}
	if st.Result != nil {
		row.Result, _ = json.Marshal(st.Result)
	}
	return row
}

func (m *Manager) save(ctx context.Context, row store.ImageScan) {
	if err := m.Store.PutImageScan(ctx, row); err != nil {
		m.Log.Warn("scan: saving result failed", "digest", row.Digest, "err", err)
	}
}

func (m *Manager) addRecent(f Finished) {
	m.recent = slices.Insert(m.recent, 0, f)
	if len(m.recent) > recentMax {
		m.recent = m.recent[:recentMax]
	}
}

func (m *Manager) doneDaily(digest string) {
	if m.daily[digest] {
		delete(m.daily, digest)
		if len(m.daily) == 0 {
			m.finishDaily()
		}
	}
}

func (m *Manager) worker(ctx context.Context) {
	for {
		job, ok := m.next(ctx)
		if !ok {
			return
		}
		m.scan(ctx, job)
	}
}

// next waits for a job (and a DB).
func (m *Manager) next(ctx context.Context) (Job, bool) {
	for {
		m.mu.Lock()
		if m.dbReady && len(m.queue) > 0 {
			j := m.queue[0]
			m.queue = m.queue[1:]
			m.mu.Unlock()
			return j, true
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return Job{}, false
		case <-m.wake:
		}
	}
}

func (m *Manager) scan(ctx context.Context, job Job) {
	sctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	start := m.Now()
	m.mu.Lock()
	cred := m.Creds.For(job.Ref)
	m.cur, m.curCancel, m.cancelled = &Running{Job: job, StartedAt: start, TimeoutSec: int64(m.Timeout / time.Second)}, cancel, false
	m.mu.Unlock()

	res, err := m.Scanner.Scan(sctx, job.Target, cred)

	m.mu.Lock()
	cancelled := m.cancelled
	m.cur, m.curCancel = nil, nil
	f := Finished{Job: job, StartedAt: start, TookMS: m.Now().Sub(start).Milliseconds(), Outcome: OutcomeOK}
	prefix := "anonymous: " + registryOf(job.Ref)
	if cred != nil {
		prefix = cred.Prefix
	}
	switch {
	case cancelled:
		f.Outcome = OutcomeCancelled
	case errors.Is(sctx.Err(), context.DeadlineExceeded):
		f.Outcome, f.Error = OutcomeTimeout, fmt.Sprintf("no result within %s", m.Timeout)
	case ctx.Err() != nil: // shutting down: retried after the restart
		m.mu.Unlock()
		return
	case errors.Is(err, ErrAuth) && !sameCred(cred, m.Creds.For(job.Ref)):
		// The login was changed while this scan ran with the old one: try again with the new one, and
		// don't mark the new one as failing.
		m.cur, m.curCancel = nil, nil
		m.queue = slices.Insert(m.queue, m.afterManual(), Job{Target: job.Target, Reason: ReasonManual, QueuedAt: m.Now()})
		m.mu.Unlock()
		signal(m.wake)
		return
	case errors.Is(err, ErrAuth):
		f.Outcome, f.Error = OutcomeFailed, authFailedPrefix+prefix
		m.authFail[prefix] = AuthFailure{Prefix: prefix, Ref: job.Ref, At: m.Now()}
	case errors.Is(err, ErrNotFound):
		f.Outcome, f.Error = OutcomeFailed, "not in the registry (pulled from a mirror, deleted, or built locally)"
	case err != nil:
		f.Outcome, f.Error = OutcomeFailed, err.Error()
	}
	if f.Outcome == OutcomeOK || errors.Is(err, ErrNotFound) {
		delete(m.authFail, prefix) // the credential (or anonymous access) works
	}
	m.addRecent(f)
	m.doneDaily(job.Digest)
	var row store.ImageScan
	if f.Outcome == OutcomeCancelled {
		row = m.recordStop(job, "cancelled by an admin")
	} else {
		prev := m.results[job.Digest]
		st := &Stored{Digest: job.Digest, Ref: job.Ref, AttemptAt: m.Now()}
		if f.Outcome == OutcomeOK {
			t := m.Now()
			st.ScannedAt, st.Result = &t, res
		} else {
			st.Error = f.Error
			if prev != nil { // keep the last good result
				st.ScannedAt, st.Result = prev.ScannedAt, prev.Result
			}
		}
		m.results[job.Digest] = st
		row = rowOf(st)
	}
	m.mu.Unlock()
	m.save(ctx, row)
	m.Log.Info("image scanned", "ref", job.Ref, "reason", job.Reason, "outcome", f.Outcome, "took", time.Duration(f.TookMS)*time.Millisecond)
}

// registryOf: the registry host of a reference ("docker.io" for Docker Hub's short names).
func registryOf(ref string) string {
	host, _, ok := strings.Cut(normPrefix(ref), "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "docker.io"
	}
	return host
}

// Credentials returns the credentials in use (shared: don't change them).
func (m *Manager) Credentials() Credentials {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Creds
}

// SetCredentials replaces the credentials (a change in the UI, a restore). Images whose last scan failed
// to log in are queued again at once, and auth failures of prefixes that changed are forgotten.
func (m *Manager) SetCredentials(ctx context.Context, cs Credentials) {
	m.init()
	targets := map[string]Target{}
	if m.Targets != nil {
		ts, err := m.Targets(ctx)
		if err != nil {
			m.Log.Warn("scan: listing running images failed", "err", err)
		}
		for _, t := range ts {
			if _, ok := targets[t.Digest]; !ok {
				targets[t.Digest] = t
			}
		}
	}
	m.mu.Lock()
	old := map[string]Credential{}
	for _, c := range m.Creds {
		old[c.Prefix] = c
	}
	prev, first := m.Creds, !m.credsSet
	m.Creds, m.credsSet = cs, true
	for _, c := range cs {
		if o, ok := old[c.Prefix]; !ok || o.Username != c.Username || o.Password != c.Password {
			delete(m.authFail, c.Prefix)
			delete(m.authFail, "anonymous: "+registryOf(c.Prefix+"/x"))
		}
		delete(old, c.Prefix)
	}
	for p := range old { // removed: its failures no longer say anything
		delete(m.authFail, p)
	}
	// Retry what failed to log in — only where the login that applies changed (not at start, not for an
	// expiry edit), after the rescans an admin asked for.
	retry := 0
	for _, st := range m.results {
		t, running := targets[st.Digest]
		if first || !running || !strings.HasPrefix(st.Error, authFailedPrefix) || m.queuedOrRunning(st.Digest) ||
			sameCred(prev.For(st.Ref), cs.For(st.Ref)) {
			continue
		}
		m.queue = slices.Insert(m.queue, m.afterManual(), Job{Target: t, Reason: ReasonManual, QueuedAt: m.Now()})
		retry++
	}
	m.mu.Unlock()
	if retry > 0 {
		m.Log.Info("registry credentials changed: retrying images that failed to log in", "images", retry)
		signal(m.wake)
	}
	signal(m.kick) // registries a new prefix allows (IP-literal hosts) start being scanned
}

const authFailedPrefix = "auth failed for "

// sameCred: both nil, or the same login for the same prefix (the expiry doesn't matter).
func sameCred(a, b *Credential) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Prefix == b.Prefix && a.Username == b.Username && a.Password == b.Password
}

// afterManual: the queue position after the manual jobs at its front (mu held).
func (m *Manager) afterManual() int {
	i := 0
	for i < len(m.queue) && m.queue[i].Reason == ReasonManual {
		i++
	}
	return i
}

// Results returns every known digest (the values are shared and must not be changed).
func (m *Manager) Results() map[string]*Stored {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*Stored, len(m.results))
	for k, v := range m.results {
		out[k] = v
	}
	return out
}

// State is where one digest is in the queue: "scanning", "queued" or "".
func (m *Manager) State() map[string]string {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, j := range m.queue {
		out[j.Digest] = "queued"
	}
	if m.cur != nil {
		out[m.cur.Digest] = "scanning"
	}
	return out
}

func (m *Manager) Status() Status {
	m.init()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{
		Queue: []Job{}, QueueLen: len(m.queue), Recent: append([]Finished{}, m.recent...), DB: m.db,
		AuthFailures: []AuthFailure{}, Prefixes: []string{},
	}
	if m.cur != nil {
		c := *m.cur
		st.Current = &c
	}
	st.Queue = append(st.Queue, m.queue[:min(len(m.queue), queueShown)]...)
	if !m.lastDaily.IsZero() {
		t := m.lastDaily
		st.LastDaily = &t
	}
	for _, a := range m.authFail {
		st.AuthFailures = append(st.AuthFailures, a)
	}
	slices.SortFunc(st.AuthFailures, func(a, b AuthFailure) int { return strings.Compare(a.Prefix, b.Prefix) })
	for _, c := range m.Creds {
		st.Prefixes = append(st.Prefixes, c.Prefix)
	}
	st.Expiring = m.Creds.Expiring(m.Now())
	return st
}
