// Package backup takes scheduled and manual database backups, copies them to S3, applies local
// retention and restores. See DESIGN.md "Operations".
package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"owl/internal/store"
)

// Settings keys (stored with store.SetSetting, edited in the UI).
const (
	KeyTime        = "backupTime"        // "03:00", server local time
	KeyLocalDays   = "backupLocalDays"   // without S3: keep local backups this many days
	KeyLocalCopies = "backupLocalCopies" // with S3: keep this many local copies (uploaded ones only)
)

type Settings struct {
	Hour, Minute int
	LocalDays    int
	LocalCopies  int
}

var Defaults = Settings{Hour: 3, Minute: 0, LocalDays: 15, LocalCopies: 3}

func (s Settings) TimeString() string { return fmt.Sprintf("%02d:%02d", s.Hour, s.Minute) }

// ParseSettings reads the stored values, falling back to defaults for anything missing or invalid.
func ParseSettings(m map[string]string) Settings {
	s := Defaults
	if h, mi, ok := ParseTime(m[KeyTime]); ok {
		s.Hour, s.Minute = h, mi
	}
	if n, err := strconv.Atoi(m[KeyLocalDays]); err == nil && n >= 1 && n <= 3650 {
		s.LocalDays = n
	}
	if n, err := strconv.Atoi(m[KeyLocalCopies]); err == nil && n >= 1 && n <= 1000 {
		s.LocalCopies = n
	}
	return s
}

func ParseTime(v string) (int, int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// Names: owl-20261117-030000.db.gz, optionally with a reason suffix (owl-…-pre-restore.db.gz).
var reName = regexp.MustCompile(`^owl-(\d{8}-\d{6})(-[a-z-]+)?\.db\.gz$`)

// ValidName reports whether n is a backup file name this package produces (safe to use as a path).
func ValidName(n string) bool { return reName.MatchString(n) }

func nameTime(n string) time.Time {
	m := reName.FindStringSubmatch(n)
	if m == nil {
		return time.Time{}
	}
	t, _ := time.ParseInLocation("20060102-150405", m[1], time.UTC)
	return t
}

type Manager struct {
	Store  store.Store
	Dir    string // local backup folder, e.g. /data/backups
	Remote Remote // nil = local only
	Log    *slog.Logger
	Now    func() time.Time

	mu      sync.Mutex // one backup or restore at a time
	stMu    sync.Mutex
	lastOK  time.Time
	lastErr string
	lastTry time.Time
	ranDay  string // day the scheduled backup already ran (local date)
	lastUp  time.Time
}

// RetryUploadsEvery is how often pending uploads are retried between backups (bucket was down).
const RetryUploadsEvery = 10 * time.Minute

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Manager) settings(ctx context.Context) Settings {
	vals, err := m.Store.GetSettings(ctx)
	if err != nil {
		return Defaults
	}
	return ParseSettings(vals)
}

// Info describes one backup, local and/or remote.
type Info struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	At       time.Time `json:"at"`
	Local    bool      `json:"local"`
	Remote   bool      `json:"remote"`
	Uploaded bool      `json:"uploaded"` // local copy confirmed in the bucket
}

// Run takes a backup now: consistent copy -> gzip -> upload pending -> local retention.
func (m *Manager) Run(ctx context.Context, reason string) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.run(ctx, reason, true)
}

// run takes a backup; upload=false skips uploading and pruning (the pre-restore safety backup must not
// wait on a slow or unreachable bucket — it's uploaded by the next run or retry).
func (m *Manager) run(ctx context.Context, reason string, upload bool) (Info, error) {
	at := m.now().UTC()
	m.setStatus(func() { m.lastTry = at })
	info, err := m.take(ctx, at, reason)
	if err != nil {
		m.setStatus(func() { m.lastErr = err.Error() })
		m.Log.Error("backup failed", "err", err)
		return Info{}, err
	}
	m.setStatus(func() { m.lastOK, m.lastErr = at, "" })
	m.Log.Info("backup taken", "name", info.Name, "bytes", info.Size, "reason", reason)
	if !upload {
		return info, nil
	}
	if m.Remote != nil {
		if err := m.uploadPending(ctx); err != nil {
			m.setStatus(func() { m.lastErr = "upload: " + err.Error() })
			m.Log.Error("backup upload failed (kept locally, retried next run)", "err", err)
		}
	}
	m.prune(ctx)
	return info, nil
}

