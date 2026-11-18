package server

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// CLI downloads (milestone 12): the image carries the client-only build of `owl` for a few platforms
// (gzipped, with a SHA256SUMS of the uncompressed binaries), served to signed-in users and tokens.

type cliFile struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	path   string
	gz     bool
}

// cliFiles caches the list once it was read successfully; an error is retried on the next request.
type cliFiles struct {
	mu    sync.Mutex
	files map[string]cliFile // "darwin-arm64" -> file
}

var rePlatform = regexp.MustCompile(`^owl-([a-z0-9]+)-([a-z0-9]+)$`)

// load reads <dir>/SHA256SUMS and the binaries it lists: owl-<os>-<arch>.gz (the image) or the plain
// binary (`make cli`). Listed files that are missing are skipped. No directory = no downloads.
func (c *cliFiles) load(dir string) (map[string]cliFile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files != nil {
		return c.files, nil
	}
	files := map[string]cliFile{}
	if dir == "" {
		c.files = files
		return files, nil
	}
	f, err := os.Open(filepath.Join(dir, "SHA256SUMS"))
	if errors.Is(err, os.ErrNotExist) {
		c.files = files
		return files, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], strings.TrimPrefix(fields[1], "*")
		m := rePlatform.FindStringSubmatch(name)
		if m == nil || len(sum) != 64 {
			continue
		}
		cf := cliFile{OS: m[1], Arch: m[2], SHA256: sum}
		if size, err := gzipSize(filepath.Join(dir, name+".gz")); err == nil {
			cf.path, cf.gz, cf.Size = filepath.Join(dir, name+".gz"), true, size
		} else if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
			cf.path, cf.Size = filepath.Join(dir, name), st.Size()
		} else {
			continue
		}
		files[m[1]+"-"+m[2]] = cf
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	c.files = files
	return files, nil
}

type cliListJSON struct {
	Version string    `json:"version"`
	Files   []cliFile `json:"files"`
}

func (s *Server) listCLI(w http.ResponseWriter, r *http.Request) {
	files, err := s.cliFiles.load(s.CLIDir)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := cliListJSON{Version: s.Version, Files: []cliFile{}}
	for _, f := range files {
		out.Files = append(out.Files, f)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].OS+out.Files[i].Arch < out.Files[j].OS+out.Files[j].Arch })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) downloadCLI(w http.ResponseWriter, r *http.Request) {
	files, err := s.cliFiles.load(s.CLIDir)
	if err != nil {
		s.fail(w, err)
		return
	}
	f, ok := files[r.PathValue("platform")]
	if !ok {
		httpError(w, http.StatusNotFound, "no CLI build for that platform (see GET /api/v1/cli)")
		return
	}
	in, err := os.Open(f.path)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer in.Close()
	var body io.Reader = in
	if f.gz {
		zr, err := gzip.NewReader(in)
		if err != nil {
			s.fail(w, err)
			return
		}
		body = zr
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="owl"`)
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("X-Checksum-Sha256", f.SHA256)
	if _, err := io.Copy(w, body); err != nil {
		s.Log.Warn("CLI download interrupted", "platform", f.OS+"-"+f.Arch, "err", err, "req", requestID(r))
	}
}

// gzipSize is the uncompressed size from the gzip trailer (ISIZE; files here are far below 4 GiB).
func gzipSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() < 18 {
		return 0, errors.New(path + ": not a gzip file")
	}
	var b [4]byte
	if _, err := f.ReadAt(b[:], st.Size()-4); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint32(b[:])), nil
}
