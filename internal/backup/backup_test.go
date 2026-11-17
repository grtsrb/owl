package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"owl/internal/store"
	"owl/internal/store/sqlite"
)

type fakeRemote struct {
	mu   sync.Mutex
	objs map[string][]byte
	fail bool
}

func (f *fakeRemote) Put(_ context.Context, name string, r io.Reader, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("bucket unreachable")
	}
	b, _ := io.ReadAll(r)
	f.objs[name] = b
	return nil
}
func (f *fakeRemote) Get(_ context.Context, name string) (io.ReadCloser, int64, error) {
	b, ok := f.objs[name]
	if !ok {
		return nil, 0, store.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}
func (f *fakeRemote) List(context.Context) ([]Object, error) {
	var out []Object
	for n, b := range f.objs {
		out = append(out, Object{Name: n, Size: int64(len(b))})
	}
	return out, nil
}
func (f *fakeRemote) String() string { return "fake" }

func setup(t *testing.T, remote Remote) (*Manager, *sqlite.DB, *time.Time) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "owl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	m := &Manager{Store: db, Dir: filepath.Join(t.TempDir(), "backups"), Remote: remote,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return clock }}
	return m, db, &clock
}

func TestLocalOnlyRetention(t *testing.T) {
	ctx := context.Background()
	m, db, clock := setup(t, nil)
	_ = db.SetSetting(ctx, KeyLocalDays, "15")
	for i := 0; i < 20; i++ {
		if _, err := m.Run(ctx, "manual"); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(24 * time.Hour)
	}
	local, _ := m.local()
	if len(local) != 16 { // today's + 15 days back (the cutoff is exclusive of exactly 15 days)
		t.Errorf("want ~15 days of backups, got %d", len(local))
	}
	if st := m.Status(ctx); len(st.Warnings) == 0 || st.Target != "" {
		t.Errorf("local only must warn: %+v", st)
	}
}

func TestRemoteUploadAndRetention(t *testing.T) {
	ctx := context.Background()
	r := &fakeRemote{objs: map[string][]byte{}}
	m, _, clock := setup(t, r)

	r.fail = true
	for i := 0; i < 5; i++ {
		if _, err := m.Run(ctx, "manual"); err != nil {
			t.Fatal(err) // the backup itself succeeds; only the upload fails
		}
		*clock = clock.Add(time.Hour)
	}
	if local, _ := m.local(); len(local) != 5 {
		t.Fatalf("copies that aren't uploaded must never be deleted: %d", len(local))
	}
	if st := m.Status(ctx); st.PendingUploads != 5 || st.LastError == "" {
		t.Errorf("pending uploads must show: %+v", st)
	}

	r.fail = false
	if _, err := m.Run(ctx, "manual"); err != nil {
		t.Fatal(err)
	}
	if len(r.objs) != 6 {
		t.Errorf("pending ones must be uploaded on the next run: %d in bucket", len(r.objs))
	}
	if local, _ := m.local(); len(local) != 3 {
		t.Errorf("with S3 keep the last 3 local copies: %d", len(local))
	}
	list, _ := m.List(ctx)
	if len(list) != 6 || !list[0].Remote {
		t.Errorf("list must merge local and remote: %+v", list)
	}
	// An object only in the bucket can still be opened.
	if rc, _, err := m.Open(ctx, list[5].Name); err != nil {
		t.Errorf("open remote-only backup: %v", err)
	} else {
		rc.Close()
	}
	if _, _, err := m.Open(ctx, "../../etc/passwd"); !errors.Is(err, store.ErrNotFound) {
		t.Error("names outside the backup pattern must be refused")
	}
}

func TestRestoreFromBackup(t *testing.T) {
	ctx := context.Background()
	m, db, clock := setup(t, nil)
	_, _ = db.CreateCluster(ctx, "prod", store.StageLive, "IT")
	b, err := m.Run(ctx, "manual")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.DeleteCluster(ctx, "prod")
	*clock = clock.Add(time.Minute)

	f, _ := os.Open(filepath.Join(m.Dir, b.Name))
	defer f.Close()
	safety, err := m.Restore(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetCluster(ctx, "prod"); err != nil {
		t.Errorf("cluster not restored: %v", err)
	}
	if !ValidName(safety) || safety == b.Name {
		t.Errorf("a safety backup must be taken first: %q", safety)
	}
	if _, err := m.Restore(ctx, bytes.NewReader([]byte("garbage"))); err == nil {
		t.Error("garbage must be rejected")
	}
}

func TestScheduler(t *testing.T) {
	ctx := context.Background()
	m, db, clock := setup(t, nil)
	_ = db.SetSetting(ctx, KeyTime, "03:00")
	*clock = time.Date(2026, 11, 17, 2, 59, 0, 0, time.UTC)
	m.tick(ctx)
	if l, _ := m.local(); len(l) != 0 {
		t.Fatal("too early")
	}
	*clock = clock.Add(2 * time.Minute)
	m.tick(ctx)
	m.tick(ctx) // only once per day
	if l, _ := m.local(); len(l) != 1 {
		t.Fatalf("one scheduled backup expected, got %d", len(l))
	}
	if st := m.Status(ctx); !st.NextRun.Equal(time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("next run: %v", st.NextRun)
	}
}

func TestPendingUploadsRetried(t *testing.T) {
	ctx := context.Background()
	r := &fakeRemote{objs: map[string][]byte{}, fail: true}
	m, _, clock := setup(t, r)
	if _, err := m.Run(ctx, "manual"); err != nil {
		t.Fatal(err)
	}
	r.fail = false
	*clock = clock.Add(RetryUploadsEvery + time.Minute)
	m.tick(ctx) // before today's scheduled time? 10:11 is after 03:00 -> runs the daily backup, which uploads
	*clock = clock.Add(RetryUploadsEvery + time.Minute)
	m.tick(ctx)
	if len(r.objs) == 0 {
		t.Fatal("pending upload was never retried")
	}
	if st := m.Status(ctx); st.PendingUploads != 0 || st.LastError != "" {
		t.Errorf("status after successful retry: %+v", st)
	}
}

func TestCleanLeftovers(t *testing.T) {
	m, _, _ := setup(t, nil)
	_ = os.MkdirAll(m.Dir, 0o750)
	keep := []string{"owl-20261117-030000.db.gz", ".owl-20261117-030000.db.gz.uploaded", "notes.txt"}
	gone := []string{".tmp-owl-20261117-040000.db", "owl-20261117-040000.db.gz.tmp", ".restore-123.db",
		".restore-123.db-wal", ".owl-20261101-030000.db.gz.uploaded"}
	for _, n := range append(append([]string{}, keep...), gone...) {
		_ = os.WriteFile(filepath.Join(m.Dir, n), nil, 0o640)
	}
	m.cleanLeftovers()
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(m.Dir, n)); err != nil {
			t.Errorf("%s must be kept: %v", n, err)
		}
	}
	for _, n := range gone {
		if _, err := os.Stat(filepath.Join(m.Dir, n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s must be removed", n)
		}
	}
}