func (m *Manager) take(ctx context.Context, at time.Time, reason string) (Info, error) {
	if err := os.MkdirAll(m.Dir, 0o750); err != nil {
		return Info{}, err
	}
	suffix := ""
	if reason != "" && reason != "scheduled" && reason != "manual" {
		suffix = "-" + reason
	}
	// Names have second resolution: two runs in the same second must not replace each other (the
	// first may already be marked uploaded). Runs are serialized by m.mu, so bumping is race-free.
	var name string
	for {
		name = "owl-" + at.Format("20060102-150405") + suffix + ".db.gz"
		if _, err := os.Stat(filepath.Join(m.Dir, name)); errors.Is(err, os.ErrNotExist) {
			break
		}
		at = at.Add(time.Second)
	}
	raw := filepath.Join(m.Dir, ".tmp-"+strings.TrimSuffix(name, ".gz"))
	defer os.Remove(raw)
	if err := m.Store.Backup(ctx, raw); err != nil {
		return Info{}, err
	}
	final := filepath.Join(m.Dir, name)
	if err := gzipTo(raw, final); err != nil {
		os.Remove(final)
		return Info{}, err
	}
	st, err := os.Stat(final)
	if err != nil {
		return Info{}, err
	}
	return Info{Name: name, Size: st.Size(), At: at, Local: true}, nil
}

func gzipTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		os.Remove(tmp)
		return fmt.Errorf("%s already exists", filepath.Base(dst)) // never replace a backup
	}
	return os.Rename(tmp, dst)
}

func marker(dir, name string) string { return filepath.Join(dir, "."+name+".uploaded") }

// uploadPending uploads every local backup without an upload marker (failed runs retry here).
func (m *Manager) uploadPending(ctx context.Context) error {
	local, err := m.local()
	if err != nil {
		return err
	}
	var firstErr error
	for _, b := range local {
		if b.Uploaded {
			continue
		}
		f, err := os.Open(filepath.Join(m.Dir, b.Name))
		if err != nil {
			return err
		}
		err = m.Remote.Put(ctx, b.Name, f, b.Size)
		f.Close()
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", b.Name, err)
			}
			continue
		}
		if err := os.WriteFile(marker(m.Dir, b.Name), nil, 0o640); err != nil {
			return err
		}
		m.Log.Info("backup uploaded", "name", b.Name, "to", m.Remote.String())
	}
	return firstErr
}

// prune applies local retention. With S3: keep the newest LocalCopies, and never delete a copy that
// isn't confirmed uploaded. Without: delete copies older than LocalDays, always keeping the newest.
func (m *Manager) prune(ctx context.Context) {
	set := m.settings(ctx)
	local, err := m.local()
	if err != nil || len(local) == 0 {
		return
	}
	cutoff := m.now().Add(-time.Duration(set.LocalDays) * 24 * time.Hour)
	for i, b := range local { // newest first
		if i == 0 {
			continue
		}
		del := false
		if m.Remote != nil {
			del = i >= set.LocalCopies && b.Uploaded
		} else {
			del = b.At.Before(cutoff)
		}
		if del {
			_ = os.Remove(filepath.Join(m.Dir, b.Name))
			_ = os.Remove(marker(m.Dir, b.Name))
			m.Log.Info("old local backup deleted", "name", b.Name)
		}
	}
}

