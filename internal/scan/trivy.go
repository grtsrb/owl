// Package scan runs image vulnerability scans with Trivy on the server (milestone 15, DESIGN.md "Image
// scanning (Trivy)"): one scan per image digest, one shared vulnerability DB, results kept in memory and
// in the store. Trivy runs as a subprocess with a clean environment — never linked as a library.
package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Vuln is one finding, trimmed to what Owl shows (no descriptions, CVSS vectors or references).
type Vuln struct {
	ID        string `json:"id"`
	Package   string `json:"package"`
	Installed string `json:"installed"`
	Fixed     string `json:"fixed,omitempty"`
	Severity  string `json:"severity"` // critical | high | medium | low | unknown
	Class     string `json:"class"`    // os | lang
	PkgType   string `json:"pkgType,omitempty"`
	Target    string `json:"target,omitempty"` // e.g. "usr/local/bin/gosu" for a Go binary
	Title     string `json:"title,omitempty"`
	URL       string `json:"url,omitempty"` // https only
}

// Result is a finished scan of one digest.
type Result struct {
	OS    string `json:"os,omitempty"` // "debian 12.5"
	Vulns []Vuln `json:"vulns"`
}

// DBInfo describes Trivy's vulnerability DB.
type DBInfo struct {
	Version    string    `json:"version"`            // Trivy's version, e.g. 0.76.2
	UpdatedAt  time.Time `json:"updatedAt,omitzero"` // omitted until a DB was downloaded (the UI says so)
	Downloaded time.Time `json:"downloadedAt,omitzero"`
}

var (
	// ErrAuth: the registry refused the credential (or wants one).
	ErrAuth = errors.New("registry authentication failed")
	// ErrNotFound: the registry doesn't have this digest (any more).
	ErrNotFound = errors.New("image not found in the registry")
	// ErrNoDB: the vulnerability DB hasn't been downloaded yet.
	ErrNoDB = errors.New("vulnerability DB not downloaded yet")
)

// Trivy runs the trivy binary.
type Trivy struct {
	Bin      string // path to trivy
	CacheDir string // DB + layer analysis cache (in the data volume)
}

const maxOutput = 64 << 20 // a JSON report bigger than this is refused

// env is the subprocess environment: built from scratch, so the server's secrets (S3 keys, the Claude API
// key, GITHUB_TOKEN, SMTP password) never reach Trivy. Only proxy and CA settings pass through.
func (t *Trivy) env(cred *Credential) []string {
	env := []string{"HOME=" + t.CacheDir, "TMPDIR=" + filepath.Join(t.CacheDir, "tmp")}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if cred != nil { // env, never arguments: arguments are visible in ps
		env = append(env, "TRIVY_USERNAME="+cred.Username, "TRIVY_PASSWORD="+cred.Password)
	}
	return env
}

