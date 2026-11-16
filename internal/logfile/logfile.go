// Package logfile is a small rotating log file: a new file every UTC day or when the current one
// reaches MaxSize, old files gzipped, files past the retention deleted. Server only (the agent logs
// to stderr and Kubernetes rotates that).
package logfile

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Writer struct {
	dir, base string // base "server" -> server.log, server-20261117-030000.log.gz
	maxSize   int64
	retention atomic.Int64 // days
	now       func() time.Time

	cleanMu sync.Mutex // one cleanup at a time (two would gzip the same file)

	mu   sync.Mutex
	f    *os.File
	size int64
	day  string
	bg   sync.WaitGroup
}

func Open(dir, base string, retentionDays int, maxSize int64) (*Writer, error) {
	return open(dir, base, retentionDays, maxSize, time.Now)
}

func open(dir, base string, retentionDays int, maxSize int64, now func() time.Time) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, base: base, maxSize: maxSize, now: now}
	w.retention.Store(int64(retentionDays))
	if err := w.openFile(); err != nil {
		return nil, err
	}
	w.bg.Add(1)
	go func() { defer w.bg.Done(); w.cleanup() }()
	return w, nil
}

// SetRetentionDays changes how long rotated files are kept (applies at the next rotation/cleanup).
func (w *Writer) SetRetentionDays(n int) {
	if n >= 1 {
		w.retention.Store(int64(n))
	}
}

func (w *Writer) current() string { return filepath.Join(w.dir, w.base+".log") }

func (w *Writer) openFile() error {
	f, err := os.OpenFile(w.current(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	w.day = st.ModTime().UTC().Format("20060102")
	if st.Size() == 0 {
		w.day = w.now().UTC().Format("20060102")
	}
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, os.ErrClosed
	}
	today := w.now().UTC().Format("20060102")
	if (today != w.day && w.size > 0) || (w.size > 0 && w.size+int64(len(p)) > w.maxSize) {
		if err := w.rotate(); err != nil {
			fmt.Fprintln(os.Stderr, "log rotation failed:", err) // keep writing to the current file
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	w.day = today
	return n, err
}

func (w *Writer) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	stamp := w.now().UTC().Format("20060102-150405.000")
	if err := os.Rename(w.current(), filepath.Join(w.dir, w.base+"-"+stamp+".log")); err != nil {
		_ = w.openFile()
		return err
	}
	if err := w.openFile(); err != nil {
		return err
	}
	w.bg.Add(1)
	go func() { defer w.bg.Done(); w.cleanup() }()
	return nil
}

// cleanup gzips rotated files and deletes those older than the retention.
func (w *Writer) cleanup() {
	w.cleanMu.Lock()
	defer w.cleanMu.Unlock()
	matches, _ := filepath.Glob(filepath.Join(w.dir, w.base+"-*.log*"))
	sort.Strings(matches)
	cutoff := w.now().Add(-time.Duration(w.retention.Load()) * 24 * time.Hour)
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			_ = os.Remove(m)
			continue
		}
		if strings.HasSuffix(m, ".log") {
			if err := gzipFile(m); err != nil {
				fmt.Fprintln(os.Stderr, "log compression failed:", err)
			}
		}
	}
}

func gzipFile(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	st, _ := in.Stat()
	tmp := path + ".gz.tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path+".gz"); err != nil {
		return err
	}
	_ = os.Chtimes(path+".gz", st.ModTime(), st.ModTime()) // age counts from when the log was written
	return os.Remove(path)
}

// Close flushes and closes the current file and waits for background compression.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bg.Wait()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