// local lists local backups, newest first.
func (m *Manager) local() ([]Info, error) {
	entries, err := os.ReadDir(m.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		_, merr := os.Stat(marker(m.Dir, e.Name()))
		out = append(out, Info{Name: e.Name(), Size: st.Size(), At: nameTime(e.Name()), Local: true, Uploaded: merr == nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// List merges local and remote backups, newest first. A remote listing error is returned alongside
// the local list so the UI can still show what's on disk.
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	local, err := m.local()
	if err != nil {
		return nil, err
	}
	out := local
	idx := map[string]int{} // indices, not pointers: append below may move the array
	for i := range out {
		idx[out[i].Name] = i
		out[i].Remote = out[i].Uploaded
	}
	var remoteErr error
	if m.Remote != nil {
		objs, err := m.Remote.List(ctx)
		remoteErr = err
		for _, o := range objs {
			if i, ok := idx[o.Name]; ok {
				out[i].Remote = true
				continue
			}
			out = append(out, Info{Name: o.Name, Size: o.Size, At: nameTime(o.Name), Remote: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, remoteErr
}

// Open returns a backup's content, preferring the local copy.
func (m *Manager) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	if !ValidName(name) {
		return nil, 0, store.ErrNotFound
	}
	if f, err := os.Open(filepath.Join(m.Dir, name)); err == nil {
		st, _ := f.Stat()
		return f, st.Size(), nil
	}
	if m.Remote == nil {
		return nil, 0, store.ErrNotFound
	}
	return m.Remote.Get(ctx, name)
}

// ErrInvalidBackup marks problems with the backup file itself (the caller's fault, not the server's).
var ErrInvalidBackup = errors.New("not a valid backup")

// MaxRestoreBytes bounds an uploaded/decompressed backup (gzip bombs).
const MaxRestoreBytes = 2 << 30

var sqliteMagic = []byte("SQLite format 3\x00")

// Restore checks the backup (gzip or plain SQLite), takes a safety backup of the current state, then
// restores. Returns the safety backup's name.
func (m *Manager) Restore(ctx context.Context, r io.Reader) (string, error) {
	// Read and check the file first, without the lock: a slow upload mustn't block backups.
	if err := os.MkdirAll(m.Dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(m.Dir, ".restore-*.db")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer os.Remove(tmp.Name() + "-wal")
	defer os.Remove(tmp.Name() + "-shm")

	br := bufio.NewReader(r)
	var src io.Reader = br
	if head, _ := br.Peek(2); bytes.Equal(head, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			tmp.Close()
			return "", fmt.Errorf("%w: %v", ErrInvalidBackup, err)
		}
		defer zr.Close()
		src = zr
	}
	n, err := io.Copy(tmp, io.LimitReader(src, MaxRestoreBytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return "", fmt.Errorf("%w: reading it failed: %v", ErrInvalidBackup, err)
	case n > MaxRestoreBytes:
		return "", fmt.Errorf("%w: larger than %d GiB", ErrInvalidBackup, MaxRestoreBytes>>30)
	}
	head := make([]byte, len(sqliteMagic))
	if f, err := os.Open(tmp.Name()); err == nil {
		_, _ = io.ReadFull(f, head)
		f.Close()
	}
	if !bytes.Equal(head, sqliteMagic) {
		return "", fmt.Errorf("%w: not an SQLite database", ErrInvalidBackup)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	safety, err := m.run(ctx, "pre-restore", false)
	if err != nil {
		return "", fmt.Errorf("safety backup failed, nothing restored: %w", err)
	}
	if err := m.Store.Restore(ctx, tmp.Name()); err != nil {
		return safety.Name, fmt.Errorf("restore failed (current data unchanged; safety backup %s): %w", safety.Name, err)
	}
	m.Log.Warn("database restored from backup", "safety", safety.Name)
	return safety.Name, nil
}

// Status is shown in Settings → Backups.
type Status struct {
	Target         string    `json:"target"` // "" = local only
	Dir            string    `json:"dir"`
	LastOK         time.Time `json:"lastOK"`
	LastAttempt    time.Time `json:"lastAttempt"`
	LastError      string    `json:"lastError,omitempty"`
	PendingUploads int       `json:"pendingUploads"`
	NextRun        time.Time `json:"nextRun"`
	Warnings       []string  `json:"warnings"`
}

func (m *Manager) Status(ctx context.Context) Status {
	set := m.settings(ctx)
	var s Status
	s.Dir = m.Dir
	if m.Remote != nil {
		s.Target = m.Remote.String()
	}
	m.stMu.Lock()
	s.LastOK, s.LastAttempt, s.LastError = m.lastOK, m.lastTry, m.lastErr
	m.stMu.Unlock()
	local, _ := m.local()
	if s.LastOK.IsZero() && len(local) > 0 {
		s.LastOK = local[0].At // after a restart, the newest file tells when the last backup ran
	}
	for _, b := range local {
		if m.Remote != nil && !b.Uploaded {
			s.PendingUploads++
		}
	}
	m.stMu.Lock()
	ran := m.ranDay
	m.stMu.Unlock()
	s.NextRun = next(set, m.now(), ran)
	s.Warnings = []string{}
	if m.Remote == nil {
		s.Warnings = append(s.Warnings, "Local only — not safe: backups are on the same disk as the database. Configure OWL_BACKUP_S3_* on the server.")
	}
	if s.LastError != "" {
		s.Warnings = append(s.Warnings, "Last backup run had a problem: "+s.LastError)
	}
	if s.PendingUploads > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf("%d backup(s) not uploaded yet — kept locally and retried on the next run.", s.PendingUploads))
	}
	if s.LastOK.IsZero() {
		s.Warnings = append(s.Warnings, "No backup has been taken yet.")
	} else if m.now().Sub(s.LastOK) > 36*time.Hour {
		s.Warnings = append(s.Warnings, "The newest backup is more than 36 hours old.")
	}
	return s
}

// scheduledAt is today's scheduled time (local).
func scheduledAt(set Settings, now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), set.Hour, set.Minute, 0, 0, now.Location())
}

// next is the next scheduled run in local time.
func next(set Settings, now time.Time, ranDay string) time.Time {
	t := scheduledAt(set, now)
	if !now.Before(t) || ranDay == now.Format("2006-01-02") {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// RunScheduler takes the daily backup at the configured time (checked every minute, so a changed
// time applies right away). A backup already taken today after that time counts.
func (m *Manager) RunScheduler(ctx context.Context) {
	if local, _ := m.local(); len(local) > 0 {
		set, now := m.settings(ctx), m.now()
		if !local[0].At.Before(scheduledAt(set, now)) {
			m.setStatus(func() { m.ranDay = now.Format("2006-01-02") })
		}
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) tick(ctx context.Context) {
	set, now := m.settings(ctx), m.now()
	day := now.Format("2006-01-02")
	sched := scheduledAt(set, now)
	m.stMu.Lock()
	ran := m.ranDay
	m.stMu.Unlock()
	if ran == day || now.Before(sched) {
		m.retryUploads(ctx, now)
		return
	}
	m.setStatus(func() { m.ranDay = day }) // one attempt per day; a failure shows as a warning, the next day retries
	_, _ = m.Run(ctx, "scheduled")
}

// retryUploads uploads backups that a failed run left behind, at most every RetryUploadsEvery.
func (m *Manager) retryUploads(ctx context.Context, now time.Time) {
	if m.Remote == nil || now.Sub(m.lastUp) < RetryUploadsEvery {
		return
	}
	m.lastUp = now
	local, _ := m.local()
	pending := false
	for _, b := range local {
		pending = pending || !b.Uploaded
	}
	if !pending {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.uploadPending(ctx); err != nil {
		m.setStatus(func() { m.lastErr = "upload: " + err.Error() })
		return
	}
	m.setStatus(func() {
		if strings.HasPrefix(m.lastErr, "upload: ") {
			m.lastErr = ""
		}
	})
	m.prune(ctx)
}

func (m *Manager) setStatus(f func()) {
	m.stMu.Lock()
	f()
	m.stMu.Unlock()
}