func (t *Trivy) run(ctx context.Context, cred *Credential, args ...string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Join(t.CacheDir, "tmp"), 0o700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, t.Bin, args...)
	cmd.Env = t.env(cred)
	cmd.Dir = t.CacheDir
	var stdout limitedBuffer
	stdout.max = maxOutput
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if stdout.over {
		return nil, fmt.Errorf("trivy report larger than %d MiB", maxOutput>>20)
	}
	if err != nil {
		return nil, classify(stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// classify turns Trivy's stderr into a short error (the last meaningful line, never more than 300 chars).
func classify(stderr string, err error) error {
	low := strings.ToLower(stderr)
	switch {
	// Registry auth errors only: never bare "401" (digests and timestamps contain it) or "denied" (a local
	// "permission denied" on the cache is not a login problem).
	case strings.Contains(stderr, "UNAUTHORIZED") || strings.Contains(stderr, "DENIED") ||
		strings.Contains(low, "401 unauthorized") || strings.Contains(low, "authentication required") ||
		strings.Contains(low, "requested access to the resource is denied") || strings.Contains(low, "403 forbidden"):
		return ErrAuth
	case strings.Contains(low, "manifest_unknown") || strings.Contains(low, "manifest unknown") || strings.Contains(low, "name_unknown"):
		return ErrNotFound
	case strings.Contains(low, "--skip-db-update cannot be specified on the first run") || strings.Contains(low, "first run cannot skip downloading db"):
		return ErrNoDB
	}
	msg := ""
	for _, l := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			msg = l
		}
	}
	if msg == "" {
		msg = err.Error()
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return errors.New("trivy: " + msg)
}

// UpdateDB downloads the vulnerability DB and the Java DB (from Trivy's default source, mirror.gcr.io).
// Scans then never touch the network for DB data, so every scan of a day uses the same DB.
func (t *Trivy) UpdateDB(ctx context.Context) error {
	for _, flag := range []string{"--download-db-only", "--download-java-db-only"} {
		if _, err := t.run(ctx, nil, "image", "--cache-dir", t.CacheDir, "--quiet", "--no-progress", flag); err != nil {
			return fmt.Errorf("%s: %w", strings.TrimPrefix(flag, "--"), err)
		}
	}
	return nil
}

// DBInfo reads the DB's metadata (ErrNoDB before the first download).
func (t *Trivy) DBInfo(ctx context.Context) (DBInfo, error) {
	out, err := t.run(ctx, nil, "version", "--cache-dir", t.CacheDir, "--format", "json")
	if err != nil {
		return DBInfo{}, err
	}
	var v struct {
		Version         string
		VulnerabilityDB *struct {
			UpdatedAt    time.Time
			DownloadedAt time.Time
		}
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return DBInfo{}, fmt.Errorf("trivy version: %w", err)
	}
	if v.VulnerabilityDB == nil || v.VulnerabilityDB.UpdatedAt.IsZero() {
		return DBInfo{Version: v.Version}, ErrNoDB
	}
	return DBInfo{Version: v.Version, UpdatedAt: v.VulnerabilityDB.UpdatedAt, Downloaded: v.VulnerabilityDB.DownloadedAt}, nil
}

// Scan scans target.Ref (repository@sha256:…) straight from its registry. Flags, and why:
//   - --platform: the nodes' architecture (a multi-arch digest otherwise means the server's);
//   - --image-src remote: never look at a local Docker/containerd/podman socket;
//   - --scanners vuln: no secret scanning (Owl must not collect secrets found in images), no misconfig;
//   - --skip-db-update / --skip-java-db-update: the DB comes only from UpdateDB;
//   - --offline-scan: don't send jar hashes to Maven Central to identify jars.
func (t *Trivy) Scan(ctx context.Context, target Target, cred *Credential) (*Result, error) {
	args := []string{"image", "--cache-dir", t.CacheDir, "--image-src", "remote",
		"--scanners", "vuln", "--skip-db-update", "--skip-java-db-update", "--offline-scan",
		"--format", "json", "--quiet", "--no-progress"}
	if target.Platform != "" {
		args = append(args, "--platform", target.Platform)
	}
	out, err := t.run(ctx, cred, append(args, "--", target.Ref)...)
	if err != nil {
		return nil, err
	}
	return ParseReport(out)
}

// ParseReport reads Trivy's JSON report.
func ParseReport(b []byte) (*Result, error) {
	var rep struct {
		Metadata struct {
			OS *struct{ Family, Name string }
		}
		Results []struct {
			Target          string
			Class           string
			Type            string
			Vulnerabilities []struct {
				VulnerabilityID  string
				PkgName          string
				InstalledVersion string
				FixedVersion     string
				Severity         string
				Title            string
				PrimaryURL       string
			}
		}
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil, fmt.Errorf("trivy report: %w", err)
	}
	res := &Result{Vulns: []Vuln{}}
	if os := rep.Metadata.OS; os != nil {
		res.OS = strings.TrimSpace(os.Family + " " + os.Name)
	}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		class := "lang"
		if r.Class == "os-pkgs" {
			class = "os"
		}
		for _, v := range r.Vulnerabilities {
			key := v.VulnerabilityID + "\x00" + v.PkgName + "\x00" + v.InstalledVersion + "\x00" + r.Target
			if v.VulnerabilityID == "" || seen[key] {
				continue
			}
			seen[key] = true
			vv := Vuln{
				ID: v.VulnerabilityID, Package: v.PkgName, Installed: v.InstalledVersion,
				Fixed: v.FixedVersion, Severity: severity(v.Severity), Class: class, PkgType: r.Type,
				Title: clip(v.Title, 200),
			}
			if class == "lang" {
				vv.Target = clip(r.Target, 200)
			}
			if strings.HasPrefix(v.PrimaryURL, "https://") {
				vv.URL = clip(v.PrimaryURL, 300)
			}
			res.Vulns = append(res.Vulns, vv)
		}
	}
	return res, nil
}

func severity(s string) string {
	switch s = strings.ToLower(s); s {
	case "critical", "high", "medium", "low":
		return s
	}
	return "unknown"
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// limitedBuffer stops collecting past max (and remembers it did).
type limitedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.over || b.Len()+len(p) > b.max {
		b.over = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

var _ io.Writer = (*limitedBuffer)(nil)
