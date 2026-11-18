package server

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIDownload(t *testing.T) {
	e := newSigninEnv(t)
	dir := t.TempDir()
	bin := []byte("#!fake owl binary\x00\x01\x02")
	sum := sha256.Sum256(bin)
	f, _ := os.Create(filepath.Join(dir, "owl-linux-amd64.gz"))
	zw := gzip.NewWriter(f)
	zw.Write(bin)
	zw.Close()
	f.Close()
	_ = os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(hex.EncodeToString(sum[:])+"  owl-linux-amd64\n"), 0o600)
	e.app.CLIDir, e.app.Version = dir, "1.2.3"

	// Signed in only.
	if code, _ := doJSON(t, &http.Client{}, "GET", e.srv.URL+"/api/v1/cli", ""); code != 401 {
		t.Fatalf("anonymous list: %d", code)
	}
	tok, _ := newToken(t, asViewer(t, e.srv.URL), e.srv.URL, `{"name":"dl"}`)
	code, out := doJSON(t, &http.Client{}, "GET", e.srv.URL+"/api/v1/cli", "", bearer(tok)...)
	files, _ := out["files"].([]any)
	if code != 200 || out["version"] != "1.2.3" || len(files) != 1 || files[0].(map[string]any)["size"].(float64) != float64(len(bin)) {
		t.Fatalf("list: %d %v", code, out)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/cli/linux-amd64", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(got) != string(bin) || resp.Header.Get("X-Checksum-Sha256") != hex.EncodeToString(sum[:]) {
		t.Fatalf("download: %d %q %v", resp.StatusCode, got, resp.Header)
	}
	if code, _ := doJSON(t, &http.Client{}, "GET", e.srv.URL+"/api/v1/cli/..%2f..%2fetc", "", bearer(tok)...); code != 404 {
		t.Fatalf("unknown platform: %d", code)
	}
}

// `make cli` output (plain binaries) works too; listed-but-missing files are skipped, not an error.
func TestCLIDownloadPlainAndMissing(t *testing.T) {
	e := newSigninEnv(t)
	dir := t.TempDir()
	bin := []byte("plain binary")
	sum := sha256.Sum256(bin)
	_ = os.WriteFile(filepath.Join(dir, "owl-darwin-arm64"), bin, 0o755)
	sums := hex.EncodeToString(sum[:]) + "  owl-darwin-arm64\n" + hex.EncodeToString(sum[:]) + "  owl-linux-amd64\n"
	_ = os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums), 0o600)
	e.app.CLIDir = dir
	code, out := doJSON(t, asAdmin(t, e.srv.URL), "GET", e.srv.URL+"/api/v1/cli", "")
	if files, _ := out["files"].([]any); code != 200 || len(files) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	resp, err := asAdmin(t, e.srv.URL).Get(e.srv.URL + "/api/v1/cli/darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != string(bin) {
		t.Fatalf("plain download: %q", got)
	}
}
