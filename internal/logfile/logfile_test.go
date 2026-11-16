package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	w, err := open(dir, "server", 5, 100, now)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(strings.Repeat("a", 60) + "\n"))
	_, _ = w.Write([]byte(strings.Repeat("b", 60) + "\n")) // over 100 bytes: rotates first
	mu.Lock()
	clock = clock.Add(24 * time.Hour)
	mu.Unlock()
	_, _ = w.Write([]byte("next day\n")) // new day: rotates again
	w.Close()

	gz, _ := filepath.Glob(filepath.Join(dir, "server-*.log.gz"))
	if len(gz) != 2 {
		all, _ := os.ReadDir(dir)
		t.Fatalf("want 2 compressed rotated files, got %v (%v)", gz, all)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, "server.log"))
	if string(cur) != "next day\n" {
		t.Errorf("current file: %q", cur)
	}

	// Retention: a file older than 5 days is deleted at the next cleanup.
	old := filepath.Join(dir, "server-20261101-000000.000.log.gz")
	_ = os.WriteFile(old, []byte("x"), 0o640)
	past := clock.Add(-6 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)
	w2, _ := open(dir, "server", 5, 100, now)
	w2.cleanup()
	w2.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("file past retention must be deleted")
	}
}
